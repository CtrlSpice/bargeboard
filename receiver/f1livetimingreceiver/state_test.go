package f1livetimingreceiver

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const (
	identityGateDescriptorA          = `{"Meeting":{"Key":1107,"Name":"Abu Dhabi Grand Prix"},"Key":6594,"Type":"Practice","Name":"Practice 1","StartDate":"2021-12-10T13:30:00","EndDate":"2021-12-10T14:30:00","GmtOffset":"04:00:00"}`
	identityGateDescriptorACorrected = `{"Meeting":{"Key":1107,"Name":"Abu Dhabi Grand Prix"},"Key":7165,"Type":"Practice","Name":"Practice 1","StartDate":"2021-12-10T13:30:00","EndDate":"2021-12-10T14:30:00","GmtOffset":"04:00:00"}`
	identityGateDescriptorB          = `{"Meeting":{"Key":1200,"Name":"Example Grand Prix"},"Key":7000,"Type":"Race","Name":"Race","StartDate":"2022-01-01T12:00:00","EndDate":"2022-01-01T14:00:00","GmtOffset":"00:00:00"}`
)

func TestReduceLiveTimingBatchBlocksWithoutSynchronizedIdentity(t *testing.T) {
	tests := []struct {
		name  string
		batch normalizedLiveTimingBatch
	}{
		{name: "other feed topic", batch: normalizeIdentityGateOtherFeed(t, json.RawMessage(`{"opaque":true}`))},
		{
			name: "snapshot did not request SessionInfo",
			batch: normalizeIdentityGateBatch(t, liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"TimingData"},
				presentTopics:   []string{"TimingData"},
				updates: []liveTimingUpdate{{
					topic:   "TimingData",
					payload: json.RawMessage(`{"opaque":true}`),
					source:  liveTimingUpdateSourceSnapshot,
				}},
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := reduceLiveTimingBatch(liveTimingState{}, test.batch)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			assertLiveTimingReduction(t, got, liveTimingReduction{})
		})
	}
}

func TestReduceLiveTimingBatchAllowsNonAuthoritativeBatchWithSynchronizedIdentity(t *testing.T) {
	state := identityGateTestState()
	batches := []normalizedLiveTimingBatch{
		normalizeIdentityGateOtherFeed(t, json.RawMessage(`{"opaque":"feed"}`)),
		normalizeIdentityGateBatch(t, liveTimingBatch{
			source:          liveTimingUpdateSourceSnapshot,
			requestedTopics: []string{"TimingData"},
		}),
	}
	for index, batch := range batches {
		got, err := reduceLiveTimingBatch(state, batch)
		if err != nil {
			t.Fatalf("batch %d error = %v", index, err)
		}
		assertLiveTimingReduction(t, got, liveTimingReduction{
			state:                       state,
			sessionScopedUpdatesAllowed: true,
		})
	}
}

func TestReduceLiveTimingBatchOpensGateAfterSessionInfo(t *testing.T) {
	feed := normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-12-10T09:30:00Z")
	snapshot := normalizeIdentityGateBatch(t, liveTimingBatch{
		source:          liveTimingUpdateSourceSnapshot,
		requestedTopics: []string{"TimingData", "SessionInfo"},
		presentTopics:   []string{"TimingData", "SessionInfo"},
		updates: []liveTimingUpdate{
			{topic: "TimingData", payload: json.RawMessage(`{"opaque":true}`), source: liveTimingUpdateSourceSnapshot},
			{topic: "SessionInfo", payload: json.RawMessage(identityGateDescriptorA), source: liveTimingUpdateSourceSnapshot},
		},
	})
	want := liveTimingReduction{
		state:                       identityGateTestState(),
		sessionInfoDisposition:      sessionInfoDispositionInstalled,
		sessionInfoAuthoritative:    true,
		sessionScopedUpdatesAllowed: true,
	}

	for _, test := range []struct {
		name  string
		batch normalizedLiveTimingBatch
	}{
		{name: "feed", batch: feed},
		{name: "mixed snapshot", batch: snapshot},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := reduceLiveTimingBatch(liveTimingState{}, test.batch)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			assertLiveTimingReduction(t, got, want)
		})
	}
}

func TestReduceLiveTimingBatchUsesOnlyLogicalIdentityForGate(t *testing.T) {
	tests := []struct {
		name         string
		payload      string
		wantRoute    bool
		wantSchedule bool
	}{
		{
			name:    "route and schedule unavailable",
			payload: `{"Meeting":{"Key":1107,"Name":"Abu Dhabi Grand Prix"},"Type":"Practice","Name":"Practice 1","StartDate":"2021-12-10T13:30:00"}`,
		},
		{
			name:         "route unavailable",
			payload:      `{"Meeting":{"Key":1107,"Name":"Abu Dhabi Grand Prix"},"Type":"Practice","Name":"Practice 1","StartDate":"2021-12-10T13:30:00","EndDate":"2021-12-10T14:30:00","GmtOffset":"04:00:00"}`,
			wantSchedule: true,
		},
		{
			name:      "schedule unavailable",
			payload:   `{"Meeting":{"Key":1107,"Name":"Abu Dhabi Grand Prix"},"Key":6594,"Type":"Practice","Name":"Practice 1","StartDate":"2021-12-10T13:30:00"}`,
			wantRoute: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := reduceLiveTimingBatch(
				liveTimingState{},
				normalizeIdentityGateSessionInfoFeed(t, test.payload, "2021-12-10T09:30:00Z"),
			)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			if !got.sessionInfoAuthoritative || !got.sessionScopedUpdatesAllowed ||
				got.sessionInfoDisposition != sessionInfoDispositionInstalled ||
				!got.state.sessionInfo.synchronized || !got.state.sessionInfo.identityAvailable {
				t.Fatalf("logical identity did not open gate: %#v", got)
			}
			if got.state.sessionInfo.routeAvailable != test.wantRoute ||
				got.state.sessionInfo.scheduleAvailable != test.wantSchedule {
				t.Fatalf("optional bundle availability = route %t, schedule %t",
					got.state.sessionInfo.routeAvailable, got.state.sessionInfo.scheduleAvailable)
			}
		})
	}
}

func TestReduceLiveTimingBatchClosesGateOnAuthoritativeFailure(t *testing.T) {
	state := identityGateTestState()
	tests := []struct {
		name  string
		batch normalizedLiveTimingBatch
	}{
		{
			name: "requested omission",
			batch: normalizeIdentityGateBatch(t, liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"SessionInfo", "TimingData"},
				presentTopics:   []string{"TimingData"},
				updates: []liveTimingUpdate{{
					topic:   "TimingData",
					payload: json.RawMessage(`{"opaque":"not retained"}`),
					source:  liveTimingUpdateSourceSnapshot,
				}},
			}),
		},
		{
			name:  "present unresolved descriptor",
			batch: normalizeIdentityGateSessionInfoFeed(t, `null`, "2025-01-01T00:00:00Z"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := reduceLiveTimingBatch(state, test.batch)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			wantState := state
			wantState.sessionInfo.synchronized = false
			assertLiveTimingReduction(t, got, liveTimingReduction{
				state:                    wantState,
				sessionInfoDisposition:   sessionInfoDispositionUnresolved,
				sessionInfoAuthoritative: true,
			})
			if !state.sessionInfo.synchronized {
				t.Fatal("reduceLiveTimingBatch mutated its input state")
			}
		})
	}
}

func TestReduceLiveTimingBatchRecoversWithoutReplayingBlockedUpdates(t *testing.T) {
	installed, err := reduceLiveTimingBatch(
		liveTimingState{},
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-12-10T09:30:00Z"),
	)
	if err != nil {
		t.Fatalf("install SessionInfo: %v", err)
	}
	assertLiveTimingReduction(t, installed, liveTimingReduction{
		state:                       identityGateTestState(),
		sessionInfoDisposition:      sessionInfoDispositionInstalled,
		sessionInfoAuthoritative:    true,
		sessionScopedUpdatesAllowed: true,
	})
	omitted, err := reduceLiveTimingBatch(installed.state, normalizeIdentityGateBatch(t, liveTimingBatch{
		source:          liveTimingUpdateSourceSnapshot,
		requestedTopics: []string{"SessionInfo", "TimingData"},
		presentTopics:   []string{"TimingData"},
		updates: []liveTimingUpdate{{
			topic:   "TimingData",
			payload: json.RawMessage(`{"marker":"blocked"}`),
			source:  liveTimingUpdateSourceSnapshot,
		}},
	}))
	if err != nil {
		t.Fatalf("omit SessionInfo: %v", err)
	}
	wantOmittedState := identityGateTestState()
	wantOmittedState.sessionInfo.synchronized = false
	assertLiveTimingReduction(t, omitted, liveTimingReduction{
		state:                    wantOmittedState,
		sessionInfoDisposition:   sessionInfoDispositionUnresolved,
		sessionInfoAuthoritative: true,
	})

	blocked, err := reduceLiveTimingBatch(
		omitted.state,
		normalizeIdentityGateOtherFeed(t, json.RawMessage(`{"marker":"also blocked"}`)),
	)
	if err != nil {
		t.Fatalf("reduce blocked feed: %v", err)
	}
	assertLiveTimingReduction(t, blocked, liveTimingReduction{state: wantOmittedState})

	recovered, err := reduceLiveTimingBatch(
		blocked.state,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-12-10T09:31:00Z"),
	)
	if err != nil {
		t.Fatalf("recover SessionInfo: %v", err)
	}
	assertLiveTimingReduction(t, recovered, liveTimingReduction{
		state:                       identityGateTestState(),
		sessionInfoDisposition:      sessionInfoDispositionRefreshed,
		sessionInfoAuthoritative:    true,
		sessionScopedUpdatesAllowed: true,
	})

	fresh, err := reduceLiveTimingBatch(
		recovered.state,
		normalizeIdentityGateOtherFeed(t, json.RawMessage(`{"marker":"fresh"}`)),
	)
	if err != nil {
		t.Fatalf("reduce fresh feed: %v", err)
	}
	assertLiveTimingReduction(t, fresh, liveTimingReduction{
		state:                       identityGateTestState(),
		sessionScopedUpdatesAllowed: true,
	})
}

func TestReduceLiveTimingBatchPreservesSessionInfoTransitions(t *testing.T) {
	installed, err := reduceLiveTimingBatch(
		liveTimingState{},
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-12-10T09:30:00Z"),
	)
	if err != nil {
		t.Fatalf("install SessionInfo: %v", err)
	}
	initialState := installed.state

	corrected, err := reduceLiveTimingBatch(
		initialState,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorACorrected, "2021-12-10T09:31:00Z"),
	)
	if err != nil {
		t.Fatalf("correct route: %v", err)
	}
	wantCorrectedState := identityGateTestState()
	wantCorrectedState.sessionInfo.routeKey = 7165
	wantCorrectedState.sessionInfo.routeEpoch = 1
	assertLiveTimingReduction(t, corrected, liveTimingReduction{
		state:                       wantCorrectedState,
		sessionInfoDisposition:      sessionInfoDispositionRefreshed,
		sessionInfoAuthoritative:    true,
		sessionInfoRouteTransition:  true,
		sessionScopedUpdatesAllowed: true,
	})
	if initialState != identityGateTestState() {
		t.Fatal("route correction mutated input state")
	}

	replaced, err := reduceLiveTimingBatch(
		corrected.state,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorB, "2022-01-01T12:00:00Z"),
	)
	if err != nil {
		t.Fatalf("replace SessionInfo: %v", err)
	}
	wantReplacedState := liveTimingState{
		sessionInfo: sessionInfoState{
			identity: sessionInfoIdentity{
				season:      2022,
				meetingKey:  1200,
				sessionType: canonicalSessionTypeRace,
				sessionName: canonicalSessionNameRace,
			},
			identityAvailable: true,
			synchronized:      true,
			routeKey:          7000,
			routeAvailable:    true,
			schedule: sessionInfoSchedule{
				startUTC: time.Date(2022, time.January, 1, 12, 0, 0, 0, time.UTC),
				endUTC:   time.Date(2022, time.January, 1, 14, 0, 0, 0, time.UTC),
			},
			scheduleAvailable: true,
			generation:        2,
		},
	}
	wantReplacedState.sessionInfo.retired.tuples[0] = sessionInfoLogicalTuple{
		season:      2021,
		meetingKey:  1107,
		sessionName: canonicalSessionNamePractice1,
	}
	wantReplacedState.sessionInfo.retired.count = 1
	assertLiveTimingReduction(t, replaced, liveTimingReduction{
		state:                       wantReplacedState,
		sessionInfoDisposition:      sessionInfoDispositionReplaced,
		sessionInfoAuthoritative:    true,
		sessionScopedUpdatesAllowed: true,
	})

	stale, err := reduceLiveTimingBatch(
		replaced.state,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2022-01-01T12:01:00Z"),
	)
	if err != nil {
		t.Fatalf("reduce stale replay: %v", err)
	}
	wantStaleState := wantReplacedState
	wantStaleState.sessionInfo.synchronized = false
	assertLiveTimingReduction(t, stale, liveTimingReduction{
		state:                    wantStaleState,
		sessionInfoDisposition:   sessionInfoDispositionStale,
		sessionInfoAuthoritative: true,
	})
}

func TestReduceLiveTimingBatchPreservesRouteLossAndRestoration(t *testing.T) {
	withoutRoute := `{"Meeting":{"Key":1107,"Name":"Abu Dhabi Grand Prix"},"Type":"Practice","Name":"Practice 1","StartDate":"2021-12-10T13:30:00","EndDate":"2021-12-10T14:30:00","GmtOffset":"04:00:00"}`
	lost, err := reduceLiveTimingBatch(
		identityGateTestState(),
		normalizeIdentityGateSessionInfoFeed(t, withoutRoute, "2021-12-10T09:31:00Z"),
	)
	if err != nil {
		t.Fatalf("lose route: %v", err)
	}
	wantLostState := identityGateTestState()
	wantLostState.sessionInfo.routeKey = 0
	wantLostState.sessionInfo.routeAvailable = false
	wantLostState.sessionInfo.routeEpoch = 1
	assertLiveTimingReduction(t, lost, liveTimingReduction{
		state:                       wantLostState,
		sessionInfoDisposition:      sessionInfoDispositionRefreshed,
		sessionInfoAuthoritative:    true,
		sessionInfoRouteTransition:  true,
		sessionScopedUpdatesAllowed: true,
	})

	restored, err := reduceLiveTimingBatch(
		lost.state,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorACorrected, "2021-12-10T09:32:00Z"),
	)
	if err != nil {
		t.Fatalf("restore route: %v", err)
	}
	wantRestoredState := identityGateTestState()
	wantRestoredState.sessionInfo.routeKey = 7165
	wantRestoredState.sessionInfo.routeEpoch = 2
	assertLiveTimingReduction(t, restored, liveTimingReduction{
		state:                       wantRestoredState,
		sessionInfoDisposition:      sessionInfoDispositionRefreshed,
		sessionInfoAuthoritative:    true,
		sessionInfoRouteTransition:  true,
		sessionScopedUpdatesAllowed: true,
	})
}

func TestReduceLiveTimingBatchClosesGateOnTokenExhaustion(t *testing.T) {
	generationExhausted := identityGateTestState()
	generationExhausted.sessionInfo.generation = ^sessionInfoGeneration(0)
	routeExhausted := identityGateTestState()
	routeExhausted.sessionInfo.routeEpoch = ^sessionInfoRouteEpoch(0)
	tests := []struct {
		name    string
		state   liveTimingState
		payload string
	}{
		{name: "generation", state: generationExhausted, payload: identityGateDescriptorB},
		{name: "routing epoch", state: routeExhausted, payload: identityGateDescriptorACorrected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := reduceLiveTimingBatch(
				test.state,
				normalizeIdentityGateSessionInfoFeed(t, test.payload, "2022-01-01T12:00:00Z"),
			)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			wantState := test.state
			wantState.sessionInfo.synchronized = false
			assertLiveTimingReduction(t, got, liveTimingReduction{
				state:                    wantState,
				sessionInfoDisposition:   sessionInfoDispositionTokenExhausted,
				sessionInfoAuthoritative: true,
			})
		})
	}
}

func TestReduceLiveTimingBatchPropagatesInvariantErrors(t *testing.T) {
	state := identityGateTestState()
	tests := []struct {
		name    string
		batch   normalizedLiveTimingBatch
		wantErr error
	}{
		{name: "invalid batch", batch: normalizedLiveTimingBatch{}, wantErr: errInvalidNormalizedSessionInfoBatch},
		{
			name: "invalid selected payload",
			batch: normalizedLiveTimingBatch{
				source: liveTimingUpdateSourceFeed,
				updates: []normalizedLiveTimingUpdate{{
					topic:   "SessionInfo",
					payload: json.RawMessage(`{`),
					source:  liveTimingUpdateSourceFeed,
				}},
			},
			wantErr: errInvalidNormalizedSessionInfo,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := reduceLiveTimingBatch(state, test.batch)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("reduceLiveTimingBatch() error = %v, want %v", err, test.wantErr)
			}
			if got != (liveTimingReduction{}) {
				t.Fatalf("failed reduction = %#v, want zero", got)
			}
		})
	}
}

func normalizeIdentityGateOtherFeed(t *testing.T, payload json.RawMessage) normalizedLiveTimingBatch {
	t.Helper()
	return normalizeIdentityGateBatch(t, liveTimingBatch{
		source: liveTimingUpdateSourceFeed,
		updates: []liveTimingUpdate{{
			topic:     "TimingData",
			payload:   payload,
			timestamp: "2025-01-01T00:00:00Z",
			source:    liveTimingUpdateSourceFeed,
		}},
	})
}

func normalizeIdentityGateSessionInfoFeed(t *testing.T, payload, timestamp string) normalizedLiveTimingBatch {
	t.Helper()
	return normalizeIdentityGateBatch(t, liveTimingBatch{
		source: liveTimingUpdateSourceFeed,
		updates: []liveTimingUpdate{{
			topic:     "SessionInfo",
			payload:   json.RawMessage(payload),
			timestamp: timestamp,
			source:    liveTimingUpdateSourceFeed,
		}},
	})
}

func normalizeIdentityGateBatch(t *testing.T, batch liveTimingBatch) normalizedLiveTimingBatch {
	t.Helper()
	observationTime := time.Date(2026, time.September, 7, 0, 0, 0, 0, time.UTC)
	normalized, err := normalizeLiveTimingBatch(batch, observationTime)
	if err != nil {
		t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
	}
	return normalized
}

func identityGateTestState() liveTimingState {
	return liveTimingState{
		sessionInfo: sessionInfoState{
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
				startUTC:  time.Date(2021, time.December, 10, 9, 30, 0, 0, time.UTC),
				endUTC:    time.Date(2021, time.December, 10, 10, 30, 0, 0, time.UTC),
				utcOffset: 4 * time.Hour,
			},
			scheduleAvailable: true,
			generation:        1,
		},
	}
}

func assertLiveTimingReduction(t *testing.T, got, want liveTimingReduction) {
	t.Helper()
	if got != want {
		t.Fatalf("reduction = %#v, want %#v", got, want)
	}
}
