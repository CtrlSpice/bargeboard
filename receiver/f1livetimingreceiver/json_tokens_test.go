package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

// Synthetic JSON tokens cover Unicode encoding, not any source field grammar.
func TestLosslessJSONString(t *testing.T) {
	for _, test := range []struct {
		name, raw, want string
		err             error
	}{
		{name: "empty", raw: `""`},
		{name: "surrounding JSON whitespace", raw: " \t\r\n\"ok\" \t", want: "ok"},
		{name: "BMP before surrogates", raw: `"\uD7FF"`, want: "\uD7FF"},
		{name: "BMP after surrogates", raw: `"\uE000"`, want: "\uE000"},
		{name: "BMP last", raw: `"\uFFFF"`, want: "\uFFFF"},
		{name: "literal replacement", raw: `"�"`, want: "�"},
		{name: "escaped replacement", raw: `"\uFFFD"`, want: "�"},
		{name: "mixed case pair", raw: `"\ud83D\uDe80"`, want: "🚀"},
		{name: "literal astral", raw: `"🚀"`, want: "🚀"},
		{name: "no double decode", raw: `"\\uD800"`, want: `\uD800`},
		{name: "escaped unicode backslash", raw: `"\u005cuD800"`, want: `\uD800`},
		{name: "escaped quote", raw: `"\"\\\"\uD800"`, err: errJSONScalar},
		{name: "quotes and backslashes", raw: `"\"\\\"\\uD800"`, want: `"\"\uD800`},
		{name: "ordinary escapes", raw: `"\b\f\n\r\t\/\u0000"`, want: "\b\f\n\r\t/\x00"},
		{name: "lone first high", raw: `"\uD800"`, err: errJSONScalar},
		{name: "lone last high", raw: `"\uDBFF"`, err: errJSONScalar},
		{name: "lone first low", raw: `"\uDC00"`, err: errJSONScalar},
		{name: "lone last low", raw: `"\uDFFF"`, err: errJSONScalar},
		{name: "reversed", raw: `"\uDC00\uD800"`, err: errJSONScalar},
		{name: "high high", raw: `"\uD800\uDBFF"`, err: errJSONScalar},
		{name: "low low", raw: `"\uDC00\uDFFF"`, err: errJSONScalar},
		{name: "below low", raw: `"\uD800\uDBFF"`, err: errJSONScalar},
		{name: "above low", raw: `"\uDBFF\uE000"`, err: errJSONScalar},
		{name: "interrupted pair", raw: `"\uD800x\uDC00"`, err: errJSONScalar},
		{name: "escaped low text", raw: `"\uD800\\uDC00"`, err: errJSONScalar},
		{name: "pair then high", raw: `"\uD800\uDC00\uD800"`, err: errJSONScalar},
		{name: "pair then low", raw: `"\uD800\uDC00\uDC00"`, err: errJSONScalar},
		{name: "high then quote", raw: `"\uD800\""`, err: errJSONScalar},
		{name: "raw UTF8", raw: "\"\xff\"", err: errJSONUTF8},
		{name: "UTF8 encoded surrogate", raw: "\"\xed\xa0\x80\"", err: errJSONUTF8},
		{name: "UTF8 before grammar", raw: "\xff", err: errJSONUTF8},
		{name: "syntax before scalar", raw: `"\uD800\x"`, err: errJSONSyntax},
		{name: "bad hex", raw: `"\uD80G"`, err: errJSONSyntax},
		{name: "short hex", raw: `"\uD80"`, err: errJSONSyntax},
		{name: "uppercase escape", raw: `"\UD800"`, err: errJSONSyntax},
		{name: "trailing token", raw: `"ok" "again"`, err: errJSONSyntax},
		{name: "unclosed", raw: `"abc`, err: errJSONSyntax},
		{name: "raw control", raw: "\"\n\"", err: errJSONSyntax},
		{name: "absent", err: errJSONSyntax},
		{name: "null shape", raw: `null`, err: errJSONString},
		{name: "object shape", raw: `{}`, err: errJSONString},
		{name: "array shape", raw: `[]`, err: errJSONString},
		{name: "number shape", raw: `1`, err: errJSONString},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(test.raw)
			got, err := decodeLosslessJSONString(raw)
			if got != test.want || err != test.err || string(raw) != test.raw {
				t.Fatalf("decode = %q, %v; want %q, %v; preserved=%t", got, err, test.want, test.err, string(raw) == test.raw)
			}
		})
	}
}

func TestLosslessJSONStringSurrogateBoundaries(t *testing.T) {
	for _, high := range []uint16{0xD800, 0xDBFF} {
		for _, low := range []uint16{0xDC00, 0xDFFF} {
			raw := fmt.Sprintf(`"\u%04X\u%04x"`, high, low)
			want := string(utf16.DecodeRune(rune(high), rune(low)))
			got, err := decodeLosslessJSONString([]byte(raw))
			if err != nil || got != want {
				t.Errorf("%s: %q, %v; want %q", raw, got, err, want)
			}
		}
	}
	// Exercise every code unit on both sides of the surrogate ranges, including
	// valid pair adjacency; these checks do not depend on Go's lossy JSON oracle.
	for unit := 0xD7FF; unit <= 0xE000; unit++ {
		raw := []byte(fmt.Sprintf(`"\u%04X"`, unit))
		got, err := decodeLosslessJSONString(raw)
		if unit >= 0xD800 && unit <= 0xDFFF {
			if got != "" || err != errJSONScalar {
				t.Fatalf("surrogate %04X: %q, %v", unit, got, err)
			}
		} else if got != string(rune(unit)) || err != nil {
			t.Fatalf("boundary %04X: %q, %v", unit, got, err)
		}
	}
	got, err := decodeLosslessJSONString([]byte(`"\uD800\uDC00\uDBFF\uDFFF"`))
	if got != "\U00010000\U0010FFFF" || err != nil {
		t.Fatalf("adjacent pairs: %q, %v", got, err)
	}
}

func TestRawJSONObjectOriginalMembers(t *testing.T) {
	raw := []byte(" \n{\t" + `"name": { "nested": ["\uD800", {"\uDFFF":null}] }, "na\u006de":-1.25e+3, "\uD800":"\uDC00", "�":true, "escaped\"\\": [false,null,{},[]]` + "\r\n} \t")
	before := bytes.Clone(raw)
	type member struct{ key, value string }
	var got []member
	cursor := 0
	err := visitRawJSONObject(raw, func(key, value json.RawMessage) error {
		got = append(got, member{string(key), string(value)})
		// The seam supplies views, not repaired or reserialized values.
		keyStart := cursor + bytes.Index(raw[cursor:], key)
		valueStart := keyStart + len(key) + bytes.Index(raw[keyStart+len(key):], value)
		if &key[0] != &raw[keyStart] || &value[0] != &raw[valueStart] {
			t.Error("member is not an original input view")
		}
		cursor = valueStart + len(value)
		return nil
	})
	want := []member{
		{`"name"`, `{ "nested": ["\uD800", {"\uDFFF":null}] }`},
		{`"na\u006de"`, `-1.25e+3`},
		{`"\uD800"`, `"\uDC00"`},
		{`"�"`, `true`},
		{`"escaped\"\\"`, `[false,null,{},[]]`},
	}
	if err != nil || !reflect.DeepEqual(got, want) || !bytes.Equal(raw, before) {
		t.Fatalf("members=%#v, err=%v", got, err)
	}
}

func TestRawJSONObjectValidationAndStop(t *testing.T) {
	for _, test := range []struct {
		raw string
		err error
	}{
		{`{}`, nil}, {" \n{ \t }\r", nil},
		{`null`, errJSONObject}, {`[]`, errJSONObject}, {`"x"`, errJSONObject},
		{``, errJSONSyntax}, {`{"a":1,}`, errJSONSyntax}, {`{"a":1} {}`, errJSONSyntax},
		{`{"a":1,"b":"\uZZZZ"}`, errJSONSyntax}, {"{\"a\":1,\"b\":\"\xff\"}", errJSONUTF8},
		{`{"a":1,"b":` + strings.Repeat("[", 10000) + `0` + strings.Repeat("]", 10000) + `}`, errJSONSyntax},
	} {
		calls := 0
		err := visitRawJSONObject([]byte(test.raw), func(_, _ json.RawMessage) error { calls++; return nil })
		if err != test.err || calls != 0 {
			t.Errorf("validation: err=%v, calls=%d; want %v, zero", err, calls, test.err)
		}
	}
	stop := errors.New("caller stop")
	calls := 0
	err := visitRawJSONObject([]byte(`{"a":1,"b":2}`), func(key, value json.RawMessage) error {
		calls++
		if string(key) != `"a"` || string(value) != `1` {
			t.Error("visited beyond caller stop")
		}
		return stop
	})
	if err != stop || calls != 1 {
		t.Fatalf("stop: %v, %d calls", err, calls)
	}
}

func TestRawJSONObjectBoundedStorage(t *testing.T) {
	// Repeated keys are intentionally exposed, not stored or adjudicated here.
	for _, count := range []int{1, 10000} {
		raw := []byte(`{` + strings.Repeat(`"x":null,`, count-1) + `"x":null}`)
		allocs := testing.AllocsPerRun(10, func() {
			calls := 0
			err := visitRawJSONObject(raw, func(key, value json.RawMessage) error {
				calls++
				if string(key) != `"x"` || string(value) != `null` {
					t.Fatal("invalid member")
				}
				return nil
			})
			if err != nil || calls != count {
				t.Fatalf("walk: %v, %d calls", err, calls)
			}
		})
		// Go 1.26.8 json.Valid may allocate its scanner and one-level syntax
		// stack when sync.Pool drops an entry (deliberately randomized by -race).
		// Allow that fixed cost for both sizes, not allocations per member.
		if allocs > 2 {
			t.Errorf("%d members: %v allocations, want at most the validator's fixed two", count, allocs)
		}
	}
	raw := []byte(`{"deep":` + strings.Repeat("[", 9999) + `"\uD800"` + strings.Repeat("]", 9999) + `}`)
	calls := 0
	if err := visitRawJSONObject(raw, func(key, value json.RawMessage) error {
		calls++
		if string(key) != `"deep"` || !bytes.Equal(value, raw[8:len(raw)-1]) {
			t.Error("deep opaque value changed")
		}
		return nil
	}); err != nil || calls != 1 {
		t.Errorf("accepted nesting boundary: %v, %d calls", err, calls)
	}
}

func TestNullableControlStringPreservesState(t *testing.T) {
	for _, test := range []struct {
		raw, want string
		err       error
	}{
		{`null`, "prior", nil}, {`""`, "", nil}, {`"next"`, "next", nil},
		{" \t\r\nnull\t", "prior", nil}, {"\u00a0null", "prior", errJSONSyntax},
		{"null\xff", "prior", errJSONUTF8},
		{`"\uD800"`, "prior", errJSONScalar}, {`123`, "prior", errJSONString},
	} {
		got := "prior"
		err := unmarshalControlString([]byte(test.raw), &got)
		if got != test.want || err != test.err {
			t.Errorf("assignment %s: %q, %v", test.raw, got, err)
		}
	}
}
