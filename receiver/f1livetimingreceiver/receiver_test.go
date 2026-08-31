package f1livetimingreceiver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/collector/receiver/receivertest"
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
	receiver.retryDelay = func(attempt int) time.Duration {
		retryAttempts <- attempt
		return 0
	}
	updates := make(chan normalizedLiveTimingUpdate, 1)
	receiver.consume = func(_ context.Context, batch []normalizedLiveTimingUpdate) error {
		for _, update := range batch {
			updates <- update
		}
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
	case update := <-updates:
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

func TestReceiverStopsOnInvalidNormalizedUpdate(t *testing.T) {
	var retries atomic.Int32
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte(
			`{"type":1,"target":"feed","arguments":["SessionStatus",{"Status":"Started"},"not-a-timestamp"]}`+"\x1e",
		))
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
	if got := retries.Load(); got != 0 {
		t.Errorf("retry count = %d, want 0", got)
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
