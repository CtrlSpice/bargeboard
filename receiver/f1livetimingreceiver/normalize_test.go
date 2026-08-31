package f1livetimingreceiver

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// First CarData.z record from the official 2025 British Grand Prix race archive.
const britishGP2025CarData = "7ZQ7DsIwEETvsnWC1rv+4TbiBtCAKCIUCSSUIqSzcvckpqeYhsbN2Fr5Sd7fZDqN8/QaPpRumS7zgxIJi2s5tOzPRhMfk/LBsmjwV2qo66ftcSazS/fsx3F4lwBT4oakqBa1Rd33vh/LUoIQ50DOg5xhFBQUREtj4BwjCAqaoygKBhBUtI8KTziao4V3A14O9KsRLU7E+rihv+wpbvZkvKv+VP2p+lP1p3/4031ZAQ=="

func TestNormalizeLiveTimingUpdateDecodesArchivedCarData(t *testing.T) {
	payload, err := json.Marshal(britishGP2025CarData)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	got, err := normalizeLiveTimingUpdate(liveTimingUpdate{
		topic:     "CarData.z",
		payload:   payload,
		timestamp: "2025-07-06T13:09:30.402376Z",
		source:    liveTimingUpdateSourceFeed,
	})
	if err != nil {
		t.Fatalf("normalizeLiveTimingUpdate() error = %v", err)
	}
	if got.topic != "CarData" {
		t.Errorf("normalized topic = %q, want CarData", got.topic)
	}
	if got.timestamp.Format(time.RFC3339Nano) != "2025-07-06T13:09:30.402376Z" {
		t.Errorf("normalized timestamp = %s", got.timestamp)
	}

	var carData struct {
		Entries []struct {
			UTC  string                     `json:"Utc"`
			Cars map[string]json.RawMessage `json:"Cars"`
		} `json:"Entries"`
	}
	if err := json.Unmarshal(got.payload, &carData); err != nil {
		t.Fatalf("Unmarshal() normalized payload error = %v", err)
	}
	if len(carData.Entries) != 2 {
		t.Fatalf("CarData entries = %d, want 2", len(carData.Entries))
	}
	if carData.Entries[0].UTC != "2025-07-06T13:09:30.402376Z" {
		t.Errorf("first CarData UTC = %q", carData.Entries[0].UTC)
	}
	if len(carData.Entries[0].Cars) != 20 {
		t.Errorf("first CarData cars = %d, want 20", len(carData.Entries[0].Cars))
	}
}

func TestNormalizeLiveTimingTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		source  liveTimingUpdateSource
		raw     string
		want    string
		wantErr string
	}{
		{name: "UTC", source: liveTimingUpdateSourceFeed, raw: "2025-07-06T13:09:30.4Z", want: "2025-07-06T13:09:30.4Z"},
		{name: "offset", source: liveTimingUpdateSourceFeed, raw: "2025-07-06T14:09:30.4+01:00", want: "2025-07-06T13:09:30.4Z"},
		{name: "snapshot", source: liveTimingUpdateSourceSnapshot},
		{name: "empty feed", source: liveTimingUpdateSourceFeed, wantErr: "empty"},
		{name: "invalid feed", source: liveTimingUpdateSourceFeed, raw: "13:09:30", wantErr: "RFC3339"},
		{name: "comma fraction", source: liveTimingUpdateSourceFeed, raw: "2025-07-06T13:09:30,4Z", wantErr: "RFC3339"},
		{name: "long fraction", source: liveTimingUpdateSourceFeed, raw: "2025-07-06T13:09:30.1234567890Z", wantErr: "RFC3339"},
		{name: "offset hour", source: liveTimingUpdateSourceFeed, raw: "2025-07-06T13:09:30+24:00", wantErr: "RFC3339"},
		{name: "offset minute", source: liveTimingUpdateSourceFeed, raw: "2025-07-06T13:09:30+00:60", wantErr: "RFC3339"},
		{name: "timestamped snapshot", source: liveTimingUpdateSourceSnapshot, raw: "2025-07-06T13:09:30Z", wantErr: "must be empty"},
		{name: "unknown source", wantErr: "unknown"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeLiveTimingTimestamp(test.source, test.raw)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("normalizeLiveTimingTimestamp() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeLiveTimingTimestamp() error = %v", err)
			}
			if test.want == "" {
				if !got.IsZero() {
					t.Errorf("normalizeLiveTimingTimestamp() = %s, want zero", got)
				}
				return
			}
			if formatted := got.Format(time.RFC3339Nano); formatted != test.want {
				t.Errorf("normalizeLiveTimingTimestamp() = %q, want %q", formatted, test.want)
			}
		})
	}
}

func TestNormalizeLiveTimingUpdatePreservesPlainJSON(t *testing.T) {
	for _, payload := range []string{`{}`, `[]`, `"text"`, `42`, `true`, `null`} {
		t.Run(payload, func(t *testing.T) {
			input := json.RawMessage(payload)
			got, err := normalizeLiveTimingUpdate(liveTimingUpdate{
				topic:   "FutureTopic",
				payload: input,
				source:  liveTimingUpdateSourceSnapshot,
			})
			if err != nil {
				t.Fatalf("normalizeLiveTimingUpdate() error = %v", err)
			}
			if string(got.payload) != payload {
				t.Errorf("normalized payload = %s, want %s", got.payload, payload)
			}
			if len(input) > 0 {
				input[0] = 'x'
				if string(got.payload) != payload {
					t.Errorf("normalized payload changed after input mutation: %s", got.payload)
				}
			}
		})
	}
}

func TestNormalizeLiveTimingUpdateRemovesOneCompressionSuffix(t *testing.T) {
	got, err := normalizeLiveTimingUpdate(liveTimingUpdate{
		topic:   "FutureTopic.z.z",
		payload: compressedJSONPayload(t, []byte(`{"value":1}`)),
		source:  liveTimingUpdateSourceSnapshot,
	})
	if err != nil {
		t.Fatalf("normalizeLiveTimingUpdate() error = %v", err)
	}
	if got.topic != "FutureTopic.z" {
		t.Errorf("normalized topic = %q, want FutureTopic.z", got.topic)
	}
}

func TestNormalizeLiveTimingUpdatesPreservesOrderAndIsAllOrNothing(t *testing.T) {
	updates := []liveTimingUpdate{
		{topic: "B", payload: json.RawMessage(`2`), source: liveTimingUpdateSourceSnapshot},
		{topic: "A", payload: json.RawMessage(`1`), source: liveTimingUpdateSourceSnapshot},
	}
	got, err := normalizeLiveTimingUpdates(updates)
	if err != nil {
		t.Fatalf("normalizeLiveTimingUpdates() error = %v", err)
	}
	if got[0].topic != "B" || got[1].topic != "A" {
		t.Errorf("normalized order = %q, %q", got[0].topic, got[1].topic)
	}

	updates[1].payload = json.RawMessage(`{`)
	got, err = normalizeLiveTimingUpdates(updates)
	if got != nil || err == nil || !strings.Contains(err.Error(), "update 1") {
		t.Fatalf("normalizeLiveTimingUpdates() = %#v, %v, want no partial batch and indexed error", got, err)
	}
	if !errors.Is(err, errInvalidLiveTimingUpdate) {
		t.Errorf("normalizeLiveTimingUpdates() error does not wrap errInvalidLiveTimingUpdate")
	}
}

func TestNormalizeLiveTimingUpdateRejectsInvalidInput(t *testing.T) {
	validCompressed := compressedJSONPayload(t, []byte(`{"ok":true}`))
	compressedWithTrailingData := appendCompressedData(t, validCompressed, []byte("trailing"))
	invalidDeflate := json.RawMessage(strconv.Quote(base64.StdEncoding.EncodeToString([]byte("not deflate"))))
	tests := []struct {
		name    string
		update  liveTimingUpdate
		wantErr string
	}{
		{name: "empty topic", update: liveTimingUpdate{payload: json.RawMessage(`{}`), source: liveTimingUpdateSourceSnapshot}, wantErr: "topic is empty"},
		{name: "invalid plain JSON", update: liveTimingUpdate{topic: "FutureTopic", payload: json.RawMessage(`{`), source: liveTimingUpdateSourceSnapshot}, wantErr: "not valid JSON"},
		{name: "invalid plain UTF-8", update: liveTimingUpdate{topic: "FutureTopic", payload: json.RawMessage{'"', 0xff, '"'}, source: liveTimingUpdateSourceSnapshot}, wantErr: "not UTF-8"},
		{name: "compressed non-string", update: liveTimingUpdate{topic: "CarData.z", payload: json.RawMessage(`{}`), source: liveTimingUpdateSourceSnapshot}, wantErr: "JSON string"},
		{name: "empty compressed string", update: liveTimingUpdate{topic: "CarData.z", payload: json.RawMessage(`""`), source: liveTimingUpdateSourceSnapshot}, wantErr: "non-empty"},
		{name: "invalid base64", update: liveTimingUpdate{topic: "CarData.z", payload: json.RawMessage(`"not base64"`), source: liveTimingUpdateSourceSnapshot}, wantErr: "canonical standard base64"},
		{name: "base64 newline", update: liveTimingUpdate{topic: "CarData.z", payload: json.RawMessage(`"/w==\n"`), source: liveTimingUpdateSourceSnapshot}, wantErr: "canonical standard base64"},
		{name: "base64 padding bits", update: liveTimingUpdate{topic: "CarData.z", payload: json.RawMessage(`"/x=="`), source: liveTimingUpdateSourceSnapshot}, wantErr: "canonical standard base64"},
		{name: "encoded size", update: liveTimingUpdate{topic: "CarData.z", payload: json.RawMessage(strconv.Quote(strings.Repeat("A", maxEncodedPayloadSize+1))), source: liveTimingUpdateSourceSnapshot}, wantErr: "encoded payload exceeds"},
		{name: "invalid DEFLATE", update: liveTimingUpdate{topic: "CarData.z", payload: invalidDeflate, source: liveTimingUpdateSourceSnapshot}, wantErr: "raw DEFLATE"},
		{name: "trailing compressed data", update: liveTimingUpdate{topic: "CarData.z", payload: compressedWithTrailingData, source: liveTimingUpdateSourceSnapshot}, wantErr: "trailing data"},
		{name: "invalid UTF-8", update: liveTimingUpdate{topic: "CarData.z", payload: compressedJSONPayload(t, []byte{0xff}), source: liveTimingUpdateSourceSnapshot}, wantErr: "UTF-8"},
		{name: "invalid inflated JSON", update: liveTimingUpdate{topic: "CarData.z", payload: compressedJSONPayload(t, []byte(`{`)), source: liveTimingUpdateSourceSnapshot}, wantErr: "valid JSON"},
		{name: "decompressed size", update: liveTimingUpdate{topic: "CarData.z", payload: compressedJSONPayload(t, bytes.Repeat([]byte(" "), maxDecompressedPayloadSize+1)), source: liveTimingUpdateSourceSnapshot}, wantErr: "decompressed payload exceeds"},
		{name: "empty semantic topic", update: liveTimingUpdate{topic: ".z", payload: validCompressed, source: liveTimingUpdateSourceSnapshot}, wantErr: "semantic topic is empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeLiveTimingUpdate(test.update)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("normalizeLiveTimingUpdate() error = %v, want containing %q", err, test.wantErr)
			}
			if !errors.Is(err, errInvalidLiveTimingUpdate) {
				t.Errorf("error does not wrap errInvalidLiveTimingUpdate")
			}
		})
	}
}

func compressedJSONPayload(t *testing.T, contents []byte) json.RawMessage {
	t.Helper()
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.BestSpeed)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}
	if _, err := writer.Write(contents); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return json.RawMessage(strconv.Quote(base64.StdEncoding.EncodeToString(compressed.Bytes())))
}

func appendCompressedData(t *testing.T, payload json.RawMessage, trailing []byte) json.RawMessage {
	t.Helper()
	var encoded string
	if err := json.Unmarshal(payload, &encoded); err != nil {
		t.Fatalf("Unmarshal() compressed payload error = %v", err)
	}
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	compressed = append(compressed, trailing...)
	return json.RawMessage(strconv.Quote(base64.StdEncoding.EncodeToString(compressed)))
}
