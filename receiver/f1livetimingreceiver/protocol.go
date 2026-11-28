package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/CtrlSpice/bargeboard/internal/f1engine"
)

const (
	subscribeInvocationID = "0"
	maxHubRecordSize      = 16 * 1024 * 1024
	maxWebSocketMessage   = 32 * 1024 * 1024

	hubMessageInvocation = 1
	hubMessageCompletion = 3
	hubMessagePing       = 6
	hubMessageClose      = 7
)

var (
	errSignalRClosed           = errors.New("SignalR connection closed")
	errSignalRReconnectAllowed = errors.New("SignalR connection closed with reconnect allowed")
)

type hubMessage struct {
	Type           *int              `json:"type"`
	InvocationID   string            `json:"invocationId"`
	Target         string            `json:"target"`
	Arguments      []json.RawMessage `json:"arguments"`
	Result         json.RawMessage   `json:"result"`
	ErrorEmpty     bool              `json:"-"`
	HasError       bool              `json:"-"`
	AllowReconnect bool              `json:"allowReconnect"`
}

func subscriptionTopics() []string {
	return []string{
		"Heartbeat",
		"AudioStreams",
		"DriverList",
		"ExtrapolatedClock",
		"RaceControlMessages",
		"SessionInfo",
		"SessionStatus",
		"TeamRadio",
		"TimingAppData",
		"TimingStats",
		"TrackStatus",
		"WeatherData",
		"Position.z",
		"CarData.z",
		"ContentStreams",
		"SessionData",
		"TimingData",
		"TopThree",
		"RcmSeries",
		"LapCount",
		"PitLaneTimeCollection",
		"PitStop",
		"PitStopSeries",
	}
}

func encodeSubscribeInvocation(topics []string) ([]byte, error) {
	if len(topics) == 0 {
		return nil, fmt.Errorf("SignalR subscription requires at least one topic")
	}
	if _, ok := f1engine.LiveTimingTopicSet(topics); !ok {
		return nil, fmt.Errorf("SignalR subscription topics must be unique and non-empty")
	}
	message := struct {
		Type         int        `json:"type"`
		InvocationID string     `json:"invocationId"`
		Target       string     `json:"target"`
		Arguments    [][]string `json:"arguments"`
	}{
		Type:         hubMessageInvocation,
		InvocationID: subscribeInvocationID,
		Target:       "Subscribe",
		Arguments:    [][]string{topics},
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode SignalR subscription: %w", err)
	}
	return append(encoded, recordSeparator), nil
}

// splitHubRecord checks only the first record and returns views of contents.
// Later records are framed only after the caller has processed this one.
func splitHubRecord(contents []byte, scanned int) (record, remaining []byte, complete bool, err error) {
	record, remaining, complete = splitFirstRecord(contents, scanned)
	if len(record) > maxHubRecordSize || (!complete && len(remaining) > maxHubRecordSize) {
		return nil, nil, false, f1engine.InvalidLiveTimingData(fmt.Sprintf("SignalR record exceeds %d bytes", maxHubRecordSize))
	}
	return record, remaining, complete, nil
}

// hubRecordBuffer owns the compaction lifecycle between successful message reads.
// Handshake pending data starts with needsCompaction set because it may retain
// the consumed handshake prefix. The caller appends messages only after next
// returns an incomplete record without error.
type hubRecordBuffer struct {
	contents        []byte
	scanned         int // Separator-free prefix of contents, independent of storage.
	needsCompaction bool
}

func (b *hubRecordBuffer) next() (record []byte, complete bool, err error) {
	record, remaining, complete, err := splitHubRecord(b.contents, b.scanned)
	if err != nil {
		return nil, false, err
	}
	if complete {
		b.contents = remaining
		b.scanned = 0
		b.needsCompaction = true
	} else {
		b.scanned = len(b.contents)
		if b.needsCompaction {
			// Detach the consumed prefix once, then reuse storage across fragments.
			// The byte-relative scan offset survives cloning and append growth.
			b.contents = bytes.Clone(remaining)
			b.needsCompaction = false
		}
	}
	return record, complete, nil
}

func decodeHubRecord(record []byte, requestedTopics []string) (*f1engine.WireBatch, error) {
	message, err := decodeHubMessage(record)
	if err != nil {
		return nil, err
	}
	if message.Type == nil {
		return nil, f1engine.InvalidLiveTimingData("SignalR hub message is missing type")
	}

	switch *message.Type {
	case hubMessageInvocation:
		return decodeFeedInvocation(message)
	case hubMessageCompletion:
		return decodeSubscriptionCompletion(message, requestedTopics)
	case hubMessagePing:
		return nil, nil
	case hubMessageClose:
		if message.AllowReconnect {
			return nil, errSignalRReconnectAllowed
		}
		return nil, errSignalRClosed
	default:
		return nil, nil
	}
}

func decodeHubMessage(record []byte) (hubMessage, error) {
	if !utf8.Valid(record) {
		return hubMessage{}, f1engine.InvalidLiveTimingData("SignalR hub message is not UTF-8")
	}
	var message hubMessage
	seen := make(map[string]struct{})
	err := f1engine.VisitRawJSONObjectMembers(record, func(key, raw json.RawMessage) error {
		field, err := f1engine.DecodeLosslessJSONString(key)
		if err != nil {
			return f1engine.InvalidLiveTimingData("decode SignalR hub message key")
		}
		if _, exists := seen[field]; exists {
			return f1engine.InvalidLiveTimingData("SignalR hub message contains a duplicate field")
		}
		seen[field] = struct{}{}

		var destination any
		switch field {
		case "type":
			destination = &message.Type
		case "invocationId":
			return f1engine.UnmarshalControlString(raw, &message.InvocationID)
		case "target":
			return f1engine.UnmarshalControlString(raw, &message.Target)
		case "arguments":
			destination = &message.Arguments
		case "result":
			destination = &message.Result
		case "error":
			message.HasError = true
			isString, empty := f1engine.JSONStringShape(raw)
			if !isString {
				return f1engine.InvalidLiveTimingData("decode SignalR hub message")
			}
			message.ErrorEmpty = empty
			return nil
		case "allowReconnect":
			destination = &message.AllowReconnect
		default:
			if isHubMessageFieldAlias(field) {
				return f1engine.InvalidLiveTimingData("SignalR hub message field names are case-sensitive")
			}
			return nil // Unknown values remain opaque, including nested keys.
		}
		return json.Unmarshal(raw, destination)
	})
	if err != nil {
		if errors.Is(err, f1engine.ErrInvalidLiveTimingData) {
			return hubMessage{}, err
		}
		return hubMessage{}, f1engine.InvalidLiveTimingData("decode SignalR hub message")
	}
	return message, nil
}

func isHubMessageFieldAlias(field string) bool {
	for _, known := range []string{
		"type", "invocationId", "target", "arguments", "result", "error", "allowReconnect",
	} {
		if strings.EqualFold(field, known) {
			return true
		}
	}
	return false
}

func decodeFeedInvocation(message hubMessage) (*f1engine.WireBatch, error) {
	if message.Target != "feed" {
		return nil, nil
	}
	if len(message.Arguments) != 3 {
		return nil, f1engine.InvalidLiveTimingData(fmt.Sprintf("F1 feed invocation has %d arguments, want 3", len(message.Arguments)))
	}

	var topic string
	if err := f1engine.UnmarshalControlString(message.Arguments[0], &topic); err != nil || topic == "" {
		return nil, f1engine.InvalidLiveTimingData("decode F1 feed topic")
	}
	var timestamp string
	if err := f1engine.UnmarshalControlString(message.Arguments[2], &timestamp); err != nil {
		return nil, f1engine.InvalidLiveTimingData("decode F1 feed timestamp")
	}
	return &f1engine.WireBatch{
		Source: f1engine.Feed,
		Updates: []f1engine.WireUpdate{{
			Topic:     topic,
			Payload:   bytes.Clone(message.Arguments[1]),
			Timestamp: timestamp,
			Source:    f1engine.Feed,
		}},
	}, nil
}

func decodeSubscriptionCompletion(message hubMessage, requestedTopics []string) (*f1engine.WireBatch, error) {
	if message.InvocationID == "" {
		return nil, f1engine.InvalidLiveTimingData("SignalR completion is missing invocation ID")
	}
	hasResult := len(message.Result) > 0
	hasError := message.HasError
	if hasResult && hasError {
		return nil, f1engine.InvalidLiveTimingData("SignalR completion contains both result and error")
	}
	if hasError && message.ErrorEmpty {
		return nil, f1engine.InvalidLiveTimingData("SignalR completion error is invalid")
	}
	if message.InvocationID != subscribeInvocationID {
		return nil, nil
	}
	requestedTopicSet, ok := f1engine.LiveTimingTopicSet(requestedTopics)
	if len(requestedTopics) == 0 || !ok {
		return nil, f1engine.InvalidLiveTimingData("F1 topic subscription completion has no requested topics")
	}
	if hasError {
		return nil, f1engine.InvalidLiveTimingData("F1 topic subscription was rejected")
	}

	topics, payloads, err := decodeSubscriptionSnapshot(message.Result, requestedTopicSet)
	if err != nil {
		return nil, err
	}

	updates := make([]f1engine.WireUpdate, 0, len(topics))
	for _, topic := range topics {
		updates = append(updates, f1engine.WireUpdate{
			Topic:   topic,
			Payload: bytes.Clone(payloads[topic]),
			Source:  f1engine.Snapshot,
		})
	}
	return &f1engine.WireBatch{
		Source:          f1engine.Snapshot,
		RequestedTopics: append([]string(nil), requestedTopics...),
		PresentTopics:   topics,
		Updates:         updates,
	}, nil
}

func decodeSubscriptionSnapshot(
	result json.RawMessage,
	requestedTopics map[string]struct{},
) ([]string, map[string]json.RawMessage, error) {
	if len(result) == 0 || bytes.Equal(bytes.TrimSpace(result), []byte("null")) {
		return []string{}, map[string]json.RawMessage{}, nil
	}

	topics := make([]string, 0)
	payloads := make(map[string]json.RawMessage)
	err := f1engine.VisitRawJSONObjectMembers(result, func(key, payload json.RawMessage) error {
		topic, err := f1engine.DecodeLosslessJSONString(key)
		if err != nil || topic == "" {
			return f1engine.InvalidLiveTimingData("decode F1 subscription snapshot manifest")
		}
		if _, exists := requestedTopics[topic]; !exists {
			return f1engine.InvalidLiveTimingData("F1 subscription snapshot contains an unrequested topic")
		}
		if _, exists := payloads[topic]; exists {
			return f1engine.InvalidLiveTimingData("F1 subscription snapshot contains a duplicate topic")
		}
		topics = append(topics, topic)
		payloads[topic] = bytes.Clone(payload)
		return nil
	})
	if err != nil {
		if errors.Is(err, f1engine.ErrInvalidLiveTimingData) {
			return nil, nil, err
		}
		return nil, nil, f1engine.InvalidLiveTimingData("decode F1 subscription snapshot")
	}

	sort.Strings(topics)
	return topics, payloads, nil
}
