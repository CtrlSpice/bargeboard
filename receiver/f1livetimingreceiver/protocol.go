package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
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
	Error          string            `json:"error"`
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

func splitHubRecords(contents []byte) (records [][]byte, remaining []byte, err error) {
	for {
		separator := bytes.IndexByte(contents, recordSeparator)
		if separator == -1 {
			if len(contents) > maxHubRecordSize {
				return nil, nil, invalidLiveTimingData(fmt.Sprintf("SignalR record exceeds %d bytes", maxHubRecordSize))
			}
			return records, contents, nil
		}
		if separator > maxHubRecordSize {
			return nil, nil, invalidLiveTimingData(fmt.Sprintf("SignalR record exceeds %d bytes", maxHubRecordSize))
		}
		records = append(records, contents[:separator])
		contents = contents[separator+1:]
	}
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
	decoder := json.NewDecoder(bytes.NewReader(record))
	token, err := decoder.Token()
	objectStart, ok := token.(json.Delim)
	if err != nil || !ok || objectStart != '{' {
		return hubMessage{}, invalidLiveTimingData("decode SignalR hub message")
	}

	var message hubMessage
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		field, ok := token.(string)
		if err != nil || !ok {
			return hubMessage{}, invalidLiveTimingData("decode SignalR hub message")
		}
		if _, exists := seen[field]; exists {
			return hubMessage{}, invalidLiveTimingData("SignalR hub message contains a duplicate field")
		}
		seen[field] = struct{}{}

		var destination any
		switch field {
		case "type":
			destination = &message.Type
		case "invocationId":
			destination = &message.InvocationID
		case "target":
			destination = &message.Target
		case "arguments":
			destination = &message.Arguments
		case "result":
			destination = &message.Result
		case "error":
			message.HasError = true
			var rawError json.RawMessage
			if err := decoder.Decode(&rawError); err != nil ||
				bytes.Equal(bytes.TrimSpace(rawError), []byte("null")) ||
				json.Unmarshal(rawError, &message.Error) != nil {
				return hubMessage{}, invalidLiveTimingData("decode SignalR hub message")
			}
			continue
		case "allowReconnect":
			destination = &message.AllowReconnect
		default:
			if isHubMessageFieldAlias(field) {
				return hubMessage{}, invalidLiveTimingData("SignalR hub message field names are case-sensitive")
			}
			destination = new(json.RawMessage)
		}
		if err := decoder.Decode(destination); err != nil {
			return hubMessage{}, invalidLiveTimingData("decode SignalR hub message")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return hubMessage{}, invalidLiveTimingData("decode SignalR hub message")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
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
	if err := json.Unmarshal(message.Arguments[0], &topic); err != nil || topic == "" {
		return nil, invalidLiveTimingData("decode F1 feed topic")
	}
	var timestamp string
	if err := json.Unmarshal(message.Arguments[2], &timestamp); err != nil {
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
	if hasError && message.Error == "" {
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

	decoder := json.NewDecoder(bytes.NewReader(result))
	token, err := decoder.Token()
	objectStart, ok := token.(json.Delim)
	if err != nil || !ok || objectStart != '{' {
		return nil, nil, invalidLiveTimingData("decode F1 subscription snapshot")
	}

	topics := make([]string, 0)
	payloads := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		topic, ok := token.(string)
		if err != nil || !ok || topic == "" {
			return nil, nil, invalidLiveTimingData("decode F1 subscription snapshot manifest")
		}
		if _, exists := requestedTopics[topic]; !exists {
			return nil, nil, invalidLiveTimingData("F1 subscription snapshot contains an unrequested topic")
		}
		if _, exists := payloads[topic]; exists {
			return nil, nil, invalidLiveTimingData("F1 subscription snapshot contains a duplicate topic")
		}

		var payload json.RawMessage
		if err := decoder.Decode(&payload); err != nil {
			return nil, nil, invalidLiveTimingData("decode F1 subscription snapshot payload")
		}
		topics = append(topics, topic)
		payloads[topic] = payload
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, nil, invalidLiveTimingData("decode F1 subscription snapshot")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
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
