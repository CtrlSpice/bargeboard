package f1id

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestDecodeLosslessString(t *testing.T) {
	for raw, want := range map[string]string{
		`""`: "", `"�"`: "�", `"\ufffd"`: "�", `"\\uD800"`: `\uD800`,
		`"\uD83D\uDE80"`: "🚀", `"\ud800\udc00"`: "\U00010000", `"\udbff\udfff"`: "\U0010ffff",
		" \t\"x\"\n": "x",
	} {
		got, err := DecodeLosslessString([]byte(raw))
		if err != nil || got != want || HasInvalidJSONScalars([]byte(raw)) {
			t.Errorf("%s: %q, %v", raw, got, err)
		}
	}
	for raw, want := range map[string]error{
		`"\ud800"`: ErrJSONScalar, `"\udc00"`: ErrJSONScalar, `"\ud800\ud800"`: ErrJSONScalar,
		`"\ud800\\udc00"`: ErrJSONScalar, `"\ud800\uabcd"`: ErrJSONScalar,
		"\"\xff\"": errJSONUTF8, `"\u123"`: errJSONSyntax, `"a" "b"`: errJSONSyntax, `null`: errJSONString,
	} {
		got, err := DecodeLosslessString([]byte(raw))
		if got != "" || !errors.Is(err, want) {
			t.Errorf("%q: %q, %v", raw, got, err)
		}
	}
	if !HasInvalidJSONScalars([]byte(`[{"\ud800":null}]`)) {
		t.Fatal("missed nested key")
	}
}

func TestJSONVisitorTokens(t *testing.T) {
	var members [][3]string
	err := VisitObject([]byte(` { "a": [1, {"x":"\"},]"}], "\u0061" : "\uD800", "🚀": false } `), func(key string, k, v json.RawMessage) error {
		members = append(members, [3]string{key, string(k), string(v)})
		return nil
	})
	want := [][3]string{{"a", `"a"`, `[1, {"x":"\"},]"}]`}, {"a", `"\u0061"`, `"\uD800"`}, {"🚀", `"🚀"`, `false`}}
	if err != nil || !reflect.DeepEqual(members, want) {
		t.Fatalf("members %#v, %v", members, err)
	}
	var elems []string
	err = VisitArray([]byte(` [ null, [ 1,2 ], {"x":"\\\""}, "\ud800" ] `), func(i int, v json.RawMessage) error {
		if i != len(elems) {
			t.Fatalf("index %d", i)
		}
		elems = append(elems, string(v))
		return nil
	})
	if err != nil || !reflect.DeepEqual(elems, []string{`null`, `[ 1,2 ]`, `{"x":"\\\""}`, `"\ud800"`}) {
		t.Fatalf("elements %#v, %v", elems, err)
	}
}

func TestJSONVisitorFailures(t *testing.T) {
	stop := errors.New("stop")
	calls := 0
	object := func(_ string, _, _ json.RawMessage) error { calls++; return stop }
	array := func(_ int, _ json.RawMessage) error { calls++; return stop }
	for raw, want := range map[string]error{
		`{"x":1,"y":}`: errJSONSyntax, `{} null`: errJSONSyntax, "{\"x\":1,\"\xff\":2}": errJSONUTF8,
		`[]`: errJSONObject, `{"\uDC00":0}`: ErrJSONScalar, `{}`: nil,
	} {
		if err := VisitObject([]byte(raw), object); err != want || calls != 0 {
			t.Fatalf("%q: %v, calls %d", raw, err, calls)
		}
	}
	for raw, want := range map[string]error{
		`[1,2,]`: errJSONSyntax, `[1] []`: errJSONSyntax, "[1,\"\xff\"]": errJSONUTF8, `{}`: errJSONArray, `[]`: nil,
	} {
		if err := VisitArray([]byte(raw), array); err != want || calls != 0 {
			t.Fatalf("%q: %v, calls %d", raw, err, calls)
		}
	}
	if err := VisitObject([]byte(`{"x":1,"y":2}`), object); err != stop || calls != 1 {
		t.Fatal("object ignored callback error")
	}
	if err := VisitArray([]byte(`[1,2]`), array); err != stop || calls != 2 {
		t.Fatal("array ignored callback error")
	}
}

func TestCanonicalIntegers(t *testing.T) {
	for _, tc := range []struct {
		raw                 string
		value               int64
		signed, nonneg, pos bool
	}{
		{"0", 0, true, true, false}, {"1", 1, true, true, true}, {"-1", -1, true, false, false},
		{"9223372036854775807", math.MaxInt64, true, true, true}, {"-9223372036854775808", math.MinInt64, true, false, false},
	} {
		check := func(ok bool) func(int64, bool) {
			return func(got int64, valid bool) {
				t.Helper()
				if valid != ok || ok && got != tc.value || !ok && got != 0 {
					t.Errorf("%q: got %d,%v", tc.raw, got, valid)
				}
			}
		}
		check(tc.signed)(ParseSignedCanonicalInt64([]byte(tc.raw)))
		check(tc.nonneg)(ParseNonNegativeCanonicalInt64([]byte(tc.raw)))
		check(tc.pos)(ParsePositiveCanonicalInt64([]byte(tc.raw)))
	}
	for _, raw := range []string{"", " ", " 1", "1 ", "00", "01", "-01", "-0", "+1", "1.0", "1e2", "1_0", "١", `"1"`, "9223372036854775808", "-9223372036854775809"} {
		for _, parse := range []func([]byte) (int64, bool){ParseSignedCanonicalInt64, ParseNonNegativeCanonicalInt64, ParsePositiveCanonicalInt64} {
			if n, ok := parse([]byte(raw)); n != 0 || ok {
				t.Errorf("accepted %q: %d,%v", raw, n, ok)
			}
		}
	}
}

func TestStrictRFC3339(t *testing.T) {
	for raw, want := range map[string]string{
		"2024-02-29T12:00:01Z":                "2024-02-29T12:00:01Z",
		"2024-02-29T00:15:00.1+01:00":         "2024-02-28T23:15:00.1Z",
		"2025-12-31T23:45:00.123456789-01:30": "2026-01-01T01:15:00.123456789Z",
	} {
		got, ok := ParseStrictRFC3339(raw)
		if !ok || got.Location() != time.UTC || got.Format(time.RFC3339Nano) != want {
			t.Errorf("%s: %v,%v", raw, got, ok)
		}
	}
	for _, raw := range []string{"2023-02-29T12:00:00Z", "2025-01-01 12:00:00Z", "2025-01-01T1:00:00Z", "2025-01-01T24:00:00Z", "2025-01-01T12:00:60Z", "2025-01-01T12:00:00", "2025-01-01T12:00:00z", "2025-01-01T12:00:00.Z", "2025-01-01T12:00:00,1Z", "2025-01-01T12:00:00.1234567890Z", "2025-01-01T12:00:00+24:00", "2025-01-01T12:00:00-01:60", "2025-01-01T12:00:00Z\n"} {
		if got, ok := ParseStrictRFC3339(raw); ok || !got.IsZero() {
			t.Errorf("accepted %q", raw)
		}
	}
}
