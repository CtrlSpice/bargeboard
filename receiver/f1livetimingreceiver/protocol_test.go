package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestSplitHubRecords(t *testing.T) {
	records, remaining, err := splitHubRecords([]byte("{\"type\":6}\x1e{\"type\":1}\x1e{"))
	if err != nil {
		t.Fatalf("splitHubRecords() error = %v", err)
	}
	wantRecords := [][]byte{[]byte(`{"type":6}`), []byte(`{"type":1}`)}
	if !reflect.DeepEqual(records, wantRecords) {
		t.Errorf("splitHubRecords() records = %q", records)
	}
	if string(remaining) != "{" {
		t.Errorf("splitHubRecords() remaining = %q", remaining)
	}
}

func TestSplitHubRecordsRejectsOversizedRecord(t *testing.T) {
	_, _, err := splitHubRecords(bytes.Repeat([]byte("x"), maxHubRecordSize+1))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("splitHubRecords() error = %v, want size error", err)
	}
	if !errors.Is(err, errInvalidLiveTimingData) {
		t.Errorf("splitHubRecords() error does not wrap errInvalidLiveTimingData")
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
