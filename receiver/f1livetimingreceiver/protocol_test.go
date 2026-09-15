package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSubscriptionTopicsReturnsCopy(t *testing.T) {
	first := subscriptionTopics()
	want := []string{
		"Heartbeat", "AudioStreams", "DriverList", "ExtrapolatedClock",
		"RaceControlMessages", "SessionInfo", "SessionStatus", "TeamRadio",
		"TimingAppData", "TimingStats", "TrackStatus", "WeatherData",
		"Position.z", "CarData.z", "ContentStreams", "SessionData",
		"TimingData", "TopThree", "RcmSeries", "LapCount",
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("subscriptionTopics() = %q, want %q", first, want)
	}
	first[0] = "changed"
	if got := subscriptionTopics()[0]; got != "Heartbeat" {
		t.Errorf("subscriptionTopics()[0] = %q", got)
	}
}

func TestEncodeSubscribeInvocation(t *testing.T) {
	encoded, err := encodeSubscribeInvocation([]string{"Heartbeat", "TimingData"})
	if err != nil {
		t.Fatalf("encodeSubscribeInvocation() error = %v", err)
	}
	want := "{\"type\":1,\"invocationId\":\"0\",\"target\":\"Subscribe\",\"arguments\":[[\"Heartbeat\",\"TimingData\"]]}\x1e"
	if string(encoded) != want {
		t.Errorf("encodeSubscribeInvocation() = %q, want %q", encoded, want)
	}
}

func TestEncodeSubscribeInvocationRejectsInvalidTopics(t *testing.T) {
	for _, topics := range [][]string{nil, {""}, {"Heartbeat", "Heartbeat"}} {
		if _, err := encodeSubscribeInvocation(topics); err == nil {
			t.Errorf("encodeSubscribeInvocation(%q) error = nil", topics)
		}
	}
}

func TestHubRecordBufferCompactsAtRecordBoundaries(t *testing.T) {
	const prefixSize = 1024 * 1024
	input := append(bytes.Repeat([]byte("x"), prefixSize), recordSeparator, '{')
	inputBefore := bytes.Clone(input)
	buffer := hubRecordBuffer{contents: input}
	first, complete, err := buffer.next()
	if err != nil || !complete || !bytes.Equal(first, inputBefore[:prefixSize]) || &first[0] != &input[0] {
		t.Fatalf("first next() = record length %d, complete %v, error %v", len(first), complete, err)
	}
	want := hubRecordBuffer{contents: []byte("{"), needsCompaction: true}
	if !reflect.DeepEqual(buffer, want) || &buffer.contents[0] != &input[prefixSize+1] {
		t.Fatalf("complete record did not advance to the unmodified tail: %#v", buffer)
	}

	// Compaction must detach the small tail from the large consumed prefix.
	record, complete, err := buffer.next()
	want.needsCompaction = false
	want.scanned = len(want.contents)
	if err != nil || complete || record != nil || !reflect.DeepEqual(buffer, want) {
		t.Fatalf("incomplete next() = %q, %v, %v; buffer = %#v", record, complete, err, buffer)
	}
	if &buffer.contents[0] == &input[prefixSize+1] {
		t.Fatal("incomplete tail still retains the consumed prefix")
	}

	// Appending may grow storage; incomplete checks themselves must not. Force
	// one append growth and also exercise small and empty fragments within capacity.
	fragments := [][]byte{nil, []byte(`"v"`), {}, []byte(":"), bytes.Repeat([]byte(" "), cap(buffer.contents)+1), []byte("true"), nil}
	for i, fragment := range fragments {
		fragmentBefore := bytes.Clone(fragment)
		storageBefore, capacityBefore := &buffer.contents[0], cap(buffer.contents)
		buffer.contents = append(buffer.contents, fragment...)
		want.contents = append(want.contents, fragment...)
		want.scanned = len(want.contents)
		storageAfterAppend, capacityAfterAppend := &buffer.contents[0], cap(buffer.contents)
		if len(buffer.contents) <= capacityBefore && storageAfterAppend != storageBefore {
			t.Fatalf("fragment %d: append within capacity changed storage", i)
		}
		if len(buffer.contents) > capacityBefore && storageAfterAppend == storageBefore {
			t.Fatalf("fragment %d: expected append growth", i)
		}
		for check := 0; check < 2; check++ {
			record, complete, err := buffer.next()
			if err != nil || complete || record != nil || !reflect.DeepEqual(buffer, want) {
				t.Fatalf("fragment %d: next() = %q, %v, %v; buffer = %#v", i, record, complete, err, buffer)
			}
			if &buffer.contents[0] != storageAfterAppend || cap(buffer.contents) != capacityAfterAppend {
				t.Fatalf("fragment %d: incomplete check replaced storage", i)
			}
		}
		if !bytes.Equal(fragment, fragmentBefore) || !bytes.Equal(input, inputBefore) || !bytes.Equal(first, inputBefore[:prefixSize]) {
			t.Fatalf("fragment %d: input or previously returned record changed", i)
		}
	}

	// Completing another record must rearm compaction without a caller-supplied flag.
	buffer.contents = append(buffer.contents, '}', recordSeparator, '{')
	wantRecord := append(bytes.Clone(want.contents), '}')
	record, complete, err = buffer.next()
	want = hubRecordBuffer{contents: []byte("{"), needsCompaction: true}
	if err != nil || !complete || !bytes.Equal(record, wantRecord) || !reflect.DeepEqual(buffer, want) {
		t.Fatalf("second complete next() = %q, %v, %v; buffer = %#v", record, complete, err, buffer)
	}
	storageBefore := &buffer.contents[0]
	previousRecord := record
	record, complete, err = buffer.next()
	want.needsCompaction = false
	want.scanned = len(want.contents)
	if err != nil || complete || record != nil || !reflect.DeepEqual(buffer, want) || &buffer.contents[0] == storageBefore {
		t.Fatalf("second boundary did not detach tail: %q, %v, %v; buffer = %#v", record, complete, err, buffer)
	}
	if !bytes.Equal(previousRecord, wantRecord) || !bytes.Equal(input, inputBefore) {
		t.Error("compaction changed previously returned record or input")
	}
}

func TestHubRecordBufferHandshakeTailAndEmptyState(t *testing.T) {
	input := []byte("{}\x1e{")
	buffer := hubRecordBuffer{contents: input[3:], needsCompaction: true}
	record, complete, err := buffer.next()
	if err != nil || complete || record != nil || !reflect.DeepEqual(buffer, hubRecordBuffer{contents: []byte("{"), scanned: 1}) ||
		&buffer.contents[0] == &input[3] || string(input) != "{}\x1e{" {
		t.Fatalf("handshake tail next() = %q, %v, %v; buffer = %#v", record, complete, err, buffer)
	}
	buffer.contents = append(buffer.contents, '}', recordSeparator)
	record, complete, err = buffer.next()
	if err != nil || !complete || string(record) != "{}" ||
		!reflect.DeepEqual(buffer, hubRecordBuffer{contents: []byte{}, needsCompaction: true}) {
		t.Fatalf("complete next() = %q, %v, %v; buffer = %#v", record, complete, err, buffer)
	}
	record, complete, err = buffer.next()
	if err != nil || complete || record != nil || cap(buffer.contents) != 0 ||
		!reflect.DeepEqual(buffer, hubRecordBuffer{contents: []byte{}}) {
		t.Fatalf("empty tail next() = %q, %v, %v; buffer = %#v", record, complete, err, buffer)
	}
	var empty hubRecordBuffer
	record, complete, err = empty.next()
	if err != nil || complete || record != nil || !reflect.DeepEqual(empty, hubRecordBuffer{}) {
		t.Fatalf("zero buffer next() = %q, %v, %v; buffer = %#v", record, complete, err, empty)
	}
}

func TestHubRecordBufferSizeErrorPreservesState(t *testing.T) {
	for _, terminated := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminated=%v", terminated), func(t *testing.T) {
			input := bytes.Repeat([]byte("x"), maxHubRecordSize+1)
			if terminated {
				input = append(input, recordSeparator)
			}
			buffer := hubRecordBuffer{contents: input, needsCompaction: true}
			want := hubRecordBuffer{contents: bytes.Clone(input), needsCompaction: true}
			capacityBefore := cap(buffer.contents)
			record, complete, err := buffer.next()
			if !errors.Is(err, errInvalidLiveTimingData) || record != nil || complete || !reflect.DeepEqual(buffer, want) ||
				&buffer.contents[0] != &input[0] || cap(buffer.contents) != capacityBefore {
				t.Fatalf("size error changed state: record length %d, complete %v, error %v", len(record), complete, err)
			}
		})
	}
}

func TestSplitHubRecord(t *testing.T) {
	for _, test := range []struct {
		name      string
		contents  []byte
		record    []byte
		remaining []byte
		complete  bool
	}{
		{name: "nil input"},
		{name: "empty record", contents: []byte("\x1e{"), record: []byte{}, remaining: []byte("{"), complete: true},
		{name: "first only", contents: []byte("{\"type\":6}\x1e{\"type\":1}\x1e{"), record: []byte(`{"type":6}`), remaining: []byte("{\"type\":1}\x1e{"), complete: true},
		{name: "incomplete", contents: []byte("{"), remaining: []byte("{")},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := bytes.Clone(test.contents)
			record, remaining, complete, err := splitHubRecord(test.contents, 0)
			if err != nil || !reflect.DeepEqual(record, test.record) ||
				!reflect.DeepEqual(remaining, test.remaining) || complete != test.complete {
				t.Errorf("splitHubRecord() = %q, %q, %v, %v", record, remaining, complete, err)
			}
			if !bytes.Equal(test.contents, before) {
				t.Error("framing mutated input")
			}
		})
	}
}

func TestSplitHubRecordSizeBoundary(t *testing.T) {
	for _, size := range []int{maxHubRecordSize, maxHubRecordSize + 1} {
		for _, terminated := range []bool{false, true} {
			name := fmt.Sprintf("size=%d/terminated=%v", size, terminated)
			t.Run(name, func(t *testing.T) {
				contents := bytes.Repeat([]byte("x"), size)
				if terminated {
					contents = append(contents, recordSeparator, '{')
				}
				before := bytes.Clone(contents)
				record, remaining, complete, err := splitHubRecord(contents, 0)
				if size > maxHubRecordSize {
					if !errors.Is(err, errInvalidLiveTimingData) || record != nil || remaining != nil || complete {
						t.Errorf("oversized result: record length %d, remaining length %d, complete %v, error %v", len(record), len(remaining), complete, err)
					}
				} else if terminated {
					if err != nil || !complete || !bytes.Equal(record, before[:size]) || string(remaining) != "{" {
						t.Errorf("complete boundary: lengths %d/%d, complete %v, error %v", len(record), len(remaining), complete, err)
					}
				} else if err != nil || complete || record != nil || !bytes.Equal(remaining, before) {
					t.Errorf("incomplete boundary: lengths %d/%d, complete %v, error %v", len(record), len(remaining), complete, err)
				}
				if !bytes.Equal(contents, before) {
					t.Error("framing mutated input")
				}
			})
		}
	}
}

func TestSplitHubRecordSeparatorDenseAllocations(t *testing.T) {
	contents := bytes.Repeat([]byte{recordSeparator}, maxWebSocketMessage)
	before := bytes.Clone(contents)
	allocs := testing.AllocsPerRun(100, func() {
		record, remaining, complete, err := splitHubRecord(contents, 0)
		if err != nil || !complete || len(record) != 0 || len(remaining) != len(contents)-1 ||
			&remaining[0] != &contents[1] {
			t.Fatal("framer did not return the first empty record and remaining input view")
		}
	})
	if allocs != 0 {
		t.Errorf("allocations = %v, want zero", allocs)
	}
	if !bytes.Equal(contents, before) {
		t.Error("framing mutated input")
	}
}

func BenchmarkSplitHubRecordSeparatorDense(b *testing.B) {
	for _, size := range []int{64 * 1024, maxWebSocketMessage} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			contents := bytes.Repeat([]byte{recordSeparator}, size)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				record, remaining, complete, err := splitHubRecord(contents, 0)
				if err != nil || !complete || len(record) != 0 || len(remaining) != size-1 {
					b.Fatal("invalid first record")
				}
			}
		})
	}
}

func TestDecodeHubRecord(t *testing.T) {
	requestedTopics := []string{"Heartbeat", "SessionInfo", "TimingData"}
	tests := []struct {
		name          string
		record        string
		wantBatch     *liveTimingBatch
		wantErr       string
		wantClosed    bool
		wantReconnect bool
	}{
		{
			name:   "feed update",
			record: `{"type":1,"target":"feed","arguments":["SessionStatus",{"Status":"Started"},"2026-08-21T10:30:00.034Z"]}`,
			wantBatch: &liveTimingBatch{
				source: liveTimingUpdateSourceFeed,
				updates: []liveTimingUpdate{{
					topic:     "SessionStatus",
					payload:   json.RawMessage(`{"Status":"Started"}`),
					timestamp: "2026-08-21T10:30:00.034Z",
					source:    liveTimingUpdateSourceFeed,
				}},
			},
		},
		{
			name:   "compressed feed update",
			record: `{"type":1,"target":"feed","arguments":["CarData.z","compressed-data","2026-08-21T10:30:00.034Z"]}`,
			wantBatch: &liveTimingBatch{
				source: liveTimingUpdateSourceFeed,
				updates: []liveTimingUpdate{{
					topic:     "CarData.z",
					payload:   json.RawMessage(`"compressed-data"`),
					timestamp: "2026-08-21T10:30:00.034Z",
					source:    liveTimingUpdateSourceFeed,
				}},
			},
		},
		{
			name:   "subscription snapshot",
			record: `{"type":3,"invocationId":"0","result":{"TimingData":{"Lines":{}},"Heartbeat":{"Utc":"now"}}}`,
			wantBatch: &liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: requestedTopics,
				presentTopics:   []string{"Heartbeat", "TimingData"},
				updates: []liveTimingUpdate{
					{topic: "Heartbeat", payload: json.RawMessage(`{"Utc":"now"}`), source: liveTimingUpdateSourceSnapshot},
					{topic: "TimingData", payload: json.RawMessage(`{"Lines":{}}`), source: liveTimingUpdateSourceSnapshot},
				},
			},
		},
		{
			name:   "null subscription snapshot",
			record: `{"type":3,"invocationId":"0","result":null}`,
			wantBatch: &liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: requestedTopics,
				presentTopics:   []string{},
				updates:         []liveTimingUpdate{},
			},
		},
		{
			name:   "subscription completion without result and with extensions",
			record: `{"type":3,"invocationId":"0","headers":{"traceparent":"value"},"futureExtension":true}`,
			wantBatch: &liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: requestedTopics,
				presentTopics:   []string{},
				updates:         []liveTimingUpdate{},
			},
		},
		{
			name:   "empty subscription snapshot",
			record: `{"type":3,"invocationId":"0","result":{}}`,
			wantBatch: &liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: requestedTopics,
				presentTopics:   []string{},
				updates:         []liveTimingUpdate{},
			},
		},
		{
			name:   "present null topic",
			record: `{"type":3,"invocationId":"0","result":{"SessionInfo":null}}`,
			wantBatch: &liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: requestedTopics,
				presentTopics:   []string{"SessionInfo"},
				updates: []liveTimingUpdate{{
					topic:   "SessionInfo",
					payload: json.RawMessage(`null`),
					source:  liveTimingUpdateSourceSnapshot,
				}},
			},
		},
		{name: "ping", record: `{"type":6}`},
		{name: "other target", record: `{"type":1,"target":"other","arguments":[]}`},
		{name: "other completion", record: `{"type":3,"invocationId":"other","result":{}}`},
		{name: "close", record: `{"type":7}`, wantClosed: true},
		{name: "close with reconnect", record: `{"type":7,"allowReconnect":true}`, wantReconnect: true},
		{name: "close with invalid error", record: `{"type":7,"error":123}`, wantErr: "decode"},
		{name: "close with null error", record: `{"type":7,"error":null}`, wantErr: "decode"},
		{name: "subscription error", record: `{"type":3,"invocationId":"0","error":"denied"}`, wantErr: "rejected"},
		{name: "missing completion invocation ID", record: `{"type":3,"result":{}}`, wantErr: "invocation ID"},
		{name: "result and error", record: `{"type":3,"invocationId":"0","result":null,"error":"denied"}`, wantErr: "both result and error"},
		{name: "empty completion error", record: `{"type":3,"invocationId":"0","error":""}`, wantErr: "error is invalid"},
		{name: "duplicate completion result", record: `{"type":3,"invocationId":"0","result":{},"result":null}`, wantErr: "duplicate field"},
		{name: "case-mismatched completion result", record: `{"type":3,"invocationId":"0","Result":{}}`, wantErr: "case-sensitive"},
		{name: "non-object subscription result", record: `{"type":3,"invocationId":"0","result":[]}`, wantErr: "decode"},
		{name: "unrequested snapshot topic", record: `{"type":3,"invocationId":"0","result":{"WeatherData":{}}}`, wantErr: "unrequested"},
		{name: "duplicate snapshot topic", record: `{"type":3,"invocationId":"0","result":{"SessionInfo":{"Key":1},"SessionInfo":{"Key":2}}}`, wantErr: "duplicate"},
		{name: "invalid UTF-8 target", record: "{\"type\":1,\"target\":\"fee\xffd\"}", wantErr: "UTF-8"},
		{name: "invalid UTF-8 feed topic", record: "{\"type\":1,\"target\":\"feed\",\"arguments\":[\"\xff\",{},\"2026-08-21T10:30:00Z\"]}", wantErr: "UTF-8"},
		{name: "invalid feed arguments", record: `{"type":1,"target":"feed","arguments":[]}`, wantErr: "want 3"},
		{name: "missing type", record: `{}`, wantErr: "missing type"},
		{name: "malformed", record: `{`, wantErr: "decode"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch, err := decodeHubRecord([]byte(test.record), requestedTopics)
			if test.wantReconnect {
				if !errors.Is(err, errSignalRReconnectAllowed) {
					t.Fatalf("decodeHubRecord() error = %v, want reconnect allowed", err)
				}
				return
			}
			if test.wantClosed {
				if !errors.Is(err, errSignalRClosed) {
					t.Fatalf("decodeHubRecord() error = %v, want connection closed", err)
				}
				return
			}
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("decodeHubRecord() error = %v, want containing %q", err, test.wantErr)
				}
				if !errors.Is(err, errInvalidLiveTimingData) {
					t.Errorf("decodeHubRecord() error does not wrap errInvalidLiveTimingData")
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeHubRecord() error = %v", err)
			}
			if !reflect.DeepEqual(batch, test.wantBatch) {
				t.Errorf("decodeHubRecord() batch = %#v, want %#v", batch, test.wantBatch)
			}
		})
	}
}

func TestDecodeSubscriptionCompletionCopiesRequestedTopics(t *testing.T) {
	requestedTopics := []string{"SessionInfo"}
	batch, err := decodeHubRecord(
		[]byte(`{"type":3,"invocationId":"0","result":{}}`),
		requestedTopics,
	)
	if err != nil {
		t.Fatalf("decodeHubRecord() error = %v", err)
	}
	requestedTopics[0] = "changed"
	if got := batch.requestedTopics[0]; got != "SessionInfo" {
		t.Errorf("batch requested topic = %q, want SessionInfo", got)
	}
}

func TestDecodeSubscriptionCompletionRequiresRequestedTopics(t *testing.T) {
	_, err := decodeHubRecord([]byte(`{"type":3,"invocationId":"0","result":{}}`), nil)
	if err == nil || !strings.Contains(err.Error(), "no requested topics") {
		t.Fatalf("decodeHubRecord() error = %v, want requested-topics error", err)
	}
	if !errors.Is(err, errInvalidLiveTimingData) {
		t.Errorf("decodeHubRecord() error does not wrap errInvalidLiveTimingData")
	}

	_, err = decodeHubRecord(
		[]byte(`{"type":3,"invocationId":"0","result":{}}`),
		[]string{"SessionInfo", "SessionInfo"},
	)
	if err == nil || !errors.Is(err, errInvalidLiveTimingData) {
		t.Fatalf("decodeHubRecord() duplicate requested topics error = %v", err)
	}
}
