package f1livetimingreceiver

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func stateUpdate(t *testing.T, s *liveTimingState, source liveTimingUpdateSource, sec int64, pairs ...string) liveTimingEffects {
	t.Helper()
	if source == liveTimingUpdateSourceFeed && len(pairs) > 2 {
		var e liveTimingEffects
		for i := 0; i < len(pairs); i += 2 {
			e = e.append(stateUpdate(t, s, source, sec, pairs[i:i+2]...))
		}
		return e
	}
	b := normalizedLiveTimingBatch{source: source, observationTime: fieldTime(sec)}
	for i := 0; i < len(pairs); i += 2 {
		b.updates = append(b.updates, normalizedLiveTimingUpdate{source: source, topic: pairs[i], payload: []byte(pairs[i+1]), timestamp: fieldTime(sec)})
		if source == liveTimingUpdateSourceSnapshot {
			b.requestedTopics = append(b.requestedTopics, pairs[i])
			b.presentTopics = append(b.presentTopics, pairs[i])
		}
	}
	r, err := reduceLiveTimingBatch(*s, b)
	if err != nil {
		t.Fatal(err)
	}
	*s = r.state
	return r.effects
}
func indexedStatus(index, sec int64, status string) string {
	return fmt.Sprintf(`{"StatusSeries":{"%d":{"Utc":%q,"SessionStatus":%q}}}`, index, fieldTime(sec).Format(time.RFC3339), status)
}
func TestFinalisedTrafficAndEnds(t *testing.T) {
	const feed = liveTimingUpdateSourceFeed
	const snap = liveTimingUpdateSourceSnapshot
	for _, recovered := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot=%t", recovered), func(t *testing.T) {
			s := identityGateTestState()
			s.drivers = fieldRoster()
			stateUpdate(t, &s, feed, 1, "SessionData", indexedStatus(0, 1, "Started"))
			last := stateUpdate(t, &s, feed, 3, "SessionData", indexedStatus(1, 3, "Finalised"))
			if len(last.spans) != 2 || !s.timing.tracesEnded || s.timing.sessionEnded || s.weather.ended {
				t.Fatal("Finalised lifecycle", last)
			}
			e := stateUpdate(t, &s, feed, 4, "TimingData", `{"Lines":{"1":{"Position":"2","NumberOfLaps":1}}}`, "ExtrapolatedClock", `{"Utc":"1970-01-01T00:00:04Z","Remaining":"00:02:00","Extrapolating":true}`, "WeatherData", `{"Rainfall":"0"}`)
			if len(e.metrics) < 2 || len(e.spans) != 0 || s.timing.drivers[1].root.kind != "" || !s.clock.scheduled {
				t.Fatal(e)
			}
			for _, sec := range []int64{5, 7} {
				stateUpdate(t, &s, feed, sec, "CarData", `{"Entries":[`+carRow(fieldTime(sec), fieldCar(1, `"2":20`))+`]}`,
					"PitStop", fmt.Sprintf(`{"RacingNumber":"1","Lap":"%d","PitStopTime":"2.3"}`, sec),
					"RaceControlMessages", fmt.Sprintf(`{"Messages":{"%d":{"Category":"Flag","Scope":"Driver","Flag":"BLUE","RacingNumber":"1","Message":"blue"}}}`, sec))
			}
			source := feed
			if recovered {
				source = snap
				s = reduceLiveTimingDisconnect(s)
			}
			e = stateUpdate(t, &s, source, 9, "SessionData", indexedStatus(2, 6, "Ends"))
			if !s.timing.sessionEnded || s.timing.sessionEndUTC != fieldTime(6) || !s.weather.ended || s.clock.scheduled {
				t.Fatal("Ends lifecycle")
			}
			if recovered {
				if !e.empty() || len(s.timing.control.blue) != 2 {
					t.Fatal("snapshot flushed deltas", e)
				}
				s, e = reduceLiveTimingShutdown(s)
			}
			if len(e.metrics) != 3 || len(e.spans) != 0 {
				t.Fatal(e)
			}
			for _, p := range e.metrics {
				if p.startTime != fieldTime(1) || p.timestamp != fieldTime(6) {
					t.Fatal(p)
				}
				switch p.name {
				case "f1.driver.blue_flags":
					if p.intVal != 1 {
						t.Fatal(p)
					}
				case "f1.pit.stop_duration":
					if p.count != 1 || p.sum != 2.3 {
						t.Fatal(p)
					}
				case "f1.field.speed":
					if p.count != 2 || p.sum != 40 {
						t.Fatal(p)
					}
				default:
					t.Fatal(p.name)
				}
			}
			_, again := reduceLiveTimingShutdown(s)
			if !again.empty() {
				t.Fatal("duplicate terminal delivery", again)
			}
		})
	}
}

func TestGenerationStartBoundaries(t *testing.T) {
	const roster = `{"1":{"Tla":"ABC"}}`
	const feed = liveTimingUpdateSourceFeed
	const snap = liveTimingUpdateSourceSnapshot
	for _, tc := range []struct {
		status     string
		lateFreeze bool
		delta      int64
	}{{"Started", false, 2}, {"Aborted", false, 4}, {"Started", true, 2}} {
		s := identityGateTestState()
		if tc.lateFreeze {
			stateUpdate(t, &s, feed, 3, "SessionData", indexedStatus(0, 2, "Started"))
			stateUpdate(t, &s, snap, 4, "DriverList", roster)
		} else {
			stateUpdate(t, &s, snap, 4, "SessionData", indexedStatus(0, 2, tc.status), "DriverList", roster, "SessionStatus", `{"Status":"`+tc.status+`"}`)
		}
		s = reduceLiveTimingDisconnect(s)
		stateUpdate(t, &s, snap, 5, "SessionData", indexedStatus(0, 2, tc.status), "DriverList", roster, "SessionStatus", `{"Status":"`+tc.status+`"}`, "PitStop", `{}`, "TimingData", `{"Lines":{}}`)
		if s.timing.deltaStart != fieldTime(tc.delta) || !s.timing.late || s.timing.lateFrom != fieldTime(4) {
			t.Fatal(tc, s.timing)
		}
		if (tc.status == "Started") != (s.series.indexedStartedUTC == fieldTime(2)) {
			t.Fatal(tc, s.series.indexedStartedUTC)
		}
		stateUpdate(t, &s, feed, 3, "TimingData", `{"Lines":{"1":{"Position":"1"}}}`)
		if s.timing.drivers[1].root.kind != "" {
			t.Fatal("root before late boundary")
		}
		e := stateUpdate(t, &s, feed, 6, "TimingData", `{"Lines":{"1":{"Position":"2","NumberOfPitStops":2}}}`)
		if s.timing.drivers[1].root.startTime != fieldTime(6) {
			t.Fatal("late root")
		}
		visits := false
		for _, p := range e.metrics {
			if p.name == "f1.driver.pit_lane_visits" {
				visits = true
				if p.startTime != fieldTime(2) {
					t.Fatal(p)
				}
			}
		}
		if visits != tc.lateFreeze {
			t.Fatal(tc, visits)
		}
		stateUpdate(t, &s, feed, 7, "PitStop", `{"RacingNumber":"1","Lap":"1","PitStopTime":"2.3"}`)
		_, e = reduceLiveTimingShutdown(s)
		if len(e.metrics) != 1 || e.metrics[0].name != "f1.pit.stop_duration" || e.metrics[0].startTime != fieldTime(tc.delta) {
			t.Fatal(tc, e.metrics)
		}
	}
}

func TestSnapshotEndsClosesOpenLapBeforeOmission(t *testing.T) {
	s := identityGateTestState()
	s.drivers = fieldRoster()
	stateUpdate(t, &s, liveTimingUpdateSourceFeed, 1, "SessionData", indexedStatus(0, 1, "Started"))
	stateUpdate(t, &s, liveTimingUpdateSourceFeed, 3, "TimingData", `{"Lines":{"1":{"NumberOfLaps":1}}}`)
	s = reduceLiveTimingDisconnect(s)
	r, err := reduceLiveTimingBatch(s, normalizedLiveTimingBatch{
		source: liveTimingUpdateSourceSnapshot, observationTime: fieldTime(12), requestedTopics: []string{"TimingData", "SessionData"}, presentTopics: []string{"SessionData"},
		updates: []normalizedLiveTimingUpdate{{source: liveTimingUpdateSourceSnapshot, topic: "SessionData", payload: []byte(indexedStatus(1, 9, "Ends"))}},
	})
	if err != nil || len(r.effects.metrics) != 0 || len(r.effects.spans) != 4 {
		t.Fatal(err, r.effects)
	}
	for _, sp := range r.effects.spans {
		if sp.endTime != fieldTime(9) {
			t.Fatal(sp)
		}
	}
}

func TestAbortedSnapshotUsesIndexedStartedInsteadOfObservationTime(t *testing.T) {
	s := identityGateTestState()
	stateUpdate(t, &s, liveTimingUpdateSourceSnapshot, 90, "DriverList", `{"1":{"Tla":"ABC"}}`, "SessionStatus", `{"Status":"Aborted"}`, "SessionData", `{"StatusSeries":[{"Utc":"1970-01-01T00:00:02Z","SessionStatus":"Started"},{"Utc":"1970-01-01T00:00:05Z","SessionStatus":"Aborted"}]}`)
	if s.timing.deltaStart != fieldTime(2) || s.series.indexedStartedUTC != fieldTime(2) || !s.timing.start.IsZero() || s.timing.lateFrom != fieldTime(90) {
		t.Fatal(s.timing.deltaStart, s.series.indexedStartedUTC, s.timing.start, s.timing.lateFrom)
	}
	stateUpdate(t, &s, liveTimingUpdateSourceFeed, 91, "RaceControlMessages", `{"Messages":[{"Category":"Flag","Scope":"Driver","Flag":"BLUE","RacingNumber":"1","Utc":"1970-01-01T00:00:06","Message":"blue"}]}`)
	_, e := reduceLiveTimingShutdown(s)
	if len(e.metrics) != 1 || e.metrics[0].startTime != fieldTime(2) || e.metrics[0].timestamp != fieldTime(6) || e.metrics[0].intVal != 1 {
		t.Fatal(e)
	}
}

const (
	identityGateDescriptorA          = `{"Meeting":{"Key":21,"Name":"Example Grand Prix"},"Key":101,"Type":"Practice","Name":"Practice 1","StartDate":"2021-05-04T10:15:00","EndDate":"2021-05-04T11:45:00","GmtOffset":"02:00:00"}`
	identityGateDescriptorACorrected = `{"Meeting":{"Key":21,"Name":"Example Grand Prix"},"Key":102,"Type":"Practice","Name":"Practice 1","StartDate":"2021-05-04T10:15:00","EndDate":"2021-05-04T11:45:00","GmtOffset":"02:00:00"}`
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
				requestedTopics: []string{"Heartbeat"},
				presentTopics:   []string{"Heartbeat"},
				updates: []liveTimingUpdate{{
					topic:   "Heartbeat",
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
			requestedTopics: []string{"Heartbeat"},
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
	feed := normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-05-04T08:15:00Z")
	snapshot := normalizeIdentityGateBatch(t, liveTimingBatch{
		source:          liveTimingUpdateSourceSnapshot,
		requestedTopics: []string{"Heartbeat", "SessionInfo"},
		presentTopics:   []string{"Heartbeat", "SessionInfo"},
		updates: []liveTimingUpdate{
			{topic: "Heartbeat", payload: json.RawMessage(`{"opaque":true}`), source: liveTimingUpdateSourceSnapshot},
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
			payload: `{"Meeting":{"Key":21,"Name":"Example Grand Prix"},"Type":"Practice","Name":"Practice 1","StartDate":"2021-05-04T10:15:00"}`,
		},
		{
			name:         "route unavailable",
			payload:      `{"Meeting":{"Key":21,"Name":"Example Grand Prix"},"Type":"Practice","Name":"Practice 1","StartDate":"2021-05-04T10:15:00","EndDate":"2021-05-04T11:45:00","GmtOffset":"02:00:00"}`,
			wantSchedule: true,
		},
		{
			name:      "schedule unavailable",
			payload:   `{"Meeting":{"Key":21,"Name":"Example Grand Prix"},"Key":101,"Type":"Practice","Name":"Practice 1","StartDate":"2021-05-04T10:15:00"}`,
			wantRoute: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := reduceLiveTimingBatch(
				liveTimingState{},
				normalizeIdentityGateSessionInfoFeed(t, test.payload, "2021-05-04T08:15:00Z"),
			)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			want := liveTimingReduction{
				state:                       identityGateTestState(),
				sessionInfoAuthoritative:    true,
				sessionInfoDisposition:      sessionInfoDispositionInstalled,
				sessionScopedUpdatesAllowed: true,
			}
			if !test.wantRoute {
				want.state.sessionInfo.routeAvailable = false
				want.state.sessionInfo.routeKey = 0
				want.sessionInfoIssues |= sessionInfoIssueRoute
			}
			if !test.wantSchedule {
				want.state.sessionInfo.scheduleAvailable = false
				want.state.sessionInfo.schedule = sessionInfoSchedule{}
				want.sessionInfoIssues |= sessionInfoIssueSchedule
			}
			assertLiveTimingReduction(t, got, want)
		})
	}
}

func TestReduceLiveTimingBatchClosesGateOnAuthoritativeFailure(t *testing.T) {
	state := identityGateTestState()
	tests := []struct {
		name   string
		batch  normalizedLiveTimingBatch
		issues sessionInfoIssueSet
	}{
		{
			name: "requested omission",
			batch: normalizeIdentityGateBatch(t, liveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"SessionInfo", "Heartbeat"},
				presentTopics:   []string{"Heartbeat"},
				updates: []liveTimingUpdate{{
					topic:   "Heartbeat",
					payload: json.RawMessage(`{"opaque":"not retained"}`),
					source:  liveTimingUpdateSourceSnapshot,
				}},
			}),
		},
		{
			name:   "present unresolved descriptor",
			batch:  normalizeIdentityGateSessionInfoFeed(t, `null`, "2025-01-01T00:00:00Z"),
			issues: sessionInfoIssueShape,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := reduceLiveTimingBatch(state, test.batch)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			wantState := state
			wantState = suspendedTestState(wantState)
			wantState.sessionInfo.synchronized = false
			assertLiveTimingReduction(t, got, liveTimingReduction{
				state:                    wantState,
				sessionInfoDisposition:   sessionInfoDispositionUnresolved,
				sessionInfoAuthoritative: true,
				sessionInfoIssues:        test.issues,
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
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-05-04T08:15:00Z"),
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
		requestedTopics: []string{"SessionInfo", "Heartbeat"},
		presentTopics:   []string{"Heartbeat"},
		updates: []liveTimingUpdate{{
			topic:   "Heartbeat",
			payload: json.RawMessage(`{"marker":"blocked"}`),
			source:  liveTimingUpdateSourceSnapshot,
		}},
	}))
	if err != nil {
		t.Fatalf("omit SessionInfo: %v", err)
	}
	wantOmittedState := identityGateTestState()
	wantOmittedState = suspendedTestState(wantOmittedState)
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
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-05-04T08:16:00Z"),
	)
	if err != nil {
		t.Fatalf("recover SessionInfo: %v", err)
	}
	assertLiveTimingReduction(t, recovered, liveTimingReduction{
		state:                       suspendedTestState(identityGateTestState()),
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
		state:                       suspendedTestState(identityGateTestState()),
		sessionScopedUpdatesAllowed: true,
	})
}

func TestReduceLiveTimingBatchPreservesSessionInfoTransitions(t *testing.T) {
	installed, err := reduceLiveTimingBatch(
		liveTimingState{},
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-05-04T08:15:00Z"),
	)
	if err != nil {
		t.Fatalf("install SessionInfo: %v", err)
	}
	initialState := installed.state

	corrected, err := reduceLiveTimingBatch(
		initialState,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorACorrected, "2021-05-04T08:16:00Z"),
	)
	if err != nil {
		t.Fatalf("correct route: %v", err)
	}
	wantCorrectedState := identityGateTestState()
	wantCorrectedState.sessionInfo.routeKey = 102
	wantCorrectedState.sessionInfo.routeEpoch = 1
	assertLiveTimingReduction(t, corrected, liveTimingReduction{
		state:                       wantCorrectedState,
		sessionInfoDisposition:      sessionInfoDispositionRefreshed,
		sessionInfoAuthoritative:    true,
		sessionInfoRouteTransition:  true,
		sessionScopedUpdatesAllowed: true,
	})
	if !reflect.DeepEqual(initialState, identityGateTestState()) {
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
		display: sessionDisplay{meeting: "Example Grand Prix"},
		origin:  time.Date(2026, time.September, 7, 0, 0, 0, 0, time.UTC),
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
		meetingKey:  21,
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
	wantStaleState = suspendedTestState(wantStaleState)
	wantStaleState.sessionInfo.synchronized = false
	assertLiveTimingReduction(t, stale, liveTimingReduction{
		state:                    wantStaleState,
		sessionInfoDisposition:   sessionInfoDispositionStale,
		sessionInfoAuthoritative: true,
	})
}

func TestReduceLiveTimingBatchPreservesRouteLossAndRestoration(t *testing.T) {
	withoutRoute := `{"Meeting":{"Key":21,"Name":"Example Grand Prix"},"Type":"Practice","Name":"Practice 1","StartDate":"2021-05-04T10:15:00","EndDate":"2021-05-04T11:45:00","GmtOffset":"02:00:00"}`
	lost, err := reduceLiveTimingBatch(
		identityGateTestState(),
		normalizeIdentityGateSessionInfoFeed(t, withoutRoute, "2021-05-04T08:16:00Z"),
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
		sessionInfoIssues:           sessionInfoIssueRoute,
		sessionScopedUpdatesAllowed: true,
	})

	restored, err := reduceLiveTimingBatch(
		lost.state,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorACorrected, "2021-05-04T08:17:00Z"),
	)
	if err != nil {
		t.Fatalf("restore route: %v", err)
	}
	wantRestoredState := identityGateTestState()
	wantRestoredState.sessionInfo.routeKey = 102
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
			wantState = suspendedTestState(wantState)
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
			if !reflect.DeepEqual(got, liveTimingReduction{}) {
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
			topic:     "Heartbeat",
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
		display: sessionDisplay{meeting: "Example Grand Prix"},
		origin:  time.Date(2026, time.September, 7, 0, 0, 0, 0, time.UTC),
		sessionInfo: sessionInfoState{
			identity: sessionInfoIdentity{
				season:      2021,
				meetingKey:  21,
				sessionType: canonicalSessionTypePractice,
				sessionName: canonicalSessionNamePractice1,
			},
			identityAvailable: true,
			synchronized:      true,
			routeKey:          101,
			routeAvailable:    true,
			schedule: sessionInfoSchedule{
				startUTC:  time.Date(2021, time.May, 4, 8, 15, 0, 0, time.UTC),
				endUTC:    time.Date(2021, time.May, 4, 9, 45, 0, 0, time.UTC),
				utcOffset: 2 * time.Hour,
			},
			scheduleAvailable: true,
			generation:        1,
		},
	}
}

func assertLiveTimingReduction(t *testing.T, got, want liveTimingReduction) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reduction = %#v, want %#v", got, want)
	}
}

func suspendedTestState(s liveTimingState) liveTimingState {
	s.drivers.synchronized = false
	s.series.unsynchronized, s.status.unsynchronized, s.track.unsynchronized = true, true, true
	s.laps.unsynchronized, s.clock.unsynchronized, s.weather.unsynchronized = true, true, true
	s.timing.cars.unsynchronized, s.radio.unsynchronized = true, true
	s.radio.rows = map[int64]*captureRecord{}
	s.timing.unsynchronized, s.timing.app.unsynchronized, s.timing.stats.unsynchronized = true, true, true
	s.timing.position.unsynchronized, s.timing.control.unsynchronized = true, true
	s.timing.pits.unsynchronized = [3]bool{true, true, true}
	return s
}

func TestSnapshotStagesDependenciesAndRollsBackOnInvariantFailure(t *testing.T) {
	const snap = liveTimingUpdateSourceSnapshot
	b := normalizedLiveTimingBatch{source: snap, observationTime: fieldTime(100)}
	for _, u := range []struct{ topic, p string }{
		{"TimingData", `{"Lines":{"1":{"Position":"2","Stopped":false}}}`},
		{"TrackStatus", `{"Status":"1","Message":"AllClear"}`},
		{"SessionStatus", `{"Status":"Started"}`},
		{"SessionData", `{"StatusSeries":[{"Utc":"1970-01-01T00:00:01Z","TrackStatus":"AllClear","SessionStatus":"Started"}]}`},
		{"DriverList", `{"1":{"Tla":"ABC"}}`},
		{"SessionInfo", identityGateDescriptorB},
	} {
		b.requestedTopics = append(b.requestedTopics, u.topic)
		b.presentTopics = append(b.presentTopics, u.topic)
		b.updates = append(b.updates, normalizedLiveTimingUpdate{source: snap, topic: u.topic, payload: []byte(u.p)})
	}
	check := func(s liveTimingState) liveTimingReduction {
		t.Helper()
		r, err := reduceLiveTimingBatch(s, b)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	initial := check(liveTimingState{})
	if !initial.state.drivers.frozen || !initial.state.timing.late || initial.state.timing.running != 1 || initial.state.timing.drivers[1].root.kind != "" || len(initial.effects.metrics) != 2 || len(initial.effects.logs) != 0 {
		t.Fatal("snapshot baseline", initial.effects)
	}
	slices.Reverse(b.presentTopics)
	slices.Reverse(b.updates)
	if !reflect.DeepEqual(check(liveTimingState{}), initial) {
		t.Fatal("snapshot order changed projection")
	}
	before := check(liveTimingState{}).state
	for i := range b.updates {
		if b.updates[i].topic == "TimingData" {
			b.updates[i].payload = []byte(`{`)
		}
	}
	failed, err := reduceLiveTimingBatch(before, b)
	if err == nil || !reflect.DeepEqual(failed, liveTimingReduction{}) || !reflect.DeepEqual(before, initial.state) {
		t.Fatal("invariant failure committed a partial batch")
	}
}
