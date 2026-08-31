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
}

func TestDecodeHubRecord(t *testing.T) {
	tests := []struct {
		name          string
		record        string
		wantUpdates   []liveTimingUpdate
		wantErr       string
		wantClosed    bool
		wantReconnect bool
	}{
		{
			name:   "feed update",
			record: `{"type":1,"target":"feed","arguments":["SessionStatus",{"Status":"Started"},"2026-08-21T10:30:00.034Z"]}`,
			wantUpdates: []liveTimingUpdate{{
				topic:     "SessionStatus",
				payload:   json.RawMessage(`{"Status":"Started"}`),
				timestamp: "2026-08-21T10:30:00.034Z",
				source:    liveTimingUpdateSourceFeed,
			}},
		},
		{
			name:   "compressed feed update",
			record: `{"type":1,"target":"feed","arguments":["CarData.z","compressed-data","2026-08-21T10:30:00.034Z"]}`,
			wantUpdates: []liveTimingUpdate{{
				topic:     "CarData.z",
				payload:   json.RawMessage(`"compressed-data"`),
				timestamp: "2026-08-21T10:30:00.034Z",
				source:    liveTimingUpdateSourceFeed,
			}},
		},
		{
			name:   "subscription snapshot",
			record: `{"type":3,"invocationId":"0","result":{"TimingData":{"Lines":{}},"Heartbeat":{"Utc":"now"}}}`,
			wantUpdates: []liveTimingUpdate{
				{topic: "Heartbeat", payload: json.RawMessage(`{"Utc":"now"}`), source: liveTimingUpdateSourceSnapshot},
				{topic: "TimingData", payload: json.RawMessage(`{"Lines":{}}`), source: liveTimingUpdateSourceSnapshot},
			},
		},
		{name: "ping", record: `{"type":6}`},
		{name: "other target", record: `{"type":1,"target":"other","arguments":[]}`},
		{name: "other completion", record: `{"type":3,"invocationId":"other","result":{}}`},
		{name: "close", record: `{"type":7}`, wantClosed: true},
		{name: "close with reconnect", record: `{"type":7,"allowReconnect":true}`, wantReconnect: true},
		{name: "subscription error", record: `{"type":3,"invocationId":"0","error":"denied"}`, wantErr: "rejected"},
		{name: "invalid feed arguments", record: `{"type":1,"target":"feed","arguments":[]}`, wantErr: "want 3"},
		{name: "missing type", record: `{}`, wantErr: "missing type"},
		{name: "malformed", record: `{`, wantErr: "decode"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updates, err := decodeHubRecord([]byte(test.record))
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
				return
			}
			if err != nil {
				t.Fatalf("decodeHubRecord() error = %v", err)
			}
			if !reflect.DeepEqual(updates, test.wantUpdates) {
				t.Errorf("decodeHubRecord() updates = %#v, want %#v", updates, test.wantUpdates)
			}
		})
	}
}
