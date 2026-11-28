package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1engine"
)

// Synthetic encoding probes exercise input quality, not source field semantics.
func TestPayloadQualityScalarScan(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"non-string scalars", `[null,true,false,0,-1.2e3]`, false},
		{"empty containers", `[{},[],""]`, false},
		{"replacement and literal astral", `["�","\uFFFD","🚀"]`, false},
		{"BMP boundaries", `["\uD7FF","\uE000","\uFFFF"]`, false},
		{"pair boundaries", `["\uD800\uDC00","\uDBFF\uDFFF"]`, false},
		{"mixed case pairs", `{"\ud83D\uDe80":"\uD800\uDC00\uDBFF\uDFFF"}`, false},
		{"literal escape text", `["\\uD800","\u005cuD800","\\\"\\uD800"]`, false},
		{"ordinary escapes", `"\b\f\n\r\t\/\u0000\"\\"`, false},
		{"first high", `"\uD800"`, true},
		{"last high", `"\uDBFF"`, true},
		{"first low", `"\uDC00"`, true},
		{"last low", `"\uDFFF"`, true},
		{"reversed", `"\uDC00\uD800"`, true},
		{"high high", `"\uD800\uDBFF"`, true},
		{"low low", `"\uDC00\uDFFF"`, true},
		{"above low", `"\uDBFF\uE000"`, true},
		{"interrupted pair", `"\uD800x\uDC00"`, true},
		{"escaped low text", `"\uD800\\uDC00"`, true},
		{"separate strings", `["\uD800","\uDC00"]`, true},
		{"key value boundary", `{"\uD800":"\uDC00"}`, true},
		{"nested unknown key", `{"unused":[0,{"\uDC00":null}]}`, true},
		{"overwritten value", `{"unused":"\uD800","unused":"ok"}`, true},
		{"noncollapsing keys", `{"\uD800":1,"\uDFFF":2,"�":3}`, true},
		{"escaped quote before surrogate", `"\"\\\"\uD800"`, true},
		{"pair then high", `"\uD800\uDC00\uD800"`, true},
		{"pair then low", `"\uD800\uDC00\uDC00"`, true},
		{"deep valid limit", strings.Repeat("[", 10000) + `"\\uD800"` + strings.Repeat("]", 10000), false},
		{"deep invalid scalar limit", nestedJSONArrays(10000), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.raw)
			if !json.Valid(raw) {
				t.Fatal("test requires valid JSON grammar")
			}
			if got := f1engine.HasInvalidJSONScalars(raw); got != tc.want || string(raw) != tc.raw {
				t.Fatalf("scan = %t, want %t; preserved=%t", got, tc.want, string(raw) == tc.raw)
			}
			// The same original representation must survive both normalization paths.
			for _, compressed := range []bool{false, true} {
				topic, payload := "Unused", json.RawMessage(raw)
				if compressed {
					topic, payload = "Unused.z", compressedJSONPayload(t, raw)
				}
				before := bytes.Clone(payload)
				at := time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC)
				batch := f1engine.WireBatch{Source: f1engine.Feed, Updates: []f1engine.WireUpdate{{Topic: topic, Payload: payload, Timestamp: "2026-08-21T10:30:00Z", Source: f1engine.Feed}}}
				got, err := f1engine.NormalizeBatch(batch, at)
				want := f1engine.Batch{Source: f1engine.Feed, RequestedTopics: []string{}, PresentTopics: []string{}, ObservationTime: at,
					Updates: []f1engine.Update{{Topic: "Unused", Payload: json.RawMessage(tc.raw), Timestamp: at, Source: f1engine.Feed}}}
				if tc.want {
					want.InvalidUnicodeUpdates = 1
				}
				if err != nil || !reflect.DeepEqual(got, want) || !bytes.Equal(payload, before) {
					t.Fatalf("compressed=%t: err=%v, full result match=%t, input preserved=%t", compressed, err, reflect.DeepEqual(got, want), bytes.Equal(payload, before))
				}
				payload[0] = '!'
				if !reflect.DeepEqual(got, want) {
					t.Fatal("normalized payload aliases wire storage")
				}
				payload[0] = before[0]
			}
		})
	}
}

func TestPayloadQualitySnapshotAtomicity(t *testing.T) {
	at := time.Unix(100, 0)
	const affected = ` { "unused":"\uD800", "\uDC00":["\uDBFF", "\uDFFF"] } `
	const clean = `{"text":"�","pair":"\uD800\uDC00"}`
	batch := f1engine.WireBatch{Source: f1engine.Snapshot,
		RequestedTopics: []string{"Missing", "Plain", "Inflated.z", "Clean"}, PresentTopics: []string{"Clean", "Inflated.z", "Plain"},
		Updates: []f1engine.WireUpdate{
			{Topic: "Clean", Payload: json.RawMessage(clean), Source: f1engine.Snapshot},
			{Topic: "Inflated.z", Payload: compressedJSONPayload(t, []byte(affected)), Source: f1engine.Snapshot},
			{Topic: "Plain", Payload: json.RawMessage(affected), Source: f1engine.Snapshot},
		}}
	got, err := f1engine.NormalizeBatch(batch, at)
	want := f1engine.Batch{Source: f1engine.Snapshot,
		RequestedTopics: []string{"Missing", "Plain", "Inflated", "Clean"}, PresentTopics: []string{"Clean", "Inflated", "Plain"}, ObservationTime: at, InvalidUnicodeUpdates: 2,
		Updates: []f1engine.Update{
			{Topic: "Clean", Payload: json.RawMessage(clean), Source: f1engine.Snapshot},
			{Topic: "Inflated", Payload: json.RawMessage(affected), Source: f1engine.Snapshot},
			{Topic: "Plain", Payload: json.RawMessage(affected), Source: f1engine.Snapshot},
		}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %#v, %v; want %#v", got, err, want)
	}
	batch.RequestedTopics[0], batch.PresentTopics[0] = "changed", "changed"
	for i := range batch.Updates {
		batch.Updates[i].Payload[0] = '!'
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("normalized snapshot aliases source bytes or manifests")
	}
}

func TestPayloadQualityRejectedBatchHasNoFindings(t *testing.T) {
	for _, tc := range []struct {
		name, topic string
		payload     json.RawMessage
	}{
		{"raw UTF8", "B", json.RawMessage("\"\xff\"")},
		{"JSON grammar", "B", json.RawMessage(`{"bad":"\uD800",}`)},
		{"escape grammar", "B", json.RawMessage(`"\uD80G"`)},
		{"depth", "B", json.RawMessage(nestedJSONArrays(10001))},
		{"inflated UTF8", "B.z", compressedJSONPayload(t, []byte("\"\xff\""))},
		{"inflated grammar", "B.z", compressedJSONPayload(t, []byte(`"\uD800" false`))},
		{"inflated depth", "B.z", compressedJSONPayload(t, []byte(nestedJSONArrays(10001)))},
		{"inflated size", "B.z", compressedJSONPayload(t, []byte(`"\uD800`+strings.Repeat("x", f1engine.MaxDecompressedPayloadSize-7)+`"`))},
		{"encoded size", "B.z", json.RawMessage(`"` + strings.Repeat("A", f1engine.MaxEncodedPayloadSize+1) + `"`)},
		{"base64", "B.z", json.RawMessage(`"!"`)},
		{"DEFLATE", "B.z", json.RawMessage(`"AAAA"`)},
		{"compressed outer scalar", "B.z", json.RawMessage(`"\uD800"`)},
		{"manifest normalized collision", "A.z", compressedJSONPayload(t, []byte(`{}`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, badFirst := range []bool{false, true} {
				batch := f1engine.WireBatch{Source: f1engine.Snapshot, RequestedTopics: []string{"A", tc.topic}, PresentTopics: []string{"A", tc.topic},
					Updates: []f1engine.WireUpdate{
						{Topic: "A", Payload: json.RawMessage(`{"\uD800":"\uDC00"}`), Source: f1engine.Snapshot},
						{Topic: tc.topic, Payload: bytes.Clone(tc.payload), Source: f1engine.Snapshot},
					}}
				if badFirst {
					batch.PresentTopics[0], batch.PresentTopics[1] = batch.PresentTopics[1], batch.PresentTopics[0]
					batch.Updates[0], batch.Updates[1] = batch.Updates[1], batch.Updates[0]
				}
				payloads := []string{string(batch.Updates[0].Payload), string(batch.Updates[1].Payload)}
				got, err := f1engine.NormalizeBatch(batch, time.Unix(100, 0))
				if !errors.Is(err, f1engine.ErrInvalidLiveTimingData) || !reflect.DeepEqual(got, f1engine.Batch{}) {
					t.Fatalf("badFirst=%t: rejected batch = %#v, %v", badFirst, got, err)
				}
				if string(batch.Updates[0].Payload) != payloads[0] || string(batch.Updates[1].Payload) != payloads[1] {
					t.Fatal("rejected normalization changed source bytes")
				}
			}
		})
	}
}

func TestPayloadQualityInflatedSizeBoundary(t *testing.T) {
	for _, size := range []int{f1engine.MaxDecompressedPayloadSize - 1, f1engine.MaxDecompressedPayloadSize} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			payload := `"\uD800` + strings.Repeat("x", size-8) + `"`
			at := time.Unix(100, 0)
			got, err := f1engine.NormalizeBatch(f1engine.WireBatch{Source: f1engine.Snapshot, RequestedTopics: []string{"A.z"}, PresentTopics: []string{"A.z"},
				Updates: []f1engine.WireUpdate{{Topic: "A.z", Payload: compressedJSONPayload(t, []byte(payload)), Source: f1engine.Snapshot}}}, at)
			want := f1engine.Batch{Source: f1engine.Snapshot, RequestedTopics: []string{"A"}, PresentTopics: []string{"A"}, ObservationTime: at, InvalidUnicodeUpdates: 1,
				Updates: []f1engine.Update{{Topic: "A", Payload: json.RawMessage(payload), Source: f1engine.Snapshot}}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("size=%d: err=%v, full result match=%t", size, err, reflect.DeepEqual(got, want))
			}
		})
	}
}
