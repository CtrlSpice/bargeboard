package f1livetimingreceiver

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestReceiverOwnsSessionInfoStateAcrossReconnect(t *testing.T) {
	var connections atomic.Int32
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = connection.Read(ctx)

		switch connections.Add(1) {
		case 1:
			_ = connection.Write(ctx, websocket.MessageText, []byte(
				sessionInfoRuntimeFeed(identityGateDescriptorA, "2022-01-01T00:00:02Z"),
			))
			_ = connection.Close(websocket.StatusGoingAway, "test reconnect")
		case 2:
			_ = connection.Write(ctx, websocket.MessageText, []byte(
				sessionInfoRuntimeFeed(identityGateDescriptorB, "2022-01-01T00:00:01Z")+incrementalClose,
			))
		default:
			t.Errorf("unexpected connection %d", connections.Load())
		}
	})

	settings := receivertest.NewNopSettings(Type)
	receiver := newLiveTimingReceiver(connectionTestConfig(t, server.URL), settings)
	receiver.retryDelay = func(int) time.Duration { return 0 }
	states := make(chan liveTimingState, 2)
	var consumeCalls atomic.Int32
	receiver.consume = func(context.Context, normalizedLiveTimingBatch) error {
		states <- receiver.state
		if consumeCalls.Add(1) == 1 {
			return errors.New("synthetic downstream failure")
		}
		return nil
	}

	if err := receiver.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = receiver.Shutdown(ctx)
	})
	select {
	case <-receiver.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for receiver completion")
	}

	wantA := identityGateTestState()
	wantBReduction, err := reduceLiveTimingBatch(
		wantA,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorB, "2022-01-01T00:00:01Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	gotStates := make([]liveTimingState, 0, 2)
	for range 2 {
		select {
		case state := <-states:
			gotStates = append(gotStates, state)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for post-reduction state")
		}
	}
	if wantStates := []liveTimingState{wantA, wantBReduction.state}; !reflect.DeepEqual(gotStates, wantStates) {
		t.Fatalf("runtime states = %#v, want %#v", gotStates, wantStates)
	}
	if receiver.state != wantBReduction.state {
		t.Fatalf("receiver state = %#v, want %#v", receiver.state, wantBReduction.state)
	}
	if connections.Load() != 2 {
		t.Fatalf("connections = %d, want 2", connections.Load())
	}
	if got := receiver.operational.state.Load(); got.consumerFailures != 1 || got.updates != 2 {
		t.Fatalf("operational state = %+v", got)
	}
}

func TestReceiverContainsReductionFailure(t *testing.T) {
	var connections atomic.Int32
	releaseClose := make(chan struct{})
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = connection.Read(ctx)
		connections.Add(1)
		_ = connection.Write(ctx, websocket.MessageText, []byte(
			sessionInfoRuntimeFeed(identityGateDescriptorA, "2022-01-01T00:00:00Z")+
				incrementalFeedA+incrementalFeedC,
		))
		<-releaseClose
		_ = connection.Write(ctx, websocket.MessageText, []byte(incrementalClose))
	})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseClose) }) }
	t.Cleanup(release)

	core, observedLogs := observer.New(zap.WarnLevel)
	settings := receivertest.NewNopSettings(Type)
	settings.Logger = zap.New(core)
	host := &statusHost{events: make(chan *componentstatus.Event, 8)}
	receiver := newLiveTimingReceiver(connectionTestConfig(t, server.URL), settings)
	baseReduce := receiver.reduce
	var reduceCalls atomic.Int32
	var accountedUpdates []int64
	receiver.reduce = func(state liveTimingState, batch normalizedLiveTimingBatch) (liveTimingReduction, error) {
		accountedUpdates = append(accountedUpdates, receiver.operational.state.Load().updates)
		if reduceCalls.Add(1) == 2 {
			return liveTimingReduction{}, errors.New("sensitive synthetic reduction failure")
		}
		return baseReduce(state, batch)
	}
	var retries atomic.Int32
	receiver.retryDelay = func(int) time.Duration {
		retries.Add(1)
		return 0
	}
	type consumedBatch struct {
		topic   string
		payload string
	}
	consumed := make(chan consumedBatch, 2)
	receiver.consume = func(_ context.Context, batch normalizedLiveTimingBatch) error {
		consumed <- consumedBatch{topic: batch.updates[0].topic, payload: string(batch.updates[0].payload)}
		return nil
	}

	if err := receiver.Start(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	requireAwaitingInput(t, host)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = receiver.Shutdown(ctx)
	})

	for _, want := range []consumedBatch{
		{topic: "SessionInfo", payload: identityGateDescriptorA},
		{topic: "SessionStatus", payload: `{"Status":"Finished"}`},
	} {
		select {
		case got := <-consumed:
			if got != want {
				t.Fatalf("consumed batch = %#v, want %#v", got, want)
			}
		case <-receiver.done:
			t.Fatalf("receiver stopped before consuming batch %#v", want)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for successful batch %#v", want)
		}
	}
	if want := []int64{1, 2, 3}; !reflect.DeepEqual(accountedUpdates, want) {
		t.Fatalf("updates at reducer entry = %v, want %v", accountedUpdates, want)
	}
	if reduceCalls.Load() != 3 || connections.Load() != 1 || retries.Load() != 0 {
		t.Fatalf("reduce calls = %d, connections = %d, retries = %d", reduceCalls.Load(), connections.Load(), retries.Load())
	}
	if receiver.state != identityGateTestState() {
		t.Fatalf("failed reduction did not preserve receiver state: %#v", receiver.state)
	}
	gotOperational := *receiver.operational.state.Load()
	wantOperational := operationalState{
		started: gotOperational.started, lastUpdate: gotOperational.lastUpdate,
		connection: true, connectionData: true, updates: 3, consumerFailures: 1,
	}
	if gotOperational.started.IsZero() || gotOperational.lastUpdate.IsZero() || gotOperational != wantOperational {
		t.Fatalf("operational state before source close = %+v, want %+v", gotOperational, wantOperational)
	}
	if len(host.events) != 0 {
		t.Fatalf("reduction failure emitted status events: %#v", host.events)
	}
	consumerLogs := observedLogs.All()
	if len(consumerLogs) != 1 || consumerLogs[0].Level != zap.WarnLevel ||
		consumerLogs[0].Message != "F1 live timing batch consumer failed; input observation does not imply export" {
		t.Fatalf("reduction failure logs = %#v", observedLogs.All())
	}
	if len(consumerLogs[0].Context) != 0 {
		t.Fatalf("reduction failure log fields = %#v, want none", consumerLogs[0].ContextMap())
	}

	release()
	select {
	case <-receiver.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for receiver completion")
	}
	if connections.Load() != 1 || retries.Load() != 0 {
		t.Fatalf("connections = %d, retries = %d after source close", connections.Load(), retries.Load())
	}
}

func TestLiveTimingReceiverStateIsInstanceOwned(t *testing.T) {
	first := newLiveTimingReceiver(nil, receivertest.NewNopSettings(Type))
	second := newLiveTimingReceiver(nil, receivertest.NewNopSettings(Type))
	if err := first.reduceNormalizedBatch(
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2022-01-01T00:00:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	if first.state != identityGateTestState() {
		t.Fatalf("first receiver state = %#v", first.state)
	}
	if second.state != (liveTimingState{}) {
		t.Fatalf("new receiver inherited state = %#v", second.state)
	}
}

func sessionInfoRuntimeFeed(payload, timestamp string) string {
	return `{"type":1,"target":"feed","arguments":["SessionInfo",` + payload + `,"` + timestamp + `"]}` + "\x1e"
}
