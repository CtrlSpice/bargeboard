package f1engine

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const (
	sessionInfoBatchDescriptorA = `{"Meeting":{"Key":21,"Name":"Example Grand Prix"},"Key":101,"Type":"Practice","Name":"Practice 1","StartDate":"2021-05-04T10:15:00","EndDate":"2021-05-04T11:45:00","GmtOffset":"02:00:00"}`
	sessionInfoBatchDescriptorB = `{"Meeting":{"Key":1200,"Name":"Example Grand Prix"},"Key":7000,"Type":"Race","Name":"Race","StartDate":"2022-01-01T12:00:00","EndDate":"2022-01-01T14:00:00","GmtOffset":"00:00:00"}`
)

func TestReduceSessionInfoBatchAppliesFeedDescriptor(t *testing.T) {
	batch := normalizeSessionInfoTestBatch(t, WireBatch{
		Source: Feed,
		Updates: []WireUpdate{{
			Topic:     "SessionInfo",
			Payload:   json.RawMessage(sessionInfoBatchDescriptorA),
			Timestamp: "2021-05-04T08:15:00Z",
			Source:    Feed,
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
	batch := normalizeSessionInfoTestBatch(t, WireBatch{
		Source: Feed,
		Updates: []WireUpdate{{
			Topic:     "SessionInfo.z",
			Payload:   compressedJSONPayload(t, []byte(sessionInfoBatchDescriptorA)),
			Timestamp: "2021-05-04T08:15:00Z",
			Source:    Feed,
		}},
	})

	got, authoritative, err := reduceSessionInfoBatch(sessionInfoState{}, batch)
	if err != nil || !authoritative {
		t.Fatalf("reduceSessionInfoBatch() = authoritative %t, error %v", authoritative, err)
	}
	assertSessionInfoReduction(t, got, expectedSessionInfoBatchReductionA(t))
}

func TestReduceSessionInfoBatchIgnoresNonAuthoritativeBatches(t *testing.T) {
	state := sessionInfoBatchTestState()
	tests := []struct {
		name  string
		batch Batch
	}{
		{
			name: "other feed topic",
			batch: normalizeSessionInfoTestBatch(t, WireBatch{
				Source: Feed,
				Updates: []WireUpdate{{
					Topic:     "TimingData",
					Payload:   json.RawMessage(`{}`),
					Timestamp: "2025-01-01T00:00:00Z",
					Source:    Feed,
				}},
			}),
		},
		{
			name: "snapshot did not request SessionInfo",
			batch: normalizeSessionInfoTestBatch(t, WireBatch{
				Source:          Snapshot,
				RequestedTopics: []string{"Heartbeat", "TimingData"},
				PresentTopics:   []string{"Heartbeat"},
				Updates: []WireUpdate{{
					Topic:   "Heartbeat",
					Payload: json.RawMessage(`"2025-01-01T00:00:00Z"`),
					Source:  Snapshot,
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
		batch WireBatch
	}{
		{
			name: "empty completion",
			batch: WireBatch{
				Source:          Snapshot,
				RequestedTopics: []string{"SessionInfo"},
			},
		},
		{
			name: "partial completion",
			batch: WireBatch{
				Source:          Snapshot,
				RequestedTopics: []string{"Heartbeat", "SessionInfo", "TimingData"},
				PresentTopics:   []string{"TimingData"},
				Updates: []WireUpdate{{
					Topic:   "TimingData",
					Payload: json.RawMessage(`{}`),
					Source:  Snapshot,
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
			wantState.Synchronized = false
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
		issues  sessionInfoIssueSet
	}{
		{name: "null", payload: json.RawMessage(`null`), issues: sessionInfoIssueShape},
		{name: "array", payload: json.RawMessage(`[]`), issues: sessionInfoIssueShape},
		{name: "scalar", payload: json.RawMessage(`42`), issues: sessionInfoIssueShape},
		{name: "empty object", payload: json.RawMessage(`{}`), issues: sessionInfoIssueIdentity | sessionInfoIssueRoute | sessionInfoIssueSchedule},
		{
			name:    "optional bundles without identity",
			payload: json.RawMessage(`{"Key":99,"StartDate":"2025-01-01T12:00:00","EndDate":"2025-01-01T13:00:00","GmtOffset":"00:00:00"}`),
			issues:  sessionInfoIssueIdentity,
		},
	}

	for _, payload := range payloads {
		for _, source := range []Source{Feed, Snapshot} {
			name := payload.name + "/feed"
			batch := WireBatch{
				Source: source,
				Updates: []WireUpdate{{
					Topic:   "SessionInfo",
					Payload: payload.payload,
					Source:  source,
				}},
			}
			if source == Feed {
				batch.Updates[0].Timestamp = "2025-01-01T00:00:00Z"
			} else {
				name = payload.name + "/snapshot"
				batch.RequestedTopics = []string{"SessionInfo"}
				batch.PresentTopics = []string{"SessionInfo"}
			}
			t.Run(name, func(t *testing.T) {
				got, authoritative, err := reduceSessionInfoBatch(state, normalizeSessionInfoTestBatch(t, batch))
				if err != nil || !authoritative {
					t.Fatalf("reduceSessionInfoBatch() = authoritative %t, error %v", authoritative, err)
				}
				wantState := state
				wantState.Synchronized = false
				assertSessionInfoReduction(t, got, sessionInfoReduction{
					state:       wantState,
					disposition: sessionInfoDispositionUnresolved,
					issues:      payload.issues,
				})
			})
		}
	}
}

func TestReduceSessionInfoBatchDoesNotInheritOptionalBundles(t *testing.T) {
	state := sessionInfoBatchTestState()
	payload := json.RawMessage(`{"Meeting":{"Key":21,"Name":"Example Grand Prix"},"Type":"Practice","Name":"Practice 1","StartDate":"2021-05-04T10:15:00"}`)
	batch := normalizeSessionInfoTestBatch(t, WireBatch{
		Source:          Snapshot,
		RequestedTopics: []string{"SessionInfo"},
		PresentTopics:   []string{"SessionInfo"},
		Updates: []WireUpdate{{
			Topic:   "SessionInfo",
			Payload: payload,
			Source:  Snapshot,
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
		disposition:     SessionInfoDispositionRefreshed,
		routeTransition: true,
		issues:          sessionInfoIssueRoute | sessionInfoIssueSchedule,
	})
}

func TestReduceSessionInfoBatchSnapshotOrderIsIrrelevant(t *testing.T) {
	batches := []WireBatch{
		{
			Source:          Snapshot,
			RequestedTopics: []string{"SessionInfo", "Heartbeat", "TimingData"},
			PresentTopics:   []string{"SessionInfo", "TimingData", "Heartbeat"},
			Updates: []WireUpdate{
				{Topic: "SessionInfo", Payload: json.RawMessage(sessionInfoBatchDescriptorA), Source: Snapshot},
				{Topic: "TimingData", Payload: json.RawMessage(`{}`), Source: Snapshot},
				{Topic: "Heartbeat", Payload: json.RawMessage(`true`), Source: Snapshot},
			},
		},
		{
			Source:          Snapshot,
			RequestedTopics: []string{"TimingData", "Heartbeat", "SessionInfo"},
			PresentTopics:   []string{"Heartbeat", "SessionInfo", "TimingData"},
			Updates: []WireUpdate{
				{Topic: "Heartbeat", Payload: json.RawMessage(`true`), Source: Snapshot},
				{Topic: "SessionInfo", Payload: json.RawMessage(sessionInfoBatchDescriptorA), Source: Snapshot},
				{Topic: "TimingData", Payload: json.RawMessage(`{}`), Source: Snapshot},
			},
		},
		{
			Source:          Snapshot,
			RequestedTopics: []string{"Heartbeat", "SessionInfo", "TimingData"},
			PresentTopics:   []string{"Heartbeat", "TimingData", "SessionInfo"},
			Updates: []WireUpdate{
				{Topic: "Heartbeat", Payload: json.RawMessage(`true`), Source: Snapshot},
				{Topic: "TimingData", Payload: json.RawMessage(`{}`), Source: Snapshot},
				{Topic: "SessionInfo", Payload: json.RawMessage(sessionInfoBatchDescriptorA), Source: Snapshot},
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

	wantA := expectedSessionInfoBatchReductionA(t)
	installedA := reduceAuthoritativeSessionInfoTestBatch(t, sessionInfoState{}, feedA)
	assertSessionInfoReduction(t, installedA, wantA)

	wantB := sessionInfoReduction{
		state: sessionInfoState{
			Identity: SessionInfoIdentity{
				Season:      2022,
				meetingKey:  1200,
				sessionType: canonicalSessionTypeRace,
				sessionName: canonicalSessionNameRace,
			},
			IdentityAvailable: true,
			Synchronized:      true,
			routeKey:          7000,
			routeAvailable:    true,
			schedule: sessionInfoSchedule{
				startUTC: mustSessionInfoTime(t, "2022-01-01T12:00:00Z"),
				endUTC:   mustSessionInfoTime(t, "2022-01-01T14:00:00Z"),
			},
			scheduleAvailable: true,
			Generation:        1,
		},
		disposition: SessionInfoDispositionInstalled,
	}
	installedB := reduceAuthoritativeSessionInfoTestBatch(t, sessionInfoState{}, feedB)
	assertSessionInfoReduction(t, installedB, wantB)

	wantAB := wantB
	wantAB.state.Generation = 2
	wantAB.state.retired.tuples[0] = sessionInfoLogicalTuple{
		season:      2021,
		meetingKey:  21,
		sessionName: canonicalSessionNamePractice1,
	}
	wantAB.state.retired.count = 1
	wantAB.disposition = SessionInfoDispositionReplaced
	gotAB := reduceAuthoritativeSessionInfoTestBatch(t, installedA.state, feedB)
	assertSessionInfoReduction(t, gotAB, wantAB)

	wantBA := wantA
	wantBA.state.Generation = 2
	wantBA.state.retired.tuples[0] = sessionInfoLogicalTuple{
		season:      2022,
		meetingKey:  1200,
		sessionName: canonicalSessionNameRace,
	}
	wantBA.state.retired.count = 1
	wantBA.disposition = SessionInfoDispositionReplaced
	gotBA := reduceAuthoritativeSessionInfoTestBatch(t, installedB.state, feedA)
	assertSessionInfoReduction(t, gotBA, wantBA)
}

func TestReduceSessionInfoBatchRejectsInvalidNormalizedShape(t *testing.T) {
	feedUpdate := Update{Topic: "SessionInfo", Payload: json.RawMessage(sessionInfoBatchDescriptorA), Source: Feed}
	snapshotUpdate := Update{Topic: "SessionInfo", Payload: json.RawMessage(sessionInfoBatchDescriptorA), Source: Snapshot}
	tests := []struct {
		name  string
		batch Batch
	}{
		{name: "unknown source", batch: Batch{}},
		{name: "feed without update", batch: Batch{Source: Feed}},
		{name: "feed with multiple updates", batch: Batch{Source: Feed, Updates: []Update{feedUpdate, feedUpdate}}},
		{name: "feed with requested manifest", batch: Batch{Source: Feed, RequestedTopics: []string{"SessionInfo"}, Updates: []Update{feedUpdate}}},
		{name: "feed with present manifest", batch: Batch{Source: Feed, PresentTopics: []string{"SessionInfo"}, Updates: []Update{feedUpdate}}},
		{name: "feed with empty topic", batch: Batch{Source: Feed, Updates: []Update{{Source: Feed}}}},
		{name: "feed source mismatch", batch: Batch{Source: Feed, Updates: []Update{snapshotUpdate}}},
		{name: "snapshot without requested manifest", batch: Batch{Source: Snapshot}},
		{name: "snapshot manifest length mismatch", batch: Batch{Source: Snapshot, RequestedTopics: []string{"SessionInfo"}, PresentTopics: []string{"SessionInfo"}}},
		{name: "snapshot duplicate requested topic", batch: Batch{Source: Snapshot, RequestedTopics: []string{"SessionInfo", "SessionInfo"}}},
		{name: "snapshot empty requested topic", batch: Batch{Source: Snapshot, RequestedTopics: []string{""}}},
		{name: "snapshot duplicate present topic", batch: Batch{Source: Snapshot, RequestedTopics: []string{"SessionInfo"}, PresentTopics: []string{"SessionInfo", "SessionInfo"}, Updates: []Update{snapshotUpdate, snapshotUpdate}}},
		{name: "snapshot empty present topic", batch: Batch{Source: Snapshot, RequestedTopics: []string{"SessionInfo"}, PresentTopics: []string{""}, Updates: []Update{{Source: Snapshot}}}},
		{name: "snapshot present topic was not requested", batch: Batch{Source: Snapshot, RequestedTopics: []string{"Heartbeat"}, PresentTopics: []string{"SessionInfo"}, Updates: []Update{snapshotUpdate}}},
		{name: "snapshot update topic mismatch", batch: Batch{Source: Snapshot, RequestedTopics: []string{"SessionInfo"}, PresentTopics: []string{"SessionInfo"}, Updates: []Update{{Topic: "Heartbeat", Source: Snapshot}}}},
		{name: "snapshot update source mismatch", batch: Batch{Source: Snapshot, RequestedTopics: []string{"SessionInfo"}, PresentTopics: []string{"SessionInfo"}, Updates: []Update{feedUpdate}}},
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
			batch := Batch{
				Source: Feed,
				Updates: []Update{{
					Topic:   "SessionInfo",
					Payload: test.payload,
					Source:  Feed,
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

func normalizeSessionInfoTestBatch(t *testing.T, batch WireBatch) Batch {
	t.Helper()
	observationTime := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	normalized, err := NormalizeBatch(batch, observationTime)
	if err != nil {
		t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
	}
	return normalized
}

func normalizeSessionInfoTestFeed(t *testing.T, payload, timestamp string) Batch {
	t.Helper()
	return normalizeSessionInfoTestBatch(t, WireBatch{
		Source: Feed,
		Updates: []WireUpdate{{
			Topic:     "SessionInfo",
			Payload:   json.RawMessage(payload),
			Timestamp: timestamp,
			Source:    Feed,
		}},
	})
}

func reduceAuthoritativeSessionInfoTestBatch(
	t *testing.T,
	state sessionInfoState,
	batch Batch,
) sessionInfoReduction {
	t.Helper()
	reduction, authoritative, err := reduceSessionInfoBatch(state, batch)
	if err != nil || !authoritative {
		t.Fatalf("reduceSessionInfoBatch() = authoritative %t, error %v", authoritative, err)
	}
	return reduction
}

func sessionInfoBatchTestState() sessionInfoState {
	identity := reducerTestIdentity(2021, 21, canonicalSessionTypePractice, canonicalSessionNamePractice1)
	return reduceSessionInfo(
		sessionInfoState{},
		reducerTestDescriptor(identity, 101, reducerTestSchedule(1)),
	).state
}

func expectedSessionInfoBatchReductionA(t *testing.T) sessionInfoReduction {
	t.Helper()
	return sessionInfoReduction{
		state: sessionInfoState{
			Identity: SessionInfoIdentity{
				Season:      2021,
				meetingKey:  21,
				sessionType: canonicalSessionTypePractice,
				sessionName: canonicalSessionNamePractice1,
			},
			IdentityAvailable: true,
			Synchronized:      true,
			routeKey:          101,
			routeAvailable:    true,
			schedule: sessionInfoSchedule{
				startUTC:  mustSessionInfoTime(t, "2021-05-04T08:15:00Z"),
				endUTC:    mustSessionInfoTime(t, "2021-05-04T09:45:00Z"),
				utcOffset: 2 * time.Hour,
			},
			scheduleAvailable: true,
			Generation:        1,
		},
		disposition: SessionInfoDispositionInstalled,
	}
}
