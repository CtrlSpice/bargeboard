package f1engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func stateUpdate(t *testing.T, s *State, source Source, sec int64, pairs ...string) Effects {
	t.Helper()
	if source == Feed && len(pairs) > 2 {
		var e Effects
		for i := 0; i < len(pairs); i += 2 {
			e = e.append(stateUpdate(t, s, source, sec, pairs[i:i+2]...))
		}
		return e
	}
	b := Batch{Source: source, ObservationTime: fieldTime(sec)}
	for i := 0; i < len(pairs); i += 2 {
		b.Updates = append(b.Updates, Update{Source: source, Topic: pairs[i], Payload: []byte(pairs[i+1]), Timestamp: fieldTime(sec)})
		if source == Snapshot {
			b.RequestedTopics = append(b.RequestedTopics, pairs[i])
			b.PresentTopics = append(b.PresentTopics, pairs[i])
		}
	}
	r, err := ReduceBatch(*s, b)
	if err != nil {
		t.Fatal(err)
	}
	*s = r.State
	return r.Effects
}
func indexedStatus(index, sec int64, status string) string {
	return fmt.Sprintf(`{"StatusSeries":{"%d":{"Utc":%q,"SessionStatus":%q}}}`, index, fieldTime(sec).Format(time.RFC3339), status)
}
func TestFinalisedTrafficAndEnds(t *testing.T) {
	const feed = Feed
	const snap = Snapshot
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
				s = ReduceDisconnect(s)
			}
			e = stateUpdate(t, &s, source, 9, "SessionData", indexedStatus(2, 6, "Ends"))
			if !s.timing.sessionEnded || s.timing.sessionEndUTC != fieldTime(6) || !s.weather.ended || s.clock.scheduled {
				t.Fatal("Ends lifecycle")
			}
			if recovered {
				if !e.empty() || len(s.timing.control.blue) != 2 {
					t.Fatal("snapshot flushed deltas", e)
				}
				s, e = ReduceShutdown(s)
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
			_, again := ReduceShutdown(s)
			if !again.empty() {
				t.Fatal("duplicate terminal delivery", again)
			}
		})
	}
}

func TestGenerationStartBoundaries(t *testing.T) {
	const roster = `{"1":{"Tla":"ABC"}}`
	const feed = Feed
	const snap = Snapshot
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
		s = ReduceDisconnect(s)
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
		_, e = ReduceShutdown(s)
		if len(e.metrics) != 1 || e.metrics[0].name != "f1.pit.stop_duration" || e.metrics[0].startTime != fieldTime(tc.delta) {
			t.Fatal(tc, e.metrics)
		}
	}
}

func TestSnapshotEndsClosesOpenLapBeforeOmission(t *testing.T) {
	s := identityGateTestState()
	s.drivers = fieldRoster()
	stateUpdate(t, &s, Feed, 1, "SessionData", indexedStatus(0, 1, "Started"))
	stateUpdate(t, &s, Feed, 3, "TimingData", `{"Lines":{"1":{"NumberOfLaps":1}}}`)
	s = ReduceDisconnect(s)
	r, err := ReduceBatch(s, Batch{
		Source: Snapshot, ObservationTime: fieldTime(12), RequestedTopics: []string{"TimingData", "SessionData"}, PresentTopics: []string{"SessionData"},
		Updates: []Update{{Source: Snapshot, Topic: "SessionData", Payload: []byte(indexedStatus(1, 9, "Ends"))}},
	})
	if err != nil || len(r.Effects.metrics) != 0 || len(r.Effects.spans) != 4 {
		t.Fatal(err, r.Effects)
	}
	for _, sp := range r.Effects.spans {
		if sp.endTime != fieldTime(9) {
			t.Fatal(sp)
		}
	}
}

func TestAbortedSnapshotUsesIndexedStartedInsteadOfObservationTime(t *testing.T) {
	s := identityGateTestState()
	stateUpdate(t, &s, Snapshot, 90, "DriverList", `{"1":{"Tla":"ABC"}}`, "SessionStatus", `{"Status":"Aborted"}`, "SessionData", `{"StatusSeries":[{"Utc":"1970-01-01T00:00:02Z","SessionStatus":"Started"},{"Utc":"1970-01-01T00:00:05Z","SessionStatus":"Aborted"}]}`)
	if s.timing.deltaStart != fieldTime(2) || s.series.indexedStartedUTC != fieldTime(2) || !s.timing.start.IsZero() || s.timing.lateFrom != fieldTime(90) {
		t.Fatal(s.timing.deltaStart, s.series.indexedStartedUTC, s.timing.start, s.timing.lateFrom)
	}
	stateUpdate(t, &s, Feed, 91, "RaceControlMessages", `{"Messages":[{"Category":"Flag","Scope":"Driver","Flag":"BLUE","RacingNumber":"1","Utc":"1970-01-01T00:00:06","Message":"blue"}]}`)
	_, e := ReduceShutdown(s)
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
		batch Batch
	}{
		{name: "other feed topic", batch: normalizeIdentityGateOtherFeed(t, json.RawMessage(`{"opaque":true}`))},
		{
			name: "snapshot did not request SessionInfo",
			batch: normalizeIdentityGateBatch(t, WireBatch{
				Source:          Snapshot,
				RequestedTopics: []string{"Heartbeat"},
				PresentTopics:   []string{"Heartbeat"},
				Updates: []WireUpdate{{
					Topic:   "Heartbeat",
					Payload: json.RawMessage(`{"opaque":true}`),
					Source:  Snapshot,
				}},
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReduceBatch(State{}, test.batch)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			assertLiveTimingReduction(t, got, Reduction{})
		})
	}
}

func TestReduceLiveTimingBatchAllowsNonAuthoritativeBatchWithSynchronizedIdentity(t *testing.T) {
	state := identityGateTestState()
	batches := []Batch{
		normalizeIdentityGateOtherFeed(t, json.RawMessage(`{"opaque":"feed"}`)),
		normalizeIdentityGateBatch(t, WireBatch{
			Source:          Snapshot,
			RequestedTopics: []string{"Heartbeat"},
		}),
	}
	for index, batch := range batches {
		got, err := ReduceBatch(state, batch)
		if err != nil {
			t.Fatalf("batch %d error = %v", index, err)
		}
		assertLiveTimingReduction(t, got, Reduction{
			State:                       state,
			sessionScopedUpdatesAllowed: true,
		})
	}
}

func TestReduceLiveTimingBatchOpensGateAfterSessionInfo(t *testing.T) {
	feed := normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-05-04T08:15:00Z")
	snapshot := normalizeIdentityGateBatch(t, WireBatch{
		Source:          Snapshot,
		RequestedTopics: []string{"Heartbeat", "SessionInfo"},
		PresentTopics:   []string{"Heartbeat", "SessionInfo"},
		Updates: []WireUpdate{
			{Topic: "Heartbeat", Payload: json.RawMessage(`{"opaque":true}`), Source: Snapshot},
			{Topic: "SessionInfo", Payload: json.RawMessage(identityGateDescriptorA), Source: Snapshot},
		},
	})
	want := Reduction{
		State:                       identityGateTestState(),
		SessionInfoDisposition:      SessionInfoDispositionInstalled,
		sessionInfoAuthoritative:    true,
		sessionScopedUpdatesAllowed: true,
	}

	for _, test := range []struct {
		name  string
		batch Batch
	}{
		{name: "feed", batch: feed},
		{name: "mixed snapshot", batch: snapshot},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReduceBatch(State{}, test.batch)
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
			got, err := ReduceBatch(
				State{},
				normalizeIdentityGateSessionInfoFeed(t, test.payload, "2021-05-04T08:15:00Z"),
			)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			want := Reduction{
				State:                       identityGateTestState(),
				sessionInfoAuthoritative:    true,
				SessionInfoDisposition:      SessionInfoDispositionInstalled,
				sessionScopedUpdatesAllowed: true,
			}
			if !test.wantRoute {
				want.State.SessionInfo.routeAvailable = false
				want.State.SessionInfo.routeKey = 0
				want.sessionInfoIssues |= sessionInfoIssueRoute
			}
			if !test.wantSchedule {
				want.State.SessionInfo.scheduleAvailable = false
				want.State.SessionInfo.schedule = sessionInfoSchedule{}
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
		batch  Batch
		issues sessionInfoIssueSet
	}{
		{
			name: "requested omission",
			batch: normalizeIdentityGateBatch(t, WireBatch{
				Source:          Snapshot,
				RequestedTopics: []string{"SessionInfo", "Heartbeat"},
				PresentTopics:   []string{"Heartbeat"},
				Updates: []WireUpdate{{
					Topic:   "Heartbeat",
					Payload: json.RawMessage(`{"opaque":"not retained"}`),
					Source:  Snapshot,
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
			got, err := ReduceBatch(state, test.batch)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			wantState := state
			wantState = suspendedTestState(wantState)
			wantState.SessionInfo.Synchronized = false
			assertLiveTimingReduction(t, got, Reduction{
				State:                    wantState,
				SessionInfoDisposition:   sessionInfoDispositionUnresolved,
				sessionInfoAuthoritative: true,
				sessionInfoIssues:        test.issues,
			})
			if !state.SessionInfo.Synchronized {
				t.Fatal("reduceLiveTimingBatch mutated its input state")
			}
		})
	}
}

func TestReduceLiveTimingBatchRecoversWithoutReplayingBlockedUpdates(t *testing.T) {
	installed, err := ReduceBatch(
		State{},
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-05-04T08:15:00Z"),
	)
	if err != nil {
		t.Fatalf("install SessionInfo: %v", err)
	}
	assertLiveTimingReduction(t, installed, Reduction{
		State:                       identityGateTestState(),
		SessionInfoDisposition:      SessionInfoDispositionInstalled,
		sessionInfoAuthoritative:    true,
		sessionScopedUpdatesAllowed: true,
	})
	omitted, err := ReduceBatch(installed.State, normalizeIdentityGateBatch(t, WireBatch{
		Source:          Snapshot,
		RequestedTopics: []string{"SessionInfo", "Heartbeat"},
		PresentTopics:   []string{"Heartbeat"},
		Updates: []WireUpdate{{
			Topic:   "Heartbeat",
			Payload: json.RawMessage(`{"marker":"blocked"}`),
			Source:  Snapshot,
		}},
	}))
	if err != nil {
		t.Fatalf("omit SessionInfo: %v", err)
	}
	wantOmittedState := identityGateTestState()
	wantOmittedState = suspendedTestState(wantOmittedState)
	wantOmittedState.SessionInfo.Synchronized = false
	assertLiveTimingReduction(t, omitted, Reduction{
		State:                    wantOmittedState,
		SessionInfoDisposition:   sessionInfoDispositionUnresolved,
		sessionInfoAuthoritative: true,
	})

	blocked, err := ReduceBatch(
		omitted.State,
		normalizeIdentityGateOtherFeed(t, json.RawMessage(`{"marker":"also blocked"}`)),
	)
	if err != nil {
		t.Fatalf("reduce blocked feed: %v", err)
	}
	assertLiveTimingReduction(t, blocked, Reduction{State: wantOmittedState})

	recovered, err := ReduceBatch(
		blocked.State,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-05-04T08:16:00Z"),
	)
	if err != nil {
		t.Fatalf("recover SessionInfo: %v", err)
	}
	assertLiveTimingReduction(t, recovered, Reduction{
		State:                       suspendedTestState(identityGateTestState()),
		SessionInfoDisposition:      SessionInfoDispositionRefreshed,
		sessionInfoAuthoritative:    true,
		sessionScopedUpdatesAllowed: true,
	})

	fresh, err := ReduceBatch(
		recovered.State,
		normalizeIdentityGateOtherFeed(t, json.RawMessage(`{"marker":"fresh"}`)),
	)
	if err != nil {
		t.Fatalf("reduce fresh feed: %v", err)
	}
	assertLiveTimingReduction(t, fresh, Reduction{
		State:                       suspendedTestState(identityGateTestState()),
		sessionScopedUpdatesAllowed: true,
	})
}

func TestReduceLiveTimingBatchPreservesSessionInfoTransitions(t *testing.T) {
	installed, err := ReduceBatch(
		State{},
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2021-05-04T08:15:00Z"),
	)
	if err != nil {
		t.Fatalf("install SessionInfo: %v", err)
	}
	initialState := installed.State

	corrected, err := ReduceBatch(
		initialState,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorACorrected, "2021-05-04T08:16:00Z"),
	)
	if err != nil {
		t.Fatalf("correct route: %v", err)
	}
	wantCorrectedState := identityGateTestState()
	wantCorrectedState.SessionInfo.routeKey = 102
	wantCorrectedState.SessionInfo.routeEpoch = 1
	assertLiveTimingReduction(t, corrected, Reduction{
		State:                       wantCorrectedState,
		SessionInfoDisposition:      SessionInfoDispositionRefreshed,
		sessionInfoAuthoritative:    true,
		sessionInfoRouteTransition:  true,
		sessionScopedUpdatesAllowed: true,
	})
	if !reflect.DeepEqual(initialState, identityGateTestState()) {
		t.Fatal("route correction mutated input state")
	}

	replaced, err := ReduceBatch(
		corrected.State,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorB, "2022-01-01T12:00:00Z"),
	)
	if err != nil {
		t.Fatalf("replace SessionInfo: %v", err)
	}
	wantReplacedState := State{
		display: sessionDisplay{meeting: "Example Grand Prix"},
		Origin:  time.Date(2026, time.September, 7, 0, 0, 0, 0, time.UTC),
		SessionInfo: sessionInfoState{
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
				startUTC: time.Date(2022, time.January, 1, 12, 0, 0, 0, time.UTC),
				endUTC:   time.Date(2022, time.January, 1, 14, 0, 0, 0, time.UTC),
			},
			scheduleAvailable: true,
			Generation:        2,
		},
	}
	wantReplacedState.SessionInfo.retired.tuples[0] = sessionInfoLogicalTuple{
		season:      2021,
		meetingKey:  21,
		sessionName: canonicalSessionNamePractice1,
	}
	wantReplacedState.SessionInfo.retired.count = 1
	assertLiveTimingReduction(t, replaced, Reduction{
		State:                       wantReplacedState,
		SessionInfoDisposition:      SessionInfoDispositionReplaced,
		sessionInfoAuthoritative:    true,
		sessionScopedUpdatesAllowed: true,
	})

	stale, err := ReduceBatch(
		replaced.State,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorA, "2022-01-01T12:01:00Z"),
	)
	if err != nil {
		t.Fatalf("reduce stale replay: %v", err)
	}
	wantStaleState := wantReplacedState
	wantStaleState = suspendedTestState(wantStaleState)
	wantStaleState.SessionInfo.Synchronized = false
	assertLiveTimingReduction(t, stale, Reduction{
		State:                    wantStaleState,
		SessionInfoDisposition:   sessionInfoDispositionStale,
		sessionInfoAuthoritative: true,
	})
}

func TestReduceLiveTimingBatchPreservesRouteLossAndRestoration(t *testing.T) {
	withoutRoute := `{"Meeting":{"Key":21,"Name":"Example Grand Prix"},"Type":"Practice","Name":"Practice 1","StartDate":"2021-05-04T10:15:00","EndDate":"2021-05-04T11:45:00","GmtOffset":"02:00:00"}`
	lost, err := ReduceBatch(
		identityGateTestState(),
		normalizeIdentityGateSessionInfoFeed(t, withoutRoute, "2021-05-04T08:16:00Z"),
	)
	if err != nil {
		t.Fatalf("lose route: %v", err)
	}
	wantLostState := identityGateTestState()
	wantLostState.SessionInfo.routeKey = 0
	wantLostState.SessionInfo.routeAvailable = false
	wantLostState.SessionInfo.routeEpoch = 1
	assertLiveTimingReduction(t, lost, Reduction{
		State:                       wantLostState,
		SessionInfoDisposition:      SessionInfoDispositionRefreshed,
		sessionInfoAuthoritative:    true,
		sessionInfoRouteTransition:  true,
		sessionInfoIssues:           sessionInfoIssueRoute,
		sessionScopedUpdatesAllowed: true,
	})

	restored, err := ReduceBatch(
		lost.State,
		normalizeIdentityGateSessionInfoFeed(t, identityGateDescriptorACorrected, "2021-05-04T08:17:00Z"),
	)
	if err != nil {
		t.Fatalf("restore route: %v", err)
	}
	wantRestoredState := identityGateTestState()
	wantRestoredState.SessionInfo.routeKey = 102
	wantRestoredState.SessionInfo.routeEpoch = 2
	assertLiveTimingReduction(t, restored, Reduction{
		State:                       wantRestoredState,
		SessionInfoDisposition:      SessionInfoDispositionRefreshed,
		sessionInfoAuthoritative:    true,
		sessionInfoRouteTransition:  true,
		sessionScopedUpdatesAllowed: true,
	})
}

func TestReduceLiveTimingBatchClosesGateOnTokenExhaustion(t *testing.T) {
	generationExhausted := identityGateTestState()
	generationExhausted.SessionInfo.Generation = ^SessionInfoGeneration(0)
	routeExhausted := identityGateTestState()
	routeExhausted.SessionInfo.routeEpoch = ^sessionInfoRouteEpoch(0)
	tests := []struct {
		name    string
		state   State
		payload string
	}{
		{name: "generation", state: generationExhausted, payload: identityGateDescriptorB},
		{name: "routing epoch", state: routeExhausted, payload: identityGateDescriptorACorrected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReduceBatch(
				test.state,
				normalizeIdentityGateSessionInfoFeed(t, test.payload, "2022-01-01T12:00:00Z"),
			)
			if err != nil {
				t.Fatalf("reduceLiveTimingBatch() error = %v", err)
			}
			wantState := test.state
			wantState = suspendedTestState(wantState)
			wantState.SessionInfo.Synchronized = false
			assertLiveTimingReduction(t, got, Reduction{
				State:                    wantState,
				SessionInfoDisposition:   sessionInfoDispositionTokenExhausted,
				sessionInfoAuthoritative: true,
			})
		})
	}
}

func TestReduceLiveTimingBatchPropagatesInvariantErrors(t *testing.T) {
	state := identityGateTestState()
	tests := []struct {
		name    string
		batch   Batch
		wantErr error
	}{
		{name: "invalid batch", batch: Batch{}, wantErr: errInvalidNormalizedSessionInfoBatch},
		{
			name: "invalid selected payload",
			batch: Batch{
				Source: Feed,
				Updates: []Update{{
					Topic:   "SessionInfo",
					Payload: json.RawMessage(`{`),
					Source:  Feed,
				}},
			},
			wantErr: errInvalidNormalizedSessionInfo,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReduceBatch(state, test.batch)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("reduceLiveTimingBatch() error = %v, want %v", err, test.wantErr)
			}
			if !reflect.DeepEqual(got, Reduction{}) {
				t.Fatalf("failed reduction = %#v, want zero", got)
			}
		})
	}
}

func normalizeIdentityGateOtherFeed(t *testing.T, payload json.RawMessage) Batch {
	t.Helper()
	return normalizeIdentityGateBatch(t, WireBatch{
		Source: Feed,
		Updates: []WireUpdate{{
			Topic:     "Heartbeat",
			Payload:   payload,
			Timestamp: "2025-01-01T00:00:00Z",
			Source:    Feed,
		}},
	})
}

func normalizeIdentityGateSessionInfoFeed(t *testing.T, payload, timestamp string) Batch {
	t.Helper()
	return normalizeIdentityGateBatch(t, WireBatch{
		Source: Feed,
		Updates: []WireUpdate{{
			Topic:     "SessionInfo",
			Payload:   json.RawMessage(payload),
			Timestamp: timestamp,
			Source:    Feed,
		}},
	})
}

func normalizeIdentityGateBatch(t *testing.T, batch WireBatch) Batch {
	t.Helper()
	observationTime := time.Date(2026, time.September, 7, 0, 0, 0, 0, time.UTC)
	normalized, err := NormalizeBatch(batch, observationTime)
	if err != nil {
		t.Fatalf("normalizeLiveTimingBatch() error = %v", err)
	}
	return normalized
}

func identityGateTestState() State {
	return State{
		display: sessionDisplay{meeting: "Example Grand Prix"},
		Origin:  time.Date(2026, time.September, 7, 0, 0, 0, 0, time.UTC),
		SessionInfo: sessionInfoState{
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
				startUTC:  time.Date(2021, time.May, 4, 8, 15, 0, 0, time.UTC),
				endUTC:    time.Date(2021, time.May, 4, 9, 45, 0, 0, time.UTC),
				utcOffset: 2 * time.Hour,
			},
			scheduleAvailable: true,
			Generation:        1,
		},
	}
}

func assertLiveTimingReduction(t *testing.T, got, want Reduction) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reduction = %#v, want %#v", got, want)
	}
}

func suspendedTestState(s State) State {
	s.drivers.synchronized = false
	s.series.unsynchronized, s.Status.unsynchronized, s.track.unsynchronized = true, true, true
	s.laps.unsynchronized, s.clock.unsynchronized, s.weather.unsynchronized = true, true, true
	s.timing.cars.unsynchronized, s.radio.unsynchronized = true, true
	s.radio.rows = map[int64]*captureRecord{}
	s.timing.unsynchronized, s.timing.app.unsynchronized, s.timing.stats.unsynchronized = true, true, true
	s.timing.position.unsynchronized, s.timing.control.unsynchronized = true, true
	s.timing.pits.unsynchronized = [3]bool{true, true, true}
	return s
}

func TestSnapshotStagesDependenciesAndRollsBackOnInvariantFailure(t *testing.T) {
	const snap = Snapshot
	b := Batch{Source: snap, ObservationTime: fieldTime(100)}
	for _, u := range []struct{ topic, p string }{
		{"TimingData", `{"Lines":{"1":{"Position":"2","Stopped":false}}}`},
		{"TrackStatus", `{"Status":"1","Message":"AllClear"}`},
		{"SessionStatus", `{"Status":"Started"}`},
		{"SessionData", `{"StatusSeries":[{"Utc":"1970-01-01T00:00:01Z","TrackStatus":"AllClear","SessionStatus":"Started"}]}`},
		{"DriverList", `{"1":{"Tla":"ABC"}}`},
		{"SessionInfo", identityGateDescriptorB},
	} {
		b.RequestedTopics = append(b.RequestedTopics, u.topic)
		b.PresentTopics = append(b.PresentTopics, u.topic)
		b.Updates = append(b.Updates, Update{Source: snap, Topic: u.topic, Payload: []byte(u.p)})
	}
	check := func(s State) Reduction {
		t.Helper()
		r, err := ReduceBatch(s, b)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	initial := check(State{})
	if !initial.State.drivers.frozen || !initial.State.timing.late || initial.State.timing.running != 1 || initial.State.timing.drivers[1].root.kind != "" || len(initial.Effects.metrics) != 2 || len(initial.Effects.logs) != 0 {
		t.Fatal("snapshot baseline", initial.Effects)
	}
	slices.Reverse(b.PresentTopics)
	slices.Reverse(b.Updates)
	if !reflect.DeepEqual(check(State{}), initial) {
		t.Fatal("snapshot order changed projection")
	}
	before := check(State{}).State
	for i := range b.Updates {
		if b.Updates[i].Topic == "TimingData" {
			b.Updates[i].Payload = []byte(`{`)
		}
	}
	failed, err := ReduceBatch(before, b)
	if err == nil || !reflect.DeepEqual(failed, Reduction{}) || !reflect.DeepEqual(before, initial.State) {
		t.Fatal("invariant failure committed a partial batch")
	}
}
