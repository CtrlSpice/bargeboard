package f1livetimingreceiver

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type carDataNormalizationFixture struct {
	Name                   string          `json:"name"`
	Source                 string          `json:"source"`
	ArchivePrefix          string          `json:"archive_prefix"`
	CompressedPayload      string          `json:"compressed_payload"`
	SyntheticFeedTimestamp string          `json:"synthetic_feed_timestamp"`
	InflatedPayload        json.RawMessage `json:"inflated_payload"`
}

func TestNormalizeLiveTimingUpdateDecodesArchivedCarData(t *testing.T) {
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
	if fixture.Name != "2025_british_grand_prix_race_first_record" {
		t.Fatalf("fixture name = %q", fixture.Name)
	}
	if fixture.Source != "https://livetiming.formula1.com/static/2025/2025-07-06_British_Grand_Prix/2025-07-06_Race/CarData.z.jsonStream" {
		t.Fatalf("fixture source = %q", fixture.Source)
	}
	if fixture.ArchivePrefix != "00:01:50.190" {
		t.Fatalf("archive prefix = %q", fixture.ArchivePrefix)
	}
	if fixture.SyntheticFeedTimestamp != "2025-07-06T13:09:30.402376Z" {
		t.Fatalf("synthetic feed timestamp = %q", fixture.SyntheticFeedTimestamp)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(fixture.CompressedPayload))); got != "b21a290f4ffc24800f470fda9a0e7fefcd0a3a33e4bd08974f690aac26340c73" {
		t.Fatalf("compressed payload SHA-256 = %s", got)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(fixture.InflatedPayload)); got != "3e2dcbdac301ca7047c6064f4bc8ac0a307e36e0859f3c0c373ae755dbc5c5eb" {
		t.Fatalf("inflated payload SHA-256 = %s", got)
	}

	payload, err := json.Marshal(fixture.CompressedPayload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	input := liveTimingUpdate{
		topic:     "CarData.z",
		payload:   payload,
		timestamp: fixture.SyntheticFeedTimestamp,
		source:    liveTimingUpdateSourceFeed,
	}
	before := input
	before.payload = bytes.Clone(input.payload)

	got, err := normalizeLiveTimingUpdate(input)
	if err != nil {
		t.Fatalf("normalizeLiveTimingUpdate() error = %v", err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("normalizeLiveTimingUpdate() changed input: got %#v, want %#v", input, before)
	}
	want := normalizedLiveTimingUpdate{
		topic:     "CarData",
		payload:   bytes.Clone(fixture.InflatedPayload),
		timestamp: time.Date(2025, 7, 6, 13, 9, 30, 402376000, time.UTC),
		source:    liveTimingUpdateSourceFeed,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeLiveTimingUpdate() = %#v, want complete archived result %#v", got, want)
	}

	input.payload[0] = 'x'
	if !reflect.DeepEqual(got, want) {
		t.Fatal("normalized result aliases compressed input storage")
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
	if !errors.Is(err, errInvalidLiveTimingData) {
		t.Errorf("normalizeLiveTimingUpdates() error does not wrap errInvalidLiveTimingData")
	}
}

func TestNormalizeLiveTimingBatchPreservesSnapshotManifest(t *testing.T) {
	requestedTopics := []string{"Heartbeat", "SessionInfo", "TimingData"}
	presentTopics := []string{"SessionInfo"}
	batch := liveTimingBatch{
		source:          liveTimingUpdateSourceSnapshot,
		requestedTopics: requestedTopics,
		presentTopics:   presentTopics,
		updates: []liveTimingUpdate{{
			topic:   "SessionInfo",
			payload: json.RawMessage(`{"Key":6594}`),
			source:  liveTimingUpdateSourceSnapshot,
		}},
	}
	observationTime := time.Date(2026, 8, 21, 12, 30, 0, 0, time.FixedZone("test", 2*60*60))

	got, err := normalizeLiveTimingBatch(batch, observationTime)
	if err != nil {
		t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
	}
	if got.source != liveTimingUpdateSourceSnapshot {
		t.Errorf("batch source = %d, want snapshot", got.source)
	}
	if !reflect.DeepEqual(got.requestedTopics, requestedTopics) {
		t.Errorf("requested topics = %q, want %q", got.requestedTopics, requestedTopics)
	}
	if !reflect.DeepEqual(got.presentTopics, presentTopics) {
		t.Errorf("present topics = %q, want %q", got.presentTopics, presentTopics)
	}
	if got.observationTime != observationTime {
		t.Errorf("observation time = %s, want exact input %s", got.observationTime, observationTime)
	}
	if len(got.updates) != 1 {
		t.Fatalf("update count = %d, want 1", len(got.updates))
	}
	if got.updates[0].topic != "SessionInfo" || got.updates[0].source != liveTimingUpdateSourceSnapshot {
		t.Errorf("normalized update = %#v", got.updates[0])
	}

	requestedTopics[0] = "changed"
	presentTopics[0] = "changed"
	if got.requestedTopics[0] != "Heartbeat" || got.presentTopics[0] != "SessionInfo" {
		t.Errorf("normalized manifest aliases input: requested = %q, present = %q", got.requestedTopics, got.presentTopics)
	}
}

func TestNormalizeLiveTimingBatchNormalizesCompressedManifest(t *testing.T) {
	observationTime := time.Now()
	got, err := normalizeLiveTimingBatch(liveTimingBatch{
		source:          liveTimingUpdateSourceSnapshot,
		requestedTopics: []string{"CarData.z", "Position.z"},
		presentTopics:   []string{"CarData.z"},
		updates: []liveTimingUpdate{{
			topic:   "CarData.z",
			payload: compressedJSONPayload(t, []byte(`{"Entries":[]}`)),
			source:  liveTimingUpdateSourceSnapshot,
		}},
	}, observationTime)
	if err != nil {
		t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
	}
	if !reflect.DeepEqual(got.requestedTopics, []string{"CarData", "Position"}) {
		t.Errorf("requested topics = %q", got.requestedTopics)
	}
	if !reflect.DeepEqual(got.presentTopics, []string{"CarData"}) {
		t.Errorf("present topics = %q", got.presentTopics)
	}
	if len(got.updates) != 1 || got.updates[0].topic != "CarData" {
		t.Errorf("normalized updates = %#v", got.updates)
	}
	if got.observationTime != observationTime {
		t.Errorf("observation time lost its exact clock value")
	}
}

func TestNormalizeLiveTimingBatchPreservesEmptySnapshot(t *testing.T) {
	observationTime := time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC)
	got, err := normalizeLiveTimingBatch(liveTimingBatch{
		source:          liveTimingUpdateSourceSnapshot,
		requestedTopics: []string{"SessionInfo"},
		presentTopics:   []string{},
		updates:         []liveTimingUpdate{},
	}, observationTime)
	if err != nil {
		t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
	}
	if len(got.presentTopics) != 0 || len(got.updates) != 0 {
		t.Fatalf("normalized empty snapshot present topics = %q, updates = %#v", got.presentTopics, got.updates)
	}
	if !got.observationTime.Equal(observationTime) {
		t.Errorf("observation time = %s, want %s", got.observationTime, observationTime)
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
			batch := liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: slices.Clone(test.requested),
				presentTopics:   slices.Clone(test.present),
			}
			if test.present != nil {
				batch.updates = make([]liveTimingUpdate, 0, len(test.present))
			}
			for _, topic := range test.present {
				payload := plainPayload
				if strings.HasSuffix(topic, ".z") {
					payload = compressedPayload
				}
				batch.updates = append(batch.updates, liveTimingUpdate{
					topic: topic, payload: bytes.Clone(payload), source: liveTimingUpdateSourceSnapshot,
				})
			}
			before := batch
			before.requestedTopics = slices.Clone(batch.requestedTopics)
			before.presentTopics = slices.Clone(batch.presentTopics)
			before.updates = slices.Clone(batch.updates)
			for index := range before.updates {
				before.updates[index].payload = bytes.Clone(batch.updates[index].payload)
			}

			got, err := normalizeLiveTimingBatch(batch, observationTime)
			if !reflect.DeepEqual(batch, before) {
				t.Errorf("input batch changed: got %#v, want %#v", batch, before)
			}
			if test.wantErr {
				if !errors.Is(err, errInvalidLiveTimingData) {
					t.Errorf("error = %v, want errInvalidLiveTimingData", err)
				}
				if !reflect.DeepEqual(got, normalizedLiveTimingBatch{}) {
					t.Errorf("failed normalization returned %#v, want zero batch", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
			}
			want := normalizedLiveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: test.wantRequested,
				presentTopics:   test.wantPresent,
				observationTime: observationTime,
				updates:         make([]normalizedLiveTimingUpdate, 0, len(test.wantPresent)),
			}
			for _, topic := range test.wantPresent {
				want.updates = append(want.updates, normalizedLiveTimingUpdate{
					topic: topic, payload: plainPayload, source: liveTimingUpdateSourceSnapshot,
				})
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("normalized batch = %#v, want %#v", got, want)
			}
		})
	}
}

func TestNormalizeLiveTimingBatchRejectsInvalidEnvelope(t *testing.T) {
	validUpdate := liveTimingUpdate{
		topic:     "SessionStatus",
		payload:   json.RawMessage(`{"Status":"Started"}`),
		timestamp: "2026-08-21T10:30:00.034Z",
		source:    liveTimingUpdateSourceFeed,
	}
	observationTime := time.Date(2026, 8, 21, 10, 30, 1, 0, time.UTC)
	tests := []struct {
		name            string
		batch           liveTimingBatch
		observationTime time.Time
	}{
		{
			name:            "zero observation time",
			batch:           liveTimingBatch{source: liveTimingUpdateSourceFeed, updates: []liveTimingUpdate{validUpdate}},
			observationTime: time.Time{},
		},
		{
			name:            "feed manifest",
			batch:           liveTimingBatch{source: liveTimingUpdateSourceFeed, presentTopics: []string{"SessionStatus"}, updates: []liveTimingUpdate{validUpdate}},
			observationTime: observationTime,
		},
		{
			name: "snapshot manifest mismatch",
			batch: liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"SessionInfo"},
				presentTopics:   []string{"SessionInfo"},
				updates:         []liveTimingUpdate{},
			},
			observationTime: observationTime,
		},
		{
			name: "inconsistent source",
			batch: liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"SessionStatus"},
				presentTopics:   []string{"SessionStatus"},
				updates:         []liveTimingUpdate{validUpdate},
			},
			observationTime: observationTime,
		},
		{
			name: "unrequested present topic",
			batch: liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"SessionInfo"},
				presentTopics:   []string{"WeatherData"},
				updates: []liveTimingUpdate{{
					topic:   "WeatherData",
					payload: json.RawMessage(`{}`),
					source:  liveTimingUpdateSourceSnapshot,
				}},
			},
			observationTime: observationTime,
		},
		{
			name: "normalized topic collision",
			batch: liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"CarData", "CarData.z"},
				presentTopics:   []string{},
				updates:         []liveTimingUpdate{},
			},
			observationTime: observationTime,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeLiveTimingBatch(test.batch, test.observationTime)
			if err == nil {
				t.Fatal("normalizeLiveTimingBatch() error = nil")
			}
			if !errors.Is(err, errInvalidLiveTimingData) {
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
			if !errors.Is(err, errInvalidLiveTimingData) {
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
