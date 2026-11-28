package f1engine

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxEncodedPayloadSize      = 64 * 1024
	MaxDecompressedPayloadSize = 1024 * 1024
)

var rfc3339Timestamp = regexp.MustCompile(
	`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-][0-9]{2}:[0-9]{2})$`,
)

type Update struct {
	Topic     string
	Payload   json.RawMessage
	Timestamp time.Time
	Source    Source
	// InitialArchiveRadio is set only on the first record of the static TeamRadio stream.
	InitialArchiveRadio bool
}

type Batch struct {
	Source          Source
	RequestedTopics []string
	PresentTopics   []string
	ObservationTime time.Time
	Updates         []Update
	// Count affected envelopes only after the complete batch validates.
	InvalidUnicodeUpdates int
}

func NormalizeBatch(
	batch WireBatch,
	observationTime time.Time,
) (Batch, error) {
	if observationTime.IsZero() {
		return Batch{}, InvalidLiveTimingData("F1 live timing batch observation time is zero")
	}

	switch batch.Source {
	case Feed:
		if len(batch.RequestedTopics) != 0 || len(batch.PresentTopics) != 0 || len(batch.Updates) != 1 {
			return Batch{}, InvalidLiveTimingData("F1 feed batch shape is invalid")
		}
	case Snapshot:
		requestedTopicSet, ok := LiveTimingTopicSet(batch.RequestedTopics)
		if len(batch.RequestedTopics) == 0 || !ok || len(batch.PresentTopics) != len(batch.Updates) {
			return Batch{}, InvalidLiveTimingData("F1 snapshot batch manifest is invalid")
		}
		for index, topic := range batch.PresentTopics {
			if _, ok := requestedTopicSet[topic]; !ok || topic != batch.Updates[index].Topic {
				return Batch{}, InvalidLiveTimingData("F1 snapshot batch manifest is invalid")
			}
		}
	default:
		return Batch{}, InvalidLiveTimingData("F1 live timing batch source is unknown")
	}

	for _, update := range batch.Updates {
		if update.Source != batch.Source {
			return Batch{}, InvalidLiveTimingData("F1 live timing batch source is inconsistent")
		}
	}
	updates, err := normalizeLiveTimingUpdates(batch.Updates)
	if err != nil {
		return Batch{}, err
	}
	requestedTopics, err := NormalizeLiveTimingTopics(batch.RequestedTopics)
	if err != nil {
		return Batch{}, err
	}
	presentTopics, err := NormalizeLiveTimingTopics(batch.PresentTopics)
	if err != nil {
		return Batch{}, err
	}
	requestedTopicSet, _ := LiveTimingTopicSet(requestedTopics)
	for index, topic := range presentTopics {
		if _, ok := requestedTopicSet[topic]; !ok || topic != updates[index].Topic {
			return Batch{}, InvalidLiveTimingData("F1 normalized snapshot batch manifest is invalid")
		}
	}
	invalidUnicodeUpdates := 0
	for _, update := range updates {
		if HasInvalidJSONScalars(update.Payload) {
			invalidUnicodeUpdates++
		}
	}
	return Batch{
		Source:                batch.Source,
		RequestedTopics:       requestedTopics,
		PresentTopics:         presentTopics,
		ObservationTime:       observationTime,
		Updates:               updates,
		InvalidUnicodeUpdates: invalidUnicodeUpdates,
	}, nil
}

func NormalizeLiveTimingTopics(topics []string) ([]string, error) {
	normalized := make([]string, 0, len(topics))
	seen := make(map[string]struct{}, len(topics))
	for _, topic := range topics {
		topic, _, err := normalizeLiveTimingTopic(topic)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[topic]; exists {
			return nil, InvalidLiveTimingData("F1 live timing topic aliases collide after normalization")
		}
		seen[topic] = struct{}{}
		normalized = append(normalized, topic)
	}
	return normalized, nil
}

func normalizeLiveTimingUpdates(updates []WireUpdate) ([]Update, error) {
	normalized := make([]Update, 0, len(updates))
	for index, update := range updates {
		result, err := normalizeLiveTimingUpdate(update)
		if err != nil {
			return nil, fmt.Errorf("update %d: %w", index, err)
		}
		normalized = append(normalized, result)
	}
	return normalized, nil
}

func normalizeLiveTimingUpdate(update WireUpdate) (Update, error) {
	topic, compressed, err := normalizeLiveTimingTopic(update.Topic)
	if err != nil {
		return Update{}, err
	}
	timestamp, err := NormalizeLiveTimingTimestamp(update.Source, update.Timestamp)
	if err != nil {
		return Update{}, invalidLiveTimingUpdate(update.Topic, err.Error())
	}

	payload := bytes.Clone(update.Payload)
	if compressed {
		payload, err = DecompressLiveTimingPayload(update.Payload)
		if err != nil {
			return Update{}, invalidLiveTimingUpdate(update.Topic, err.Error())
		}
	} else {
		if !utf8.Valid(payload) {
			return Update{}, invalidLiveTimingUpdate(update.Topic, "payload is not UTF-8")
		}
		if !json.Valid(payload) {
			return Update{}, invalidLiveTimingUpdate(update.Topic, "payload is not valid JSON")
		}
	}

	return Update{
		Topic:     topic,
		Payload:   payload,
		Timestamp: timestamp,
		Source:    update.Source,
	}, nil
}

func normalizeLiveTimingTopic(topic string) (string, bool, error) {
	if topic == "" {
		return "", false, invalidLiveTimingUpdate(topic, "topic is empty")
	}
	if !strings.HasSuffix(topic, ".z") {
		return topic, false, nil
	}
	topic = strings.TrimSuffix(topic, ".z")
	if topic == "" {
		return "", false, invalidLiveTimingUpdate(topic, "semantic topic is empty")
	}
	return topic, true, nil
}

func NormalizeLiveTimingTimestamp(source Source, raw string) (time.Time, error) {
	switch source {
	case Feed:
		if raw == "" {
			return time.Time{}, fmt.Errorf("feed timestamp is empty")
		}
		if !rfc3339Timestamp.MatchString(raw) || !validRFC3339Offset(raw) {
			return time.Time{}, fmt.Errorf("feed timestamp is not RFC3339")
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("feed timestamp is not RFC3339")
		}
		return parsed.UTC(), nil
	case Snapshot:
		if raw != "" {
			return time.Time{}, fmt.Errorf("snapshot timestamp must be empty")
		}
		return time.Time{}, nil
	default:
		return time.Time{}, fmt.Errorf("source is unknown")
	}
}

func validRFC3339Offset(raw string) bool {
	if strings.HasSuffix(raw, "Z") {
		return true
	}
	offset := raw[len(raw)-5:]
	return offset[:2] <= "23" && offset[3:] <= "59"
}

func DecompressLiveTimingPayload(payload json.RawMessage) (json.RawMessage, error) {
	encoded, err := DecodeLosslessJSONString(payload)
	if err != nil || encoded == "" {
		return nil, fmt.Errorf("compressed payload must be a non-empty JSON string")
	}
	if len(encoded) > MaxEncodedPayloadSize {
		return nil, fmt.Errorf("encoded payload exceeds %d bytes", MaxEncodedPayloadSize)
	}
	if strings.ContainsAny(encoded, "\r\n") {
		return nil, fmt.Errorf("compressed payload is not canonical standard base64")
	}
	compressed, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("compressed payload is not canonical standard base64")
	}
	if base64.StdEncoding.EncodeToString(compressed) != encoded {
		return nil, fmt.Errorf("compressed payload is not canonical standard base64")
	}
	source := bytes.NewReader(compressed)
	inflater := flate.NewReader(source)
	decompressed, err := io.ReadAll(io.LimitReader(inflater, MaxDecompressedPayloadSize+1))
	closeErr := inflater.Close()
	if err != nil || closeErr != nil {
		return nil, fmt.Errorf("compressed payload is not raw DEFLATE")
	}
	if len(decompressed) > MaxDecompressedPayloadSize {
		return nil, fmt.Errorf("decompressed payload exceeds %d bytes", MaxDecompressedPayloadSize)
	}
	if source.Len() != 0 {
		return nil, fmt.Errorf("compressed payload has trailing data")
	}
	if !utf8.Valid(decompressed) {
		return nil, fmt.Errorf("decompressed payload is not UTF-8")
	}
	if !json.Valid(decompressed) {
		return nil, fmt.Errorf("decompressed payload is not valid JSON")
	}
	return json.RawMessage(decompressed), nil
}

func invalidLiveTimingUpdate(_ string, reason string) error {
	return InvalidLiveTimingData(reason)
}
