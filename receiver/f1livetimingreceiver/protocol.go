package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
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
	errInvalidLiveTimingData   = errors.New("invalid F1 live timing data")
)

type liveTimingUpdate struct {
	topic     string
	payload   json.RawMessage
	timestamp string
	source    liveTimingUpdateSource
}

type liveTimingBatch struct {
	source          liveTimingUpdateSource
	requestedTopics []string
	presentTopics   []string
	updates         []liveTimingUpdate
}

type liveTimingUpdateSource uint8

const (
	liveTimingUpdateSourceFeed liveTimingUpdateSource = iota + 1
	liveTimingUpdateSourceSnapshot
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
	}
}

func encodeSubscribeInvocation(topics []string) ([]byte, error) {
	if len(topics) == 0 {
		return nil, fmt.Errorf("SignalR subscription requires at least one topic")
	}
	if _, ok := liveTimingTopicSet(topics); !ok {
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
func splitHubRecord(contents []byte) (record, remaining []byte, complete bool, err error) {
	record, remaining, complete = splitFirstRecord(contents)
	if len(record) > maxHubRecordSize || (!complete && len(remaining) > maxHubRecordSize) {
		return nil, nil, false, invalidLiveTimingData(fmt.Sprintf("SignalR record exceeds %d bytes", maxHubRecordSize))
	}
	return record, remaining, complete, nil
}

// hubRecordBuffer owns the compaction lifecycle between successful message reads.
// Handshake pending data starts with needsCompaction set because it may retain
// the consumed handshake prefix. The caller appends messages only after next
// returns an incomplete record without error.
type hubRecordBuffer struct {
	contents        []byte
	needsCompaction bool
}

func (b *hubRecordBuffer) next() (record []byte, complete bool, err error) {
	record, remaining, complete, err := splitHubRecord(b.contents)
	if err != nil {
		return nil, false, err
	}
	if complete {
		b.contents = remaining
		b.needsCompaction = true
	} else if b.needsCompaction {
		// Detach the consumed prefix once, then reuse storage across fragments
		// until another complete record creates a new compaction boundary.
		b.contents = bytes.Clone(remaining)
		b.needsCompaction = false
	}
	return record, complete, nil
}

func decodeHubRecord(record []byte, requestedTopics []string) (*liveTimingBatch, error) {
	message, err := decodeHubMessage(record)
	if err != nil {
		return nil, err
	}
	if message.Type == nil {
		return nil, invalidLiveTimingData("SignalR hub message is missing type")
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
		return hubMessage{}, invalidLiveTimingData("SignalR hub message is not UTF-8")
	}
	var message hubMessage
	seen := make(map[string]struct{})
	err := visitRawJSONObject(record, func(key, raw json.RawMessage) error {
		field, err := decodeLosslessJSONString(key)
		if err != nil {
			return invalidLiveTimingData("decode SignalR hub message key")
		}
		if _, exists := seen[field]; exists {
			return invalidLiveTimingData("SignalR hub message contains a duplicate field")
		}
		seen[field] = struct{}{}

		var destination any
		switch field {
		case "type":
			destination = &message.Type
		case "invocationId":
			return unmarshalControlString(raw, &message.InvocationID)
		case "target":
			return unmarshalControlString(raw, &message.Target)
		case "arguments":
			destination = &message.Arguments
		case "result":
			destination = &message.Result
		case "error":
			message.HasError = true
			isString, empty := jsonStringShape(raw)
			if !isString {
				return invalidLiveTimingData("decode SignalR hub message")
			}
			message.ErrorEmpty = empty
			return nil
		case "allowReconnect":
			destination = &message.AllowReconnect
		default:
			if isHubMessageFieldAlias(field) {
				return invalidLiveTimingData("SignalR hub message field names are case-sensitive")
			}
			return nil // Unknown values remain opaque, including nested keys.
		}
		return json.Unmarshal(raw, destination)
	})
	if err != nil {
		if errors.Is(err, errInvalidLiveTimingData) {
			return hubMessage{}, err
		}
		return hubMessage{}, invalidLiveTimingData("decode SignalR hub message")
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

func decodeFeedInvocation(message hubMessage) (*liveTimingBatch, error) {
	if message.Target != "feed" {
		return nil, nil
	}
	if len(message.Arguments) != 3 {
		return nil, invalidLiveTimingData(fmt.Sprintf("F1 feed invocation has %d arguments, want 3", len(message.Arguments)))
	}

	var topic string
	if err := unmarshalControlString(message.Arguments[0], &topic); err != nil || topic == "" {
		return nil, invalidLiveTimingData("decode F1 feed topic")
	}
	var timestamp string
	if err := unmarshalControlString(message.Arguments[2], &timestamp); err != nil {
		return nil, invalidLiveTimingData("decode F1 feed timestamp")
	}
	return &liveTimingBatch{
		source: liveTimingUpdateSourceFeed,
		updates: []liveTimingUpdate{{
			topic:     topic,
			payload:   bytes.Clone(message.Arguments[1]),
			timestamp: timestamp,
			source:    liveTimingUpdateSourceFeed,
		}},
	}, nil
}

func decodeSubscriptionCompletion(message hubMessage, requestedTopics []string) (*liveTimingBatch, error) {
	if message.InvocationID == "" {
		return nil, invalidLiveTimingData("SignalR completion is missing invocation ID")
	}
	hasResult := len(message.Result) > 0
	hasError := message.HasError
	if hasResult && hasError {
		return nil, invalidLiveTimingData("SignalR completion contains both result and error")
	}
	if hasError && message.ErrorEmpty {
		return nil, invalidLiveTimingData("SignalR completion error is invalid")
	}
	if message.InvocationID != subscribeInvocationID {
		return nil, nil
	}
	requestedTopicSet, ok := liveTimingTopicSet(requestedTopics)
	if len(requestedTopics) == 0 || !ok {
		return nil, invalidLiveTimingData("F1 topic subscription completion has no requested topics")
	}
	if hasError {
		return nil, invalidLiveTimingData("F1 topic subscription was rejected")
	}

	topics, payloads, err := decodeSubscriptionSnapshot(message.Result, requestedTopicSet)
	if err != nil {
		return nil, err
	}

	updates := make([]liveTimingUpdate, 0, len(topics))
	for _, topic := range topics {
		updates = append(updates, liveTimingUpdate{
			topic:   topic,
			payload: bytes.Clone(payloads[topic]),
			source:  liveTimingUpdateSourceSnapshot,
		})
	}
	return &liveTimingBatch{
		source:          liveTimingUpdateSourceSnapshot,
		requestedTopics: append([]string(nil), requestedTopics...),
		presentTopics:   topics,
		updates:         updates,
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
	err := visitRawJSONObject(result, func(key, payload json.RawMessage) error {
		topic, err := decodeLosslessJSONString(key)
		if err != nil || topic == "" {
			return invalidLiveTimingData("decode F1 subscription snapshot manifest")
		}
		if _, exists := requestedTopics[topic]; !exists {
			return invalidLiveTimingData("F1 subscription snapshot contains an unrequested topic")
		}
		if _, exists := payloads[topic]; exists {
			return invalidLiveTimingData("F1 subscription snapshot contains a duplicate topic")
		}
		topics = append(topics, topic)
		payloads[topic] = bytes.Clone(payload)
		return nil
	})
	if err != nil {
		if errors.Is(err, errInvalidLiveTimingData) {
			return nil, nil, err
		}
		return nil, nil, invalidLiveTimingData("decode F1 subscription snapshot")
	}

	sort.Strings(topics)
	return topics, payloads, nil
}

func liveTimingTopicSet(topics []string) (map[string]struct{}, bool) {
	set := make(map[string]struct{}, len(topics))
	for _, topic := range topics {
		if topic == "" {
			return nil, false
		}
		if _, exists := set[topic]; exists {
			return nil, false
		}
		set[topic] = struct{}{}
	}
	return set, true
}

func invalidLiveTimingData(reason string) error {
	return fmt.Errorf("%w: %s", errInvalidLiveTimingData, reason)
}
