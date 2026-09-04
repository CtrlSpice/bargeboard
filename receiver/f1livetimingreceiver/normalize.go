package f1livetimingreceiver

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
	maxEncodedPayloadSize      = 64 * 1024
	maxDecompressedPayloadSize = 1024 * 1024
)

var rfc3339Timestamp = regexp.MustCompile(
	`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-][0-9]{2}:[0-9]{2})$`,
)

type normalizedLiveTimingUpdate struct {
	topic     string
	payload   json.RawMessage
	timestamp time.Time
	source    liveTimingUpdateSource
}

type normalizedLiveTimingBatch struct {
	source          liveTimingUpdateSource
	requestedTopics []string
	presentTopics   []string
	observationTime time.Time
	updates         []normalizedLiveTimingUpdate
}

func normalizeLiveTimingBatch(
	batch liveTimingBatch,
	observationTime time.Time,
) (normalizedLiveTimingBatch, error) {
	if observationTime.IsZero() {
		return normalizedLiveTimingBatch{}, invalidLiveTimingData("F1 live timing batch observation time is zero")
	}

	switch batch.source {
	case liveTimingUpdateSourceFeed:
		if len(batch.requestedTopics) != 0 || len(batch.presentTopics) != 0 || len(batch.updates) != 1 {
			return normalizedLiveTimingBatch{}, invalidLiveTimingData("F1 feed batch shape is invalid")
		}
	case liveTimingUpdateSourceSnapshot:
		if len(batch.requestedTopics) == 0 || len(batch.presentTopics) != len(batch.updates) {
			return normalizedLiveTimingBatch{}, invalidLiveTimingData("F1 snapshot batch manifest is invalid")
		}
		for index, topic := range batch.presentTopics {
			if topic == "" || topic != batch.updates[index].topic {
				return normalizedLiveTimingBatch{}, invalidLiveTimingData("F1 snapshot batch manifest is invalid")
			}
		}
	default:
		return normalizedLiveTimingBatch{}, invalidLiveTimingData("F1 live timing batch source is unknown")
	}

	for _, update := range batch.updates {
		if update.source != batch.source {
			return normalizedLiveTimingBatch{}, invalidLiveTimingData("F1 live timing batch source is inconsistent")
		}
	}
	updates, err := normalizeLiveTimingUpdates(batch.updates)
	if err != nil {
		return normalizedLiveTimingBatch{}, err
	}
	requestedTopics, err := normalizeLiveTimingTopics(batch.requestedTopics)
	if err != nil {
		return normalizedLiveTimingBatch{}, err
	}
	presentTopics, err := normalizeLiveTimingTopics(batch.presentTopics)
	if err != nil {
		return normalizedLiveTimingBatch{}, err
	}
	requestedTopicSet, _ := liveTimingTopicSet(requestedTopics)
	for index, topic := range presentTopics {
		if _, ok := requestedTopicSet[topic]; !ok || topic != updates[index].topic {
			return normalizedLiveTimingBatch{}, invalidLiveTimingData("F1 normalized snapshot batch manifest is invalid")
		}
	}
	return normalizedLiveTimingBatch{
		source:          batch.source,
		requestedTopics: requestedTopics,
		presentTopics:   presentTopics,
		observationTime: observationTime,
		updates:         updates,
	}, nil
}

func normalizeLiveTimingTopics(topics []string) ([]string, error) {
	normalized := make([]string, 0, len(topics))
	seen := make(map[string]struct{}, len(topics))
	for _, topic := range topics {
		topic, _, err := normalizeLiveTimingTopic(topic)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[topic]; exists {
			return nil, invalidLiveTimingData("F1 live timing topic aliases collide after normalization")
		}
		seen[topic] = struct{}{}
		normalized = append(normalized, topic)
	}
	return normalized, nil
}

func normalizeLiveTimingUpdates(updates []liveTimingUpdate) ([]normalizedLiveTimingUpdate, error) {
	normalized := make([]normalizedLiveTimingUpdate, 0, len(updates))
	for index, update := range updates {
		result, err := normalizeLiveTimingUpdate(update)
		if err != nil {
			return nil, fmt.Errorf("update %d: %w", index, err)
		}
		normalized = append(normalized, result)
	}
	return normalized, nil
}

func normalizeLiveTimingUpdate(update liveTimingUpdate) (normalizedLiveTimingUpdate, error) {
	topic, compressed, err := normalizeLiveTimingTopic(update.topic)
	if err != nil {
		return normalizedLiveTimingUpdate{}, err
	}
	timestamp, err := normalizeLiveTimingTimestamp(update.source, update.timestamp)
	if err != nil {
		return normalizedLiveTimingUpdate{}, invalidLiveTimingUpdate(update.topic, err.Error())
	}

	payload := bytes.Clone(update.payload)
	if compressed {
		payload, err = decompressLiveTimingPayload(update.payload)
		if err != nil {
			return normalizedLiveTimingUpdate{}, invalidLiveTimingUpdate(update.topic, err.Error())
		}
	} else {
		if !utf8.Valid(payload) {
			return normalizedLiveTimingUpdate{}, invalidLiveTimingUpdate(update.topic, "payload is not UTF-8")
		}
		if !json.Valid(payload) {
			return normalizedLiveTimingUpdate{}, invalidLiveTimingUpdate(update.topic, "payload is not valid JSON")
		}
	}

	return normalizedLiveTimingUpdate{
		topic:     topic,
		payload:   payload,
		timestamp: timestamp,
		source:    update.source,
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

func normalizeLiveTimingTimestamp(source liveTimingUpdateSource, raw string) (time.Time, error) {
	switch source {
	case liveTimingUpdateSourceFeed:
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
	case liveTimingUpdateSourceSnapshot:
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

func decompressLiveTimingPayload(payload json.RawMessage) (json.RawMessage, error) {
	var encoded string
	if err := json.Unmarshal(payload, &encoded); err != nil || encoded == "" {
		return nil, fmt.Errorf("compressed payload must be a non-empty JSON string")
	}
	if len(encoded) > maxEncodedPayloadSize {
		return nil, fmt.Errorf("encoded payload exceeds %d bytes", maxEncodedPayloadSize)
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
	decompressed, err := io.ReadAll(io.LimitReader(inflater, maxDecompressedPayloadSize+1))
	closeErr := inflater.Close()
	if err != nil || closeErr != nil {
		return nil, fmt.Errorf("compressed payload is not raw DEFLATE")
	}
	if len(decompressed) > maxDecompressedPayloadSize {
		return nil, fmt.Errorf("decompressed payload exceeds %d bytes", maxDecompressedPayloadSize)
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
	return invalidLiveTimingData(reason)
}
