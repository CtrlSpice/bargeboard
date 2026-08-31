package f1livetimingreceiver

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"errors"
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

var errInvalidLiveTimingUpdate = errors.New("invalid F1 live timing update")

var rfc3339Timestamp = regexp.MustCompile(
	`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-][0-9]{2}:[0-9]{2})$`,
)

type normalizedLiveTimingUpdate struct {
	topic     string
	payload   json.RawMessage
	timestamp time.Time
	source    liveTimingUpdateSource
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
	if update.topic == "" {
		return normalizedLiveTimingUpdate{}, invalidLiveTimingUpdate(update.topic, "topic is empty")
	}
	timestamp, err := normalizeLiveTimingTimestamp(update.source, update.timestamp)
	if err != nil {
		return normalizedLiveTimingUpdate{}, invalidLiveTimingUpdate(update.topic, err.Error())
	}

	topic := update.topic
	payload := bytes.Clone(update.payload)
	if strings.HasSuffix(topic, ".z") {
		topic = strings.TrimSuffix(topic, ".z")
		if topic == "" {
			return normalizedLiveTimingUpdate{}, invalidLiveTimingUpdate(update.topic, "semantic topic is empty")
		}
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

func invalidLiveTimingUpdate(topic, reason string) error {
	return fmt.Errorf("%w: topic %q: %s", errInvalidLiveTimingUpdate, topic, reason)
}
