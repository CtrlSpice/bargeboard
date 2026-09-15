package f1livetimingreceiver

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func sessionInfoUnicodeFeed(topic, payload string) normalizedLiveTimingBatch {
	return normalizedLiveTimingBatch{
		source: liveTimingUpdateSourceFeed,
		updates: []normalizedLiveTimingUpdate{{
			topic: topic, payload: json.RawMessage(payload), source: liveTimingUpdateSourceFeed,
		}},
	}
}

func sessionInfoUnicodeRecoveryState() liveTimingState {
	state := identityGateTestState()
	state.sessionInfo.generation = 37
	state.sessionInfo.routeEpoch = 8
	state.sessionInfo.retired.start = 255
	state.sessionInfo.retired.count = 2
	state.sessionInfo.retired.tuples[255] = sessionInfoLogicalTuple{2020, 1057, canonicalSessionNamePractice1}
	state.sessionInfo.retired.tuples[0] = sessionInfoLogicalTuple{2021, 1107, canonicalSessionNamePractice2}
	return state
}

func TestSessionInfoUnicodeGateIndependentBundlesAndRecovery(t *testing.T) {
	state := sessionInfoUnicodeRecoveryState()
	for _, test := range []struct {
		name, old, replacement string
		issues                 sessionInfoIssueSet
	}{
		{"identity", `Abu Dhabi Grand Prix`, `\ud800 Grand Prix`, sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"start", `2021-12-10T13:30:00`, `\ud800`, sessionInfoIssueUnicode | sessionInfoIssueIdentity | sessionInfoIssueSchedule},
		{"end", `2021-12-10T14:30:00`, `\ud800`, sessionInfoIssueUnicode | sessionInfoIssueSchedule},
		{"offset", `04:00:00`, `\ud800`, sessionInfoIssueUnicode | sessionInfoIssueSchedule},
		{"route", `"Key":6594`, `"Key":"\ud800"`, sessionInfoIssueUnicode | sessionInfoIssueRoute},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := strings.Replace(sessionInfoBatchDescriptorA, test.old, test.replacement, 1)
			if test.issues&sessionInfoIssueIdentity != 0 {
				raw = strings.Replace(raw, `"Key":6594`, `"Key":7165`, 1)
				raw = strings.Replace(raw, "2021-12-10T14:30:00", "2021-12-10T15:30:00", 1)
			}
			wantState := state
			want := liveTimingReduction{
				sessionInfoAuthoritative:    true,
				sessionInfoDisposition:      sessionInfoDispositionRefreshed,
				sessionInfoIssues:           test.issues,
				sessionScopedUpdatesAllowed: true,
			}
			if test.issues&sessionInfoIssueIdentity != 0 {
				wantState.sessionInfo.synchronized = false
				want.sessionInfoDisposition = sessionInfoDispositionUnresolved
				want.sessionScopedUpdatesAllowed = false
			} else {
				if test.issues&sessionInfoIssueSchedule != 0 {
					wantState.sessionInfo.schedule = sessionInfoSchedule{}
					wantState.sessionInfo.scheduleAvailable = false
				}
				if test.issues&sessionInfoIssueRoute != 0 {
					wantState.sessionInfo.routeKey = 0
					wantState.sessionInfo.routeAvailable = false
					wantState.sessionInfo.routeEpoch++
					want.sessionInfoRouteTransition = true
				}
			}
			want.state = wantState
			batch := sessionInfoUnicodeFeed("SessionInfo", raw)
			got, err := reduceLiveTimingBatch(state, batch)
			if err != nil {
				t.Fatal(err)
			}
			assertLiveTimingReduction(t, got, want)
			if state != sessionInfoUnicodeRecoveryState() {
				t.Fatal("mutated input state")
			}
			if !reflect.DeepEqual(batch, sessionInfoUnicodeFeed("SessionInfo", raw)) {
				t.Fatal("mutated batch")
			}

			// Repeating a bad descriptor retains its occurrence issues, but cannot
			// advance counters again or apply independently valid stale metadata.
			repeated, err := reduceLiveTimingBatch(got.state, batch)
			if err != nil {
				t.Fatal(err)
			}
			want.sessionInfoRouteTransition = false
			assertLiveTimingReduction(t, repeated, want)
			unrelated, err := reduceLiveTimingBatch(repeated.state, sessionInfoUnicodeFeed("TimingData", `{"marker":"unbound"}`))
			if err != nil {
				t.Fatal(err)
			}
			assertLiveTimingReduction(t, unrelated, liveTimingReduction{
				state: wantState, sessionScopedUpdatesAllowed: want.sessionScopedUpdatesAllowed,
			})

			recovered, err := reduceLiveTimingBatch(unrelated.state, sessionInfoUnicodeFeed("SessionInfo", sessionInfoBatchDescriptorA))
			if err != nil {
				t.Fatal(err)
			}
			wantRecovered := liveTimingReduction{
				state: state, sessionInfoDisposition: sessionInfoDispositionRefreshed,
				sessionInfoAuthoritative: true, sessionScopedUpdatesAllowed: true,
			}
			if test.issues == sessionInfoIssueUnicode|sessionInfoIssueRoute {
				wantRecovered.state.sessionInfo.routeEpoch += 2
				wantRecovered.sessionInfoRouteTransition = true
			}
			assertLiveTimingReduction(t, recovered, wantRecovered)
		})
	}
}

func TestSessionInfoUnicodeSnapshotAtomicGateAndOwnership(t *testing.T) {
	state := sessionInfoUnicodeRecoveryState()
	bad := strings.Replace(sessionInfoBatchDescriptorA, "Abu Dhabi", `\ud800`, 1)
	for _, order := range [][]string{{"SessionInfo", "TimingData"}, {"TimingData", "SessionInfo"}} {
		makeBatch := func(payload string) normalizedLiveTimingBatch {
			batch := normalizedLiveTimingBatch{
				source:          liveTimingUpdateSourceSnapshot,
				requestedTopics: []string{"TimingData", "SessionInfo", "DriverList"},
				presentTopics:   append([]string(nil), order...),
			}
			for _, topic := range order {
				raw := `{"opaque":"\ud800"}`
				if topic == "SessionInfo" {
					raw = payload
				}
				batch.updates = append(batch.updates, normalizedLiveTimingUpdate{topic: topic, payload: json.RawMessage(raw), source: liveTimingUpdateSourceSnapshot})
			}
			return batch
		}
		batch := makeBatch(bad)
		want := liveTimingReduction{
			state: state, sessionInfoAuthoritative: true,
			sessionInfoDisposition: sessionInfoDispositionUnresolved,
			sessionInfoIssues:      sessionInfoIssueUnicode | sessionInfoIssueIdentity,
		}
		want.state.sessionInfo.synchronized = false
		got, err := reduceLiveTimingBatch(state, batch)
		if err != nil {
			t.Fatal(err)
		}
		assertLiveTimingReduction(t, got, want)
		if !reflect.DeepEqual(batch, makeBatch(bad)) {
			t.Fatal("snapshot bytes or manifests changed")
		}
		// The normalized adapter must expose the same complete outcome directly.
		descriptor, authoritative, err := reduceSessionInfoBatch(state.sessionInfo, batch)
		if err != nil || !authoritative {
			t.Fatalf("adapter authority/error = %t/%v", authoritative, err)
		}
		if descriptor != (sessionInfoReduction{state: want.state.sessionInfo, disposition: sessionInfoDispositionUnresolved, issues: want.sessionInfoIssues}) {
			t.Fatal("adapter lost issues or recovery state")
		}
		restored, err := reduceLiveTimingBatch(got.state, makeBatch(sessionInfoBatchDescriptorA))
		if err != nil {
			t.Fatal(err)
		}
		assertLiveTimingReduction(t, restored, liveTimingReduction{
			state: state, sessionInfoAuthoritative: true,
			sessionInfoDisposition:      sessionInfoDispositionRefreshed,
			sessionScopedUpdatesAllowed: true,
		})
		// Corrupt normalized invariants remain all-or-nothing, even with Unicode
		// findings in an otherwise usable earlier descriptor.
		invalid := makeBatch(bad)
		invalid.updates[1].source = liveTimingUpdateSourceFeed
		failed, err := reduceLiveTimingBatch(state, invalid)
		if !errors.Is(err, errInvalidNormalizedSessionInfoBatch) || failed != (liveTimingReduction{}) {
			t.Fatal("invalid manifest exposed a partial reduction")
		}
	}
}

func TestSessionInfoUnicodeFeedOrderIssueUnionIsNotLastState(t *testing.T) {
	state := sessionInfoUnicodeRecoveryState()
	allIssues := sessionInfoIssueShape | sessionInfoIssueIdentity | sessionInfoIssueClassification |
		sessionInfoIssueRoute | sessionInfoIssueSchedule | sessionInfoIssueKeyframe | sessionInfoIssueUnicode
	bad := `{"Key":0,"Meeting":{"Key":1107,"Name":"\ud800 Grand Prix"},"_kf":false}`
	unknownClass := strings.Replace(sessionInfoBatchDescriptorA, `"Practice 1"`, `"Practice 4"`, 1)
	for _, reverse := range []bool{false, true} {
		payloads := []string{bad, "null", unknownClass}
		if reverse {
			payloads[0], payloads[2] = payloads[2], payloads[0]
		}
		current := state
		var occurrences sessionInfoIssueSet
		for _, raw := range payloads {
			got, err := reduceLiveTimingBatch(current, sessionInfoUnicodeFeed("SessionInfo", raw))
			if err != nil {
				t.Fatal(err)
			}
			occurrences |= got.sessionInfoIssues
			current = got.state
			wantState := state
			wantState.sessionInfo.synchronized = false
			wantIssues := sessionInfoIssueClassification
			switch raw {
			case bad:
				wantIssues = sessionInfoIssueUnicode | sessionInfoIssueIdentity | sessionInfoIssueRoute | sessionInfoIssueSchedule | sessionInfoIssueKeyframe
			case "null":
				wantIssues = sessionInfoIssueShape
			}
			assertLiveTimingReduction(t, got, liveTimingReduction{
				state: wantState, sessionInfoAuthoritative: true,
				sessionInfoDisposition: sessionInfoDispositionUnresolved, sessionInfoIssues: wantIssues,
			})
			blocked, err := reduceLiveTimingBatch(current, sessionInfoUnicodeFeed("TimingData", `{"marker":"never replay"}`))
			if err != nil {
				t.Fatal(err)
			}
			assertLiveTimingReduction(t, blocked, liveTimingReduction{state: wantState})
		}
		lastSessionInfo, err := reduceLiveTimingBatch(current, sessionInfoUnicodeFeed("SessionInfo", sessionInfoBatchDescriptorA))
		if err != nil {
			t.Fatal(err)
		}
		occurrences |= lastSessionInfo.sessionInfoIssues
		assertLiveTimingReduction(t, lastSessionInfo, liveTimingReduction{
			state: state, sessionInfoAuthoritative: true,
			sessionInfoDisposition: sessionInfoDispositionRefreshed, sessionScopedUpdatesAllowed: true,
		})
		if occurrences != allIssues {
			t.Fatalf("occurrence union = %x, want %x", occurrences, allIssues)
		}
	}
}

func TestSessionInfoUnicodeRetiredAndExhaustedOutcomesKeepIssues(t *testing.T) {
	state := sessionInfoUnicodeRecoveryState()
	stale := strings.Replace(sessionInfoBatchDescriptorA, `"Practice 1"`, `"Practice 2"`, 1)
	stale = strings.Replace(stale, "04:00:00", `\ud800`, 1)
	for _, test := range []struct {
		name        string
		state       liveTimingState
		raw         string
		disposition sessionInfoDisposition
	}{
		{"retired", state, stale, sessionInfoDispositionStale},
		{"generation exhausted", func() liveTimingState { s := state; s.sessionInfo.generation = ^sessionInfoGeneration(0); return s }(), strings.Replace(sessionInfoBatchDescriptorB, "00:00:00", `\ud800`, 1), sessionInfoDispositionTokenExhausted},
		{"route exhausted", func() liveTimingState { s := state; s.sessionInfo.routeEpoch = ^sessionInfoRouteEpoch(0); return s }(), strings.Replace(identityGateDescriptorACorrected, "04:00:00", `\ud800`, 1), sessionInfoDispositionTokenExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := reduceLiveTimingBatch(test.state, sessionInfoUnicodeFeed("SessionInfo", test.raw))
			if err != nil {
				t.Fatal(err)
			}
			want := liveTimingReduction{
				state: test.state, sessionInfoAuthoritative: true, sessionInfoDisposition: test.disposition,
				sessionInfoIssues: sessionInfoIssueUnicode | sessionInfoIssueSchedule,
			}
			want.state.sessionInfo.synchronized = false
			assertLiveTimingReduction(t, got, want)
		})
	}
}

func TestSessionInfoUnicodeInstallAndReplaceKeepIssues(t *testing.T) {
	raw := sessionInfoBatchDescriptorA[:len(sessionInfoBatchDescriptorA)-1] + `,"\ud800":null}`
	for _, replace := range []bool{false, true} {
		var state liveTimingState
		wantState := identityGateTestState()
		disposition := sessionInfoDispositionInstalled
		if replace {
			state = sessionInfoUnicodeRecoveryState()
			state.sessionInfo.identity.meetingKey = 1200
			wantState.sessionInfo.generation = state.sessionInfo.generation + 1
			wantState.sessionInfo.retired = state.sessionInfo.retired
			wantState.sessionInfo.retired.tuples[1] = sessionInfoLogicalTuple{2021, 1200, canonicalSessionNamePractice1}
			wantState.sessionInfo.retired.count++
			disposition = sessionInfoDispositionReplaced
		}
		got, err := reduceLiveTimingBatch(state, sessionInfoUnicodeFeed("SessionInfo", raw))
		if err != nil {
			t.Fatal(err)
		}
		assertLiveTimingReduction(t, got, liveTimingReduction{
			state: wantState, sessionInfoDisposition: disposition,
			sessionInfoAuthoritative: true, sessionScopedUpdatesAllowed: true,
			sessionInfoIssues: sessionInfoIssueUnicode,
		})
	}
}
