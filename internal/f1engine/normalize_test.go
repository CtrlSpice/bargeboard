package f1engine

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type carDataNormalizationFixture struct {
	Name                   string `json:"name"`
	CompressedPayload      string `json:"compressed_payload"`
	SyntheticFeedTimestamp string `json:"synthetic_feed_timestamp"`
	ExpectedPayload        string `json:"expected_payload"`
}

func TestNormalizeLiveTimingUpdateDecodesSyntheticPayload(t *testing.T) {
	contents, err := os.ReadFile("testdata/car_data/normalization_cases.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures []carDataNormalizationFixture
	if err := json.Unmarshal(contents, &fixtures); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("fixture count = %d, want 1", len(fixtures))
	}
	fixture := fixtures[0]
	if fixture.Name != "synthetic_nested_json" {
		t.Fatalf("fixture name = %q", fixture.Name)
	}

	// Fixed Python/zlib output, independently generated as documented in SOURCES.md.
	encodedPayload, err := json.Marshal(fixture.CompressedPayload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	// Leave enough spare capacity to detect reuse beyond the compressed input.
	payloadStorage := bytes.Repeat([]byte{0xa5}, len(encodedPayload)+len(fixture.ExpectedPayload))
	copy(payloadStorage, encodedPayload)
	input := WireUpdate{
		Topic:     "CarData.z",
		Payload:   json.RawMessage(payloadStorage[:len(encodedPayload):len(payloadStorage)]),
		Timestamp: fixture.SyntheticFeedTimestamp,
		Source:    Feed,
	}
	before := input
	before.Payload = bytes.Clone(input.Payload)
	beforeStorage := bytes.Clone(payloadStorage)

	got, err := normalizeLiveTimingUpdate(input)
	if err != nil {
		t.Fatalf("normalizeLiveTimingUpdate() error = %v", err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("normalizeLiveTimingUpdate() changed input: got %#v, want %#v", input, before)
	}
	if !bytes.Equal(payloadStorage, beforeStorage) {
		t.Fatal("normalizeLiveTimingUpdate() changed input backing storage")
	}
	want := Update{
		Topic:     "CarData",
		Payload:   json.RawMessage(fixture.ExpectedPayload),
		Timestamp: time.Date(2032, 4, 5, 6, 7, 9, 123456789, time.UTC),
		Source:    Feed,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeLiveTimingUpdate() = %#v, want %#v", got, want)
	}

	clear(input.Payload[:cap(input.Payload)])
	if !reflect.DeepEqual(got, want) {
		t.Fatal("normalized result aliases compressed input backing storage")
	}
}

func TestNormalizeLiveTimingTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		source  Source
		raw     string
		want    string
		wantErr string
	}{
		{name: "UTC", source: Feed, raw: "2025-07-06T13:09:30.4Z", want: "2025-07-06T13:09:30.4Z"},
		{name: "offset", source: Feed, raw: "2025-07-06T14:09:30.4+01:00", want: "2025-07-06T13:09:30.4Z"},
		{name: "snapshot", source: Snapshot},
		{name: "empty feed", source: Feed, wantErr: "empty"},
		{name: "invalid feed", source: Feed, raw: "13:09:30", wantErr: "RFC3339"},
		{name: "comma fraction", source: Feed, raw: "2025-07-06T13:09:30,4Z", wantErr: "RFC3339"},
		{name: "long fraction", source: Feed, raw: "2025-07-06T13:09:30.1234567890Z", wantErr: "RFC3339"},
		{name: "offset hour", source: Feed, raw: "2025-07-06T13:09:30+24:00", wantErr: "RFC3339"},
		{name: "offset minute", source: Feed, raw: "2025-07-06T13:09:30+00:60", wantErr: "RFC3339"},
		{name: "timestamped snapshot", source: Snapshot, raw: "2025-07-06T13:09:30Z", wantErr: "must be empty"},
		{name: "unknown source", wantErr: "unknown"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeLiveTimingTimestamp(test.source, test.raw)
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
			got, err := normalizeLiveTimingUpdate(WireUpdate{
				Topic:   "FutureTopic",
				Payload: input,
				Source:  Snapshot,
			})
			if err != nil {
				t.Fatalf("normalizeLiveTimingUpdate() error = %v", err)
			}
			if string(got.Payload) != payload {
				t.Errorf("normalized payload = %s, want %s", got.Payload, payload)
			}
			if len(input) > 0 {
				input[0] = 'x'
				if string(got.Payload) != payload {
					t.Errorf("normalized payload changed after input mutation: %s", got.Payload)
				}
			}
		})
	}
}

func TestNormalizeLiveTimingUpdateRemovesOneCompressionSuffix(t *testing.T) {
	got, err := normalizeLiveTimingUpdate(WireUpdate{
		Topic:   "FutureTopic.z.z",
		Payload: compressedJSONPayload(t, []byte(`{"value":1}`)),
		Source:  Snapshot,
	})
	if err != nil {
		t.Fatalf("normalizeLiveTimingUpdate() error = %v", err)
	}
	if got.Topic != "FutureTopic.z" {
		t.Errorf("normalized topic = %q, want FutureTopic.z", got.Topic)
	}
}

func TestNormalizeLiveTimingUpdatesPreservesOrderAndIsAllOrNothing(t *testing.T) {
	updates := []WireUpdate{
		{Topic: "B", Payload: json.RawMessage(`2`), Source: Snapshot},
		{Topic: "A", Payload: json.RawMessage(`1`), Source: Snapshot},
	}
	got, err := normalizeLiveTimingUpdates(updates)
	if err != nil {
		t.Fatalf("normalizeLiveTimingUpdates() error = %v", err)
	}
	if got[0].Topic != "B" || got[1].Topic != "A" {
		t.Errorf("normalized order = %q, %q", got[0].Topic, got[1].Topic)
	}

	updates[1].Payload = json.RawMessage(`{`)
	got, err = normalizeLiveTimingUpdates(updates)
	if got != nil || err == nil || !strings.Contains(err.Error(), "update 1") {
		t.Fatalf("normalizeLiveTimingUpdates() = %#v, %v, want no partial batch and indexed error", got, err)
	}
	if !errors.Is(err, ErrInvalidLiveTimingData) {
		t.Errorf("normalizeLiveTimingUpdates() error does not wrap errInvalidLiveTimingData")
	}
}

func TestNormalizeLiveTimingBatchPreservesSnapshotManifest(t *testing.T) {
	requestedTopics := []string{"Heartbeat", "SessionInfo", "TimingData"}
	presentTopics := []string{"SessionInfo"}
	batch := WireBatch{
		Source:          Snapshot,
		RequestedTopics: requestedTopics,
		PresentTopics:   presentTopics,
		Updates: []WireUpdate{{
			Topic:   "SessionInfo",
			Payload: json.RawMessage(`{"Key":42017}`),
			Source:  Snapshot,
		}},
	}
	observationTime := time.Date(2026, 8, 21, 12, 30, 0, 0, time.FixedZone("test", 2*60*60))

	got, err := NormalizeBatch(batch, observationTime)
	if err != nil {
		t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
	}
	if got.Source != Snapshot {
		t.Errorf("batch source = %d, want snapshot", got.Source)
	}
	if !reflect.DeepEqual(got.RequestedTopics, requestedTopics) {
		t.Errorf("requested topics = %q, want %q", got.RequestedTopics, requestedTopics)
	}
	if !reflect.DeepEqual(got.PresentTopics, presentTopics) {
		t.Errorf("present topics = %q, want %q", got.PresentTopics, presentTopics)
	}
	if got.ObservationTime != observationTime {
		t.Errorf("observation time = %s, want exact input %s", got.ObservationTime, observationTime)
	}
	if len(got.Updates) != 1 {
		t.Fatalf("update count = %d, want 1", len(got.Updates))
	}
	if got.Updates[0].Topic != "SessionInfo" || got.Updates[0].Source != Snapshot {
		t.Errorf("normalized update = %#v", got.Updates[0])
	}

	requestedTopics[0] = "changed"
	presentTopics[0] = "changed"
	if got.RequestedTopics[0] != "Heartbeat" || got.PresentTopics[0] != "SessionInfo" {
		t.Errorf("normalized manifest aliases input: requested = %q, present = %q", got.RequestedTopics, got.PresentTopics)
	}
}

func TestNormalizeLiveTimingBatchNormalizesCompressedManifest(t *testing.T) {
	observationTime := time.Now()
	got, err := NormalizeBatch(WireBatch{
		Source:          Snapshot,
		RequestedTopics: []string{"CarData.z", "Position.z"},
		PresentTopics:   []string{"CarData.z"},
		Updates: []WireUpdate{{
			Topic:   "CarData.z",
			Payload: compressedJSONPayload(t, []byte(`{"Entries":[]}`)),
			Source:  Snapshot,
		}},
	}, observationTime)
	if err != nil {
		t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
	}
	if !reflect.DeepEqual(got.RequestedTopics, []string{"CarData", "Position"}) {
		t.Errorf("requested topics = %q", got.RequestedTopics)
	}
	if !reflect.DeepEqual(got.PresentTopics, []string{"CarData"}) {
		t.Errorf("present topics = %q", got.PresentTopics)
	}
	if len(got.Updates) != 1 || got.Updates[0].Topic != "CarData" {
		t.Errorf("normalized updates = %#v", got.Updates)
	}
	if got.ObservationTime != observationTime {
		t.Errorf("observation time lost its exact clock value")
	}
}

func TestNormalizeLiveTimingBatchPreservesEmptySnapshot(t *testing.T) {
	observationTime := time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC)
	got, err := NormalizeBatch(WireBatch{
		Source:          Snapshot,
		RequestedTopics: []string{"SessionInfo"},
		PresentTopics:   []string{},
		Updates:         []WireUpdate{},
	}, observationTime)
	if err != nil {
		t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
	}
	if len(got.PresentTopics) != 0 || len(got.Updates) != 0 {
		t.Fatalf("normalized empty snapshot present topics = %q, updates = %#v", got.PresentTopics, got.Updates)
	}
	if !got.ObservationTime.Equal(observationTime) {
		t.Errorf("observation time = %s, want %s", got.ObservationTime, observationTime)
	}
}

func TestNormalizeLiveTimingBatchSnapshotWireMembership(t *testing.T) {
	observationTime := time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC)
	// Synthetic payloads isolate manifest validation from source semantics.
	plainPayload := json.RawMessage(`{"Entries":[]}`)
	compressedPayload := compressedJSONPayload(t, plainPayload)
	tests := []struct {
		name          string
		requested     []string
		present       []string
		wantRequested []string
		wantPresent   []string
		wantErr       bool
	}{
		{
			name:      "plain topic cannot satisfy compressed request",
			requested: []string{"Position", "CarData.z"}, present: []string{"Position", "CarData"},
			wantErr: true,
		},
		{
			name:      "compressed topic cannot satisfy plain request",
			requested: []string{"Position", "CarData"}, present: []string{"Position", "CarData.z"},
			wantErr: true,
		},
		{
			name:      "exact compressed match",
			requested: []string{"CarData.z"}, present: []string{"CarData.z"},
			wantRequested: []string{"CarData"}, wantPresent: []string{"CarData"},
		},
		{
			name:      "exact plain match",
			requested: []string{"CarData"}, present: []string{"CarData"},
			wantRequested: []string{"CarData"}, wantPresent: []string{"CarData"},
		},
		{
			name:      "partial snapshot preserves manifest order",
			requested: []string{"Position.z", "CarData.z", "SessionInfo"}, present: []string{"CarData.z", "Position.z"},
			wantRequested: []string{"Position", "CarData", "SessionInfo"}, wantPresent: []string{"CarData", "Position"},
		},
		{
			name:          "nil empty snapshot",
			requested:     []string{"CarData.z", "Position"},
			wantRequested: []string{"CarData", "Position"}, wantPresent: []string{},
		},
		{
			name:      "non-nil empty snapshot",
			requested: []string{"CarData.z", "Position"}, present: []string{},
			wantRequested: []string{"CarData", "Position"}, wantPresent: []string{},
		},
		{
			name:      "duplicate requested topic",
			requested: []string{"CarData.z", "CarData.z"}, present: []string{"CarData.z"},
			wantErr: true,
		},
		{
			name:      "duplicate requested topic in empty snapshot",
			requested: []string{"CarData", "CarData"}, wantErr: true,
		},
		{
			name:      "requested normalized collision",
			requested: []string{"CarData.z", "CarData"}, present: []string{"CarData.z"},
			wantErr: true,
		},
		{
			name:      "requested normalized collision in empty snapshot",
			requested: []string{"CarData.z", "CarData"}, wantErr: true,
		},
		{
			name:      "present normalized collision",
			requested: []string{"CarData.z", "CarData"}, present: []string{"CarData.z", "CarData"},
			wantErr: true,
		},
		{
			name:      "duplicate present topic",
			requested: []string{"CarData.z"}, present: []string{"CarData.z", "CarData.z"},
			wantErr: true,
		},
		{
			name:      "empty requested topic",
			requested: []string{""}, wantErr: true,
		},
		{
			name:      "no requested topics",
			requested: []string{}, wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := WireBatch{
				Source:          Snapshot,
				RequestedTopics: slices.Clone(test.requested),
				PresentTopics:   slices.Clone(test.present),
			}
			if test.present != nil {
				batch.Updates = make([]WireUpdate, 0, len(test.present))
			}
			for _, topic := range test.present {
				payload := plainPayload
				if strings.HasSuffix(topic, ".z") {
					payload = compressedPayload
				}
				batch.Updates = append(batch.Updates, WireUpdate{
					Topic: topic, Payload: bytes.Clone(payload), Source: Snapshot,
				})
			}
			before := batch
			before.RequestedTopics = slices.Clone(batch.RequestedTopics)
			before.PresentTopics = slices.Clone(batch.PresentTopics)
			before.Updates = slices.Clone(batch.Updates)
			for index := range before.Updates {
				before.Updates[index].Payload = bytes.Clone(batch.Updates[index].Payload)
			}

			got, err := NormalizeBatch(batch, observationTime)
			if !reflect.DeepEqual(batch, before) {
				t.Errorf("input batch changed: got %#v, want %#v", batch, before)
			}
			if test.wantErr {
				if !errors.Is(err, ErrInvalidLiveTimingData) {
					t.Errorf("error = %v, want errInvalidLiveTimingData", err)
				}
				if !reflect.DeepEqual(got, Batch{}) {
					t.Errorf("failed normalization returned %#v, want zero batch", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
			}
			want := Batch{
				Source:          Snapshot,
				RequestedTopics: test.wantRequested,
				PresentTopics:   test.wantPresent,
				ObservationTime: observationTime,
				Updates:         make([]Update, 0, len(test.wantPresent)),
			}
			for _, topic := range test.wantPresent {
				want.Updates = append(want.Updates, Update{
					Topic: topic, Payload: plainPayload, Source: Snapshot,
				})
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("normalized batch = %#v, want %#v", got, want)
			}
		})
	}
}

func TestNormalizeLiveTimingBatchRejectsInvalidEnvelope(t *testing.T) {
	validUpdate := WireUpdate{
		Topic:     "SessionStatus",
		Payload:   json.RawMessage(`{"Status":"Started"}`),
		Timestamp: "2026-08-21T10:30:00.034Z",
		Source:    Feed,
	}
	observationTime := time.Date(2026, 8, 21, 10, 30, 1, 0, time.UTC)
	tests := []struct {
		name            string
		batch           WireBatch
		observationTime time.Time
	}{
		{
			name:            "zero observation time",
			batch:           WireBatch{Source: Feed, Updates: []WireUpdate{validUpdate}},
			observationTime: time.Time{},
		},
		{
			name:            "feed manifest",
			batch:           WireBatch{Source: Feed, PresentTopics: []string{"SessionStatus"}, Updates: []WireUpdate{validUpdate}},
			observationTime: observationTime,
		},
		{
			name: "snapshot manifest mismatch",
			batch: WireBatch{
				Source:          Snapshot,
				RequestedTopics: []string{"SessionInfo"},
				PresentTopics:   []string{"SessionInfo"},
				Updates:         []WireUpdate{},
			},
			observationTime: observationTime,
		},
		{
			name: "inconsistent source",
			batch: WireBatch{
				Source:          Snapshot,
				RequestedTopics: []string{"SessionStatus"},
				PresentTopics:   []string{"SessionStatus"},
				Updates:         []WireUpdate{validUpdate},
			},
			observationTime: observationTime,
		},
		{
			name: "unrequested present topic",
			batch: WireBatch{
				Source:          Snapshot,
				RequestedTopics: []string{"SessionInfo"},
				PresentTopics:   []string{"WeatherData"},
				Updates: []WireUpdate{{
					Topic:   "WeatherData",
					Payload: json.RawMessage(`{}`),
					Source:  Snapshot,
				}},
			},
			observationTime: observationTime,
		},
		{
			name: "normalized topic collision",
			batch: WireBatch{
				Source:          Snapshot,
				RequestedTopics: []string{"CarData", "CarData.z"},
				PresentTopics:   []string{},
				Updates:         []WireUpdate{},
			},
			observationTime: observationTime,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeBatch(test.batch, test.observationTime)
			if err == nil {
				t.Fatal("normalizeLiveTimingBatch() error = nil")
			}
			if !errors.Is(err, ErrInvalidLiveTimingData) {
				t.Errorf("error does not wrap errInvalidLiveTimingData")
			}
		})
	}
}

func TestNormalizeLiveTimingUpdateRejectsInvalidInput(t *testing.T) {
	validCompressed := compressedJSONPayload(t, []byte(`{"ok":true}`))
	compressedWithTrailingData := appendCompressedData(t, validCompressed, []byte("trailing"))
	invalidDeflate := json.RawMessage(strconv.Quote(base64.StdEncoding.EncodeToString([]byte("not deflate"))))
	tests := []struct {
		name    string
		update  WireUpdate
		wantErr string
	}{
		{name: "empty topic", update: WireUpdate{Payload: json.RawMessage(`{}`), Source: Snapshot}, wantErr: "topic is empty"},
		{name: "invalid plain JSON", update: WireUpdate{Topic: "FutureTopic", Payload: json.RawMessage(`{`), Source: Snapshot}, wantErr: "not valid JSON"},
		{name: "invalid plain UTF-8", update: WireUpdate{Topic: "FutureTopic", Payload: json.RawMessage{'"', 0xff, '"'}, Source: Snapshot}, wantErr: "not UTF-8"},
		{name: "compressed non-string", update: WireUpdate{Topic: "CarData.z", Payload: json.RawMessage(`{}`), Source: Snapshot}, wantErr: "JSON string"},
		{name: "empty compressed string", update: WireUpdate{Topic: "CarData.z", Payload: json.RawMessage(`""`), Source: Snapshot}, wantErr: "non-empty"},
		{name: "invalid base64", update: WireUpdate{Topic: "CarData.z", Payload: json.RawMessage(`"not base64"`), Source: Snapshot}, wantErr: "canonical standard base64"},
		{name: "base64 newline", update: WireUpdate{Topic: "CarData.z", Payload: json.RawMessage(`"/w==\n"`), Source: Snapshot}, wantErr: "canonical standard base64"},
		{name: "base64 padding bits", update: WireUpdate{Topic: "CarData.z", Payload: json.RawMessage(`"/x=="`), Source: Snapshot}, wantErr: "canonical standard base64"},
		{name: "encoded size", update: WireUpdate{Topic: "CarData.z", Payload: json.RawMessage(strconv.Quote(strings.Repeat("A", MaxEncodedPayloadSize+1))), Source: Snapshot}, wantErr: "encoded payload exceeds"},
		{name: "invalid DEFLATE", update: WireUpdate{Topic: "CarData.z", Payload: invalidDeflate, Source: Snapshot}, wantErr: "raw DEFLATE"},
		{name: "trailing compressed data", update: WireUpdate{Topic: "CarData.z", Payload: compressedWithTrailingData, Source: Snapshot}, wantErr: "trailing data"},
		{name: "invalid UTF-8", update: WireUpdate{Topic: "CarData.z", Payload: compressedJSONPayload(t, []byte{0xff}), Source: Snapshot}, wantErr: "UTF-8"},
		{name: "invalid inflated JSON", update: WireUpdate{Topic: "CarData.z", Payload: compressedJSONPayload(t, []byte(`{`)), Source: Snapshot}, wantErr: "valid JSON"},
		{name: "decompressed size", update: WireUpdate{Topic: "CarData.z", Payload: compressedJSONPayload(t, bytes.Repeat([]byte(" "), MaxDecompressedPayloadSize+1)), Source: Snapshot}, wantErr: "decompressed payload exceeds"},
		{name: "empty semantic topic", update: WireUpdate{Topic: ".z", Payload: validCompressed, Source: Snapshot}, wantErr: "semantic topic is empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeLiveTimingUpdate(test.update)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("normalizeLiveTimingUpdate() error = %v, want containing %q", err, test.wantErr)
			}
			if !errors.Is(err, ErrInvalidLiveTimingData) {
				t.Errorf("error does not wrap errInvalidLiveTimingData")
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
