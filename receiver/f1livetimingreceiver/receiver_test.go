package f1livetimingreceiver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestReceiverReconnectsAndConsumesFeed(t *testing.T) {
	var connections atomic.Int32
	retryAttempts := make(chan int, 8)
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = connection.Read(ctx)

		if connections.Add(1) <= 2 {
			_ = connection.Close(websocket.StatusGoingAway, "test reconnect")
			return
		}
		_ = connection.Write(ctx, websocket.MessageText, []byte(
			`{"type":1,"target":"feed","arguments":["SessionStatus",`,
		))
		_ = connection.Write(ctx, websocket.MessageText, []byte(
			`{"Status":"Started"},"2026-08-21T10:30:00.034Z"]}`+"\x1e",
		))
		_, _, _ = connection.Read(ctx)
	})

	receiver := newLiveTimingReceiver(connectionTestConfig(t, server.URL), receivertest.NewNopSettings(Type))
	observationTime := time.Date(2026, 8, 21, 10, 30, 1, 0, time.FixedZone("test", 2*60*60))
	receiver.now = func() time.Time { return observationTime }
	receiver.retryDelay = func(attempt int) time.Duration {
		retryAttempts <- attempt
		return 0
	}
	batches := make(chan normalizedLiveTimingBatch, 1)
	receiver.consume = func(_ context.Context, batch normalizedLiveTimingBatch) error {
		batches <- batch
		return nil
	}

	if err := receiver.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = receiver.Shutdown(ctx)
	})

	select {
	case batch := <-batches:
		if batch.source != liveTimingUpdateSourceFeed {
			t.Errorf("batch source = %d, want feed", batch.source)
		}
		if batch.observationTime != observationTime {
			t.Errorf("batch observation time = %s, want exact input %s", batch.observationTime, observationTime)
		}
		if len(batch.updates) != 1 {
			t.Fatalf("batch update count = %d, want 1", len(batch.updates))
		}
		update := batch.updates[0]
		if update.topic != "SessionStatus" {
			t.Errorf("update topic = %q", update.topic)
		}
		if string(update.payload) != `{"Status":"Started"}` {
			t.Errorf("update payload = %s", update.payload)
		}
		if got := update.timestamp.Format(time.RFC3339Nano); got != "2026-08-21T10:30:00.034Z" {
			t.Errorf("update timestamp = %q", got)
		}
		if update.source != liveTimingUpdateSourceFeed {
			t.Errorf("update source = %d, want feed", update.source)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for feed update after reconnect")
	}
	if got := connections.Load(); got != 3 {
		t.Errorf("connection count = %d, want 3", got)
	}
	for _, want := range []int{0, 1} {
		select {
		case got := <-retryAttempts:
			if got != want {
				t.Errorf("retry attempt = %d, want %d", got, want)
			}
		default:
			t.Fatalf("missing retry attempt %d", want)
		}
	}
}

func TestReceiverConsumesEmptySubscriptionSnapshot(t *testing.T) {
	var connections atomic.Int32
	retryAttempts := make(chan int, 8)
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = connection.Read(ctx)
		connectionNumber := connections.Add(1)
		if connectionNumber <= 2 {
			_ = connection.Write(ctx, websocket.MessageText, []byte(`{"type":6}`+"\x1e"))
			_ = connection.Close(websocket.StatusGoingAway, "test reconnect")
			return
		}
		if connectionNumber > 3 {
			_, _, _ = connection.Read(ctx)
			return
		}
		_ = connection.Write(ctx, websocket.MessageText, []byte(
			`{"type":3,"invocationId":"0","result":null}`+"\x1e",
		))
		_ = connection.Close(websocket.StatusGoingAway, "test reconnect")
	})

	receiver := newLiveTimingReceiver(connectionTestConfig(t, server.URL), receivertest.NewNopSettings(Type))
	observationTime := time.Date(2026, 8, 21, 10, 30, 1, 0, time.UTC)
	var observationCalls atomic.Int32
	receiver.now = func() time.Time {
		observationCalls.Add(1)
		return observationTime
	}
	receiver.retryDelay = func(attempt int) time.Duration {
		retryAttempts <- attempt
		return 0
	}
	batches := make(chan normalizedLiveTimingBatch, 1)
	receiver.consume = func(_ context.Context, batch normalizedLiveTimingBatch) error {
		batches <- batch
		return nil
	}
	wantRequestedTopics, err := normalizeLiveTimingTopics(subscriptionTopics())
	if err != nil {
		t.Fatalf("normalizeLiveTimingTopics() error = %v", err)
	}

	if err := receiver.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = receiver.Shutdown(ctx)
	})

	select {
	case batch := <-batches:
		if batch.source != liveTimingUpdateSourceSnapshot {
			t.Errorf("batch source = %d, want snapshot", batch.source)
		}
		if !reflect.DeepEqual(batch.requestedTopics, wantRequestedTopics) {
			t.Errorf("requested topics = %q, want %q", batch.requestedTopics, wantRequestedTopics)
		}
		if len(batch.presentTopics) != 0 || len(batch.updates) != 0 {
			t.Errorf("empty snapshot present topics = %q, updates = %#v", batch.presentTopics, batch.updates)
		}
		if !batch.observationTime.Equal(observationTime) {
			t.Errorf("batch observation time = %s, want %s", batch.observationTime, observationTime)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for empty subscription snapshot")
	}
	if got := observationCalls.Load(); got != 1 {
		t.Errorf("observation clock calls = %d, want 1", got)
	}

	for _, want := range []int{0, 1, 0} {
		select {
		case got := <-retryAttempts:
			if got != want {
				t.Errorf("retry attempt = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for retry attempt %d", want)
		}
	}
}

func TestReceiverStopsWhenServerDisallowsReconnect(t *testing.T) {
	var connections atomic.Int32
	var retries atomic.Int32
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = connection.Read(ctx)
		connections.Add(1)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{\"type\":7}\x1e"))
	})

	receiver := newLiveTimingReceiver(connectionTestConfig(t, server.URL), receivertest.NewNopSettings(Type))
	receiver.retryDelay = func(int) time.Duration {
		retries.Add(1)
		return 0
	}
	if err := receiver.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	done := receiver.done
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = receiver.Shutdown(ctx)
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for receiver to stop")
	}
	if got := connections.Load(); got != 1 {
		t.Errorf("connection count = %d, want 1", got)
	}
	if got := retries.Load(); got != 0 {
		t.Errorf("retry count = %d, want 0", got)
	}
}

func TestReceiverReportsPermanentInvalidServerData(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		sensitive []string
	}{
		{
			name:      "invalid normalized update",
			message:   `{"type":1,"target":"feed","arguments":["sensitive-topic",{"secret":"sensitive-payload"},"not-a-timestamp"]}`,
			sensitive: []string{"sensitive-topic", "sensitive-payload", "not-a-timestamp"},
		},
		{
			name:      "invalid subscription snapshot",
			message:   `{"type":3,"invocationId":"0","result":"sensitive-snapshot"}`,
			sensitive: []string{"sensitive-snapshot"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var connections atomic.Int32
			var retries atomic.Int32
			server := newConnectionTestServer(t, func(connection *websocket.Conn) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()

				_, _, _ = connection.Read(ctx)
				_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
				_, _, _ = connection.Read(ctx)
				connections.Add(1)
				_ = connection.Write(ctx, websocket.MessageText, []byte(test.message+"\x1e"))
			})

			core, observedLogs := observer.New(zap.ErrorLevel)
			settings := receivertest.NewNopSettings(Type)
			settings.Logger = zap.New(core)
			receiver := newLiveTimingReceiver(connectionTestConfig(t, server.URL), settings)
			receiver.retryDelay = func(int) time.Duration {
				retries.Add(1)
				return 0
			}
			host := &statusHost{events: make(chan *componentstatus.Event, 1)}
			if err := receiver.Start(context.Background(), host); err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			done := receiver.done
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = receiver.Shutdown(ctx)
			})

			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for receiver to stop")
			}
			if got := connections.Load(); got != 1 {
				t.Errorf("connection count = %d, want 1", got)
			}
			if got := retries.Load(); got != 0 {
				t.Errorf("retry count = %d, want 0", got)
			}

			select {
			case event := <-host.events:
				if event.Status() != componentstatus.StatusPermanentError {
					t.Errorf("reported status = %s, want permanent error", event.Status())
				}
				if !errors.Is(event.Err(), errPermanentLiveTimingFailure) {
					t.Errorf("reported error = %v, want permanent live timing failure", event.Err())
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for permanent status")
			}

			logs := observedLogs.All()
			if len(logs) != 1 {
				t.Fatalf("error log count = %d, want 1", len(logs))
			}
			message := logs[0].Message
			if message != errPermanentLiveTimingFailure.Error() {
				t.Errorf("error log = %q, want sanitized permanent failure", message)
			}
			if len(message) > 128 {
				t.Errorf("error log is not bounded and sanitized: %q", message)
			}
			for _, sensitive := range test.sensitive {
				if strings.Contains(message, sensitive) {
					t.Errorf("error log contains sensitive server data %q: %q", sensitive, message)
				}
			}
		})
	}
}

func TestReceiverStopsOnInvalidReconnectSetup(t *testing.T) {
	tests := []struct {
		name               string
		failure            string
		wantNegotiations   int32
		wantWebConnections int32
	}{
		{name: "missing preflight cookie", failure: "preflight", wantNegotiations: 1, wantWebConnections: 1},
		{name: "malformed negotiation", failure: "negotiation", wantNegotiations: 2, wantWebConnections: 1},
		{name: "missing upgrade affinity", failure: "affinity", wantNegotiations: 2, wantWebConnections: 1},
		{name: "malformed WebSocket upgrade", failure: "upgrade", wantNegotiations: 2, wantWebConnections: 2},
		{name: "malformed handshake", failure: "handshake", wantNegotiations: 2, wantWebConnections: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var preflights atomic.Int32
			var negotiations atomic.Int32
			var webConnections atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.Method {
				case http.MethodOptions:
					attempt := preflights.Add(1)
					if attempt > 1 && test.failure == "preflight" {
						writer.WriteHeader(http.StatusNoContent)
						return
					}
					http.SetCookie(writer, &http.Cookie{Name: affinityCookieName, Value: "affinity-token"})
					writer.WriteHeader(http.StatusMethodNotAllowed)
				case http.MethodPost:
					attempt := negotiations.Add(1)
					if attempt > 1 && test.failure == "negotiation" {
						_, _ = writer.Write([]byte(`{`))
						return
					}
					if attempt > 1 && test.failure == "affinity" {
						http.SetCookie(writer, &http.Cookie{Name: affinityCookieName, MaxAge: -1})
						http.SetCookie(writer, &http.Cookie{Name: "AWSALB", Value: "unrelated-cookie"})
					}
					_, _ = writer.Write([]byte(`{
						"connectionId":"public-id",
						"connectionToken":"connection-token",
						"negotiateVersion":1,
						"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]
					}`))
				case http.MethodGet:
					attempt := webConnections.Add(1)
					if attempt > 1 && test.failure == "upgrade" {
						connection, buffered, err := writer.(http.Hijacker).Hijack()
						if err != nil {
							t.Errorf("Hijack() error = %v", err)
							return
						}
						_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
							"Connection: Upgrade\r\n" +
							"Upgrade: websocket\r\n" +
							"Sec-WebSocket-Accept: invalid\r\n\r\n")
						_ = buffered.Flush()
						_ = connection.Close()
						return
					}
					connection, err := websocket.Accept(writer, request, nil)
					if err != nil {
						t.Errorf("Accept() error = %v", err)
						return
					}
					defer connection.CloseNow()
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					_, _, _ = connection.Read(ctx)
					if attempt > 1 && test.failure == "handshake" {
						_ = connection.Write(ctx, websocket.MessageText, []byte("{\x1e"))
						return
					}
					_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
					_, _, _ = connection.Read(ctx)
					_ = connection.Close(websocket.StatusGoingAway, "force reconnect")
				default:
					writer.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))
			t.Cleanup(server.Close)

			var retries atomic.Int32
			receiver := newLiveTimingReceiver(connectionTestConfig(t, server.URL), receivertest.NewNopSettings(Type))
			receiver.retryDelay = func(int) time.Duration {
				retries.Add(1)
				return 0
			}
			host := &statusHost{events: make(chan *componentstatus.Event, 1)}
			if err := receiver.Start(context.Background(), host); err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			done := receiver.done
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = receiver.Shutdown(ctx)
			})

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for receiver to stop")
			}
			if got := negotiations.Load(); got != test.wantNegotiations {
				t.Errorf("negotiation count = %d, want %d", got, test.wantNegotiations)
			}
			if got := preflights.Load(); got != 2 {
				t.Errorf("preflight count = %d, want 2", got)
			}
			if got := webConnections.Load(); got != test.wantWebConnections {
				t.Errorf("WebSocket connection count = %d, want %d", got, test.wantWebConnections)
			}
			if got := retries.Load(); got != 1 {
				t.Errorf("retry-delay count = %d, want 1", got)
			}
			select {
			case event := <-host.events:
				if event.Status() != componentstatus.StatusPermanentError ||
					!errors.Is(event.Err(), errPermanentLiveTimingFailure) {
					t.Errorf("reported event = %s, %v, want permanent live timing failure", event.Status(), event.Err())
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for permanent status")
			}
		})
	}
}

func TestReconnectDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: -1, want: time.Second},
		{attempt: 0, want: time.Second},
		{attempt: 1, want: 2 * time.Second},
		{attempt: 4, want: 16 * time.Second},
		{attempt: 5, want: 30 * time.Second},
		{attempt: 100, want: 30 * time.Second},
	}

	for _, test := range tests {
		if got := reconnectDelay(test.attempt); got != test.want {
			t.Errorf("reconnectDelay(%d) = %s, want %s", test.attempt, got, test.want)
		}
	}
}

type statusHost struct {
	events chan *componentstatus.Event
}

func (h *statusHost) GetExtensions() map[component.ID]component.Component {
	return nil
}

func (h *statusHost) Report(event *componentstatus.Event) {
	h.events <- event
}
