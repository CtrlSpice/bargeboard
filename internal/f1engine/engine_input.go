package f1engine

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidLiveTimingData = errors.New("invalid F1 live timing data")

type WireUpdate struct {
	Topic     string
	Payload   json.RawMessage
	Timestamp string
	Source    Source
}

type WireBatch struct {
	Source          Source
	RequestedTopics []string
	PresentTopics   []string
	Updates         []WireUpdate
}

type Source uint8

const (
	Feed Source = iota + 1
	Snapshot
)

func LiveTimingTopicSet(topics []string) (map[string]struct{}, bool) {
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

func InvalidLiveTimingData(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidLiveTimingData, reason)
}

type RacingDiagnostics [3]bool

func (s State) Diagnostics() RacingDiagnostics {
	return RacingDiagnostics{s.idCollision, s.timing.cars.lateSample || s.timing.lateObservation || s.timing.control.blueAfterEnd,
		s.timing.control.overflow || s.radio.overflow || len(s.timing.control.repairOverflow) > 0}
}

func (active RacingDiagnostics) NewMessages(reported RacingDiagnostics) (RacingDiagnostics, []string) {
	var messages []string
	for i, message := range [...]string{
		"F1 span ID collision; affected span subtree suppressed",
		"F1 late metric observation excluded after window finalization",
		"F1 session record bound reached; excess records suppressed",
	} {
		if active[i] && !reported[i] {
			messages = append(messages, message)
			reported[i] = true
		}
	}
	return reported, messages
}
