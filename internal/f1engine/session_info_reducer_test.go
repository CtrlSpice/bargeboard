package f1engine

import (
	"slices"
	"testing"
	"time"
)

func TestReduceSessionInfoInstallsInitialDescriptor(t *testing.T) {
	identity := reducerTestIdentity(2025, 31, canonicalSessionTypeRace, canonicalSessionNameRace)
	schedule := reducerTestSchedule(1)
	descriptor := reducerTestDescriptor(identity, 401, schedule)

	got := reduceSessionInfo(sessionInfoState{}, descriptor)
	want := sessionInfoReduction{
		state: sessionInfoState{
			Identity:          identity,
			IdentityAvailable: true,
			Synchronized:      true,
			routeKey:          401,
			routeAvailable:    true,
			schedule:          schedule,
			scheduleAvailable: true,
			Generation:        1,
		},
		disposition: SessionInfoDispositionInstalled,
	}
	assertSessionInfoReduction(t, got, want)
}

func TestReduceSessionInfoInstallsWithoutOptionalBundles(t *testing.T) {
	identity := reducerTestIdentity(2025, 31, canonicalSessionTypeRace, canonicalSessionNameRace)
	descriptor := reducerTestDescriptor(identity, 401, reducerTestSchedule(1))
	descriptor.routeKey = 99
	descriptor.routeAvailable = false
	descriptor.schedule = reducerTestSchedule(2)
	descriptor.scheduleAvailable = false
	descriptor.issues = sessionInfoIssueRoute | sessionInfoIssueSchedule

	got := reduceSessionInfo(sessionInfoState{}, descriptor)
	want := sessionInfoReduction{
		state: sessionInfoState{
			Identity:          identity,
			IdentityAvailable: true,
			Synchronized:      true,
			Generation:        1,
		},
		disposition: SessionInfoDispositionInstalled,
		issues:      sessionInfoIssueRoute | sessionInfoIssueSchedule,
	}
	assertSessionInfoReduction(t, got, want)
}

func TestReduceSessionInfoUnresolvedIdentityRetainsRecoveryState(t *testing.T) {
	identityA := reducerTestIdentity(2025, 100, canonicalSessionTypeRace, canonicalSessionNameRace)
	identityB := reducerTestIdentity(2025, 101, canonicalSessionTypeRace, canonicalSessionNameRace)
	state := reduceSessionInfo(
		sessionInfoState{},
		reducerTestDescriptor(identityA, 10, reducerTestSchedule(1)),
	).state
	state = reduceSessionInfo(
		state,
		reducerTestDescriptor(identityB, 20, reducerTestSchedule(2)),
	).state
	state = reduceSessionInfo(
		state,
		reducerTestDescriptor(identityB, 21, reducerTestSchedule(3)),
	).state

	for _, issues := range []sessionInfoIssueSet{
		sessionInfoIssueShape,
		sessionInfoIssueIdentity | sessionInfoIssueRoute | sessionInfoIssueSchedule,
		sessionInfoIssueClassification,
	} {
		t.Run(reducerTestIssueName(issues), func(t *testing.T) {
			descriptor := sessionInfoParseResult{
				routeKey:          99,
				routeAvailable:    true,
				schedule:          reducerTestSchedule(4),
				scheduleAvailable: true,
				issues:            issues,
			}
			got := reduceSessionInfo(state, descriptor)
			wantState := state
			wantState.Synchronized = false
			assertSessionInfoReduction(t, got, sessionInfoReduction{
				state:       wantState,
				disposition: sessionInfoDispositionUnresolved,
				issues:      issues,
			})
			if !state.Synchronized {
				t.Fatal("reduceSessionInfo mutated its input state")
			}

			fresh := reduceSessionInfo(sessionInfoState{}, descriptor)
			assertSessionInfoReduction(t, fresh, sessionInfoReduction{
				disposition: sessionInfoDispositionUnresolved,
				issues:      issues,
			})
		})
	}
}

func TestReduceSessionInfoReplacesCompleteOptionalBundles(t *testing.T) {
	identity := reducerTestIdentity(2021, 21, canonicalSessionTypePractice, canonicalSessionNamePractice1)
	descriptor := reducerTestDescriptor(identity, 101, reducerTestSchedule(1))
	state := reduceSessionInfo(sessionInfoState{}, descriptor).state

	refresh := reducerTestDescriptor(identity, 101, reducerTestSchedule(2))
	got := reduceSessionInfo(state, refresh)
	wantState := state
	wantState.schedule = refresh.schedule
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       wantState,
		disposition: SessionInfoDispositionRefreshed,
	})
	state = got.state

	clear := reducerTestDescriptor(identity, 9999, reducerTestSchedule(3))
	clear.routeAvailable = false
	clear.scheduleAvailable = false
	got = reduceSessionInfo(state, clear)
	wantState = state
	wantState.routeKey = 0
	wantState.routeAvailable = false
	wantState.schedule = sessionInfoSchedule{}
	wantState.scheduleAvailable = false
	wantState.routeEpoch = 1
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:           wantState,
		disposition:     SessionInfoDispositionRefreshed,
		routeTransition: true,
	})
	state = got.state

	repeatedClear := clear
	repeatedClear.routeKey = 102
	repeatedClear.schedule = reducerTestSchedule(4)
	got = reduceSessionInfo(state, repeatedClear)
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       state,
		disposition: SessionInfoDispositionRefreshed,
	})
	state = got.state

	restore := reducerTestDescriptor(identity, 102, reducerTestSchedule(1))
	got = reduceSessionInfo(state, restore)
	wantState = state
	wantState.routeKey = 102
	wantState.routeAvailable = true
	wantState.schedule = restore.schedule
	wantState.scheduleAvailable = true
	wantState.routeEpoch = 2
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:           wantState,
		disposition:     SessionInfoDispositionRefreshed,
		routeTransition: true,
	})
	state = got.state

	repeat := restore
	repeat.schedule = reducerTestSchedule(2)
	repeat.issues = sessionInfoIssueKeyframe
	got = reduceSessionInfo(state, repeat)
	wantState = state
	wantState.schedule = repeat.schedule
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       wantState,
		disposition: SessionInfoDispositionRefreshed,
		issues:      sessionInfoIssueKeyframe,
	})
	state = got.state

	revert := reducerTestDescriptor(identity, 101, reducerTestSchedule(3))
	got = reduceSessionInfo(state, revert)
	wantState = state
	wantState.routeKey = 101
	wantState.schedule = revert.schedule
	wantState.routeEpoch = 3
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:           wantState,
		disposition:     SessionInfoDispositionRefreshed,
		routeTransition: true,
	})
}

func TestReduceSessionInfoScheduleChangesDoNotAdvanceRouteEpoch(t *testing.T) {
	identity := reducerTestIdentity(2025, 100, canonicalSessionTypeRace, canonicalSessionNameRace)
	descriptor := reducerTestDescriptor(identity, 10, reducerTestSchedule(1))
	state := reduceSessionInfo(sessionInfoState{}, descriptor).state

	cleared := descriptor
	cleared.schedule = reducerTestSchedule(3)
	cleared.scheduleAvailable = false
	got := reduceSessionInfo(state, cleared)
	wantState := state
	wantState.schedule = sessionInfoSchedule{}
	wantState.scheduleAvailable = false
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       wantState,
		disposition: SessionInfoDispositionRefreshed,
	})

	restored := descriptor
	restored.schedule = reducerTestSchedule(2)
	got = reduceSessionInfo(got.state, restored)
	wantState.schedule = restored.schedule
	wantState.scheduleAvailable = true
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       wantState,
		disposition: SessionInfoDispositionRefreshed,
	})
}

func TestReduceSessionInfoRestoresSynchronization(t *testing.T) {
	identity := reducerTestIdentity(2021, 21, canonicalSessionTypePractice, canonicalSessionNamePractice1)
	descriptor := reducerTestDescriptor(identity, 101, reducerTestSchedule(1))
	state := reduceSessionInfo(sessionInfoState{}, descriptor).state

	state = reduceSessionInfo(state, sessionInfoParseResult{issues: sessionInfoIssueIdentity}).state
	if state.Synchronized {
		t.Fatal("unresolved identity remained synchronized")
	}

	got := reduceSessionInfo(state, descriptor)
	wantState := state
	wantState.Synchronized = true
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       wantState,
		disposition: SessionInfoDispositionRefreshed,
	})
	state = got.state

	state = reduceSessionInfo(state, sessionInfoParseResult{issues: sessionInfoIssueIdentity}).state
	corrected := reducerTestDescriptor(identity, 102, reducerTestSchedule(2))
	got = reduceSessionInfo(state, corrected)
	wantState = state
	wantState.Synchronized = true
	wantState.routeKey = 102
	wantState.schedule = corrected.schedule
	wantState.routeEpoch = 1
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:           wantState,
		disposition:     SessionInfoDispositionRefreshed,
		routeTransition: true,
	})
}

func TestReduceSessionInfoLogicalTupleSelectsGeneration(t *testing.T) {
	identity := reducerTestIdentity(2025, 100, canonicalSessionTypeRace, canonicalSessionNameRace)
	tests := []struct {
		name     string
		incoming SessionInfoIdentity
		routeKey int64
		route    bool
	}{
		{
			name:     "season",
			incoming: reducerTestIdentity(2026, 100, canonicalSessionTypeRace, canonicalSessionNameRace),
			routeKey: 11,
			route:    true,
		},
		{
			name:     "meeting key with reused route",
			incoming: reducerTestIdentity(2025, 101, canonicalSessionTypeRace, canonicalSessionNameRace),
			routeKey: 10,
			route:    true,
		},
		{
			name:     "canonical session name without route",
			incoming: reducerTestIdentity(2025, 100, canonicalSessionTypePractice, canonicalSessionNamePractice1),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := reduceSessionInfo(
				sessionInfoState{},
				reducerTestDescriptor(identity, 10, reducerTestSchedule(1)),
			).state
			corrected := reducerTestDescriptor(identity, 12, reducerTestSchedule(2))
			state = reduceSessionInfo(state, corrected).state
			if state.routeEpoch != 1 {
				t.Fatalf("route epoch before replacement = %d, want 1", state.routeEpoch)
			}

			incoming := reducerTestDescriptor(test.incoming, test.routeKey, reducerTestSchedule(3))
			incoming.routeAvailable = test.route
			got := reduceSessionInfo(state, incoming)
			wantState := sessionInfoState{
				Identity:          test.incoming,
				IdentityAvailable: true,
				Synchronized:      true,
				routeKey:          test.routeKey,
				routeAvailable:    test.route,
				schedule:          incoming.schedule,
				scheduleAvailable: true,
				Generation:        2,
			}
			wantState.retired.tuples[0] = identity.logicalTuple()
			wantState.retired.count = 1
			assertSessionInfoReduction(t, got, sessionInfoReduction{
				state:       wantState,
				disposition: SessionInfoDispositionReplaced,
			})
		})
	}
}

func TestReduceSessionInfoRejectsRetiredTupleAsStale(t *testing.T) {
	identityA := reducerTestIdentity(2025, 100, canonicalSessionTypeRace, canonicalSessionNameRace)
	identityB := reducerTestIdentity(2025, 101, canonicalSessionTypeRace, canonicalSessionNameRace)
	descriptorA := reducerTestDescriptor(identityA, 10, reducerTestSchedule(1))
	descriptorB := reducerTestDescriptor(identityB, 20, reducerTestSchedule(2))
	state := reduceSessionInfo(sessionInfoState{}, descriptorA).state
	state = reduceSessionInfo(state, descriptorB).state

	staleDescriptor := reducerTestDescriptor(identityA, 99, reducerTestSchedule(4))
	got := reduceSessionInfo(state, staleDescriptor)
	wantState := state
	wantState.Synchronized = false
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       wantState,
		disposition: sessionInfoDispositionStale,
	})

	restored := reduceSessionInfo(got.state, descriptorB)
	wantState.Synchronized = true
	assertSessionInfoReduction(t, restored, sessionInfoReduction{
		state:       wantState,
		disposition: SessionInfoDispositionRefreshed,
	})
}

func TestReduceSessionInfoGenerationAdvancesOnlyForUnseenTuples(t *testing.T) {
	identityA := reducerTestIdentity(2025, 100, canonicalSessionTypeRace, canonicalSessionNameRace)
	identityB := reducerTestIdentity(2025, 101, canonicalSessionTypeRace, canonicalSessionNameRace)
	identityC := reducerTestIdentity(2025, 102, canonicalSessionTypeRace, canonicalSessionNameRace)
	descriptorA := reducerTestDescriptor(identityA, 10, reducerTestSchedule(1))
	state := reduceSessionInfo(sessionInfoState{}, descriptorA).state

	state = reduceSessionInfo(state, descriptorA).state
	if state.Generation != 1 {
		t.Fatalf("generation after repetition = %d, want 1", state.Generation)
	}
	state = reduceSessionInfo(state, reducerTestDescriptor(identityA, 11, reducerTestSchedule(2))).state
	if state.Generation != 1 {
		t.Fatalf("generation after route correction = %d, want 1", state.Generation)
	}
	state = reduceSessionInfo(state, sessionInfoParseResult{issues: sessionInfoIssueIdentity}).state
	if state.Generation != 1 {
		t.Fatalf("generation after unresolved identity = %d, want 1", state.Generation)
	}
	state = reduceSessionInfo(state, reducerTestDescriptor(identityB, 10, reducerTestSchedule(3))).state
	if state.Generation != 2 {
		t.Fatalf("generation after replacement = %d, want 2", state.Generation)
	}
	state = reduceSessionInfo(state, descriptorA).state
	if state.Generation != 2 {
		t.Fatalf("generation after stale replay = %d, want 2", state.Generation)
	}
	state = reduceSessionInfo(state, reducerTestDescriptor(identityC, 0, reducerTestSchedule(4))).state
	if state.Generation != 3 {
		t.Fatalf("generation after second replacement = %d, want 3", state.Generation)
	}
}

func TestReduceSessionInfoTokenBoundaries(t *testing.T) {
	identityA := reducerTestIdentity(2025, 100, canonicalSessionTypeRace, canonicalSessionNameRace)
	identityB := reducerTestIdentity(2025, 101, canonicalSessionTypeRace, canonicalSessionNameRace)
	identityC := reducerTestIdentity(2025, 102, canonicalSessionTypeRace, canonicalSessionNameRace)
	descriptorA := reducerTestDescriptor(identityA, 10, reducerTestSchedule(1))
	descriptorB := reducerTestDescriptor(identityB, 20, reducerTestSchedule(2))
	descriptorC := reducerTestDescriptor(identityC, 30, reducerTestSchedule(3))

	t.Run("generation", func(t *testing.T) {
		state := reduceSessionInfo(sessionInfoState{}, descriptorA).state
		state.Generation = ^SessionInfoGeneration(0) - 1
		state.routeEpoch = 7
		before := state

		advanced := reduceSessionInfo(state, descriptorB)
		wantAdvanced := sessionInfoState{
			Identity:          identityB,
			IdentityAvailable: true,
			Synchronized:      true,
			routeKey:          20,
			routeAvailable:    true,
			schedule:          reducerTestSchedule(2),
			scheduleAvailable: true,
			Generation:        ^SessionInfoGeneration(0),
		}
		wantAdvanced.retired.tuples[0] = identityA.logicalTuple()
		wantAdvanced.retired.count = 1
		assertSessionInfoReduction(t, advanced, sessionInfoReduction{
			state:       wantAdvanced,
			disposition: SessionInfoDispositionReplaced,
		})
		if state != before {
			t.Fatal("final legal generation advance mutated input state")
		}

		got := reduceSessionInfo(advanced.state, descriptorC)
		wantExhausted := wantAdvanced
		wantExhausted.Synchronized = false
		assertSessionInfoReduction(t, got, sessionInfoReduction{
			state:       wantExhausted,
			disposition: sessionInfoDispositionTokenExhausted,
		})

		recovered := reduceSessionInfo(got.state, descriptorB)
		assertSessionInfoReduction(t, recovered, sessionInfoReduction{
			state:       wantAdvanced,
			disposition: SessionInfoDispositionRefreshed,
		})
	})

	t.Run("routing epoch", func(t *testing.T) {
		state := reduceSessionInfo(sessionInfoState{}, descriptorA).state
		state.routeEpoch = ^sessionInfoRouteEpoch(0) - 1
		before := state

		corrected := reducerTestDescriptor(identityA, 20, reducerTestSchedule(2))
		advanced := reduceSessionInfo(state, corrected)
		wantAdvanced := sessionInfoState{
			Identity:          identityA,
			IdentityAvailable: true,
			Synchronized:      true,
			routeKey:          20,
			routeAvailable:    true,
			schedule:          reducerTestSchedule(2),
			scheduleAvailable: true,
			Generation:        1,
			routeEpoch:        ^sessionInfoRouteEpoch(0),
		}
		assertSessionInfoReduction(t, advanced, sessionInfoReduction{
			state:           wantAdvanced,
			disposition:     SessionInfoDispositionRefreshed,
			routeTransition: true,
		})
		if state != before {
			t.Fatal("final legal routing-epoch advance mutated input state")
		}

		got := reduceSessionInfo(advanced.state, reducerTestDescriptor(identityA, 30, reducerTestSchedule(3)))
		wantExhausted := wantAdvanced
		wantExhausted.Synchronized = false
		assertSessionInfoReduction(t, got, sessionInfoReduction{
			state:       wantExhausted,
			disposition: sessionInfoDispositionTokenExhausted,
		})

		recovered := reduceSessionInfo(got.state, corrected)
		assertSessionInfoReduction(t, recovered, sessionInfoReduction{
			state:       wantAdvanced,
			disposition: SessionInfoDispositionRefreshed,
		})

		replaced := reduceSessionInfo(got.state, descriptorB)
		wantReplacement := sessionInfoState{
			Identity:          identityB,
			IdentityAvailable: true,
			Synchronized:      true,
			routeKey:          20,
			routeAvailable:    true,
			schedule:          reducerTestSchedule(2),
			scheduleAvailable: true,
			Generation:        2,
		}
		wantReplacement.retired.tuples[0] = identityA.logicalTuple()
		wantReplacement.retired.count = 1
		assertSessionInfoReduction(t, replaced, sessionInfoReduction{
			state:       wantReplacement,
			disposition: SessionInfoDispositionReplaced,
		})
	})
}

func TestReduceSessionInfoRetirementHorizon(t *testing.T) {
	const (
		retirementHorizon = 256
		finalGeneration   = 2*retirementHorizon + 17
	)
	assertStale := func(state sessionInfoState, index int) {
		t.Helper()
		before := state
		got := reduceSessionInfo(state, reducerTestIndexedDescriptor(index))
		wantState := state
		wantState.Synchronized = false
		assertSessionInfoReduction(t, got, sessionInfoReduction{
			state:       wantState,
			disposition: sessionInfoDispositionStale,
		})
		if state != before {
			t.Fatalf("retained tuple %d replay mutated input state", index)
		}
	}

	var state sessionInfoState
	for generation := 1; generation <= finalGeneration; generation++ {
		index := generation - 1
		before := state
		got := reduceSessionInfo(state, reducerTestIndexedDescriptor(index))
		disposition := SessionInfoDispositionReplaced
		if generation == 1 {
			disposition = SessionInfoDispositionInstalled
		}
		firstRetired := index - retirementHorizon
		if firstRetired < 0 {
			firstRetired = 0
		}
		want := sessionInfoReduction{
			state: sessionInfoState{
				Identity:          reducerTestIndexedIdentity(index),
				IdentityAvailable: true,
				Synchronized:      true,
				routeKey:          99,
				routeAvailable:    true,
				schedule:          reducerTestSchedule(1),
				scheduleAvailable: true,
				Generation:        SessionInfoGeneration(generation),
			},
			disposition: disposition,
		}
		assertSessionInfoReductionWithRetiredRange(t, got, want, firstRetired, index-1)
		if state != before {
			t.Fatalf("generation %d mutated input state", generation)
		}
		state = got.state

		switch generation {
		case 257:
			assertStale(state, 0)
		case 258:
			assertStale(state, 256)
		case 513:
			assertStale(state, 511)
		}
	}

	firstRetired := finalGeneration - 1 - retirementHorizon
	for index := firstRetired; index < finalGeneration-1; index++ {
		assertStale(state, index)
	}

	evictedIndex := firstRetired - 1
	before := state
	replayed := reduceSessionInfo(state, reducerTestIndexedDescriptor(evictedIndex))
	wantReplay := sessionInfoReduction{
		state: sessionInfoState{
			Identity:          reducerTestIndexedIdentity(evictedIndex),
			IdentityAvailable: true,
			Synchronized:      true,
			routeKey:          99,
			routeAvailable:    true,
			schedule:          reducerTestSchedule(1),
			scheduleAvailable: true,
			Generation:        finalGeneration + 1,
		},
		disposition: SessionInfoDispositionReplaced,
	}
	assertSessionInfoReductionWithRetiredRange(
		t,
		replayed,
		wantReplay,
		firstRetired+1,
		finalGeneration-1,
	)
	if state != before {
		t.Fatal("evicted tuple replay mutated input state")
	}

	resetIndex := finalGeneration - 2
	fresh := reduceSessionInfo(sessionInfoState{}, reducerTestIndexedDescriptor(resetIndex))
	wantFresh := sessionInfoReduction{
		state: sessionInfoState{
			Identity:          reducerTestIndexedIdentity(resetIndex),
			IdentityAvailable: true,
			Synchronized:      true,
			routeKey:          99,
			routeAvailable:    true,
			schedule:          reducerTestSchedule(1),
			scheduleAvailable: true,
			Generation:        1,
		},
		disposition: SessionInfoDispositionInstalled,
	}
	assertSessionInfoReductionWithRetiredRange(t, fresh, wantFresh, 0, -1)
}

func reducerTestIdentity(
	season int64,
	meetingKey int64,
	sessionType canonicalSessionType,
	sessionName canonicalSessionName,
) SessionInfoIdentity {
	return SessionInfoIdentity{
		Season:      season,
		meetingKey:  meetingKey,
		sessionType: sessionType,
		sessionName: sessionName,
	}
}

func reducerTestSchedule(day int) sessionInfoSchedule {
	start := time.Date(2025, time.January, day, 12, 0, 0, 0, time.UTC)
	return sessionInfoSchedule{
		startUTC:  start,
		endUTC:    start.Add(time.Hour),
		utcOffset: time.Duration(day) * time.Hour,
	}
}

func reducerTestDescriptor(
	identity SessionInfoIdentity,
	routeKey int64,
	schedule sessionInfoSchedule,
) sessionInfoParseResult {
	return sessionInfoParseResult{
		identity:          identity,
		identityAvailable: true,
		routeKey:          routeKey,
		routeAvailable:    routeKey > 0,
		schedule:          schedule,
		scheduleAvailable: true,
	}
}

func reducerTestIndexedIdentity(index int) SessionInfoIdentity {
	return reducerTestIdentity(2025, int64(index+1), canonicalSessionTypeRace, canonicalSessionNameRace)
}

func reducerTestIndexedDescriptor(index int) sessionInfoParseResult {
	return reducerTestDescriptor(reducerTestIndexedIdentity(index), 99, reducerTestSchedule(1))
}

func reducerTestIssueName(issues sessionInfoIssueSet) string {
	switch issues {
	case sessionInfoIssueShape:
		return "shape"
	case sessionInfoIssueIdentity | sessionInfoIssueRoute | sessionInfoIssueSchedule:
		return "missing bundles"
	case sessionInfoIssueClassification:
		return "classification"
	default:
		return "unknown"
	}
}

func assertSessionInfoReduction(t *testing.T, got, want sessionInfoReduction) {
	t.Helper()
	gotRetired := activeRetiredTuples(got.state.retired)
	wantRetired := activeRetiredTuples(want.state.retired)
	got.state.retired = sessionInfoRetiredTuples{}
	want.state.retired = sessionInfoRetiredTuples{}
	if got != want {
		t.Fatalf("reduction = %#v, want %#v", got, want)
	}
	if !slices.Equal(gotRetired, wantRetired) {
		t.Fatalf("retired tuples = %#v, want %#v", gotRetired, wantRetired)
	}
}

func assertSessionInfoReductionWithRetiredRange(
	t *testing.T,
	got, want sessionInfoReduction,
	firstRetired, lastRetired int,
) {
	t.Helper()
	retired := got.state.retired
	got.state.retired = sessionInfoRetiredTuples{}
	if got != want {
		t.Fatalf("reduction = %#v, want %#v", got, want)
	}
	assertRetiredTupleRange(t, retired, firstRetired, lastRetired)
}

func activeRetiredTuples(retired sessionInfoRetiredTuples) []sessionInfoLogicalTuple {
	tuples := make([]sessionInfoLogicalTuple, retired.count)
	for offset := range tuples {
		index := (int(retired.start) + offset) % len(retired.tuples)
		tuples[offset] = retired.tuples[index]
	}
	return tuples
}

func assertRetiredTupleRange(t *testing.T, retired sessionInfoRetiredTuples, first, last int) {
	t.Helper()
	tuples := activeRetiredTuples(retired)
	if len(tuples) != last-first+1 {
		t.Fatalf("retired tuple count = %d, want %d", len(tuples), last-first+1)
	}
	for offset, tuple := range tuples {
		want := reducerTestIndexedIdentity(first + offset).logicalTuple()
		if tuple != want {
			t.Fatalf("retired tuple %d = %#v, want %#v", offset, tuple, want)
		}
	}
}
