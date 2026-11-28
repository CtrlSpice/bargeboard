package f1engine

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func sessionInfoUnicodeFeed(topic, payload string) Batch {
	return Batch{
		Source: Feed, ObservationTime: identityGateTestState().Origin,
		Updates: []Update{{
			Topic: topic, Payload: json.RawMessage(payload), Source: Feed,
		}},
	}
}

func sessionInfoUnicodeRecoveryState() State {
	state := identityGateTestState()
	state.SessionInfo.Generation = 37
	state.SessionInfo.routeEpoch = 8
	state.SessionInfo.retired.start = 255
	state.SessionInfo.retired.count = 2
	state.SessionInfo.retired.tuples[255] = sessionInfoLogicalTuple{2020, 1057, canonicalSessionNamePractice1}
	state.SessionInfo.retired.tuples[0] = sessionInfoLogicalTuple{2021, 21, canonicalSessionNamePractice2}
	return state
}

func TestSessionInfoUnicodeGateIndependentBundlesAndRecovery(t *testing.T) {
	state := sessionInfoUnicodeRecoveryState()
	for _, test := range []struct {
		name, old, replacement string
		issues                 sessionInfoIssueSet
	}{
		{"identity", `Example Grand Prix`, `\ud800 Grand Prix`, sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"start", `2021-05-04T10:15:00`, `\ud800`, sessionInfoIssueUnicode | sessionInfoIssueIdentity | sessionInfoIssueSchedule},
		{"end", `2021-05-04T11:45:00`, `\ud800`, sessionInfoIssueUnicode | sessionInfoIssueSchedule},
		{"offset", `02:00:00`, `\ud800`, sessionInfoIssueUnicode | sessionInfoIssueSchedule},
		{"route", `"Key":101`, `"Key":"\ud800"`, sessionInfoIssueUnicode | sessionInfoIssueRoute},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := strings.Replace(sessionInfoBatchDescriptorA, test.old, test.replacement, 1)
			if test.issues&sessionInfoIssueIdentity != 0 {
				raw = strings.Replace(raw, `"Key":101`, `"Key":102`, 1)
				raw = strings.Replace(raw, "2021-05-04T11:45:00", "2021-05-04T12:45:00", 1)
			}
			wantState := state
			want := Reduction{
				sessionInfoAuthoritative:    true,
				SessionInfoDisposition:      SessionInfoDispositionRefreshed,
				sessionInfoIssues:           test.issues,
				sessionScopedUpdatesAllowed: true,
			}
			if test.issues&sessionInfoIssueIdentity != 0 {
				wantState = suspendedTestState(wantState)
				wantState.SessionInfo.Synchronized = false
				want.SessionInfoDisposition = sessionInfoDispositionUnresolved
				want.sessionScopedUpdatesAllowed = false
			} else {
				if test.issues&sessionInfoIssueSchedule != 0 {
					wantState.SessionInfo.schedule = sessionInfoSchedule{}
					wantState.SessionInfo.scheduleAvailable = false
				}
				if test.issues&sessionInfoIssueRoute != 0 {
					wantState.SessionInfo.routeKey = 0
					wantState.SessionInfo.routeAvailable = false
					wantState.SessionInfo.routeEpoch++
					want.sessionInfoRouteTransition = true
				}
			}
			want.State = wantState
			batch := sessionInfoUnicodeFeed("SessionInfo", raw)
			got, err := ReduceBatch(state, batch)
			if err != nil {
				t.Fatal(err)
			}
			assertLiveTimingReduction(t, got, want)
			if !reflect.DeepEqual(state, sessionInfoUnicodeRecoveryState()) {
				t.Fatal("mutated input state")
			}
			if !reflect.DeepEqual(batch, sessionInfoUnicodeFeed("SessionInfo", raw)) {
				t.Fatal("mutated batch")
			}

			// Repeating a bad descriptor retains its occurrence issues, but cannot
			// advance counters again or apply independently valid stale metadata.
			repeated, err := ReduceBatch(got.State, batch)
			if err != nil {
				t.Fatal(err)
			}
			want.sessionInfoRouteTransition = false
			assertLiveTimingReduction(t, repeated, want)
			unrelated, err := ReduceBatch(repeated.State, sessionInfoUnicodeFeed("Heartbeat", `{"marker":"unbound"}`))
			if err != nil {
				t.Fatal(err)
			}
			assertLiveTimingReduction(t, unrelated, Reduction{
				State: wantState, sessionScopedUpdatesAllowed: want.sessionScopedUpdatesAllowed,
			})

			recovered, err := ReduceBatch(unrelated.State, sessionInfoUnicodeFeed("SessionInfo", sessionInfoBatchDescriptorA))
			if err != nil {
				t.Fatal(err)
			}
			wantRecovered := Reduction{
				State: state, SessionInfoDisposition: SessionInfoDispositionRefreshed,
				sessionInfoAuthoritative: true, sessionScopedUpdatesAllowed: true,
			}
			if test.issues&sessionInfoIssueIdentity != 0 {
				wantRecovered.State = suspendedTestState(state)
			}
			if test.issues == sessionInfoIssueUnicode|sessionInfoIssueRoute {
				wantRecovered.State.SessionInfo.routeEpoch += 2
				wantRecovered.sessionInfoRouteTransition = true
			}
			assertLiveTimingReduction(t, recovered, wantRecovered)
		})
	}
}

func TestSessionInfoUnicodeSnapshotAtomicGateAndOwnership(t *testing.T) {
	state := sessionInfoUnicodeRecoveryState()
	bad := strings.Replace(sessionInfoBatchDescriptorA, "Example", `\ud800`, 1)
	for _, order := range [][]string{{"SessionInfo", "Heartbeat"}, {"Heartbeat", "SessionInfo"}} {
		makeBatch := func(payload string) Batch {
			batch := Batch{
				Source:          Snapshot,
				RequestedTopics: []string{"Heartbeat", "SessionInfo", "DriverList"},
				PresentTopics:   append([]string(nil), order...),
			}
			for _, topic := range order {
				raw := `{"opaque":"\ud800"}`
				if topic == "SessionInfo" {
					raw = payload
				}
				batch.Updates = append(batch.Updates, Update{Topic: topic, Payload: json.RawMessage(raw), Source: Snapshot})
			}
			return batch
		}
		batch := makeBatch(bad)
		want := Reduction{
			State: state, sessionInfoAuthoritative: true,
			SessionInfoDisposition: sessionInfoDispositionUnresolved,
			sessionInfoIssues:      sessionInfoIssueUnicode | sessionInfoIssueIdentity,
		}
		want.State = suspendedTestState(want.State)
		want.State.SessionInfo.Synchronized = false
		got, err := ReduceBatch(state, batch)
		if err != nil {
			t.Fatal(err)
		}
		assertLiveTimingReduction(t, got, want)
		if !reflect.DeepEqual(batch, makeBatch(bad)) {
			t.Fatal("snapshot bytes or manifests changed")
		}
		// The normalized adapter must expose the same complete outcome directly.
		descriptor, authoritative, err := reduceSessionInfoBatch(state.SessionInfo, batch)
		if err != nil || !authoritative {
			t.Fatalf("adapter authority/error = %t/%v", authoritative, err)
		}
		if descriptor != (sessionInfoReduction{state: want.State.SessionInfo, disposition: sessionInfoDispositionUnresolved, issues: want.sessionInfoIssues}) {
			t.Fatal("adapter lost issues or recovery state")
		}
		restored, err := ReduceBatch(got.State, makeBatch(sessionInfoBatchDescriptorA))
		if err != nil {
			t.Fatal(err)
		}
		assertLiveTimingReduction(t, restored, Reduction{
			State: suspendedTestState(state), sessionInfoAuthoritative: true,
			SessionInfoDisposition:      SessionInfoDispositionRefreshed,
			sessionScopedUpdatesAllowed: true,
		})
		// Corrupt normalized invariants remain all-or-nothing, even with Unicode
		// findings in an otherwise usable earlier descriptor.
		invalid := makeBatch(bad)
		invalid.Updates[1].Source = Feed
		failed, err := ReduceBatch(state, invalid)
		if !errors.Is(err, errInvalidNormalizedSessionInfoBatch) || !reflect.DeepEqual(failed, Reduction{}) {
			t.Fatal("invalid manifest exposed a partial reduction")
		}
	}
}

func TestSessionInfoUnicodeFeedOrderIssueUnionIsNotLastState(t *testing.T) {
	state := sessionInfoUnicodeRecoveryState()
	allIssues := sessionInfoIssueShape | sessionInfoIssueIdentity | sessionInfoIssueClassification |
		sessionInfoIssueRoute | sessionInfoIssueSchedule | sessionInfoIssueKeyframe | sessionInfoIssueUnicode
	bad := `{"Key":0,"Meeting":{"Key":21,"Name":"\ud800 Grand Prix"},"_kf":false}`
	unknownClass := strings.Replace(sessionInfoBatchDescriptorA, `"Practice 1"`, `"Practice 4"`, 1)
	for _, reverse := range []bool{false, true} {
		payloads := []string{bad, "null", unknownClass}
		if reverse {
			payloads[0], payloads[2] = payloads[2], payloads[0]
		}
		current := state
		var occurrences sessionInfoIssueSet
		for _, raw := range payloads {
			got, err := ReduceBatch(current, sessionInfoUnicodeFeed("SessionInfo", raw))
			if err != nil {
				t.Fatal(err)
			}
			occurrences |= got.sessionInfoIssues
			current = got.State
			wantState := state
			wantState = suspendedTestState(wantState)
			wantState.SessionInfo.Synchronized = false
			wantIssues := sessionInfoIssueClassification
			switch raw {
			case bad:
				wantIssues = sessionInfoIssueUnicode | sessionInfoIssueIdentity | sessionInfoIssueRoute | sessionInfoIssueSchedule | sessionInfoIssueKeyframe
			case "null":
				wantIssues = sessionInfoIssueShape
			}
			assertLiveTimingReduction(t, got, Reduction{
				State: wantState, sessionInfoAuthoritative: true,
				SessionInfoDisposition: sessionInfoDispositionUnresolved, sessionInfoIssues: wantIssues,
			})
			blocked, err := ReduceBatch(current, sessionInfoUnicodeFeed("Heartbeat", `{"marker":"never replay"}`))
			if err != nil {
				t.Fatal(err)
			}
			assertLiveTimingReduction(t, blocked, Reduction{State: wantState})
		}
		lastSessionInfo, err := ReduceBatch(current, sessionInfoUnicodeFeed("SessionInfo", sessionInfoBatchDescriptorA))
		if err != nil {
			t.Fatal(err)
		}
		occurrences |= lastSessionInfo.sessionInfoIssues
		assertLiveTimingReduction(t, lastSessionInfo, Reduction{
			State: suspendedTestState(state), sessionInfoAuthoritative: true,
			SessionInfoDisposition: SessionInfoDispositionRefreshed, sessionScopedUpdatesAllowed: true,
		})
		if occurrences != allIssues {
			t.Fatalf("occurrence union = %x, want %x", occurrences, allIssues)
		}
	}
}

func TestSessionInfoUnicodeRetiredAndExhaustedOutcomesKeepIssues(t *testing.T) {
	state := sessionInfoUnicodeRecoveryState()
	stale := strings.Replace(sessionInfoBatchDescriptorA, `"Practice 1"`, `"Practice 2"`, 1)
	stale = strings.Replace(stale, "02:00:00", `\ud800`, 1)
	for _, test := range []struct {
		name        string
		state       State
		raw         string
		disposition sessionInfoDisposition
	}{
		{"retired", state, stale, sessionInfoDispositionStale},
		{"generation exhausted", func() State { s := state; s.SessionInfo.Generation = ^SessionInfoGeneration(0); return s }(), strings.Replace(sessionInfoBatchDescriptorB, "00:00:00", `\ud800`, 1), sessionInfoDispositionTokenExhausted},
		{"route exhausted", func() State { s := state; s.SessionInfo.routeEpoch = ^sessionInfoRouteEpoch(0); return s }(), strings.Replace(identityGateDescriptorACorrected, "02:00:00", `\ud800`, 1), sessionInfoDispositionTokenExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReduceBatch(test.state, sessionInfoUnicodeFeed("SessionInfo", test.raw))
			if err != nil {
				t.Fatal(err)
			}
			want := Reduction{
				State: test.state, sessionInfoAuthoritative: true, SessionInfoDisposition: test.disposition,
				sessionInfoIssues: sessionInfoIssueUnicode | sessionInfoIssueSchedule,
			}
			want.State = suspendedTestState(want.State)
			want.State.SessionInfo.Synchronized = false
			assertLiveTimingReduction(t, got, want)
		})
	}
}

func TestSessionInfoUnicodeInstallAndReplaceKeepIssues(t *testing.T) {
	raw := sessionInfoBatchDescriptorA[:len(sessionInfoBatchDescriptorA)-1] + `,"\ud800":null}`
	for _, replace := range []bool{false, true} {
		var state State
		wantState := identityGateTestState()
		disposition := SessionInfoDispositionInstalled
		if replace {
			state = sessionInfoUnicodeRecoveryState()
			state.SessionInfo.Identity.meetingKey = 1200
			wantState.SessionInfo.Generation = state.SessionInfo.Generation + 1
			wantState.SessionInfo.retired = state.SessionInfo.retired
			wantState.SessionInfo.retired.tuples[1] = sessionInfoLogicalTuple{2021, 1200, canonicalSessionNamePractice1}
			wantState.SessionInfo.retired.count++
			disposition = SessionInfoDispositionReplaced
		}
		got, err := ReduceBatch(state, sessionInfoUnicodeFeed("SessionInfo", raw))
		if err != nil {
			t.Fatal(err)
		}
		assertLiveTimingReduction(t, got, Reduction{
			State: wantState, SessionInfoDisposition: disposition,
			sessionInfoAuthoritative: true, sessionScopedUpdatesAllowed: true,
			sessionInfoIssues: sessionInfoIssueUnicode,
		})
	}
}
