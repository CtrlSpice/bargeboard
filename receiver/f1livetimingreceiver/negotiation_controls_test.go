package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

const negotiationTextCapability = `{"transport":"WebSockets","transferFormats":["Text"]}`
const negotiationValidMembers = `"connectionId":"id","availableTransports":[` + negotiationTextCapability + `]`

func TestNegotiationControlsSourceExamples(t *testing.T) {
	raw, err := os.ReadFile("testdata/negotiation/responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name     string
		Response json.RawMessage
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	transports := []negotiateTransport{
		{Transport: "WebSockets", TransferFormats: []jsonControlString{"Text", "Binary"}},
		{Transport: "ServerSentEvents", TransferFormats: []jsonControlString{"Text"}},
		{Transport: "LongPolling", TransferFormats: []jsonControlString{"Text", "Binary"}},
	}
	wants := []struct {
		name    string
		decoded negotiateResponse
		result  negotiation
		reason  string
	}{
		{"version 1", negotiateResponse{ConnectionID: "807809a5-31bf-470d-9e23-afaee35d8a0d", ConnectionToken: "05265228-1e2c-46c5-82a1-6a5bcc3f0143", NegotiateVersion: 1, AvailableTransports: transports}, negotiation{connectionToken: "05265228-1e2c-46c5-82a1-6a5bcc3f0143"}, ""},
		{"version 0", negotiateResponse{ConnectionID: "807809a5-31bf-470d-9e23-afaee35d8a0d", AvailableTransports: transports}, negotiation{connectionToken: "807809a5-31bf-470d-9e23-afaee35d8a0d"}, ""},
		{"redirect", negotiateResponse{URL: "https://myapp.com/chat", AccessToken: "accessToken"}, negotiation{}, "SignalR negotiation redirects are not supported"},
		{"error", negotiateResponse{ErrorNonempty: true}, negotiation{}, "SignalR negotiation rejected the connection"},
	}
	if len(fixtures) != len(wants) {
		t.Fatalf("fixture count = %d, want %d", len(fixtures), len(wants))
	}
	for i, fixture := range fixtures {
		want := wants[i]
		t.Run(want.name, func(t *testing.T) {
			if fixture.Name != want.name {
				t.Fatalf("fixture name = %q", fixture.Name)
			}
			assertNegotiationResult(t, string(fixture.Response), want.decoded, want.result, want.reason)
		})
	}
}

func assertNegotiationResult(t *testing.T, raw string, wantDecoded negotiateResponse, want negotiation, reason string) {
	t.Helper()
	input := []byte(raw)
	decoded, err := decodeNegotiateResponse(input)
	if err != nil || !reflect.DeepEqual(decoded, wantDecoded) {
		t.Fatalf("decoded = %#v, %v; want %#v", decoded, err, wantDecoded)
	}
	got, err := parseNegotiateResponse(input)
	if got != want {
		t.Errorf("parsed result mismatch: got token %q, want %q", got.connectionToken, want.connectionToken)
	}
	if reason == "" {
		if err != nil {
			t.Errorf("parse error = %v", err)
		}
	} else if !errors.Is(err, errInvalidLiveTimingData) || err.Error() != "invalid F1 live timing data: "+reason {
		t.Errorf("parse error = %v, want %s", err, reason)
	}
	if string(input) != raw {
		t.Error("decoding changed input bytes")
	}
	clear(input)
	if !reflect.DeepEqual(decoded, wantDecoded) || got != want {
		t.Error("result retains mutable input storage")
	}
}

// Synthetic mutations of the attributed negotiation examples in
// testdata/negotiation; these are not captured F1 wire responses.
func TestNegotiationControlsRejectionErasureAndInheritance(t *testing.T) {
	for _, raw := range []string{
		`{"connectionId":"id","error":"denied","ERROR":"","availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]}`,
		`{"connectionId":"id","availableTransports":[{"transport":"WebSockets","transferFormats":["Binary"]}],"availableTransports":[{"transferFormats":["Text"]}]}`,
	} {
		assertNegotiationDecodeFailure(t, raw)
	}
}

func assertNegotiationDecodeFailure(t *testing.T, raw string) {
	t.Helper()
	input := []byte(raw)
	before := bytes.Clone(input)
	decoded, decodeErr := decodeNegotiateResponse(input)
	if decodeErr == nil || !reflect.DeepEqual(decoded, negotiateResponse{}) {
		t.Errorf("decode accepted invalid controls or returned partial state: %#v, %v", decoded, decodeErr)
	}
	got, err := parseNegotiateResponse(input)
	if got != (negotiation{}) || !errors.Is(err, errInvalidLiveTimingData) || err.Error() != "invalid F1 live timing data: decode SignalR negotiation response" {
		t.Errorf("parse accepted invalid controls or returned partial state: %#v, %v", got, err)
	}
	if !bytes.Equal(input, before) {
		t.Error("decoding changed input bytes")
	}
}

func TestNegotiationControlsKnownMemberMatrix(t *testing.T) {
	for _, scope := range []struct {
		name    string
		fields  []string
		values  []string
		enclose func(string) string
	}{
		{"response", []string{"connectionId", "connectionToken", "negotiateVersion", "url", "accessToken", "error", "availableTransports"},
			[]string{`"id"`, `"token"`, `1`, `""`, `""`, `""`, `[` + negotiationTextCapability + `]`},
			func(members string) string { return `{` + members + `}` }},
		{"selected capability", []string{"transport", "transferFormats"}, []string{`"WebSockets"`, `["Text"]`},
			func(members string) string { return `{"connectionId":"id","availableTransports":[{` + members + `}]}` }},
		// A later irrelevant entry must still be checked after a usable capability.
		{"irrelevant capability", []string{"transport", "transferFormats"}, []string{`"LongPolling"`, `["Binary"]`},
			func(members string) string {
				return `{"connectionId":"id","availableTransports":[` + negotiationTextCapability + `,{` + members + `}]}`
			}},
	} {
		for index, name := range scope.fields {
			value := scope.values[index]
			key := `"` + name + `"`
			escaped := fmt.Sprintf(`"\u%04x%s"`, name[0], name[1:])
			alias := `"` + strings.ToUpper(name) + `"`
			escapedAlias := fmt.Sprintf(`"\u%04x%s"`, strings.ToUpper(name)[0], name[1:])
			member := key + `:` + value
			for _, mutation := range []struct{ name, members string }{
				{"identical duplicate", member + `,` + member},
				{"escaped duplicate", member + `,` + escaped + `:` + value},
				{"escaped first duplicate", escaped + `:` + value + `,` + member},
				{"case alias alone", alias + `:` + value},
				{"escaped case alias alone", escapedAlias + `:` + value},
				{"case alias last", member + `,` + alias + `:` + value},
				{"case alias first", alias + `:` + value + `,` + member},
				{"null alone", key + `: null `},
				{"null last", member + `,` + key + `:null`},
				{"null first", key + `:null,` + member},
				{"wrong type", key + `:true`},
			} {
				t.Run(scope.name+"/"+name+"/"+mutation.name, func(t *testing.T) {
					members := make([]string, len(scope.fields))
					for i, field := range scope.fields {
						members[i] = `"` + field + `":` + scope.values[i]
					}
					members[index] = mutation.members
					assertNegotiationDecodeFailure(t, scope.enclose(strings.Join(members, ",")))
				})
			}
		}
	}
}

func TestNegotiationControlsExactDecodedNamesAndOpaqueExtensions(t *testing.T) {
	// Every known name is escaped but decodes to the canonical spelling.
	raw := `{"\u0063onnectionId":"id","\u0063onnectionToken":"token","\u006eegotiateVersion":1,"\u0075rl":"","\u0061ccessToken":"","\u0065rror":"","\u0061vailableTransports":[{"\u0074ransport":"WebSockets","\u0074ransferFormats":["Text"]}]}`
	want := negotiateResponse{ConnectionID: "id", ConnectionToken: "token", NegotiateVersion: 1, AvailableTransports: []negotiateTransport{{Transport: "WebSockets", TransferFormats: []jsonControlString{"Text"}}}}
	assertNegotiationResult(t, raw, want, negotiation{connectionToken: "token"}, "")
	// Nonempty values prove that escaped rejection controls were recognized too.
	raw = strings.Replace(raw, `"\u0075rl":""`, `"\u0075rl":"https://synthetic.test/redirect"`, 1)
	raw = strings.Replace(raw, `"\u0061ccessToken":""`, `"\u0061ccessToken":"redirect-token"`, 1)
	raw = strings.Replace(raw, `"\u0065rror":""`, `"\u0065rror":"\uD800"`, 1)
	want.URL, want.AccessToken, want.ErrorNonempty = "https://synthetic.test/redirect", "redirect-token", true
	assertNegotiationResult(t, raw, want, negotiation{}, "SignalR negotiation rejected the connection")
	// Decoding once does not interpret a literal backslash escape a second time,
	// normalize confusables, trim keys, or inspect nested unknown values.
	const extensions = `"connection\\u0049d":"\uD800","connectionId ":null,"cоnnectionId":false,"extension":{"\uD800":"\uDC00","error":null},"exten\u0073ion":"\uD800","�":null,"\uFFFD":[],"\uD83D\uDE80":"\uD800"`
	raw = `{"connectionId":"id",` + extensions + `,"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"],` + extensions + `}]}`
	want = negotiateResponse{ConnectionID: "id", AvailableTransports: []negotiateTransport{{Transport: "WebSockets", TransferFormats: []jsonControlString{"Text"}}}}
	assertNegotiationResult(t, raw, want, negotiation{connectionToken: "id"}, "")
	for _, member := range []string{`"acceſsToken":""`, `"connectionToKen":""`, `"availableTranſports":[]`} {
		assertNegotiationDecodeFailure(t, `{`+negotiationValidMembers+`,`+member+`}`)
	}
	for _, member := range []string{`"tranſport":"LongPolling"`, `"transferFormatſ":[]`} {
		assertNegotiationDecodeFailure(t, `{"connectionId":"id","availableTransports":[`+negotiationTextCapability+`,{`+member+`}]}`)
	}
}

func TestNegotiationControlsArrayEntriesAndLosslessStrings(t *testing.T) {
	for _, entry := range []string{
		`null`, `1`, `[]`, `"WebSockets"`,
		`{"transport":null}`, `{"transferFormats":null}`, `{"transferFormats":[null]}`,
		`{"transport":"WebSockets","transferFormats":["Text",null]}`,
		`{"transport":"WebSockets","transferFormats":[null,"Text"]}`,
		`{"transport":"LongPolling","transferFormats":["Binary",null]}`,
		`{"transport":"\uD800"}`, `{"transferFormats":["Text","\uDC00"]}`,
		`{"\uD800":null}`, `{"�":1,"\uD800":2}`,
	} {
		for _, entries := range []string{entry + `,` + negotiationTextCapability, negotiationTextCapability + `,` + entry} {
			assertNegotiationDecodeFailure(t, `{"connectionId":"id","availableTransports":[`+entries+`]}`)
		}
	}
	for _, member := range []string{
		`"connectionId":"\uD800"`, `"connectionToken":"\uD800"`, `"url":"\uD800"`, `"accessToken":"\uD800"`,
		`"\uD800":null`, `"�":null,"\uD800":null`,
	} {
		// No duplicate is needed to reject malformed strings, including unused v0 tokens.
		assertNegotiationDecodeFailure(t, `{`+member+`,"availableTransports":[`+negotiationTextCapability+`]}`)
	}
}

func TestNegotiationControlsPreservedDomainDecisions(t *testing.T) {
	capability := []negotiateTransport{{Transport: "WebSockets", TransferFormats: []jsonControlString{"Text"}}}
	for _, version := range []string{"", `,"negotiateVersion":0`, `,"negotiateVersion":1`} {
		want := negotiateResponse{ConnectionID: "id", ConnectionToken: "token", AvailableTransports: capability}
		result := negotiation{connectionToken: "id"}
		if strings.HasSuffix(version, ":1") {
			want.NegotiateVersion, result.connectionToken = 1, "token"
		}
		assertNegotiationResult(t, `{`+negotiationValidMembers+`,"connectionToken":"token","error":"","url":"","accessToken":""`+version+`}`, want, result, "")
	}
	for _, test := range []struct {
		raw     string
		decoded negotiateResponse
		reason  string
	}{
		{`{"connectionId":""}`, negotiateResponse{}, "SignalR negotiation did not return a connection token"},
		{`{"connectionId":"","connectionToken":"token","negotiateVersion":1}`, negotiateResponse{ConnectionToken: "token", NegotiateVersion: 1}, "SignalR negotiation did not return a connection ID"},
		{`{"connectionId":"id","connectionToken":"","negotiateVersion":1}`, negotiateResponse{ConnectionID: "id", NegotiateVersion: 1}, "SignalR negotiation did not return a connection token"},
		{`{"connectionId":"id","negotiateVersion":2}`, negotiateResponse{ConnectionID: "id", NegotiateVersion: 2}, "SignalR negotiation returned unsupported version 2"},
		{`{"connectionId":"id","negotiateVersion":-1}`, negotiateResponse{ConnectionID: "id", NegotiateVersion: -1}, "SignalR negotiation returned unsupported version -1"},
		{`{"url":"https://synthetic.test/private"}`, negotiateResponse{URL: "https://synthetic.test/private"}, "SignalR negotiation redirects are not supported"},
		{`{"accessToken":"private"}`, negotiateResponse{AccessToken: "private"}, "SignalR negotiation redirects are not supported"},
		{`{"error":"\uD800"}`, negotiateResponse{ErrorNonempty: true}, "SignalR negotiation rejected the connection"},
	} {
		assertNegotiationResult(t, test.raw, test.decoded, negotiation{}, test.reason)
	}
	for _, test := range []struct {
		entries string
		want    []negotiateTransport
	}{
		{"", []negotiateTransport{}},
		{`{}`, []negotiateTransport{{}}},
		{`{"transport":"WebSockets"}`, []negotiateTransport{{Transport: "WebSockets"}}},
		{`{"transferFormats":["Text"]}`, []negotiateTransport{{TransferFormats: []jsonControlString{"Text"}}}},
		{`{"transport":"WebSockets","transferFormats":[]}`, []negotiateTransport{{Transport: "WebSockets", TransferFormats: []jsonControlString{}}}},
		{`{"transport":"","transferFormats":["Text"]}`, []negotiateTransport{{TransferFormats: []jsonControlString{"Text"}}}},
		{`{"transport":"WebSockets","transferFormats":[""]}`, []negotiateTransport{{Transport: "WebSockets", TransferFormats: []jsonControlString{""}}}},
		{`{"transport":"WebSockets","transferFormats":["Binary"]},{"transferFormats":["Text"]}`, []negotiateTransport{{Transport: "WebSockets", TransferFormats: []jsonControlString{"Binary"}}, {TransferFormats: []jsonControlString{"Text"}}}},
	} {
		want := negotiateResponse{ConnectionID: "id", AvailableTransports: test.want}
		assertNegotiationResult(t, `{"connectionId":"id","availableTransports":[`+test.entries+`]}`, want, negotiation{}, "SignalR negotiation does not support WebSockets with text frames")
		if test.entries != "" {
			want.AvailableTransports = append(append([]negotiateTransport(nil), test.want...), capability...)
			assertNegotiationResult(t, `{"connectionId":"id","availableTransports":[`+test.entries+`,`+negotiationTextCapability+`]}`, want, negotiation{connectionToken: "id"}, "")
		}
	}
}

func TestNegotiationControlsCapabilityDestinationAtomicity(t *testing.T) {
	for _, raw := range []string{
		`null`, `{"transport":"new","transferFormats":["Text",null]}`,
		`{"transport":"new","transport":"new"}`, `{"transport":"new","TRANSFERFORMATS":[]}`,
		`{"transferFormats":["Text"],"transport":"\uD800"}`,
		`{"transport":"new","extension":1,"\uD800":false}`, `{"transport":"new",`,
	} {
		originalFormats := []jsonControlString{"Binary", "private-original"}
		destination := negotiateTransport{Transport: "original", TransferFormats: originalFormats}
		want := negotiateTransport{Transport: "original", TransferFormats: []jsonControlString{"Binary", "private-original"}}
		input := []byte(raw)
		err := destination.UnmarshalJSON(input)
		if err == nil || !reflect.DeepEqual(destination, want) || &destination.TransferFormats[0] != &originalFormats[0] || !reflect.DeepEqual(originalFormats, want.TransferFormats) || string(input) != raw {
			t.Errorf("failed decode mutated destination/storage/input: %#v, %v", destination, err)
		}
	}
	for _, test := range []struct {
		raw  string
		want negotiateTransport
	}{
		{`{}`, negotiateTransport{}},
		{`{"transferFormats":["Text"]}`, negotiateTransport{TransferFormats: []jsonControlString{"Text"}}},
		{`{"transport":"LongPolling"}`, negotiateTransport{Transport: "LongPolling"}},
	} {
		destination := negotiateTransport{Transport: "WebSockets", TransferFormats: []jsonControlString{"Binary"}}
		input := []byte(test.raw)
		if err := json.Unmarshal(input, &destination); err != nil || !reflect.DeepEqual(destination, test.want) || string(input) != test.raw {
			t.Errorf("replacement inherited omitted fields: %#v, %v", destination, err)
		}
	}
}

func TestNegotiationControlsSetupPrevention(t *testing.T) {
	for _, raw := range []string{
		`{` + negotiationValidMembers + `,"error":"private-denial","ERROR":""}`,
		`{"connectionId":"private-id","availableTransports":[{"transport":"WebSockets","transferFormats":["Binary"]}],"availableTransports":[{"transferFormats":["Text"]}]}`,
		`{` + negotiationValidMembers + `,"connectionToken":"private-token","connectionToken":"private-token"}`,
		`{` + negotiationValidMembers + `,"url":"https://synthetic.test/private-redirect","url":""}`,
		`{` + negotiationValidMembers + `,"error":null}`,
		`{"connectionId":"private-id","availableTransports":[` + negotiationTextCapability + `,null]}`,
		`{"connectionId":"private-id","availableTransports":[` + negotiationTextCapability + `,{"transport":"LongPolling","transferFormats":[null]}]}`,
	} {
		cfg := connectionTestConfig(t, "http://synthetic.test/private-path")
		// Any attempt to construct the upgrade URL would replace the expected error.
		cfg.Endpoint = ":invalid-websocket-url"
		var requests []string
		preflight := &preflightTestBody{}
		body := &countedHTTPBody{Reader: strings.NewReader(raw)}
		client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			requests = append(requests, request.Method+" "+request.URL.String())
			switch request.Method {
			case http.MethodOptions:
				return &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"AWSALBCORS=private-affinity; Path=/"}}, Body: preflight}, nil
			case http.MethodPost:
				return &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"AWSALBCORS=private-replacement; Path=/"}}, Body: body}, nil
			default:
				t.Error("invalid negotiation reached upgrade")
				return nil, errors.New("unexpected request")
			}
		})}
		connection, err := connectSignalR(t.Context(), client, cfg)
		wantRequests := []string{"OPTIONS " + cfg.NegotiateEndpoint, "POST " + cfg.NegotiateEndpoint + "?negotiateVersion=1"}
		if connection != nil || !reflect.DeepEqual(requests, wantRequests) || *preflight != (preflightTestBody{closes: 1}) || body.closes != 1 || body.bytes != len(raw) {
			t.Fatalf("setup leaked state or lost cleanup: connection=%v requests=%v preflight=%+v body=%+v", connection, requests, preflight, body)
		}
		assertNegotiationSetupError(t, err)
		// Exercise the HTTP seam directly to prove failed decoding cannot return
		// either a partial identity or cookies from the invalid response.
		requests = nil
		body = &countedHTTPBody{Reader: strings.NewReader(raw)}
		got, cookies, err := negotiate(t.Context(), client, cfg.NegotiateEndpoint, connectionCredentials{}, nil)
		if got != (negotiation{}) || cookies != nil || !reflect.DeepEqual(requests, wantRequests[1:]) || body.closes != 1 || body.bytes != len(raw) {
			t.Fatalf("negotiation leaked state/cookies or lost cleanup: %#v, %v, %v", got, cookies, err)
		}
		assertNegotiationSetupError(t, err)
	}
}

func assertNegotiationSetupError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errInvalidLiveTimingData) || err.Error() != "invalid F1 live timing data: decode SignalR negotiation response" || errors.Unwrap(err) != errInvalidLiveTimingData || asSetupHTTPError(err) != nil {
		t.Fatalf("unsanitized or misclassified setup error: %v", err)
	}
}

func TestNegotiationControlsDepthAndByteBounds(t *testing.T) {
	for _, depth := range []int{9997, 9998} {
		raw := `{"connectionId":"id","availableTransports":[{"transport":"WebSockets","transferFormats":["Text"],"extension":` + nestedJSONArrays(depth) + `}]}`
		if depth == 9998 {
			assertNegotiationDecodeFailure(t, raw)
		} else {
			want := negotiateResponse{ConnectionID: "id", AvailableTransports: []negotiateTransport{{Transport: "WebSockets", TransferFormats: []jsonControlString{"Text"}}}}
			assertNegotiationResult(t, raw, want, negotiation{connectionToken: "id"}, "")
		}
	}
	base := `{` + negotiationValidMembers + `,"extension":""}`
	for _, extra := range []int{0, 1} {
		raw := strings.Replace(base, `"extension":""`, `"extension":"`+strings.Repeat("x", maxNegotiateResponseSize-len(base)+extra)+`"`, 1)
		body := &countedHTTPBody{Reader: strings.NewReader(raw)}
		client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: body}, nil
		})}
		got, cookies, err := negotiate(t.Context(), client, "http://synthetic.test", connectionCredentials{}, nil)
		if extra == 0 {
			if got != (negotiation{connectionToken: "id"}) || !reflect.DeepEqual(cookies, []*http.Cookie{}) || err != nil {
				t.Errorf("exact byte cap rejected: %#v, %v, %v", got, cookies, err)
			}
		} else if got != (negotiation{}) || cookies != nil || !errors.Is(err, errInvalidLiveTimingData) || err.Error() != "invalid F1 live timing data: SignalR negotiation response exceeds 65536 bytes" {
			t.Errorf("over byte cap accepted: %#v, %v, %v", got, cookies, err)
		}
		if body.closes != 1 || body.bytes != len(raw) {
			t.Errorf("bounded body read/close: %+v", body)
		}
	}
}

func TestNegotiationControlsResultShape(t *testing.T) {
	// New fields require an explicit review of the complete-result oracles.
	for _, test := range []struct {
		value any
		names []string
	}{
		{negotiation{}, []string{"connectionToken"}},
		{negotiateResponse{}, []string{"ConnectionID", "ConnectionToken", "NegotiateVersion", "URL", "AccessToken", "ErrorNonempty", "AvailableTransports"}},
		{negotiateTransport{}, []string{"Transport", "TransferFormats"}},
	} {
		typeOf := reflect.TypeOf(test.value)
		var names []string
		for i := 0; i < typeOf.NumField(); i++ {
			names = append(names, typeOf.Field(i).Name)
		}
		if !reflect.DeepEqual(names, test.names) {
			t.Errorf("%s fields = %v, want %v; update result oracles", typeOf.Name(), names, test.names)
		}
	}
}
