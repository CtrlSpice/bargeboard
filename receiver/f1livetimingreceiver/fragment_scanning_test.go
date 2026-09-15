package f1livetimingreceiver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestFragmentScanStartsAtCursor(t *testing.T) {
	// A separator planted in the already-scanned prefix is a white-box probe:
	// restarting the search would find it. Production never mutates that prefix.
	// Check both the shared handshake/hub framer and the hub buffer's wiring.
	for _, suffix := range []string{"", "y", "y\x1etail"} {
		t.Run(fmt.Sprintf("suffix=%q", suffix), func(t *testing.T) {
			input := []byte("xxx")
			buffer := hubRecordBuffer{contents: input}
			if record, complete, err := buffer.next(); record != nil || complete || err != nil ||
				!reflect.DeepEqual(buffer, hubRecordBuffer{contents: []byte("xxx"), scanned: 3}) {
				t.Fatalf("initial scan = %q, %v, %v; state = %#v", record, complete, err, buffer)
			}
			buffer.contents[0] = recordSeparator
			buffer.contents = append(buffer.contents, suffix...)
			contents := buffer.contents
			before := bytes.Clone(buffer.contents)
			wantComplete := strings.Contains(suffix, "\x1e")
			var wantRecord []byte
			wantRemaining := before
			want := hubRecordBuffer{contents: before, scanned: len(before)}
			if wantComplete {
				wantRecord = before[:4]
				wantRemaining = []byte("tail")
				want = hubRecordBuffer{contents: wantRemaining, needsCompaction: true}
			}
			record, remaining, complete := splitFirstRecord(buffer.contents, 3)
			if !reflect.DeepEqual(record, wantRecord) || !reflect.DeepEqual(remaining, wantRemaining) || complete != wantComplete {
				t.Fatalf("shared framer rescanned prefix: %q, %q, %v", record, remaining, complete)
			}
			record, complete, err := buffer.next()
			if err != nil || complete != wantComplete || !reflect.DeepEqual(record, wantRecord) || !reflect.DeepEqual(buffer, want) {
				t.Fatalf("buffer scan = %q, %v, %v; state = %#v, want %#v", record, complete, err, buffer, want)
			}
			if !bytes.Equal(contents, before) {
				t.Fatal("unexpected input change")
			}
		})
	}
}

func TestHubRecordBufferTinyFragmentProgress(t *testing.T) {
	// Include a consumed prefix so the first incomplete check must compact.
	input := []byte("previous\x1e")
	buffer := hubRecordBuffer{contents: input}
	record, complete, err := buffer.next()
	if err != nil || !complete || string(record) != "previous" ||
		!reflect.DeepEqual(buffer, hubRecordBuffer{contents: []byte{}, needsCompaction: true}) {
		t.Fatalf("prefix = %q, %v, %v; state = %#v", record, complete, err, buffer)
	}
	for size := 1; size <= 4096; size++ {
		buffer.contents = append(buffer.contents, 'x')
		for check := 0; check < 2; check++ { // Repeated call models an empty fragment.
			record, complete, err = buffer.next()
			want := hubRecordBuffer{contents: bytes.Repeat([]byte("x"), size), scanned: size}
			if err != nil || complete || record != nil || !reflect.DeepEqual(buffer, want) {
				t.Fatalf("byte %d check %d: result %q, %v, %v; cursor %d", size, check, record, complete, err, buffer.scanned)
			}
		}
	}
	buffer.contents = append(buffer.contents, []byte("\x1e\x1ey\x1ez")...)
	for _, test := range []struct {
		record string
		tail   string
	}{
		{strings.Repeat("x", 4096), "\x1ey\x1ez"},
		{"", "y\x1ez"},
		{"y", "z"},
	} {
		record, complete, err = buffer.next()
		want := hubRecordBuffer{contents: []byte(test.tail), needsCompaction: true}
		if err != nil || !complete || string(record) != test.record || !reflect.DeepEqual(buffer, want) {
			t.Fatalf("consumption = length %d, %v, %v; state = %#v", len(record), complete, err, buffer)
		}
	}
	record, complete, err = buffer.next()
	if err != nil || complete || record != nil ||
		!reflect.DeepEqual(buffer, hubRecordBuffer{contents: []byte("z"), scanned: 1}) || string(input) != "previous\x1e" {
		t.Fatalf("tail = %q, %v, %v; state = %#v", record, complete, err, buffer)
	}
}

func TestHubRecordBufferFragmentedSizeBoundary(t *testing.T) {
	for _, extra := range []string{"", "\x1e", "x", "x\x1e"} {
		t.Run(fmt.Sprintf("extra=%q", extra), func(t *testing.T) {
			buffer := hubRecordBuffer{contents: bytes.Repeat([]byte("x"), maxHubRecordSize)}
			record, complete, err := buffer.next()
			if err != nil || complete || record != nil || buffer.scanned != maxHubRecordSize {
				t.Fatalf("exact incomplete bound = length %d, %v, %v; cursor %d", len(record), complete, err, buffer.scanned)
			}
			buffer.contents = append(buffer.contents, extra...)
			contents := buffer.contents
			before := bytes.Clone(buffer.contents)
			storage, capacity := &buffer.contents[0], cap(buffer.contents)
			want := hubRecordBuffer{contents: before, scanned: maxHubRecordSize}
			record, complete, err = buffer.next()
			if strings.HasPrefix(extra, "x") {
				if !errors.Is(err, errInvalidLiveTimingData) || record != nil || complete ||
					!reflect.DeepEqual(buffer, want) || &buffer.contents[0] != storage || cap(buffer.contents) != capacity {
					t.Fatalf("overflow changed buffer: length %d, %v, %v; cursor %d", len(record), complete, err, buffer.scanned)
				}
			} else if extra == "\x1e" {
				if err != nil || !complete || !bytes.Equal(record, before[:maxHubRecordSize]) || &record[0] != storage ||
					!reflect.DeepEqual(buffer, hubRecordBuffer{contents: []byte{}, needsCompaction: true}) {
					t.Fatalf("exact complete bound = length %d, %v, %v; cursor %d", len(record), complete, err, buffer.scanned)
				}
			} else if err != nil || complete || record != nil || !reflect.DeepEqual(buffer, want) || &buffer.contents[0] != storage {
				t.Fatalf("empty fragment = length %d, %v, %v; cursor %d", len(record), complete, err, buffer.scanned)
			}
			if !bytes.Equal(contents, before) {
				t.Fatal("framing changed input")
			}
		})
	}
}

func TestHandshakeTinyFragments(t *testing.T) {
	// Synthetic JSON padding reaches the exact handshake bound. The hub tail is
	// larger than that bound and must be returned untouched, never scanned here.
	response := "{" + strings.Repeat(" ", maxHandshakeResponseSize-2) + "}"
	tail := strings.Repeat("x", maxHandshakeResponseSize+1) + "\x1e"
	socket := &livenessSocket{reads: make(chan livenessRead, len(response)*2+2)}
	for i := range response {
		socket.reads <- livenessRead{contents: response[i : i+1]}
		socket.reads <- livenessRead{} // Empty successful text fragment.
	}
	socket.reads <- livenessRead{contents: "\x1e" + tail}
	socket.reads <- livenessRead{err: errors.New("unexpected extra read")}
	pending, err := readHandshakeResponse(t.Context(), socket)
	if err != nil || string(pending) != tail || len(socket.reads) != 1 || socket.written() != nil {
		t.Fatalf("handshake = pending length %d, %v; unread messages %d", len(pending), err, len(socket.reads))
	}
}

func TestHandshakeFragmentFailuresDiscardPending(t *testing.T) {
	for _, test := range []struct {
		name   string
		prefix string
		last   livenessRead
		want   string
	}{
		{"incomplete plus one", strings.Repeat(" ", maxHandshakeResponseSize), livenessRead{contents: "x"}, fmt.Sprintf("invalid F1 live timing data: SignalR handshake response exceeds %d bytes", maxHandshakeResponseSize)},
		{"complete plus one", strings.Repeat(" ", maxHandshakeResponseSize), livenessRead{contents: "x\x1e" + incrementalFeedC}, fmt.Sprintf("invalid F1 live timing data: SignalR handshake response exceeds %d bytes", maxHandshakeResponseSize)},
		{"empty record", "", livenessRead{contents: "\x1e" + incrementalFeedC}, "invalid F1 live timing data: decode SignalR handshake response"},
		{"malformed JSON", "{", livenessRead{contents: "x}\x1e" + incrementalFeedC}, "invalid F1 live timing data: decode SignalR handshake response"},
		{"read failure", "{", livenessRead{contents: "}\x1e" + incrementalFeedC, err: errors.New("synthetic-private-reason")}, "read SignalR handshake failed"},
		{"invalid WebSocket read", "{", livenessRead{contents: "}\x1e" + incrementalFeedC, err: websocket.ErrMessageTooBig}, "invalid F1 live timing data: SignalR handshake WebSocket data is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			socket := &livenessSocket{reads: make(chan livenessRead, 4)}
			socket.reads <- livenessRead{contents: test.prefix}
			socket.reads <- livenessRead{}
			socket.reads <- test.last
			socket.reads <- livenessRead{err: errors.New("unexpected extra read")}
			pending, err := readHandshakeResponse(t.Context(), socket)
			wantInvalid := strings.HasPrefix(test.want, "invalid F1 live timing data:")
			if err == nil || err.Error() != test.want || errors.Is(err, errInvalidLiveTimingData) != wantInvalid ||
				pending != nil || len(socket.reads) != 1 || socket.written() != nil {
				t.Fatalf("handshake = %q, %v; unread messages %d", pending, err, len(socket.reads))
			}
		})
	}
}

func TestIncrementalTinyFragmentsStopAtFailure(t *testing.T) {
	for _, test := range []struct {
		name string
		last livenessRead
		want string
	}{
		{"invalid record", livenessRead{contents: "\x1e" + incrementalFeedC}, "invalid F1 live timing data: decode SignalR hub message"},
		{"failed read completing record", livenessRead{contents: incrementalFeedC[1:], err: errors.New("synthetic-private-reason")}, "read F1 live timing message failed"},
		{"invalid WebSocket read completing record", livenessRead{contents: incrementalFeedC[1:], err: websocket.ErrMessageTooBig}, "invalid F1 live timing data: F1 live timing WebSocket data is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			socket := &livenessSocket{reads: make(chan livenessRead, len(incrementalFeedA)*2+3)}
			for i := range incrementalFeedA {
				socket.reads <- livenessRead{contents: incrementalFeedA[i : i+1]}
				socket.reads <- livenessRead{}
			}
			socket.reads <- livenessRead{contents: "{"}
			socket.reads <- test.last
			socket.reads <- livenessRead{err: errors.New("unexpected extra read")}
			connection := &signalRConnection{conn: socket, requestedTopics: []string{"SessionStatus"}}
			var batches []liveTimingBatch
			err := connection.read(t.Context(), func(_ context.Context, batch liveTimingBatch) error {
				batches = append(batches, batch)
				return nil
			})
			wantInvalid := strings.HasPrefix(test.want, "invalid F1 live timing data:")
			if err == nil || err.Error() != test.want || errors.Is(err, errInvalidLiveTimingData) != wantInvalid ||
				!reflect.DeepEqual(batches, []liveTimingBatch{incrementalWantFeed("Started", "2026-08-21T10:30:00Z")}) ||
				!reflect.DeepEqual(connection.requestedTopics, []string{"SessionStatus"}) || connection.pending != nil || len(socket.reads) != 1 {
				t.Fatalf("read = %#v, %v; state = %#v; unread messages %d", batches, err, connection, len(socket.reads))
			}
		})
	}
}

// Preallocated storage isolates separator scanning from append growth. Each
// successful fragment contributes one byte to a single incomplete record.
func BenchmarkHubRecordBufferTinyFragments(b *testing.B) {
	for _, size := range []int{16 * 1024, 64 * 1024, 256 * 1024} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			storage := bytes.Repeat([]byte("x"), size+1)
			storage[size] = recordSeparator
			b.SetBytes(int64(size + 1))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				buffer := hubRecordBuffer{contents: storage[:0]}
				for end := 1; end <= size; end++ {
					buffer.contents = storage[:end]
					if record, complete, err := buffer.next(); record != nil || complete || err != nil {
						b.Fatal("unexpected incomplete result")
					}
				}
				buffer.contents = storage
				if record, complete, err := buffer.next(); len(record) != size || !complete || err != nil {
					b.Fatal("unexpected complete result")
				}
			}
		})
	}
}
