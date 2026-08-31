package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

type liveTimingUpdate struct {
	topic     string
	payload   json.RawMessage
	timestamp string
	source    liveTimingUpdateSource
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
				return nil, nil, fmt.Errorf("SignalR record exceeds %d bytes", maxHubRecordSize)
			}
			return records, contents, nil
		}
		if separator > maxHubRecordSize {
			return nil, nil, fmt.Errorf("SignalR record exceeds %d bytes", maxHubRecordSize)
		}
		records = append(records, contents[:separator])
		contents = contents[separator+1:]
	}
}

func decodeHubRecord(record []byte) ([]liveTimingUpdate, error) {
	var message hubMessage
	if err := json.Unmarshal(record, &message); err != nil {
		return nil, fmt.Errorf("decode SignalR hub message: %w", err)
	}
	if message.Type == nil {
		return nil, fmt.Errorf("SignalR hub message is missing type")
	}

	switch *message.Type {
	case hubMessageInvocation:
		return decodeFeedInvocation(message)
	case hubMessageCompletion:
		return decodeSubscriptionCompletion(message)
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

func decodeFeedInvocation(message hubMessage) ([]liveTimingUpdate, error) {
	if message.Target != "feed" {
		return nil, nil
	}
	if len(message.Arguments) != 3 {
		return nil, fmt.Errorf("F1 feed invocation has %d arguments, want 3", len(message.Arguments))
	}

	var topic string
	if err := json.Unmarshal(message.Arguments[0], &topic); err != nil || topic == "" {
		return nil, fmt.Errorf("decode F1 feed topic")
	}
	var timestamp string
	if err := json.Unmarshal(message.Arguments[2], &timestamp); err != nil {
		return nil, fmt.Errorf("decode F1 feed timestamp")
	}
	return []liveTimingUpdate{{
		topic:     topic,
		payload:   bytes.Clone(message.Arguments[1]),
		timestamp: timestamp,
		source:    liveTimingUpdateSourceFeed,
	}}, nil
}

func decodeSubscriptionCompletion(message hubMessage) ([]liveTimingUpdate, error) {
	if message.InvocationID != subscribeInvocationID {
		return nil, nil
	}
	if message.Error != "" {
		return nil, fmt.Errorf("F1 topic subscription was rejected")
	}
	if len(message.Result) == 0 || bytes.Equal(message.Result, []byte("null")) {
		return nil, nil
	}

	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(message.Result, &snapshot); err != nil {
		return nil, fmt.Errorf("decode F1 subscription snapshot: %w", err)
	}
	topics := make([]string, 0, len(snapshot))
	for topic := range snapshot {
		topics = append(topics, topic)
	}
	sort.Strings(topics)

	updates := make([]liveTimingUpdate, 0, len(topics))
	for _, topic := range topics {
		updates = append(updates, liveTimingUpdate{
			topic:   topic,
			payload: bytes.Clone(snapshot[topic]),
			source:  liveTimingUpdateSourceSnapshot,
		})
	}
	return updates, nil
}
