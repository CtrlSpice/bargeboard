package f1livetimingreceiver

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
)

// Synthetic message-level I/O: time advances only in the synctest bubble.
type livenessRead struct {
	contents string
	err      error
}

type livenessSocket struct {
	mu     sync.Mutex
	reads  chan livenessRead
	writes []string
	write  func(context.Context, []byte) error
}

func newLivenessSocket() *livenessSocket {
	return &livenessSocket{reads: make(chan livenessRead)}
}

func (s *livenessSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case message := <-s.reads:
		return websocket.MessageText, []byte(message.contents), message.err
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (s *livenessSocket) Write(ctx context.Context, kind websocket.MessageType, contents []byte) error {
	if kind != websocket.MessageText {
		panic("non-text hub write")
	}
	if s.write != nil {
		if err := s.write(ctx, contents); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.writes = append(s.writes, string(contents))
	s.mu.Unlock()
	return nil
}

func (s *livenessSocket) written() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}

func (*livenessSocket) CloseNow() error    { return nil }
func (*livenessSocket) SetReadLimit(int64) {}

func TestLivenessIdleReadExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		socket := newLivenessSocket()
		connection := &signalRConnection{conn: socket}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		started := time.Now()
		err := connection.read(ctx, func(context.Context, liveTimingBatch) error {
			t.Error("idle connection delivered a batch")
			return nil
		})
		if elapsed := time.Since(started); elapsed != 30*time.Second {
			t.Errorf("idle read lasted %s, want 30s", elapsed)
		}
		if err == nil || err.Error() != "SignalR receive timeout" || errors.Is(err, errInvalidLiveTimingData) {
			t.Errorf("idle read = %v, want sanitized transient receive timeout", err)
		}
	})
}

func startLivenessRead(ctx context.Context, connection *signalRConnection, consume func(context.Context, liveTimingBatch) error) <-chan error {
	result := make(chan error, 1)
	go func() { result <- connection.read(ctx, consume) }()
	synctest.Wait()
	return result
}

func noLivenessBatch(t *testing.T) func(context.Context, liveTimingBatch) error {
	return func(context.Context, liveTimingBatch) error {
		t.Error("unexpected batch")
		return nil
	}
}

func requireLivenessRunning(t *testing.T, result <-chan error) {
	t.Helper()
	synctest.Wait()
	select {
	case err := <-result:
		t.Fatalf("read stopped early: %v", err)
	default:
	}
}

func requireLivenessResult(t *testing.T, result <-chan error, want error) {
	t.Helper()
	synctest.Wait()
	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("read = %v, want %v", err, want)
		}
	default:
		t.Fatal("read did not stop")
	}
}

func TestLivenessSubscribeClockAndPingEncoding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		socket := newLivenessSocket()
		socket.write = func(context.Context, []byte) error {
			time.Sleep(4 * time.Second) // Virtual write duration, not server wait.
			return nil
		}
		connection := &signalRConnection{conn: socket}
		if err := connection.subscribe(t.Context()); err != nil {
			t.Fatal(err)
		}
		success := time.Now()
		if connection.subscribedAt != success || !reflect.DeepEqual(connection.requestedTopics, subscriptionTopics()) {
			t.Fatalf("subscription state = %+v", connection)
		}
		socket.write = nil
		time.Sleep(7 * time.Second) // Initial handoff must not restart keepalive.
		connection.pending = []byte(`{"type":3,"invocationId":"0"}` + "\x1e")
		ctx, cancel := context.WithCancel(t.Context())
		var batches []liveTimingBatch
		result := startLivenessRead(ctx, connection, func(_ context.Context, batch liveTimingBatch) error {
			batches = append(batches, batch)
			return nil
		})
		wantBatch := liveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: subscriptionTopics(), presentTopics: []string{}, updates: []liveTimingUpdate{}}
		if !reflect.DeepEqual(batches, []liveTimingBatch{wantBatch}) || connection.requestedTopics != nil || connection.pending != nil {
			t.Fatal("initial pending completion or manifest was not consumed")
		}
		time.Sleep(8*time.Second - 1)
		synctest.Wait()
		if len(socket.written()) != 1 {
			t.Fatalf("ping before idle boundary: %q", socket.written())
		}
		time.Sleep(1)
		synctest.Wait()
		encoded, _ := encodeSubscribeInvocation(subscriptionTopics())
		if !reflect.DeepEqual(socket.written(), []string{string(encoded), "{\"type\":6}\x1e"}) {
			t.Fatalf("outbound messages = %q", socket.written())
		}
		// A successful ping restarts the outbound clock, without a burst.
		time.Sleep(15*time.Second - 1)
		synctest.Wait()
		if len(socket.written()) != 2 {
			t.Fatal("ping did not suppress writes until the next idle boundary")
		}
		cancel()
		requireLivenessResult(t, result, context.Canceled)
		time.Sleep(time.Minute)
		if len(socket.written()) != 2 || connection.subscribedAt != success {
			t.Fatal("writer survived read exit or changed subscription timestamp")
		}
	})
}

func TestLivenessInitialServerWait(t *testing.T) {
	for _, delay := range []time.Duration{12 * time.Second, 30 * time.Second, 31 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				connection := &signalRConnection{conn: newLivenessSocket()}
				if err := connection.subscribe(t.Context()); err != nil {
					t.Fatal(err)
				}
				time.Sleep(delay)
				started := time.Now()
				err := connection.read(t.Context(), noLivenessBatch(t))
				if err != errSignalRSubscriptionTimeout || time.Since(started) != max(0, 30*time.Second-delay) {
					t.Fatalf("initial handoff: error = %v, remaining wait = %s", err, time.Since(started))
				}
			})
		})
	}
}

func TestLivenessAcceptedRecordsResetReceive(t *testing.T) {
	for _, record := range []string{
		"{\"type\":6}\x1e", "{\"type\":42}\x1e",
		`{"type":1,"target":"ignored"}` + "\x1e",
		`{"type":3,"invocationId":"other"}` + "\x1e",
		incrementalFeedA,
	} {
		t.Run(record, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				socket := newLivenessSocket()
				connection := &signalRConnection{conn: socket}
				var batches []liveTimingBatch
				result := startLivenessRead(t.Context(), connection, func(_ context.Context, batch liveTimingBatch) error {
					batches = append(batches, batch)
					return nil
				})
				for range 2 {
					time.Sleep(20 * time.Second)
					socket.reads <- livenessRead{contents: record}
					requireLivenessRunning(t, result)
				}
				time.Sleep(30*time.Second - 1)
				requireLivenessRunning(t, result)
				time.Sleep(1)
				requireLivenessResult(t, result, errSignalRReceiveTimeout)
				var want []liveTimingBatch
				if record == incrementalFeedA {
					want = []liveTimingBatch{incrementalWantFeed("Started", "2026-08-21T10:30:00Z"), incrementalWantFeed("Started", "2026-08-21T10:30:00Z")}
				}
				if !reflect.DeepEqual(batches, want) || len(socket.written()) != 0 || connection.requestedTopics != nil {
					t.Fatalf("batches = %#v, writes = %q, manifest = %q", batches, socket.written(), connection.requestedTopics)
				}
			})
		})
	}
}

func TestLivenessPartialAndEmptyReads(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "no complete record", true: "completed fragments"}[complete], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				socket := newLivenessSocket()
				result := startLivenessRead(t.Context(), &signalRConnection{conn: socket}, noLivenessBatch(t))
				time.Sleep(10 * time.Second)
				socket.reads <- livenessRead{contents: `{"type":`}
				time.Sleep(10 * time.Second)
				socket.reads <- livenessRead{}
				if complete {
					socket.reads <- livenessRead{contents: "6}\x1e"}
				}
				time.Sleep(10*time.Second - 1)
				requireLivenessRunning(t, result)
				time.Sleep(1)
				if complete {
					requireLivenessRunning(t, result)
					time.Sleep(20 * time.Second)
				}
				requireLivenessResult(t, result, errSignalRReceiveTimeout)
			})
		})
	}
}

func TestLivenessSubscriptionTimeoutDespitePings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		socket := newLivenessSocket()
		connection := &signalRConnection{conn: socket}
		if err := connection.subscribe(t.Context()); err != nil {
			t.Fatal(err)
		}
		result := startLivenessRead(t.Context(), connection, noLivenessBatch(t))
		for range 5 {
			time.Sleep(5 * time.Second)
			socket.reads <- livenessRead{contents: hubPingRecord}
			requireLivenessRunning(t, result)
		}
		time.Sleep(5*time.Second - 1)
		requireLivenessRunning(t, result)
		time.Sleep(1)
		requireLivenessResult(t, result, errSignalRSubscriptionTimeout)
		if !reflect.DeepEqual(connection.requestedTopics, subscriptionTopics()) {
			t.Fatal("pings satisfied subscription completion")
		}
	})
}

func TestLivenessLocalProcessingPausesBothBudgetsButNotPings(t *testing.T) {
	for _, completion := range []bool{false, true} {
		t.Run(map[bool]string{false: "feed while subscription pending", true: "completion"}[completion], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				socket := newLivenessSocket()
				connection := &signalRConnection{conn: socket}
				if err := connection.subscribe(t.Context()); err != nil {
					t.Fatal(err)
				}
				var batches []liveTimingBatch
				result := startLivenessRead(t.Context(), connection, func(_ context.Context, batch liveTimingBatch) error {
					// Normalization and the receiver's synchronous consume execute here.
					time.Sleep(time.Minute)
					batches = append(batches, batch)
					return nil
				})
				time.Sleep(10 * time.Second)
				record := incrementalFeedA
				want := incrementalWantFeed("Started", "2026-08-21T10:30:00Z")
				if completion {
					record = `{"type":3,"invocationId":"0","result":null}` + "\x1e"
					want = liveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: subscriptionTopics(), presentTopics: []string{}, updates: []liveTimingUpdate{}}
				}
				socket.reads <- livenessRead{contents: record}
				synctest.Wait()
				time.Sleep(time.Minute)
				requireLivenessRunning(t, result)
				if len(socket.written()) != 5 || !reflect.DeepEqual(batches, []liveTimingBatch{want}) {
					t.Fatalf("local work: writes = %q, batches = %#v", socket.written(), batches)
				}
				remaining, expiry := 20*time.Second, errSignalRSubscriptionTimeout
				if completion {
					remaining, expiry = 30*time.Second, errSignalRReceiveTimeout
				}
				time.Sleep(remaining - 1)
				requireLivenessRunning(t, result)
				time.Sleep(1)
				requireLivenessResult(t, result, expiry)
			})
		})
	}
}

func TestLivenessPingFailureInterruptsRead(t *testing.T) {
	for _, failure := range []string{"peer error", "write timeout", "parent cancellation"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				socket := newLivenessSocket()
				connection := &signalRConnection{conn: socket}
				if err := connection.subscribe(t.Context()); err != nil {
					t.Fatal(err)
				}
				connection.pending = []byte(`{"type":3,"invocationId":"0"}` + "\x1e")
				writeExited := false
				socket.write = func(ctx context.Context, _ []byte) error {
					defer func() { writeExited = true }()
					if failure == "peer error" {
						return websocket.CloseError{Code: websocket.StatusProtocolError, Reason: "synthetic-confidential-peer-text"}
					}
					<-ctx.Done()
					return ctx.Err()
				}
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				result := startLivenessRead(ctx, connection, func(context.Context, liveTimingBatch) error { return nil })
				time.Sleep(15 * time.Second)
				want := "write SignalR ping failed"
				if failure != "peer error" {
					requireLivenessRunning(t, result)
					time.Sleep(5 * time.Second)
					socket.reads <- livenessRead{contents: hubPingRecord}
					if failure == "parent cancellation" {
						cancel(errors.New("synthetic-confidential-cause"))
						want = "context canceled"
					} else {
						time.Sleep(20 * time.Second)
						socket.reads <- livenessRead{contents: hubPingRecord}
						time.Sleep(5 * time.Second)
						want = "write SignalR ping: context deadline exceeded"
					}
				}
				synctest.Wait()
				select {
				case err := <-result:
					if err == nil || err.Error() != want || errors.Is(err, errInvalidLiveTimingData) {
						t.Fatalf("ping failure = %v, want sanitized transient %q", err, want)
					}
					if cause := errors.Unwrap(err); cause != nil && cause != context.DeadlineExceeded {
						t.Fatalf("unexpected error cause: %v", cause)
					}
				default:
					t.Fatal("ping failure did not interrupt blocked read")
				}
				if !writeExited || len(socket.written()) != 1 {
					t.Fatal("writer not joined or failed write recorded as successful")
				}
				time.Sleep(time.Minute)
				if len(socket.written()) != 1 {
					t.Fatal("writer survived read exit")
				}
			})
		})
	}
}

func TestLivenessReadExitJoinsBlockedWriter(t *testing.T) {
	consumerErr := errors.New("synthetic consumer failure")
	for _, test := range []struct {
		name, record string
		callbackErr  error
		want         error
	}{
		{"invalid record", "{\x1e", nil, errInvalidLiveTimingData},
		{"source close", incrementalClose, nil, errSignalRClosed},
		{"reconnect close", "{\"type\":7,\"allowReconnect\":true}\x1e", nil, errSignalRReconnectAllowed},
		{"consumer exit", incrementalFeedA, consumerErr, consumerErr},
		{"normalization exit", incrementalFeedA, invalidLiveTimingData("synthetic normalization failure"), errInvalidLiveTimingData},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				socket := newLivenessSocket()
				connection := &signalRConnection{conn: socket}
				if err := connection.subscribe(t.Context()); err != nil {
					t.Fatal(err)
				}
				exited := false
				socket.write = func(ctx context.Context, _ []byte) error {
					<-ctx.Done()
					exited = true
					return ctx.Err()
				}
				result := startLivenessRead(t.Context(), connection, func(context.Context, liveTimingBatch) error { return test.callbackErr })
				time.Sleep(16 * time.Second)
				socket.reads <- livenessRead{contents: test.record}
				requireLivenessResult(t, result, test.want)
				if !exited || len(socket.written()) != 1 {
					t.Fatal("read returned before writer cleanup")
				}
			})
		})
	}
}

func TestLivenessFailedReadDiscardsBytesAndSanitizes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		socket := newLivenessSocket()
		connection := &signalRConnection{conn: socket, pending: []byte(incrementalFeedA)}
		var batches []liveTimingBatch
		result := startLivenessRead(t.Context(), connection, func(_ context.Context, batch liveTimingBatch) error {
			batches = append(batches, batch)
			return nil
		})
		socket.reads <- livenessRead{contents: incrementalFeedC, err: errors.New("synthetic-confidential-read-error")}
		synctest.Wait()
		err := <-result
		if err == nil || err.Error() != "read F1 live timing message failed" || errors.Unwrap(err) != nil ||
			!reflect.DeepEqual(batches, []liveTimingBatch{incrementalWantFeed("Started", "2026-08-21T10:30:00Z")}) {
			t.Fatalf("failed read = %v, batches = %#v", err, batches)
		}
	})
}

func TestLivenessCallerDeadlineWinsTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeoutCause(t.Context(), 30*time.Second, errors.New("synthetic-confidential-deadline-cause"))
		defer cancel()
		connection := &signalRConnection{conn: newLivenessSocket()}
		err := connection.read(ctx, noLivenessBatch(t))
		if err != context.DeadlineExceeded {
			t.Fatalf("simultaneous caller and receive deadline = %v, want canonical caller deadline", err)
		}
	})
}

func TestLivenessSuccessfulPingWriteStartsNextInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		socket := newLivenessSocket()
		first := true
		socket.write = func(context.Context, []byte) error {
			if first {
				time.Sleep(5 * time.Second)
				first = false
			}
			return nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		started := time.Now()
		done := make(chan error, 1)
		go func() { done <- writeHubPings(ctx, socket, started) }()
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if !reflect.DeepEqual(socket.written(), []string{hubPingRecord}) {
			t.Fatalf("first completed write = %q", socket.written())
		}
		time.Sleep(15*time.Second - 1)
		synctest.Wait()
		if len(socket.written()) != 1 {
			t.Fatal("write start, rather than successful completion, reset outbound clock")
		}
		time.Sleep(1)
		synctest.Wait()
		if !reflect.DeepEqual(socket.written(), []string{hubPingRecord, hubPingRecord}) {
			t.Fatalf("second write = %q", socket.written())
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestLivenessFailedSubscriptionPreservesClockAndState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		socket := newLivenessSocket()
		connection := &signalRConnection{conn: socket, pending: []byte("pending"), requestedTopics: []string{"existing"}, subscribedAt: time.Now()}
		before := signalRConnection{conn: socket, pending: []byte("pending"), requestedTopics: []string{"existing"}, subscribedAt: connection.subscribedAt}
		socket.write = func(context.Context, []byte) error {
			time.Sleep(5 * time.Second)
			return errors.New("synthetic-confidential-write-error")
		}
		err := connection.subscribe(t.Context())
		if err == nil || err.Error() != "write F1 topic subscription failed" || errors.Unwrap(err) != nil || !reflect.DeepEqual(*connection, before) || len(socket.written()) != 0 {
			t.Fatalf("failed subscribe = %v, state preserved = %t, writes = %q", err, reflect.DeepEqual(*connection, before), socket.written())
		}
	})
}

func TestLivenessWriteDeadlineSurvivesReadCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stopRead := context.WithCancel(t.Context())
		defer stopRead()
		socket := newLivenessSocket()
		socket.write = func(writeCtx context.Context, _ []byte) error {
			<-writeCtx.Done()
			// The real WebSocket deadline closes the transport. Force the order
			// where the reader wakes and cancels its lifetime before Write returns.
			stopRead()
			return writeCtx.Err()
		}
		started := time.Now()
		err := writeHubPings(ctx, socket, started)
		if err == nil || err.Error() != "write SignalR ping: context deadline exceeded" ||
			errors.Unwrap(err) != context.DeadlineExceeded || time.Since(started) != 45*time.Second || len(socket.written()) != 0 {
			t.Fatalf("write deadline after read cleanup = %v, elapsed = %s, writes = %q", err, time.Since(started), socket.written())
		}
	})
}
