package f1livetimingreceiver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/CtrlSpice/bargeboard/internal/f1engine"
)

// Synthetic nesting probes, not live F1 samples. The malformed scalar at the
// leaf also verifies that depth validation never materializes opaque strings.
func nestedJSONArrays(depth int) string {
	return strings.Repeat("[", depth) + `"\uD800"` + strings.Repeat("]", depth)
}

type rawJSONTestMember struct{ key, value string }

// Shallow grammar/member oracle from the outer Token + per-value Decode pattern
// in protocol.go at b222a3c305a8e3b03ad0eae03b98de5c1b1a978e. Its callers stay
// below nesting limits because mixed Token/Decode depth accounting is not stable
// across Go releases. Production must never use Token to validate a key after
// its lossy conversion.
func shallowJSONMemberOracle(raw []byte) ([]rawJSONTestMember, error) {
	if !utf8.Valid(raw) {
		return nil, f1engine.ErrJSONUTF8
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, f1engine.ErrJSONObject
	}
	var members []rawJSONTestMember
	for decoder.More() {
		start := decoder.InputOffset()
		token, err := decoder.Token()
		if _, ok := token.(string); err != nil || !ok {
			return nil, f1engine.ErrJSONSyntax
		}
		key := bytes.TrimLeft(raw[start:decoder.InputOffset()], " \t\r\n,")
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, f1engine.ErrJSONSyntax
		}
		members = append(members, rawJSONTestMember{string(key), string(value)})
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, f1engine.ErrJSONSyntax
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return nil, f1engine.ErrJSONSyntax
	}
	return members, nil
}

func TestUnicodeControlsDepthHubBoundaries(t *testing.T) {
	for _, kind := range []string{"feed", "snapshot", "opaque extension"} {
		for _, extra := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/limit+%d", kind, extra), func(t *testing.T) {
				depth := 9999 + extra // arguments array or snapshot result adds one.
				if kind == "opaque extension" {
					depth++ // The extension is itself a hub member value.
				}
				payload := nestedJSONArrays(depth)
				var record string
				var want *f1engine.WireBatch
				requested := []string{"SessionInfo", "Heartbeat"}
				switch kind {
				case "feed":
					record = `{"type":1,"target":"feed","arguments":["SessionInfo",` + payload + `,"2026-08-21T10:30:30Z"]}`
					want = &f1engine.WireBatch{Source: f1engine.Feed, Updates: []f1engine.WireUpdate{{Topic: "SessionInfo", Payload: json.RawMessage(payload), Timestamp: "2026-08-21T10:30:30Z", Source: f1engine.Feed}}}
				case "snapshot":
					record = `{"type":3,"invocationId":"0","result":{"Heartbeat":{},"SessionInfo":` + payload + `}}`
					want = &f1engine.WireBatch{Source: f1engine.Snapshot, RequestedTopics: requested, PresentTopics: []string{"Heartbeat", "SessionInfo"}, Updates: []f1engine.WireUpdate{
						{Topic: "Heartbeat", Payload: json.RawMessage(`{}`), Source: f1engine.Snapshot},
						{Topic: "SessionInfo", Payload: json.RawMessage(payload), Source: f1engine.Snapshot},
					}}
				case "opaque extension":
					record = `{"type":6,"extension":` + payload + `}`
				}
				input := []byte(record)
				if len(input) >= maxHubRecordSize || len(input) >= maxWebSocketMessage {
					t.Fatal("depth probe exceeds wire byte caps")
				}
				got, err := decodeHubRecord(input, requested)
				if extra == 1 {
					if got != nil || !errors.Is(err, f1engine.ErrInvalidLiveTimingData) {
						t.Fatalf("over-depth returned partial batch or wrong error: %v", err)
					}
				} else if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("previously accepted depth %d: error=%v, full batch match=%t", depth, err, reflect.DeepEqual(got, want))
				}
				if string(input) != record {
					t.Error("depth validation changed wire input")
				}
			})
		}
	}
}

func TestUnicodeControlsDepthSnapshotMemberBoundary(t *testing.T) {
	for _, depth := range []int{10000, 10001} {
		payload := nestedJSONArrays(depth)
		result := []byte(`{"Heartbeat":{},"SessionInfo":` + payload + `}`)
		before := bytes.Clone(result)
		if len(result) >= maxHubRecordSize {
			t.Fatal("snapshot probe exceeds record byte cap")
		}
		topics, payloads, err := decodeSubscriptionSnapshot(result, map[string]struct{}{"Heartbeat": {}, "SessionInfo": {}})
		if depth == 10001 {
			if topics != nil || payloads != nil || !errors.Is(err, f1engine.ErrInvalidLiveTimingData) {
				t.Errorf("over-depth snapshot returned partial state: %v", err)
			}
		} else if err != nil || !reflect.DeepEqual(topics, []string{"Heartbeat", "SessionInfo"}) || !reflect.DeepEqual(payloads, map[string]json.RawMessage{"Heartbeat": json.RawMessage(`{}`), "SessionInfo": json.RawMessage(payload)}) {
			t.Errorf("previously accepted snapshot member depth: %v", err)
		}
		if !bytes.Equal(result, before) {
			t.Error("snapshot validation changed input")
		}
	}
}

func TestRawJSONObjectDepthProfiles(t *testing.T) {
	for _, profile := range []struct {
		name       string
		visit      func([]byte, func(json.RawMessage, json.RawMessage) error) error
		valueLimit int
	}{
		{"whole object", f1engine.VisitRawJSONObject, 9999},
		{"member value", f1engine.VisitRawJSONObjectMembers, 10000},
	} {
		for _, depth := range []int{9999, 10000, 10001} {
			t.Run(fmt.Sprintf("%s/value-depth=%d", profile.name, depth), func(t *testing.T) {
				payload := nestedJSONArrays(depth)
				// Two deep values prove that nesting resets independently per value.
				raw := []byte(` {"first":true,"deep":` + payload + `,"de\u0065p":` + payload + `} `)
				before := bytes.Clone(raw)
				var got []rawJSONTestMember
				err := profile.visit(raw, func(key, value json.RawMessage) error {
					got = append(got, rawJSONTestMember{string(key), string(value)})
					return nil
				})
				if depth > profile.valueLimit {
					if err != f1engine.ErrJSONSyntax || got != nil {
						t.Fatalf("over-depth profile: err=%v, callbacks=%d", err, len(got))
					}
				} else {
					want := []rawJSONTestMember{{`"first"`, `true`}, {`"deep"`, payload}, {`"de\u0065p"`, payload}}
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("accepted profile: err=%v, complete member match=%t", err, reflect.DeepEqual(got, want))
					}
				}
				if !bytes.Equal(raw, before) {
					t.Error("visitor changed depth probe")
				}
			})
		}
	}
}

func TestUnicodeControlsDepthNegotiationWholeBudget(t *testing.T) {
	for _, depth := range []int{9999, 10000} {
		raw := []byte(`{"connectionId":"id","availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}],"extension":` + nestedJSONArrays(depth) + `}`)
		before := bytes.Clone(raw)
		if len(raw) >= maxNegotiateResponseSize {
			t.Fatal("negotiation probe exceeds byte cap")
		}
		got, err := parseNegotiateResponse(raw)
		if depth == 9999 {
			if err != nil || got != (negotiation{connectionToken: "id"}) {
				t.Errorf("whole-object boundary: %v", err)
			}
		} else if !errors.Is(err, f1engine.ErrInvalidLiveTimingData) || got != (negotiation{}) {
			t.Errorf("negotiation exceeded whole-object budget: %v", err)
		}
		if !bytes.Equal(raw, before) {
			t.Error("negotiation validation changed input")
		}
	}
}

func TestUnicodeControlsDepthOrderedBatches(t *testing.T) {
	for _, extra := range []int{0, 1} {
		payload := nestedJSONArrays(9999 + extra)
		pending := []byte(incrementalFeedA + `{"type":1,"target":"feed","arguments":["SessionInfo",` + payload + `,"2026-08-21T10:30:30Z"]}` + "\x1e" + incrementalFeedC + incrementalClose)
		before := bytes.Clone(pending)
		if len(pending) >= maxHubRecordSize || len(pending) >= maxWebSocketMessage {
			t.Fatal("ordered depth probe exceeds byte caps")
		}
		connection := &signalRConnection{pending: pending, requestedTopics: []string{"SessionInfo"}}
		observation := time.Date(2026, 8, 21, 10, 32, 0, 0, time.UTC)
		var gotWire []f1engine.WireBatch
		var gotNormalized []f1engine.Batch
		err := connection.read(t.Context(), func(_ context.Context, batch f1engine.WireBatch) error {
			gotWire = append(gotWire, batch)
			normalized, err := f1engine.NormalizeBatch(batch, observation)
			if err == nil {
				gotNormalized = append(gotNormalized, normalized)
			}
			return err
		})
		wantWire := []f1engine.WireBatch{incrementalWantFeed("Started", "2026-08-21T10:30:00Z")}
		wantNormalized := []f1engine.Batch{{Source: f1engine.Feed, RequestedTopics: []string{}, PresentTopics: []string{}, ObservationTime: observation, Updates: []f1engine.Update{{Topic: "SessionStatus", Payload: json.RawMessage(`{"Status":"Started"}`), Timestamp: time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC), Source: f1engine.Feed}}}}
		wantErr := f1engine.ErrInvalidLiveTimingData
		if extra == 0 {
			wantErr = errSignalRClosed
			wantWire = append(wantWire,
				f1engine.WireBatch{Source: f1engine.Feed, Updates: []f1engine.WireUpdate{{Topic: "SessionInfo", Payload: json.RawMessage(payload), Timestamp: "2026-08-21T10:30:30Z", Source: f1engine.Feed}}},
				incrementalWantFeed("Finished", "2026-08-21T10:31:00Z"))
			wantNormalized = append(wantNormalized,
				f1engine.Batch{Source: f1engine.Feed, RequestedTopics: []string{}, PresentTopics: []string{}, ObservationTime: observation, InvalidUnicodeUpdates: 1, Updates: []f1engine.Update{{Topic: "SessionInfo", Payload: json.RawMessage(payload), Timestamp: time.Date(2026, 8, 21, 10, 30, 30, 0, time.UTC), Source: f1engine.Feed}}},
				f1engine.Batch{Source: f1engine.Feed, RequestedTopics: []string{}, PresentTopics: []string{}, ObservationTime: observation, Updates: []f1engine.Update{{Topic: "SessionStatus", Payload: json.RawMessage(`{"Status":"Finished"}`), Timestamp: time.Date(2026, 8, 21, 10, 31, 0, 0, time.UTC), Source: f1engine.Feed}}})
		}
		if !errors.Is(err, wantErr) || !reflect.DeepEqual(gotWire, wantWire) || !reflect.DeepEqual(gotNormalized, wantNormalized) {
			t.Errorf("limit+%d: error=%v, wire match=%t, normalized match=%t", extra, err, reflect.DeepEqual(gotWire, wantWire), reflect.DeepEqual(gotNormalized, wantNormalized))
		}
		if !bytes.Equal(pending, before) || connection.pending != nil || !reflect.DeepEqual(connection.requestedTopics, []string{"SessionInfo"}) {
			t.Error("ordered processing changed input/subscription state")
		}
	}
}
