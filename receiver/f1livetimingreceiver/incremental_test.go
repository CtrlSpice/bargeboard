package f1livetimingreceiver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Synthetic SignalR records exercise transport boundaries, not source semantics.
const incrementalFeedA = `{"type":1,"target":"feed","arguments":["SessionStatus",{"Status":"Started"},"2026-08-21T10:30:00Z"]}` + "\x1e"
const incrementalFeedC = `{"type":1,"target":"feed","arguments":["SessionStatus",{"Status":"Finished"},"2026-08-21T10:31:00Z"]}` + "\x1e"
const incrementalClose = "{\"type\":7}\x1e"

func incrementalWantFeed(status, timestamp string) liveTimingBatch {
	return liveTimingBatch{
		source: liveTimingUpdateSourceFeed,
		updates: []liveTimingUpdate{{
			topic: "SessionStatus", payload: json.RawMessage(`{"Status":"` + status + `"}`),
			timestamp: timestamp, source: liveTimingUpdateSourceFeed,
		}},
	}
}

func TestIncrementalReadCommitsBeforeInvalidRecord(t *testing.T) {
	for _, test := range []struct {
		name string
		bad  string
		tail bool
	}{
		{name: "malformed JSON", bad: "{\x1e"},
		{name: "malformed envelope", bad: "{\"type\":1,\"target\":\"feed\",\"arguments\":[]}\x1e"},
		{name: "empty record", bad: "\x1e"},
		{name: "oversized complete", bad: strings.Repeat("x", maxHubRecordSize+1) + "\x1e"},
		{name: "oversized incomplete tail", bad: strings.Repeat("x", maxHubRecordSize+1), tail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			contents := []byte(incrementalFeedA + test.bad)
			if !test.tail {
				contents = append(contents, incrementalFeedC...)
			}
			before := bytes.Clone(contents)
			connection := incrementalTestConnection(t, [][]byte{contents}, false)
			connection.requestedTopics = []string{"SessionStatus"}
			var got []liveTimingBatch
			err := connection.read(t.Context(), func(_ context.Context, batch liveTimingBatch) error {
				got = append(got, batch)
				return nil
			})
			if !errors.Is(err, errInvalidLiveTimingData) {
				t.Fatalf("read() error = %v, want invalid data", err)
			}
			want := []liveTimingBatch{incrementalWantFeed("Started", "2026-08-21T10:30:00Z")}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("batches = %#v, want %#v", got, want)
			}
			if !bytes.Equal(contents, before) || connection.pending != nil ||
				!reflect.DeepEqual(connection.requestedTopics, []string{"SessionStatus"}) {
				t.Error("input or subscription state changed, or pending data retained")
			}
		})
	}
}

func TestIncrementalReadCancellationBetweenRecords(t *testing.T) {
	for _, alreadyCanceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "after A", true: "before A"}[alreadyCanceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if alreadyCanceled {
				cancel()
			}
			pending := []byte(incrementalFeedA + `{"type":6}` + "\x1e" + incrementalFeedC + incrementalClose)
			before := bytes.Clone(pending)
			connection := &signalRConnection{pending: pending, requestedTopics: []string{"SessionStatus"}}
			var got []liveTimingBatch
			err := connection.read(ctx, func(_ context.Context, batch liveTimingBatch) error {
				got = append(got, batch)
				cancel()
				return nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("read() error = %v, want canceled", err)
			}
			var want []liveTimingBatch
			if !alreadyCanceled {
				want = []liveTimingBatch{incrementalWantFeed("Started", "2026-08-21T10:30:00Z")}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("batches = %#v, want %#v", got, want)
			}
			if !bytes.Equal(pending, before) || connection.pending != nil ||
				!reflect.DeepEqual(connection.requestedTopics, []string{"SessionStatus"}) {
				t.Error("input or subscription state changed, or pending data retained")
			}
		})
	}
}

func TestIncrementalReadFragmentationAndHandshakePending(t *testing.T) {
	snapshot := `{"type":3,"invocationId":"0","result":{"SessionStatus":{"Status":"Inactive"},"Heartbeat":{"Utc":"now"}}}` + "\x1e"
	want := []liveTimingBatch{
		{
			source:          liveTimingUpdateSourceSnapshot,
			requestedTopics: []string{"SessionStatus", "Heartbeat"},
			presentTopics:   []string{"Heartbeat", "SessionStatus"},
			updates: []liveTimingUpdate{
				{topic: "Heartbeat", payload: json.RawMessage(`{"Utc":"now"}`), source: liveTimingUpdateSourceSnapshot},
				{topic: "SessionStatus", payload: json.RawMessage(`{"Status":"Inactive"}`), source: liveTimingUpdateSourceSnapshot},
			},
		},
		incrementalWantFeed("Started", "2026-08-21T10:30:00Z"),
		incrementalWantFeed("Finished", "2026-08-21T10:31:00Z"),
	}
	for _, test := range []struct {
		name      string
		chunks    [][]byte
		handshake bool
	}{
		{name: "one message", chunks: [][]byte{[]byte(snapshot + incrementalFeedA + incrementalFeedC + incrementalClose)}},
		{name: "fragmented messages", chunks: [][]byte{
			[]byte(snapshot[:11]), []byte(snapshot[11:] + incrementalFeedA[:5]),
			{}, []byte(incrementalFeedA[5 : len(incrementalFeedA)-1]),
			[]byte("\x1e" + incrementalFeedC + incrementalClose),
		}},
		{name: "complete handshake pending", handshake: true, chunks: [][]byte{[]byte(snapshot + incrementalFeedA + incrementalFeedC + incrementalClose)}},
		{name: "partial handshake pending", handshake: true, chunks: [][]byte{
			[]byte(snapshot + incrementalFeedA[:5]), []byte(incrementalFeedA[5:] + incrementalFeedC + incrementalClose),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := incrementalTestConnection(t, test.chunks, test.handshake)
			requested := []string{"SessionStatus", "Heartbeat"}
			connection.requestedTopics = requested
			pendingBefore := bytes.Clone(connection.pending)
			pending := connection.pending
			var got []liveTimingBatch
			err := connection.read(t.Context(), func(_ context.Context, batch liveTimingBatch) error {
				if connection.requestedTopics != nil {
					t.Error("subscription state not cleared before snapshot callback")
				}
				got = append(got, batch)
				return nil
			})
			if !errors.Is(err, errSignalRClosed) || !reflect.DeepEqual(got, want) {
				t.Errorf("read() = %#v, %v, want %#v and closed", got, err, want)
			}
			if connection.pending != nil || connection.requestedTopics != nil ||
				!bytes.Equal(pending, pendingBefore) || !reflect.DeepEqual(requested, []string{"SessionStatus", "Heartbeat"}) {
				t.Error("unexpected pending, requested topics, or input mutation")
			}
		})
	}
}

func TestIncrementalReadStopsInWireOrder(t *testing.T) {
	snapshot := `{"type":3,"invocationId":"0","result":{}}` + "\x1e"
	for _, test := range []struct {
		name       string
		contents   string
		want       []liveTimingBatch
		wantTopics []string
		wantErr    error
	}{
		{
			name: "duplicate completion", contents: snapshot + snapshot + incrementalFeedC,
			want:    []liveTimingBatch{{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"SessionStatus"}, presentTopics: []string{}, updates: []liveTimingUpdate{}}},
			wantErr: errInvalidLiveTimingData,
		},
		{
			name: "close before oversized record", contents: incrementalFeedA + incrementalClose + strings.Repeat("x", maxHubRecordSize+1) + "\x1e" + incrementalFeedC,
			want:       []liveTimingBatch{incrementalWantFeed("Started", "2026-08-21T10:30:00Z")},
			wantTopics: []string{"SessionStatus"}, wantErr: errSignalRClosed,
		},
		{
			name: "reconnect close before invalid record", contents: incrementalFeedA + `{"type":7,"allowReconnect":true}` + "\x1e{\x1e" + incrementalFeedC,
			want:       []liveTimingBatch{incrementalWantFeed("Started", "2026-08-21T10:30:00Z")},
			wantTopics: []string{"SessionStatus"}, wantErr: errSignalRReconnectAllowed,
		},
		{
			name: "invalid before close", contents: incrementalFeedA + "{\x1e" + incrementalClose,
			want:       []liveTimingBatch{incrementalWantFeed("Started", "2026-08-21T10:30:00Z")},
			wantTopics: []string{"SessionStatus"}, wantErr: errInvalidLiveTimingData,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pending := []byte(test.contents)
			connection := &signalRConnection{pending: pending, requestedTopics: []string{"SessionStatus"}}
			var got []liveTimingBatch
			err := connection.read(t.Context(), func(_ context.Context, batch liveTimingBatch) error {
				got = append(got, batch)
				return nil
			})
			if !errors.Is(err, test.wantErr) || !reflect.DeepEqual(got, test.want) {
				t.Errorf("read() = %#v, %v, want %#v, %v", got, err, test.want, test.wantErr)
			}
			if connection.pending != nil || !reflect.DeepEqual(connection.requestedTopics, test.wantTopics) || string(pending) != test.contents {
				t.Error("unexpected pending, subscription state, or input mutation")
			}
		})
	}
}

func TestIncrementalReadDiscardsErroredWebSocketMessage(t *testing.T) {
	// A complete hub record inside a message that exceeds the WebSocket read
	// limit must never be delivered, even though the transport read returns bytes.
	connection := incrementalTestConnection(t, [][]byte{[]byte(incrementalFeedA + incrementalFeedC)}, false)
	connection.conn.SetReadLimit(int64(len(incrementalFeedA)))
	connection.requestedTopics = []string{"SessionStatus"}
	var got []liveTimingBatch
	err := connection.read(t.Context(), func(_ context.Context, batch liveTimingBatch) error {
		got = append(got, batch)
		return nil
	})
	if !errors.Is(err, errInvalidLiveTimingData) || got != nil || connection.pending != nil ||
		!reflect.DeepEqual(connection.requestedTopics, []string{"SessionStatus"}) {
		t.Errorf("read() = %#v, %v, state = %#v", got, err, connection)
	}
}

func TestIncrementalReadNormalizationKeepsSnapshotAtomic(t *testing.T) {
	valid := compressedJSONPayload(t, []byte(`{"Entries":[]}`))
	invalid := compressedJSONPayload(t, []byte(`{`))
	pending := []byte(`{"type":3,"invocationId":"0","result":{"CarData.z":` + string(valid) + `,"Position.z":` + string(invalid) + `}}` + "\x1e" + incrementalFeedC)
	before := bytes.Clone(pending)
	requested := []string{"CarData.z", "Position.z"}
	connection := &signalRConnection{pending: pending, requestedTopics: requested}
	wantWire := liveTimingBatch{
		source:          liveTimingUpdateSourceSnapshot,
		requestedTopics: []string{"CarData.z", "Position.z"}, presentTopics: []string{"CarData.z", "Position.z"},
		updates: []liveTimingUpdate{
			{topic: "CarData.z", payload: bytes.Clone(valid), source: liveTimingUpdateSourceSnapshot},
			{topic: "Position.z", payload: bytes.Clone(invalid), source: liveTimingUpdateSourceSnapshot},
		},
	}
	var wire []liveTimingBatch
	var normalized []normalizedLiveTimingBatch
	err := connection.read(t.Context(), func(_ context.Context, batch liveTimingBatch) error {
		wire = append(wire, batch)
		result, err := normalizeLiveTimingBatch(batch, time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC))
		if !reflect.DeepEqual(batch, wantWire) {
			t.Error("normalization mutated wire batch")
		}
		if err != nil {
			if !reflect.DeepEqual(result, normalizedLiveTimingBatch{}) {
				t.Errorf("failed normalization returned partial batch: %#v", result)
			}
			return err
		}
		normalized = append(normalized, result)
		return nil
	})
	if !errors.Is(err, errInvalidLiveTimingData) || !strings.Contains(err.Error(), "update 1:") ||
		!reflect.DeepEqual(wire, []liveTimingBatch{wantWire}) || normalized != nil {
		t.Errorf("read() wire = %#v, normalized = %#v, error = %v", wire, normalized, err)
	}
	if connection.pending != nil || connection.requestedTopics != nil || !bytes.Equal(pending, before) ||
		!reflect.DeepEqual(requested, []string{"CarData.z", "Position.z"}) {
		t.Error("unexpected pending, subscription state, or input mutation")
	}
}

// All writes are local successful WebSocket messages. The optional handshake
// puts the first chunk in the real handshake's pending buffer.
func incrementalTestConnection(t *testing.T, chunks [][]byte, handshake bool) *signalRConnection {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("Accept() error = %v", err)
			return
		}
		defer conn.CloseNow()
		if handshake {
			_, request, err := conn.Read(ctx)
			if err != nil || string(request) != handshakeRequest {
				t.Errorf("handshake = %q, %v", request, err)
				return
			}
		}
		for i, chunk := range chunks {
			if handshake && i == 0 {
				chunk = append([]byte("{}\x1e"), chunk...)
			}
			if err := conn.Write(ctx, websocket.MessageText, chunk); err != nil {
				t.Errorf("Write() error = %v", err)
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	conn.SetReadLimit(maxWebSocketMessage)
	t.Cleanup(func() {
		_ = conn.CloseNow()
		<-done
	})
	connection := &signalRConnection{conn: conn}
	if handshake {
		connection.pending, err = exchangeHandshake(ctx, conn)
		if err != nil {
			t.Fatalf("exchangeHandshake() error = %v", err)
		}
	}
	return connection
}
