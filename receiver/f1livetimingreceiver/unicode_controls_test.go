package f1livetimingreceiver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Synthetic JSON/SignalR fixtures; these are boundary probes, not live F1 samples.
func TestUnicodeControlsRejectBeforeRouting(t *testing.T) {
	for _, record := range []string{
		`{"type":6,"\uD800":1}`,
		`{"type":6,"�":1,"\uD800":2}`,
		`{"type":6,"target":"\uD800"}`,
		`{"type":3,"invocationId":"\uD800","result":{}}`,
		`{"type":1,"target":"feed","arguments":["\uD800",{},"2026-08-21T10:30:00Z"]}`,
		`{"type":1,"target":"feed","arguments":["SessionInfo",{},"\uD800"]}`,
		`{"type":3,"invocationId":"0","result":{"SessionInfo":{},"\uD800":{}}}`,
	} {
		input := []byte(record)
		batch, err := decodeHubRecord(input, []string{"SessionInfo", "�"})
		if batch != nil || !errors.Is(err, errInvalidLiveTimingData) || string(input) != record {
			t.Errorf("control probe %q: batch=%#v, err=%v, input preserved=%t", record, batch, err, string(input) == record)
		}
	}
}

func TestUnicodeControlsNegotiationAssignments(t *testing.T) {
	const valid = `"connectionId":"id","connectionToken":"token","negotiateVersion":1,"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]`
	for _, member := range []string{
		`"connectionToken":"\uD800"`, `"connectionId":"\uDFFF"`,
		`"url":"\uD800"`, `"accessToken":"\uD800"`, `"\uD800":null`,
		`"availableTransports":[{"transport":"WebSockets","transferFormats":["Text","\uD800"]}]`,
		`"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]},{"transport":"\uD800"}]`,
		`"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"],"\uD800":null}]`,
	} {
		input := []byte("{" + valid + "," + member + "}")
		before := bytes.Clone(input)
		got, err := parseNegotiateResponse(input)
		if got != (negotiation{}) || !errors.Is(err, errInvalidLiveTimingData) || !bytes.Equal(input, before) {
			t.Errorf("assignment probe %s: result=%#v, err=%v", member, got, err)
		}
	}
	if err := parseHandshakeResponse([]byte(`{"\uD800":true}`)); !errors.Is(err, errInvalidLiveTimingData) {
		t.Errorf("malformed handshake key: %v", err)
	}
}

func TestUnicodeControlsErrorDescriptions(t *testing.T) {
	for _, description := range []string{`"denied"`, `"\uD800"`, `"\uDC00"`, `"�"`} {
		for _, reconnect := range []bool{false, true} {
			flag, wantErr := "false", errSignalRClosed
			if reconnect {
				flag, wantErr = "true", errSignalRReconnectAllowed
			}
			batch, err := decodeHubRecord([]byte(`{"type":7,"allowReconnect":`+flag+`,"error":`+description+`}`), nil)
			if batch != nil || err != wantErr {
				t.Errorf("close description=%s: %#v, %v", description, batch, err)
			}
		}
		for _, parse := range []func([]byte) error{
			parseHandshakeResponse,
			func(raw []byte) error { _, err := parseNegotiateResponse(raw); return err },
			func(raw []byte) error {
				_, err := decodeHubRecord([]byte(`{"type":3,"invocationId":"0",`+string(raw[1:])), []string{"SessionInfo"})
				return err
			},
		} {
			err := parse([]byte(`{"error":` + description + `}`))
			if !errors.Is(err, errInvalidLiveTimingData) || !strings.Contains(err.Error(), "rejected") || strings.Contains(err.Error(), description) {
				t.Errorf("description changed rejection: %v", err)
			}
		}
	}
}

func TestUnicodeControlsOpaqueExtensions(t *testing.T) {
	const extension = `{"\uD800":"\uDC00","�":"\uD800","nested":["\uD800"]}`
	const payload = `{ "Name" : "\uD800", "extension": ` + extension + ` }`
	record := []byte(`{"type":1,"target":"feed","extension":` + extension + `,"arguments":["SessionInfo",` + payload + `,"2026-08-21T10:30:00Z"]}`)
	before := bytes.Clone(record)
	got, err := decodeHubRecord(record, nil)
	want := &liveTimingBatch{source: liveTimingUpdateSourceFeed, updates: []liveTimingUpdate{{
		topic: "SessionInfo", payload: json.RawMessage(payload), timestamp: "2026-08-21T10:30:00Z", source: liveTimingUpdateSourceFeed,
	}}}
	if err != nil || !reflect.DeepEqual(got, want) || !bytes.Equal(record, before) {
		t.Fatalf("opaque payload: %#v, %v", got, err)
	}
	if err := parseHandshakeResponse([]byte(`{"extension":` + extension + `}`)); err != nil {
		t.Errorf("opaque handshake extension: %v", err)
	}
	gotNegotiation, err := parseNegotiateResponse([]byte(`{"connectionId":"id","availableTransports":[{"transport":"WebSockets","transferFormats":["Text"],"extension":` + extension + `}],"extension":` + extension + `}`))
	if err != nil || gotNegotiation != (negotiation{connectionToken: "id"}) {
		t.Errorf("opaque negotiation extension: %#v, %v", gotNegotiation, err)
	}
}

func TestUnicodeControlsValidTokens(t *testing.T) {
	for _, test := range []struct{ token, decoded string }{
		{`"�"`, "�"}, {`"\uFFFD"`, "�"}, {`"\ud83D\uDe80"`, "🚀"},
		{`"\\uD800"`, `\uD800`}, {`"\"\\"`, "\"\\"},
	} {
		// Feed topic grammar is currently nonempty, without an ASCII allowlist.
		record := []byte(`{"ty\u0070e":1,"tar\u0067et":"f\u0065ed","arguments":[` + test.token + `,{},"2026-08-21T10:30:00Z"],` + test.token + `:null}`)
		got, err := decodeHubRecord(record, nil)
		want := &liveTimingBatch{source: liveTimingUpdateSourceFeed, updates: []liveTimingUpdate{{topic: test.decoded, payload: json.RawMessage(`{}`), timestamp: "2026-08-21T10:30:00Z", source: liveTimingUpdateSourceFeed}}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("valid feed %s: %#v, %v", test.token, got, err)
		}
		got, err = decodeHubRecord([]byte(`{"type":3,"invocationId":"\u0030","result":{`+test.token+`:{}}}`), []string{test.decoded})
		want = &liveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{test.decoded}, presentTopics: []string{test.decoded}, updates: []liveTimingUpdate{{topic: test.decoded, payload: json.RawMessage(`{}`), source: liveTimingUpdateSourceSnapshot}}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("valid manifest %s: %#v, %v", test.token, got, err)
		}
		negotiated, err := parseNegotiateResponse([]byte(`{"connectionId":"id","connectionToken":` + test.token + `,"negotiateVersion":1,"availableTransports":[{"transport":"Web\u0053ockets","transferFormats":["T\u0065xt",` + test.token + `]}]}`))
		if err != nil || negotiated != (negotiation{connectionToken: test.decoded}) {
			t.Errorf("valid token %s: %#v, %v", test.token, negotiated, err)
		}
		if err := parseHandshakeResponse([]byte(`{` + test.token + `:null}`)); err != nil {
			t.Errorf("valid handshake key: %v", err)
		}
		// Unicode acceptance is separate from timestamp and base64 grammar.
		if _, err := normalizeLiveTimingTimestamp(liveTimingUpdateSourceFeed, test.decoded); err == nil {
			t.Error("Unicode validity bypassed timestamp grammar")
		}
		if got, err := decompressLiveTimingPayload([]byte(test.token)); got != nil || err == nil {
			t.Error("Unicode validity bypassed base64 grammar")
		}
	}
}

func TestUnicodeControlsOwningKeyPolicies(t *testing.T) {
	for _, test := range []struct{ record, reason string }{
		{`{"type":6,"ty\u0070e":6}`, "duplicate field"},
		{`{"type":6,"extension":1,"exten\u0073ion":2}`, "duplicate field"},
		{`{"type":6,"\u0054ype":6}`, "case-sensitive"},
		{`{"type":3,"invocationId":"0","result":{"SessionInfo":{},"Session\u0049nfo":{}}}`, "duplicate topic"},
		{`{"type":6,"�":1,"\uD800":2}`, "message key"},
		{`{"type":3,"invocationId":"0","result":{"�":1,"\uD800":2}}`, "snapshot manifest"},
	} {
		got, err := decodeHubRecord([]byte(test.record), []string{"SessionInfo", "�"})
		if got != nil || !errors.Is(err, errInvalidLiveTimingData) || !strings.Contains(err.Error(), test.reason) {
			t.Errorf("key policy %s: %#v, %v", test.record, got, err)
		}
	}
	for _, test := range []struct{ record, reason string }{
		{`{"extension":1,"exten\u0073ion":2}`, ""},
		{`{"Error":"\uD800","Type":6}`, ""},                                                  // Handshake keys remain case-sensitive.
		{`{"error":123,"err\u006fr":"\uD800"}`, "SignalR handshake rejected the connection"}, // Last raw value wins.
		{`{"error":"\uD800","error":""}`, "decode SignalR handshake error"},
		{`{"error":null}`, "decode SignalR handshake error"},
		{`{"error":"\uD800","ty\u0070e":null}`, "expected a SignalR handshake response"},
		{`{"�":1,"\uD800":2}`, "decode SignalR handshake response"},
	} {
		err := parseHandshakeResponse([]byte(test.record))
		if test.reason == "" {
			if err != nil {
				t.Errorf("handshake %s: %v", test.record, err)
			}
		} else if !errors.Is(err, errInvalidLiveTimingData) || err.Error() != "invalid F1 live timing data: "+test.reason {
			t.Errorf("handshake %s: %v", test.record, err)
		}
	}
}

func TestUnicodeControlsHubMessageCompleteResult(t *testing.T) {
	typeValue := 99
	for _, test := range []struct {
		raw  string
		want hubMessage
	}{
		{`{"type":99,"invocationId":"\u0030","target":"f\u0065ed","arguments":["\uD800",{"\uD800":true}],"result":{"\uD800":"\uDC00"},"error":"\uD800","allowReconnect":true,"extension":{"\uD800":null}}`, hubMessage{
			Type: &typeValue, InvocationID: "0", Target: "feed",
			Arguments: []json.RawMessage{json.RawMessage(`"\uD800"`), json.RawMessage(`{"\uD800":true}`)},
			Result:    json.RawMessage(`{"\uD800":"\uDC00"}`), HasError: true, AllowReconnect: true,
		}},
		{`{"type":null,"invocationId":null,"target":null,"arguments":null,"result":null,"error":"","allowReconnect":null}`, hubMessage{Result: json.RawMessage(`null`), HasError: true, ErrorEmpty: true}},
		{`{}`, hubMessage{}},
	} {
		raw := []byte(test.raw)
		got, err := decodeHubMessage(raw)
		if err != nil || !reflect.DeepEqual(got, test.want) || string(raw) != test.raw {
			t.Errorf("message: %#v, %v; want %#v", got, err, test.want)
		}
		// Retained result and argument bytes must survive reuse of the wire buffer.
		clear(raw)
		if !reflect.DeepEqual(got, test.want) {
			t.Error("message retains mutable input storage")
		}
	}
	for _, raw := range []string{
		`{"type":6,"target":"\uD800"}`, `{"type":6,"error":null}`,
		`{"type":6,"type":7}`, `{"type":6,"arguments":false}`, `{"type":6,"allowReconnect":"true"}`,
	} {
		got, err := decodeHubMessage([]byte(raw))
		if !reflect.DeepEqual(got, hubMessage{}) || !errors.Is(err, errInvalidLiveTimingData) {
			t.Errorf("failed message returns partial state: %#v, %v", got, err)
		}
	}
}

func TestUnicodeControlsNegotiationCompatibility(t *testing.T) {
	const capabilities = `"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]`
	for _, test := range []struct{ members, want, reason string }{
		{`"connectionId":"id",` + capabilities, "id", ""},
		{`"connectionId":"first","connection\u0049d":"last",` + capabilities, "last", ""},
		{`"CONNECTIONID":"id","negotiateVersion":null,` + capabilities, "id", ""},
		{`"connectionId":"id","connectionId":null,` + capabilities, "id", ""},
		{`"connectionId":"id","connectionToken":"token","negotiateVersion":1,"negotiateVersion":null,` + capabilities, "token", ""},
		{`"connectionId":"id","error":"denied","ERROR":"",` + capabilities, "id", ""},
		{`"connectionId":"id","error":"\uD800","error":null,` + capabilities, "", "rejected the connection"},
		{`"connectionId":"id","error":null,"url":null,"accessToken":null,` + capabilities, "id", ""},
		{`"connectionId":"id",` + capabilities + `,"availableTransports":[{}]`, "id", ""},
		{`"connectionId":"id",` + capabilities + `,"availableTransports":[null]`, "id", ""},
		{`"connectionId":"id",` + capabilities + `,"availableTransports":[{"Transport":null,"TRANSFERFORMATS":[null]}]`, "id", ""},
		{`"connectionId":"id",` + capabilities + `,"availableTransports":null`, "", "does not support"},
		{`"connectionId":"id",` + capabilities + `,"availableTransports":[]`, "", "does not support"},
		{`"connectionId":"id",` + capabilities + `,"availableTransports":[{"transferFormats":[]}]`, "", "does not support"},
		{`"connectionId":123,"connectionId":"id",` + capabilities, "", "decode"},
		{`"connectionId":"\uD800","connectionId":"id",` + capabilities, "", "decode"},
		{`"connectionId":"id","connectionToken":"\uD800",` + capabilities, "", "decode"}, // Even unused v0 token assignments validate.
		{`"connectionId":"id","error":123,"error":"",` + capabilities, "", "decode"},
		{`"connectionId":"id","availableTransports":[{"transport":"\uD800","transport":"WebSockets","transferFormats":["Text"]}]`, "", "decode"},
	} {
		raw := []byte("{" + test.members + "}")
		before := bytes.Clone(raw)
		got, err := parseNegotiateResponse(raw)
		if got != (negotiation{connectionToken: test.want}) || !bytes.Equal(raw, before) {
			t.Errorf("negotiation result=%#v or input changed for %s", got, test.members)
		}
		if test.reason == "" {
			if err != nil {
				t.Errorf("compatibility %s: %v", test.members, err)
			}
		} else if !errors.Is(err, errInvalidLiveTimingData) || !strings.Contains(err.Error(), test.reason) {
			t.Errorf("compatibility %s: %v, want %s", test.members, err, test.reason)
		}
	}
	for _, test := range []struct{ raw, reason string }{
		{`null`, "SignalR negotiation did not return a connection token"},
		{" \t\r\nnull\n", "SignalR negotiation did not return a connection token"},
		{"\u00a0null", "decode SignalR negotiation response"},
		{`null {}`, "decode SignalR negotiation response"},
	} {
		got, err := parseNegotiateResponse([]byte(test.raw))
		if got != (negotiation{}) || !errors.Is(err, errInvalidLiveTimingData) || err.Error() != "invalid F1 live timing data: "+test.reason {
			t.Errorf("top-level null policy: %#v, %v", got, err)
		}
	}
}

func TestUnicodeControlsConnectionTokenNeverReachesUpgrade(t *testing.T) {
	// Only synthetic test-token configuration and in-memory HTTP are used.
	cfg := connectionTestConfig(t, "http://synthetic.test")
	// If setup reaches URL construction at all, this deliberately invalid URL
	// would produce a different error before any upgrade request can be sent.
	cfg.Endpoint = ":invalid-websocket-url"
	var requests []string
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.String())
		switch request.Method {
		case http.MethodOptions:
			return &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"AWSALBCORS=synthetic-affinity; Path=/"}}, Body: http.NoBody}, nil
		case http.MethodPost:
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"connectionId":"id","connectionToken":"\uD800","negotiateVersion":1,"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]}`))}, nil
		default:
			t.Error("attempted upgrade after malformed token")
			return nil, errors.New("unexpected request")
		}
	})}
	connection, err := connectSignalR(t.Context(), client, cfg)
	wantRequests := []string{"OPTIONS " + cfg.NegotiateEndpoint, "POST " + cfg.NegotiateEndpoint + "?negotiateVersion=1"}
	if connection != nil || !errors.Is(err, errInvalidLiveTimingData) || err.Error() != "invalid F1 live timing data: decode SignalR negotiation response" || !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("setup: connection=%v, err=%v, requests=%v", connection, err, requests)
	}
}

func TestUnicodeControlsOrderedBatches(t *testing.T) {
	const payload = `{ "Name":"\uD800", "extension":{"\uD800":"\uDC00"} }`
	for _, compressed := range []bool{false, true} {
		wirePayload, topic := json.RawMessage(payload), "SessionInfo"
		if compressed {
			wirePayload, topic = compressedJSONPayload(t, []byte(payload)), "CarData.z"
		}
		for _, badControl := range []bool{false, true} {
			wireTopic := `"` + topic + `"`
			if badControl {
				wireTopic = `"\uD800"`
			}
			pending := []byte(incrementalFeedA + `{"type":1,"target":"feed","arguments":[` + wireTopic + `,` + string(wirePayload) + `,"2026-08-21T10:30:30Z"]}` + "\x1e" + incrementalFeedC + incrementalClose)
			before := bytes.Clone(pending)
			connection := &signalRConnection{pending: pending, requestedTopics: []string{"SessionStatus"}}
			observation := time.Date(2026, 8, 21, 10, 32, 0, 0, time.UTC)
			var wire []liveTimingBatch
			var normalized []normalizedLiveTimingBatch
			err := connection.read(t.Context(), func(_ context.Context, batch liveTimingBatch) error {
				wire = append(wire, batch)
				result, err := normalizeLiveTimingBatch(batch, observation)
				if err == nil {
					normalized = append(normalized, result)
				}
				return err
			})
			wantWire := []liveTimingBatch{incrementalWantFeed("Started", "2026-08-21T10:30:00Z")}
			wantNormalized := []normalizedLiveTimingBatch{{source: liveTimingUpdateSourceFeed, requestedTopics: []string{}, presentTopics: []string{}, observationTime: observation, updates: []normalizedLiveTimingUpdate{{topic: "SessionStatus", payload: json.RawMessage(`{"Status":"Started"}`), timestamp: time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC), source: liveTimingUpdateSourceFeed}}}}
			wantErr := errInvalidLiveTimingData
			if !badControl {
				wantErr = errSignalRClosed
				wantWire = append(wantWire, liveTimingBatch{source: liveTimingUpdateSourceFeed, updates: []liveTimingUpdate{{topic: topic, payload: wirePayload, timestamp: "2026-08-21T10:30:30Z", source: liveTimingUpdateSourceFeed}}}, incrementalWantFeed("Finished", "2026-08-21T10:31:00Z"))
				wantNormalized = append(wantNormalized,
					normalizedLiveTimingBatch{source: liveTimingUpdateSourceFeed, requestedTopics: []string{}, presentTopics: []string{}, observationTime: observation, invalidUnicodeUpdates: 1, updates: []normalizedLiveTimingUpdate{{topic: strings.TrimSuffix(topic, ".z"), payload: json.RawMessage(payload), timestamp: time.Date(2026, 8, 21, 10, 30, 30, 0, time.UTC), source: liveTimingUpdateSourceFeed}}},
					normalizedLiveTimingBatch{source: liveTimingUpdateSourceFeed, requestedTopics: []string{}, presentTopics: []string{}, observationTime: observation, updates: []normalizedLiveTimingUpdate{{topic: "SessionStatus", payload: json.RawMessage(`{"Status":"Finished"}`), timestamp: time.Date(2026, 8, 21, 10, 31, 0, 0, time.UTC), source: liveTimingUpdateSourceFeed}}})
			}
			if !errors.Is(err, wantErr) || !reflect.DeepEqual(wire, wantWire) || !reflect.DeepEqual(normalized, wantNormalized) {
				t.Errorf("compressed=%t badControl=%t: wire=%#v, normalized=%#v, err=%v", compressed, badControl, wire, normalized, err)
			}
			if !bytes.Equal(pending, before) || connection.pending != nil || !reflect.DeepEqual(connection.requestedTopics, []string{"SessionStatus"}) {
				t.Error("record processing changed input or subscription state")
			}
		}
	}
}

func TestUnicodeControlsSnapshotAtomicity(t *testing.T) {
	const payload = `{ "Name":"\uD800", "\uDC00": null }`
	compressed := compressedJSONPayload(t, []byte(payload))
	for _, badKey := range []bool{false, true} {
		key := `"SessionInfo"`
		if badKey {
			key = `"\uD800"`
		}
		pending := []byte(`{"type":3,"invocationId":"0","result":{"CarData.z":` + string(compressed) + `,` + key + `:` + payload + `}}` + "\x1e" + incrementalClose)
		before := bytes.Clone(pending)
		requested := []string{"SessionInfo", "CarData.z", "�"}
		connection := &signalRConnection{pending: pending, requestedTopics: append([]string(nil), requested...)}
		observation := time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC)
		var wire []liveTimingBatch
		var normalized []normalizedLiveTimingBatch
		err := connection.read(t.Context(), func(_ context.Context, batch liveTimingBatch) error {
			wire = append(wire, batch)
			result, err := normalizeLiveTimingBatch(batch, observation)
			if err == nil {
				normalized = append(normalized, result)
			}
			return err
		})
		var wantWire []liveTimingBatch
		var wantNormalized []normalizedLiveTimingBatch
		wantTopics, wantErr := requested, errInvalidLiveTimingData
		if !badKey {
			wantTopics, wantErr = nil, errSignalRClosed
			wantWire = []liveTimingBatch{{source: liveTimingUpdateSourceSnapshot, requestedTopics: requested, presentTopics: []string{"CarData.z", "SessionInfo"}, updates: []liveTimingUpdate{
				{topic: "CarData.z", payload: compressed, source: liveTimingUpdateSourceSnapshot},
				{topic: "SessionInfo", payload: json.RawMessage(payload), source: liveTimingUpdateSourceSnapshot},
			}}}
			wantNormalized = []normalizedLiveTimingBatch{{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"SessionInfo", "CarData", "�"}, presentTopics: []string{"CarData", "SessionInfo"}, observationTime: observation, invalidUnicodeUpdates: 2, updates: []normalizedLiveTimingUpdate{
				{topic: "CarData", payload: json.RawMessage(payload), source: liveTimingUpdateSourceSnapshot},
				{topic: "SessionInfo", payload: json.RawMessage(payload), source: liveTimingUpdateSourceSnapshot},
			}}}
		}
		if !errors.Is(err, wantErr) || !reflect.DeepEqual(wire, wantWire) || !reflect.DeepEqual(normalized, wantNormalized) || !reflect.DeepEqual(connection.requestedTopics, wantTopics) || connection.pending != nil || !bytes.Equal(pending, before) {
			t.Errorf("badKey=%t: wire=%#v normalized=%#v err=%v", badKey, wire, normalized, err)
		}
	}
}
