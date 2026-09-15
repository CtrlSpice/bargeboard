package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"unicode/utf8"
)

// Synthetic outer-grammar probes, also used as the fuzz seed corpus. Invalid
// suffixes follow valid first members to catch accidental early callbacks.
func rawJSONObjectGrammarCorpus() []string {
	return []string{
		``, ` `, `{`, `}`, `[]`, `null`, `0`, `true`, `"text"`,
		`{}`, " \t\r\n{ \n}\t", `{"x":null}`, `{"x":[]}`, `{"x":{}}`,
		`{"x":""}`, `{"x":"\"}:[,\\","y":-1.2e+3}`, `{"x":"\\uD800"}`,
		`{"x":true,"\u0078":false,"\uD800":"\uDC00","�":{"\uD800":null}}`,
		`{"x":[null,false,0,"{}[]",{"a":[]}]}`, `{"x":1,"y":2}`, "{\"x\"\t:\r1\n,\t\"y\" : 2}",
		`{,}`, `{"x"}`, `{"x":}`, `{"x":,}`, `{"x" 1}`, `{"x"::1}`,
		`{"x":1,}`, `{"x":1,,"y":2}`, `{"x":1 "y":2}`, `{"x":1,"y" 2}`,
		`{"x":1,`, `{"x":1,"y":`, `{"x":1,"y": `, `{"x":1,"y":}`, `{"x":1,"y":,}`,
		`{"x":1,"y":[]]}`, `{"x":1,"y":{]}`, `{"x":1,"y":[}}`, `{"x":1,"y":[{]}}`,
		`{"x":1,"y":null true}`, `{"x":1,"y":01}`, `{"x":1,"y":1e}`, `{"x":1,"y":tru}`,
		`{"x":1,"y":"unterminated}`, `{"x":1,"y":"\`, `{"x":1,"y":"\u123"}`,
		`{"x":1,"y":"\q"}`, `{"x":1,"\q":2}`, `{"x":1,"y`, `{"x":1,"y\`,
		`{"x":1} null`, `{"x":1}{}`, `{"x":1}}`, `{"x":1},`, `{}[]`, `{}{`,
		`{"x":1,2:3}`, `{"x":1,null:3}`, `{"x":1,[]:3}`, `{"x":1,"y";2}`,
		"\u00a0{}", "{}\u00a0", "{\u00a0\"x\":1}", "{\"x\":1,\v\"y\":2}",
		"{\"x\"\f:1}", "{\"x\":\v1}", "{\"x\":1\x00}", "{\"x\":1,\"y\":\"\n\"}",
		"{\"x\":1,\"y\":\"\xff\"}", "{\"x\":1,\"\xff\":null}", "\xff{}",
	}
}

func TestRawJSONObjectMembersGrammarEquivalence(t *testing.T) {
	for index, raw := range rawJSONObjectGrammarCorpus() {
		t.Run(fmt.Sprintf("corpus-%d", index), func(t *testing.T) {
			checkRawJSONObjectGrammar(t, []byte(raw))
		})
	}
	// Truncate at every byte through escaped quotes, backslashes, UTF-8, and
	// nested delimiters, then corrupt each byte with outer-grammar punctuation.
	raw := []byte(` {"x":"é\\\"","\uD800":[true,{"a":null}],"x":-1.5e+2} `)
	for end := 0; end <= len(raw); end++ {
		checkRawJSONObjectGrammar(t, bytes.Clone(raw[:end]))
	}
	for index := range raw {
		for _, replacement := range []byte{'{', '}', '[', ']', ':', ',', '"', '\\', ' ', '\t', '\n', '\r', 0, 0xff} {
			probe := bytes.Clone(raw)
			probe[index] = replacement
			checkRawJSONObjectGrammar(t, probe)
		}
	}
}

func FuzzRawJSONObjectMembersGrammar(f *testing.F) {
	for _, raw := range rawJSONObjectGrammarCorpus() {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		// At this byte bound no JSON object can reach either nesting limit, so
		// json.Valid is an independent acceptance oracle for BOTH profiles.
		if len(raw) > 4096 {
			return
		}
		checkRawJSONObjectGrammar(t, raw)
	})
}

func checkRawJSONObjectGrammar(t *testing.T, raw []byte) {
	t.Helper()
	before := bytes.Clone(raw)
	trimmed := bytes.Trim(raw, " \t\r\n")
	var wantErr error
	switch {
	case !utf8.Valid(raw):
		wantErr = errJSONUTF8
	case !json.Valid(raw):
		wantErr = errJSONSyntax
	case len(trimmed) == 0 || trimmed[0] != '{':
		wantErr = errJSONObject
	}
	want, legacyErr := legacyJSONMemberOracle(raw)
	if (legacyErr == nil) != (wantErr == nil) {
		t.Fatalf("legacy/whole grammar disagreement for %q: %v / %v", raw, legacyErr, wantErr)
	}
	for _, profile := range []struct {
		name  string
		visit func([]byte, func(json.RawMessage, json.RawMessage) error) error
	}{
		{"whole", visitRawJSONObject}, {"members", visitRawJSONObjectMembers},
	} {
		var got []rawJSONTestMember
		cursor := 0
		err := profile.visit(raw, func(key, value json.RawMessage) error {
			got = append(got, rawJSONTestMember{string(key), string(value)})
			keyOffset := bytes.Index(raw[cursor:], key)
			if len(key) == 0 || keyOffset < 0 {
				t.Fatalf("%s: missing original key view", profile.name)
			}
			keyStart := cursor + keyOffset
			cursor = keyStart + len(key)
			valueOffset := bytes.Index(raw[cursor:], value)
			if len(value) == 0 || valueOffset < 0 {
				t.Fatalf("%s: missing original value view", profile.name)
			}
			valueStart := cursor + valueOffset
			if &key[0] != &raw[keyStart] || &value[0] != &raw[valueStart] {
				t.Errorf("%s: callback does not expose raw input views", profile.name)
			}
			cursor = valueStart + len(value)
			return nil
		})
		if err != wantErr || !reflect.DeepEqual(got, want) || !bytes.Equal(raw, before) {
			t.Fatalf("%s input %q: err=%v want=%v; members=%#v want=%#v; preserved=%t", profile.name, raw, err, wantErr, got, want, bytes.Equal(raw, before))
		}
	}
}

func TestRawJSONObjectMembersValidationBeforeCallbacks(t *testing.T) {
	stop := errors.New("caller stop")
	for _, test := range []struct {
		raw   string
		err   error
		calls int
	}{
		{`{"x":1,"y":2}`, stop, 1},
		{`{"x":1,"y":}`, errJSONSyntax, 0},
		{`{"x":1,"y":2} trailing`, errJSONSyntax, 0},
		{`{"x":1,"y":` + nestedJSONArrays(10001) + `}`, errJSONSyntax, 0},
		{`{"\uD800":1,"y":2}`, stop, 1}, // Scalar policy still belongs to the caller.
	} {
		calls := 0
		err := visitRawJSONObjectMembers([]byte(test.raw), func(key, value json.RawMessage) error {
			calls++
			if (string(key) != `"x"` && string(key) != `"\uD800"`) || string(value) != `1` {
				t.Error("visited beyond caller stop")
			}
			return stop
		})
		if err != test.err || calls != test.calls {
			t.Errorf("validation before callbacks: err=%v calls=%d; want %v, %d", err, calls, test.err, test.calls)
		}
	}
}
