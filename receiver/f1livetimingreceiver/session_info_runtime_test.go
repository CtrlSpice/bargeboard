package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
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
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = connection.Read(ctx)
		connections.Add(1)
		_ = connection.Write(ctx, websocket.MessageText, []byte(
			sessionInfoRuntimeFeed(identityGateDescriptorA, "2022-01-01T00:00:00Z")+
				incrementalFeedA+incrementalFeedC+incrementalClose,
		))
	})

	core, observedLogs := observer.New(zap.WarnLevel)
	settings := receivertest.NewNopSettings(Type)
	settings.Logger = zap.New(core)
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

	for _, want := range []consumedBatch{
		{topic: "SessionInfo", payload: identityGateDescriptorA},
		{topic: "SessionStatus", payload: `{"Status":"Finished"}`},
	} {
		select {
		case got := <-consumed:
			if got != want {
				t.Fatalf("consumed batch = %#v, want %#v", got, want)
			}
		default:
			t.Fatalf("successful batch %#v was not consumed", want)
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
	if got := receiver.operational.state.Load(); got.consumerFailures != 1 || got.updates != 3 {
		t.Fatalf("operational state = %+v", got)
	}
	consumerLogs := observedLogs.FilterMessage("F1 live timing batch consumer failed; input observation does not imply export").All()
	if len(consumerLogs) != 1 {
		t.Fatalf("reduction failure logs = %#v", observedLogs.All())
	}
	if len(consumerLogs[0].Context) != 0 {
		t.Fatalf("reduction failure log fields = %#v, want none", consumerLogs[0].ContextMap())
	}
	for _, entry := range observedLogs.All() {
		output := entry.Message + fmt.Sprint(entry.ContextMap())
		if strings.Contains(output, "sensitive synthetic reduction failure") {
			t.Fatalf("reduction failure log exposed internal error: %q", output)
		}
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
