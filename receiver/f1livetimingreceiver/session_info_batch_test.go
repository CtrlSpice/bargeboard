package f1livetimingreceiver

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const (
	sessionInfoBatchDescriptorA = `{"Meeting":{"Key":1107,"Name":"Abu Dhabi Grand Prix"},"Key":6594,"Type":"Practice","Name":"Practice 1","StartDate":"2021-12-10T13:30:00","EndDate":"2021-12-10T14:30:00","GmtOffset":"04:00:00"}`
	sessionInfoBatchDescriptorB = `{"Meeting":{"Key":1200,"Name":"Example Grand Prix"},"Key":7000,"Type":"Race","Name":"Race","StartDate":"2022-01-01T12:00:00","EndDate":"2022-01-01T14:00:00","GmtOffset":"00:00:00"}`
)

func TestReduceSessionInfoBatchAppliesFeedDescriptor(t *testing.T) {
	batch := normalizeSessionInfoTestBatch(t, liveTimingBatch{
		source: liveTimingUpdateSourceFeed,
		updates: []liveTimingUpdate{{
			topic:     "SessionInfo",
			payload:   json.RawMessage(sessionInfoBatchDescriptorA),
			timestamp: "2021-12-10T09:30:00Z",
			source:    liveTimingUpdateSourceFeed,
		}},
	})

	got, authoritative, err := reduceSessionInfoBatch(sessionInfoState{}, batch)
	if err != nil {
		t.Fatalf("reduceSessionInfoBatch() error = %v", err)
	}
	if !authoritative {
		t.Fatal("SessionInfo feed was not authoritative")
	}
	assertSessionInfoReduction(t, got, expectedSessionInfoBatchReductionA(t))
}

func TestReduceSessionInfoBatchAppliesCompressedSemanticTopic(t *testing.T) {
	batch := normalizeSessionInfoTestBatch(t, liveTimingBatch{
		source: liveTimingUpdateSourceFeed,
		updates: []liveTimingUpdate{{
			topic:     "SessionInfo.z",
			payload:   compressedJSONPayload(t, []byte(sessionInfoBatchDescriptorA)),
			timestamp: "2021-12-10T09:30:00Z",
			source:    liveTimingUpdateSourceFeed,
		}},
	})

	got, authoritative, err := reduceSessionInfoBatch(sessionInfoState{}, batch)
	if err != nil || !authoritative {
		t.Fatalf("reduceSessionInfoBatch() = authoritative %t, error %v", authoritative, err)
	}
	if got.disposition != sessionInfoDispositionInstalled || got.state.routeKey != 6594 {
		t.Fatalf("compressed SessionInfo reduction = %#v", got)
	}
}

func TestReduceSessionInfoBatchIgnoresNonAuthoritativeBatches(t *testing.T) {
	state := sessionInfoBatchTestState()
	tests := []struct {
		name  string
		batch normalizedLiveTimingBatch
	}{
		{
			name: "other feed topic",
			batch: normalizeSessionInfoTestBatch(t, liveTimingBatch{
				source: liveTimingUpdateSourceFeed,
				updates: []liveTimingUpdate{{
					topic:     "TimingData",
					payload:   json.RawMessage(`{}`),
					timestamp: "2025-01-01T00:00:00Z",
					source:    liveTimingUpdateSourceFeed,
				}},
			}),
		},
		{
			name: "snapshot did not request SessionInfo",
			batch: normalizeSessionInfoTestBatch(t, liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"Heartbeat", "TimingData"},
				presentTopics:   []string{"Heartbeat"},
				updates: []liveTimingUpdate{{
					topic:   "Heartbeat",
					payload: json.RawMessage(`"2025-01-01T00:00:00Z"`),
					source:  liveTimingUpdateSourceSnapshot,
				}},
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, authoritative, err := reduceSessionInfoBatch(state, test.batch)
			if err != nil {
				t.Fatalf("reduceSessionInfoBatch() error = %v", err)
			}
			if authoritative || got != (sessionInfoReduction{}) {
				t.Fatalf("reduceSessionInfoBatch() = %#v, authoritative %t, want no-op", got, authoritative)
			}
		})
	}
}

func TestReduceSessionInfoBatchAppliesRequestedSnapshotOmission(t *testing.T) {
	state := sessionInfoBatchTestState()
	tests := []struct {
		name  string
		batch liveTimingBatch
	}{
		{
			name: "empty completion",
			batch: liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"SessionInfo"},
			},
		},
		{
			name: "partial completion",
			batch: liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"Heartbeat", "SessionInfo", "TimingData"},
				presentTopics:   []string{"TimingData"},
				updates: []liveTimingUpdate{{
					topic:   "TimingData",
					payload: json.RawMessage(`{}`),
					source:  liveTimingUpdateSourceSnapshot,
				}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, authoritative, err := reduceSessionInfoBatch(state, normalizeSessionInfoTestBatch(t, test.batch))
			if err != nil || !authoritative {
				t.Fatalf("reduceSessionInfoBatch() = authoritative %t, error %v", authoritative, err)
			}
			wantState := state
			wantState.synchronized = false
			assertSessionInfoReduction(t, got, sessionInfoReduction{
				state:       wantState,
				disposition: sessionInfoDispositionUnresolved,
			})
		})
	}
}

func TestReduceSessionInfoBatchTreatsPresentSemanticFailuresAsAuthoritative(t *testing.T) {
	state := sessionInfoBatchTestState()
	payloads := []struct {
		name    string
		payload json.RawMessage
	}{
		{name: "null", payload: json.RawMessage(`null`)},
		{name: "array", payload: json.RawMessage(`[]`)},
		{name: "scalar", payload: json.RawMessage(`42`)},
		{name: "empty object", payload: json.RawMessage(`{}`)},
		{
			name:    "optional bundles without identity",
			payload: json.RawMessage(`{"Key":99,"StartDate":"2025-01-01T12:00:00","EndDate":"2025-01-01T13:00:00","GmtOffset":"00:00:00"}`),
		},
	}

	for _, payload := range payloads {
		for _, source := range []liveTimingUpdateSource{liveTimingUpdateSourceFeed, liveTimingUpdateSourceSnapshot} {
			name := payload.name + "/feed"
			batch := liveTimingBatch{
				source: source,
				updates: []liveTimingUpdate{{
					topic:   "SessionInfo",
					payload: payload.payload,
					source:  source,
				}},
			}
			if source == liveTimingUpdateSourceFeed {
				batch.updates[0].timestamp = "2025-01-01T00:00:00Z"
			} else {
				name = payload.name + "/snapshot"
				batch.requestedTopics = []string{"SessionInfo"}
				batch.presentTopics = []string{"SessionInfo"}
			}
			t.Run(name, func(t *testing.T) {
				got, authoritative, err := reduceSessionInfoBatch(state, normalizeSessionInfoTestBatch(t, batch))
				if err != nil || !authoritative {
					t.Fatalf("reduceSessionInfoBatch() = authoritative %t, error %v", authoritative, err)
				}
				wantState := state
				wantState.synchronized = false
				assertSessionInfoReduction(t, got, sessionInfoReduction{
					state:       wantState,
					disposition: sessionInfoDispositionUnresolved,
				})
			})
		}
	}
}

func TestReduceSessionInfoBatchDoesNotInheritOptionalBundles(t *testing.T) {
	state := sessionInfoBatchTestState()
	payload := json.RawMessage(`{"Meeting":{"Key":1107,"Name":"Abu Dhabi Grand Prix"},"Type":"Practice","Name":"Practice 1","StartDate":"2021-12-10T13:30:00"}`)
	batch := normalizeSessionInfoTestBatch(t, liveTimingBatch{
		source:          liveTimingUpdateSourceSnapshot,
		requestedTopics: []string{"SessionInfo"},
		presentTopics:   []string{"SessionInfo"},
		updates: []liveTimingUpdate{{
			topic:   "SessionInfo",
			payload: payload,
			source:  liveTimingUpdateSourceSnapshot,
		}},
	})

	got, authoritative, err := reduceSessionInfoBatch(state, batch)
	if err != nil || !authoritative {
		t.Fatalf("reduceSessionInfoBatch() = authoritative %t, error %v", authoritative, err)
	}
	wantState := state
	wantState.routeKey = 0
	wantState.routeAvailable = false
	wantState.schedule = sessionInfoSchedule{}
	wantState.scheduleAvailable = false
	wantState.routeEpoch++
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:           wantState,
		disposition:     sessionInfoDispositionRefreshed,
		routeTransition: true,
	})
}

func TestReduceSessionInfoBatchSnapshotOrderIsIrrelevant(t *testing.T) {
	batches := []liveTimingBatch{
		{
			source:          liveTimingUpdateSourceSnapshot,
			requestedTopics: []string{"SessionInfo", "Heartbeat", "TimingData"},
			presentTopics:   []string{"SessionInfo", "TimingData", "Heartbeat"},
			updates: []liveTimingUpdate{
				{topic: "SessionInfo", payload: json.RawMessage(sessionInfoBatchDescriptorA), source: liveTimingUpdateSourceSnapshot},
				{topic: "TimingData", payload: json.RawMessage(`{}`), source: liveTimingUpdateSourceSnapshot},
				{topic: "Heartbeat", payload: json.RawMessage(`true`), source: liveTimingUpdateSourceSnapshot},
			},
		},
		{
			source:          liveTimingUpdateSourceSnapshot,
			requestedTopics: []string{"TimingData", "Heartbeat", "SessionInfo"},
			presentTopics:   []string{"Heartbeat", "SessionInfo", "TimingData"},
			updates: []liveTimingUpdate{
				{topic: "Heartbeat", payload: json.RawMessage(`true`), source: liveTimingUpdateSourceSnapshot},
				{topic: "SessionInfo", payload: json.RawMessage(sessionInfoBatchDescriptorA), source: liveTimingUpdateSourceSnapshot},
				{topic: "TimingData", payload: json.RawMessage(`{}`), source: liveTimingUpdateSourceSnapshot},
			},
		},
		{
			source:          liveTimingUpdateSourceSnapshot,
			requestedTopics: []string{"Heartbeat", "SessionInfo", "TimingData"},
			presentTopics:   []string{"Heartbeat", "TimingData", "SessionInfo"},
			updates: []liveTimingUpdate{
				{topic: "Heartbeat", payload: json.RawMessage(`true`), source: liveTimingUpdateSourceSnapshot},
				{topic: "TimingData", payload: json.RawMessage(`{}`), source: liveTimingUpdateSourceSnapshot},
				{topic: "SessionInfo", payload: json.RawMessage(sessionInfoBatchDescriptorA), source: liveTimingUpdateSourceSnapshot},
			},
		},
	}

	for index, batch := range batches {
		got, authoritative, err := reduceSessionInfoBatch(
			sessionInfoState{},
			normalizeSessionInfoTestBatch(t, batch),
		)
		if err != nil || !authoritative {
			t.Fatalf("batch %d reduction = authoritative %t, error %v", index, authoritative, err)
		}
		assertSessionInfoReduction(t, got, expectedSessionInfoBatchReductionA(t))
	}
}

func TestReduceSessionInfoBatchKeepsFeedWireOrder(t *testing.T) {
	feedA := normalizeSessionInfoTestFeed(t, sessionInfoBatchDescriptorA, "2022-01-01T00:00:02Z")
	feedB := normalizeSessionInfoTestFeed(t, sessionInfoBatchDescriptorB, "2022-01-01T00:00:01Z")

	stateAB := reduceAuthoritativeSessionInfoTestBatch(t, sessionInfoState{}, feedA).state
	stateAB = reduceAuthoritativeSessionInfoTestBatch(t, stateAB, feedB).state
	stateBA := reduceAuthoritativeSessionInfoTestBatch(t, sessionInfoState{}, feedB).state
	stateBA = reduceAuthoritativeSessionInfoTestBatch(t, stateBA, feedA).state

	if stateAB.identity.meetingKey != 1200 || stateBA.identity.meetingKey != 1107 {
		t.Fatalf("feed order produced meeting keys %d and %d", stateAB.identity.meetingKey, stateBA.identity.meetingKey)
	}
	if stateAB.generation != 2 || stateBA.generation != 2 {
		t.Fatalf("feed order generations = %d and %d, want 2", stateAB.generation, stateBA.generation)
	}
}

func TestReduceSessionInfoBatchRejectsInvalidNormalizedShape(t *testing.T) {
	feedUpdate := normalizedLiveTimingUpdate{topic: "SessionInfo", payload: json.RawMessage(sessionInfoBatchDescriptorA), source: liveTimingUpdateSourceFeed}
	snapshotUpdate := normalizedLiveTimingUpdate{topic: "SessionInfo", payload: json.RawMessage(sessionInfoBatchDescriptorA), source: liveTimingUpdateSourceSnapshot}
	tests := []struct {
		name  string
		batch normalizedLiveTimingBatch
	}{
		{name: "unknown source", batch: normalizedLiveTimingBatch{}},
		{name: "feed without update", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceFeed}},
		{name: "feed with multiple updates", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceFeed, updates: []normalizedLiveTimingUpdate{feedUpdate, feedUpdate}}},
		{name: "feed with requested manifest", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceFeed, requestedTopics: []string{"SessionInfo"}, updates: []normalizedLiveTimingUpdate{feedUpdate}}},
		{name: "feed with present manifest", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceFeed, presentTopics: []string{"SessionInfo"}, updates: []normalizedLiveTimingUpdate{feedUpdate}}},
		{name: "feed with empty topic", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceFeed, updates: []normalizedLiveTimingUpdate{{source: liveTimingUpdateSourceFeed}}}},
		{name: "feed source mismatch", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceFeed, updates: []normalizedLiveTimingUpdate{snapshotUpdate}}},
		{name: "snapshot without requested manifest", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot}},
		{name: "snapshot manifest length mismatch", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"SessionInfo"}, presentTopics: []string{"SessionInfo"}}},
		{name: "snapshot duplicate requested topic", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"SessionInfo", "SessionInfo"}}},
		{name: "snapshot empty requested topic", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{""}}},
		{name: "snapshot duplicate present topic", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"SessionInfo"}, presentTopics: []string{"SessionInfo", "SessionInfo"}, updates: []normalizedLiveTimingUpdate{snapshotUpdate, snapshotUpdate}}},
		{name: "snapshot empty present topic", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"SessionInfo"}, presentTopics: []string{""}, updates: []normalizedLiveTimingUpdate{{source: liveTimingUpdateSourceSnapshot}}}},
		{name: "snapshot present topic was not requested", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"Heartbeat"}, presentTopics: []string{"SessionInfo"}, updates: []normalizedLiveTimingUpdate{snapshotUpdate}}},
		{name: "snapshot update topic mismatch", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"SessionInfo"}, presentTopics: []string{"SessionInfo"}, updates: []normalizedLiveTimingUpdate{{topic: "Heartbeat", source: liveTimingUpdateSourceSnapshot}}}},
		{name: "snapshot update source mismatch", batch: normalizedLiveTimingBatch{source: liveTimingUpdateSourceSnapshot, requestedTopics: []string{"SessionInfo"}, presentTopics: []string{"SessionInfo"}, updates: []normalizedLiveTimingUpdate{feedUpdate}}},
	}

	state := sessionInfoBatchTestState()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, authoritative, err := reduceSessionInfoBatch(state, test.batch)
			if !errors.Is(err, errInvalidNormalizedSessionInfoBatch) {
				t.Fatalf("reduceSessionInfoBatch() error = %v", err)
			}
			if err.Error() != errInvalidNormalizedSessionInfoBatch.Error() {
				t.Errorf("error = %q, want fixed sentinel", err)
			}
			if authoritative || got != (sessionInfoReduction{}) {
				t.Fatalf("invalid batch reduction = %#v, authoritative %t", got, authoritative)
			}
		})
	}
}

func TestReduceSessionInfoBatchRejectsInvalidSelectedPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload json.RawMessage
	}{
		{name: "invalid JSON", payload: json.RawMessage(`{`)},
		{name: "invalid UTF-8", payload: json.RawMessage{'{', '"', 0xff, '"', ':', '1', '}'}},
	}
	state := sessionInfoBatchTestState()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := normalizedLiveTimingBatch{
				source: liveTimingUpdateSourceFeed,
				updates: []normalizedLiveTimingUpdate{{
					topic:   "SessionInfo",
					payload: test.payload,
					source:  liveTimingUpdateSourceFeed,
				}},
			}
			got, authoritative, err := reduceSessionInfoBatch(state, batch)
			if !errors.Is(err, errInvalidNormalizedSessionInfo) {
				t.Fatalf("reduceSessionInfoBatch() error = %v", err)
			}
			if errors.Is(err, errInvalidNormalizedSessionInfoBatch) {
				t.Fatal("selected payload error was classified as a batch shape error")
			}
			if authoritative || got != (sessionInfoReduction{}) {
				t.Fatalf("invalid payload reduction = %#v, authoritative %t", got, authoritative)
			}
		})
	}
}

func normalizeSessionInfoTestBatch(t *testing.T, batch liveTimingBatch) normalizedLiveTimingBatch {
	t.Helper()
	observationTime := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	normalized, err := normalizeLiveTimingBatch(batch, observationTime)
	if err != nil {
		t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
	}
	return normalized
}

func normalizeSessionInfoTestFeed(t *testing.T, payload, timestamp string) normalizedLiveTimingBatch {
	t.Helper()
	return normalizeSessionInfoTestBatch(t, liveTimingBatch{
		source: liveTimingUpdateSourceFeed,
		updates: []liveTimingUpdate{{
			topic:     "SessionInfo",
			payload:   json.RawMessage(payload),
			timestamp: timestamp,
			source:    liveTimingUpdateSourceFeed,
		}},
	})
}

func reduceAuthoritativeSessionInfoTestBatch(
	t *testing.T,
	state sessionInfoState,
	batch normalizedLiveTimingBatch,
) sessionInfoReduction {
	t.Helper()
	reduction, authoritative, err := reduceSessionInfoBatch(state, batch)
	if err != nil || !authoritative {
		t.Fatalf("reduceSessionInfoBatch() = authoritative %t, error %v", authoritative, err)
	}
	return reduction
}

func sessionInfoBatchTestState() sessionInfoState {
	identity := reducerTestIdentity(2021, 1107, canonicalSessionTypePractice, canonicalSessionNamePractice1)
	return reduceSessionInfo(
		sessionInfoState{},
		reducerTestDescriptor(identity, 6594, reducerTestSchedule(1)),
	).state
}

func expectedSessionInfoBatchReductionA(t *testing.T) sessionInfoReduction {
	t.Helper()
	return sessionInfoReduction{
		state: sessionInfoState{
			identity: sessionInfoIdentity{
				season:      2021,
				meetingKey:  1107,
				sessionType: canonicalSessionTypePractice,
				sessionName: canonicalSessionNamePractice1,
			},
			identityAvailable: true,
			synchronized:      true,
			routeKey:          6594,
			routeAvailable:    true,
			schedule: sessionInfoSchedule{
				startUTC:  mustSessionInfoTime(t, "2021-12-10T09:30:00Z"),
				endUTC:    mustSessionInfoTime(t, "2021-12-10T10:30:00Z"),
				utcOffset: 4 * time.Hour,
			},
			scheduleAvailable: true,
			generation:        1,
		},
		disposition: sessionInfoDispositionInstalled,
	}
}
