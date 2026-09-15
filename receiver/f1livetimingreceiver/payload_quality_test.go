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
			if got := hasInvalidJSONScalars(raw); got != tc.want || string(raw) != tc.raw {
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
				batch := liveTimingBatch{source: liveTimingUpdateSourceFeed, updates: []liveTimingUpdate{{topic: topic, payload: payload, timestamp: "2026-08-21T10:30:00Z", source: liveTimingUpdateSourceFeed}}}
				got, err := normalizeLiveTimingBatch(batch, at)
				want := normalizedLiveTimingBatch{source: liveTimingUpdateSourceFeed, requestedTopics: []string{}, presentTopics: []string{}, observationTime: at,
					updates: []normalizedLiveTimingUpdate{{topic: "Unused", payload: json.RawMessage(tc.raw), timestamp: at, source: liveTimingUpdateSourceFeed}}}
				if tc.want {
					want.invalidUnicodeUpdates = 1
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
	batch := liveTimingBatch{source: liveTimingUpdateSourceSnapshot,
		requestedTopics: []string{"Missing", "Plain", "Inflated.z", "Clean"}, presentTopics: []string{"Clean", "Inflated.z", "Plain"},
		updates: []liveTimingUpdate{
			{topic: "Clean", payload: json.RawMessage(clean), source: liveTimingUpdateSourceSnapshot},
			{topic: "Inflated.z", payload: compressedJSONPayload(t, []byte(affected)), source: liveTimingUpdateSourceSnapshot},
			{topic: "Plain", payload: json.RawMessage(affected), source: liveTimingUpdateSourceSnapshot},
		}}
	got, err := normalizeLiveTimingBatch(batch, at)
	want := normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot,
		requestedTopics: []string{"Missing", "Plain", "Inflated", "Clean"}, presentTopics: []string{"Clean", "Inflated", "Plain"}, observationTime: at, invalidUnicodeUpdates: 2,
		updates: []normalizedLiveTimingUpdate{
			{topic: "Clean", payload: json.RawMessage(clean), source: liveTimingUpdateSourceSnapshot},
			{topic: "Inflated", payload: json.RawMessage(affected), source: liveTimingUpdateSourceSnapshot},
			{topic: "Plain", payload: json.RawMessage(affected), source: liveTimingUpdateSourceSnapshot},
		}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %#v, %v; want %#v", got, err, want)
	}
	batch.requestedTopics[0], batch.presentTopics[0] = "changed", "changed"
	for i := range batch.updates {
		batch.updates[i].payload[0] = '!'
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
		{"inflated size", "B.z", compressedJSONPayload(t, []byte(`"\uD800`+strings.Repeat("x", maxDecompressedPayloadSize-7)+`"`))},
		{"encoded size", "B.z", json.RawMessage(`"` + strings.Repeat("A", maxEncodedPayloadSize+1) + `"`)},
		{"base64", "B.z", json.RawMessage(`"!"`)},
		{"DEFLATE", "B.z", json.RawMessage(`"AAAA"`)},
		{"compressed outer scalar", "B.z", json.RawMessage(`"\uD800"`)},
		{"manifest normalized collision", "A.z", compressedJSONPayload(t, []byte(`{}`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, badFirst := range []bool{false, true} {
				batch := liveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"A", tc.topic}, presentTopics: []string{"A", tc.topic},
					updates: []liveTimingUpdate{
						{topic: "A", payload: json.RawMessage(`{"\uD800":"\uDC00"}`), source: liveTimingUpdateSourceSnapshot},
						{topic: tc.topic, payload: bytes.Clone(tc.payload), source: liveTimingUpdateSourceSnapshot},
					}}
				if badFirst {
					batch.presentTopics[0], batch.presentTopics[1] = batch.presentTopics[1], batch.presentTopics[0]
					batch.updates[0], batch.updates[1] = batch.updates[1], batch.updates[0]
				}
				payloads := []string{string(batch.updates[0].payload), string(batch.updates[1].payload)}
				got, err := normalizeLiveTimingBatch(batch, time.Unix(100, 0))
				if !errors.Is(err, errInvalidLiveTimingData) || !reflect.DeepEqual(got, normalizedLiveTimingBatch{}) {
					t.Fatalf("badFirst=%t: rejected batch = %#v, %v", badFirst, got, err)
				}
				if string(batch.updates[0].payload) != payloads[0] || string(batch.updates[1].payload) != payloads[1] {
					t.Fatal("rejected normalization changed source bytes")
				}
			}
		})
	}
}

func TestPayloadQualityInflatedSizeBoundary(t *testing.T) {
	for _, size := range []int{maxDecompressedPayloadSize - 1, maxDecompressedPayloadSize} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			payload := `"\uD800` + strings.Repeat("x", size-8) + `"`
			at := time.Unix(100, 0)
			got, err := normalizeLiveTimingBatch(liveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"A.z"}, presentTopics: []string{"A.z"},
				updates: []liveTimingUpdate{{topic: "A.z", payload: compressedJSONPayload(t, []byte(payload)), source: liveTimingUpdateSourceSnapshot}}}, at)
			want := normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"A"}, presentTopics: []string{"A"}, observationTime: at, invalidUnicodeUpdates: 1,
				updates: []normalizedLiveTimingUpdate{{topic: "A", payload: json.RawMessage(payload), source: liveTimingUpdateSourceSnapshot}}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("size=%d: err=%v, full result match=%t", size, err, reflect.DeepEqual(got, want))
			}
		})
	}
}
