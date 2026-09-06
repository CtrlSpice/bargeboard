package f1livetimingreceiver

import (
	"slices"
	"testing"
	"time"
)

func TestReduceSessionInfoInstallsInitialDescriptor(t *testing.T) {
	identity := reducerTestIdentity(2025, 1253, canonicalSessionTypeRace, canonicalSessionNameRace)
	schedule := reducerTestSchedule(1)
	descriptor := reducerTestDescriptor(identity, 9688, schedule)

	got := reduceSessionInfo(sessionInfoState{}, descriptor)
	want := sessionInfoReduction{
		state: sessionInfoState{
			identity:          identity,
			identityAvailable: true,
			synchronized:      true,
			routeKey:          9688,
			routeAvailable:    true,
			schedule:          schedule,
			scheduleAvailable: true,
			generation:        1,
		},
		disposition: sessionInfoDispositionInstalled,
	}
	assertSessionInfoReduction(t, got, want)
}

func TestReduceSessionInfoInstallsWithoutOptionalBundles(t *testing.T) {
	identity := reducerTestIdentity(2025, 1253, canonicalSessionTypeRace, canonicalSessionNameRace)
	descriptor := reducerTestDescriptor(identity, 9688, reducerTestSchedule(1))
	descriptor.routeKey = 99
	descriptor.routeAvailable = false
	descriptor.schedule = reducerTestSchedule(2)
	descriptor.scheduleAvailable = false
	descriptor.issues = sessionInfoIssueRoute | sessionInfoIssueSchedule

	got := reduceSessionInfo(sessionInfoState{}, descriptor)
	want := sessionInfoReduction{
		state: sessionInfoState{
			identity:          identity,
			identityAvailable: true,
			synchronized:      true,
			generation:        1,
		},
		disposition: sessionInfoDispositionInstalled,
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
			wantState.synchronized = false
			assertSessionInfoReduction(t, got, sessionInfoReduction{
				state:       wantState,
				disposition: sessionInfoDispositionUnresolved,
			})
			if !state.synchronized {
				t.Fatal("reduceSessionInfo mutated its input state")
			}

			fresh := reduceSessionInfo(sessionInfoState{}, descriptor)
			assertSessionInfoReduction(t, fresh, sessionInfoReduction{
				disposition: sessionInfoDispositionUnresolved,
			})
		})
	}
}

func TestReduceSessionInfoReplacesCompleteOptionalBundles(t *testing.T) {
	identity := reducerTestIdentity(2021, 1107, canonicalSessionTypePractice, canonicalSessionNamePractice1)
	descriptor := reducerTestDescriptor(identity, 6594, reducerTestSchedule(1))
	state := reduceSessionInfo(sessionInfoState{}, descriptor).state

	refresh := reducerTestDescriptor(identity, 6594, reducerTestSchedule(2))
	got := reduceSessionInfo(state, refresh)
	wantState := state
	wantState.schedule = refresh.schedule
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       wantState,
		disposition: sessionInfoDispositionRefreshed,
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
		disposition:     sessionInfoDispositionRefreshed,
		routeTransition: true,
	})
	state = got.state

	repeatedClear := clear
	repeatedClear.routeKey = 7165
	repeatedClear.schedule = reducerTestSchedule(4)
	got = reduceSessionInfo(state, repeatedClear)
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       state,
		disposition: sessionInfoDispositionRefreshed,
	})
	state = got.state

	restore := reducerTestDescriptor(identity, 7165, reducerTestSchedule(1))
	got = reduceSessionInfo(state, restore)
	wantState = state
	wantState.routeKey = 7165
	wantState.routeAvailable = true
	wantState.schedule = restore.schedule
	wantState.scheduleAvailable = true
	wantState.routeEpoch = 2
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:           wantState,
		disposition:     sessionInfoDispositionRefreshed,
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
		disposition: sessionInfoDispositionRefreshed,
	})
	state = got.state

	revert := reducerTestDescriptor(identity, 6594, reducerTestSchedule(3))
	got = reduceSessionInfo(state, revert)
	wantState = state
	wantState.routeKey = 6594
	wantState.schedule = revert.schedule
	wantState.routeEpoch = 3
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:           wantState,
		disposition:     sessionInfoDispositionRefreshed,
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
		disposition: sessionInfoDispositionRefreshed,
	})

	restored := descriptor
	restored.schedule = reducerTestSchedule(2)
	got = reduceSessionInfo(got.state, restored)
	wantState.schedule = restored.schedule
	wantState.scheduleAvailable = true
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       wantState,
		disposition: sessionInfoDispositionRefreshed,
	})
}

func TestReduceSessionInfoRestoresSynchronization(t *testing.T) {
	identity := reducerTestIdentity(2021, 1107, canonicalSessionTypePractice, canonicalSessionNamePractice1)
	descriptor := reducerTestDescriptor(identity, 6594, reducerTestSchedule(1))
	state := reduceSessionInfo(sessionInfoState{}, descriptor).state

	state = reduceSessionInfo(state, sessionInfoParseResult{issues: sessionInfoIssueIdentity}).state
	if state.synchronized {
		t.Fatal("unresolved identity remained synchronized")
	}

	got := reduceSessionInfo(state, descriptor)
	wantState := state
	wantState.synchronized = true
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       wantState,
		disposition: sessionInfoDispositionRefreshed,
	})
	state = got.state

	state = reduceSessionInfo(state, sessionInfoParseResult{issues: sessionInfoIssueIdentity}).state
	corrected := reducerTestDescriptor(identity, 7165, reducerTestSchedule(2))
	got = reduceSessionInfo(state, corrected)
	wantState = state
	wantState.synchronized = true
	wantState.routeKey = 7165
	wantState.schedule = corrected.schedule
	wantState.routeEpoch = 1
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:           wantState,
		disposition:     sessionInfoDispositionRefreshed,
		routeTransition: true,
	})
}

func TestReduceSessionInfoLogicalTupleSelectsGeneration(t *testing.T) {
	identity := reducerTestIdentity(2025, 100, canonicalSessionTypeRace, canonicalSessionNameRace)
	tests := []struct {
		name     string
		incoming sessionInfoIdentity
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
				identity:          test.incoming,
				identityAvailable: true,
				synchronized:      true,
				routeKey:          test.routeKey,
				routeAvailable:    test.route,
				schedule:          incoming.schedule,
				scheduleAvailable: true,
				generation:        2,
			}
			wantState.retired.tuples[0] = identity.logicalTuple()
			wantState.retired.count = 1
			assertSessionInfoReduction(t, got, sessionInfoReduction{
				state:       wantState,
				disposition: sessionInfoDispositionReplaced,
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
	wantState.synchronized = false
	assertSessionInfoReduction(t, got, sessionInfoReduction{
		state:       wantState,
		disposition: sessionInfoDispositionStale,
	})

	restored := reduceSessionInfo(got.state, descriptorB)
	wantState.synchronized = true
	assertSessionInfoReduction(t, restored, sessionInfoReduction{
		state:       wantState,
		disposition: sessionInfoDispositionRefreshed,
	})
}

func TestReduceSessionInfoGenerationAdvancesOnlyForUnseenTuples(t *testing.T) {
	identityA := reducerTestIdentity(2025, 100, canonicalSessionTypeRace, canonicalSessionNameRace)
	identityB := reducerTestIdentity(2025, 101, canonicalSessionTypeRace, canonicalSessionNameRace)
	identityC := reducerTestIdentity(2025, 102, canonicalSessionTypeRace, canonicalSessionNameRace)
	descriptorA := reducerTestDescriptor(identityA, 10, reducerTestSchedule(1))
	state := reduceSessionInfo(sessionInfoState{}, descriptorA).state

	state = reduceSessionInfo(state, descriptorA).state
	if state.generation != 1 {
		t.Fatalf("generation after repetition = %d, want 1", state.generation)
	}
	state = reduceSessionInfo(state, reducerTestDescriptor(identityA, 11, reducerTestSchedule(2))).state
	if state.generation != 1 {
		t.Fatalf("generation after route correction = %d, want 1", state.generation)
	}
	state = reduceSessionInfo(state, sessionInfoParseResult{issues: sessionInfoIssueIdentity}).state
	if state.generation != 1 {
		t.Fatalf("generation after unresolved identity = %d, want 1", state.generation)
	}
	state = reduceSessionInfo(state, reducerTestDescriptor(identityB, 10, reducerTestSchedule(3))).state
	if state.generation != 2 {
		t.Fatalf("generation after replacement = %d, want 2", state.generation)
	}
	state = reduceSessionInfo(state, descriptorA).state
	if state.generation != 2 {
		t.Fatalf("generation after stale replay = %d, want 2", state.generation)
	}
	state = reduceSessionInfo(state, reducerTestDescriptor(identityC, 0, reducerTestSchedule(4))).state
	if state.generation != 3 {
		t.Fatalf("generation after second replacement = %d, want 3", state.generation)
	}
}

func TestReduceSessionInfoTokenExhaustionFailsClosed(t *testing.T) {
	identityA := reducerTestIdentity(2025, 100, canonicalSessionTypeRace, canonicalSessionNameRace)
	identityB := reducerTestIdentity(2025, 101, canonicalSessionTypeRace, canonicalSessionNameRace)
	descriptorA := reducerTestDescriptor(identityA, 10, reducerTestSchedule(1))

	t.Run("generation", func(t *testing.T) {
		state := reduceSessionInfo(sessionInfoState{}, descriptorA).state
		state.generation = ^sessionInfoGeneration(0)
		got := reduceSessionInfo(state, reducerTestDescriptor(identityB, 20, reducerTestSchedule(2)))
		wantState := state
		wantState.synchronized = false
		assertSessionInfoReduction(t, got, sessionInfoReduction{
			state:       wantState,
			disposition: sessionInfoDispositionTokenExhausted,
		})

		wantState.synchronized = true
		recovered := reduceSessionInfo(got.state, descriptorA)
		assertSessionInfoReduction(t, recovered, sessionInfoReduction{
			state:       wantState,
			disposition: sessionInfoDispositionRefreshed,
		})
	})

	t.Run("routing epoch", func(t *testing.T) {
		state := reduceSessionInfo(sessionInfoState{}, descriptorA).state
		state.routeEpoch = ^sessionInfoRouteEpoch(0)
		got := reduceSessionInfo(state, reducerTestDescriptor(identityA, 20, reducerTestSchedule(2)))
		wantState := state
		wantState.synchronized = false
		assertSessionInfoReduction(t, got, sessionInfoReduction{
			state:       wantState,
			disposition: sessionInfoDispositionTokenExhausted,
		})

		wantState.synchronized = true
		recovered := reduceSessionInfo(got.state, descriptorA)
		assertSessionInfoReduction(t, recovered, sessionInfoReduction{
			state:       wantState,
			disposition: sessionInfoDispositionRefreshed,
		})

		replaced := reduceSessionInfo(got.state, reducerTestDescriptor(identityB, 20, reducerTestSchedule(2)))
		wantReplacement := sessionInfoState{
			identity:          identityB,
			identityAvailable: true,
			synchronized:      true,
			routeKey:          20,
			routeAvailable:    true,
			schedule:          reducerTestSchedule(2),
			scheduleAvailable: true,
			generation:        2,
		}
		wantReplacement.retired.tuples[0] = identityA.logicalTuple()
		wantReplacement.retired.count = 1
		assertSessionInfoReduction(t, replaced, sessionInfoReduction{
			state:       wantReplacement,
			disposition: sessionInfoDispositionReplaced,
		})
	})
}

func TestReduceSessionInfoRetainsExactly256LogicalTuples(t *testing.T) {
	const currentIndex = sessionInfoRetiredTupleLimit
	var state sessionInfoState
	for index := 0; index <= currentIndex; index++ {
		got := reduceSessionInfo(state, reducerTestIndexedDescriptor(index))
		wantDisposition := sessionInfoDispositionReplaced
		if index == 0 {
			wantDisposition = sessionInfoDispositionInstalled
		}
		if got.disposition != wantDisposition {
			t.Fatalf("descriptor %d disposition = %d, want %d", index, got.disposition, wantDisposition)
		}
		state = got.state
	}

	if state.generation != currentIndex+1 || state.identity != reducerTestIndexedIdentity(currentIndex) {
		t.Fatalf("current generation = %d, identity = %#v", state.generation, state.identity)
	}
	assertRetiredTupleRange(t, state.retired, 0, sessionInfoRetiredTupleLimit-1)

	stale := reduceSessionInfo(state, reducerTestIndexedDescriptor(0))
	wantStaleState := state
	wantStaleState.synchronized = false
	assertSessionInfoReduction(t, stale, sessionInfoReduction{
		state:       wantStaleState,
		disposition: sessionInfoDispositionStale,
	})

	const nextIndex = sessionInfoRetiredTupleLimit + 1
	replaced := reduceSessionInfo(state, reducerTestIndexedDescriptor(nextIndex))
	if replaced.disposition != sessionInfoDispositionReplaced || replaced.state.generation != nextIndex+1 {
		t.Fatalf("257th retirement = %#v", replaced)
	}
	assertRetiredTupleRange(t, replaced.state.retired, 1, sessionInfoRetiredTupleLimit)
	newestStale := reduceSessionInfo(replaced.state, reducerTestIndexedDescriptor(sessionInfoRetiredTupleLimit))
	wantNewestStaleState := replaced.state
	wantNewestStaleState.synchronized = false
	assertSessionInfoReduction(t, newestStale, sessionInfoReduction{
		state:       wantNewestStaleState,
		disposition: sessionInfoDispositionStale,
	})

	replayed := reduceSessionInfo(replaced.state, reducerTestIndexedDescriptor(0))
	if replayed.disposition != sessionInfoDispositionReplaced || replayed.state.generation != nextIndex+2 ||
		replayed.state.identity != reducerTestIndexedIdentity(0) {
		t.Fatalf("evicted tuple replay = disposition %d, generation %d, identity %#v",
			replayed.disposition, replayed.state.generation, replayed.state.identity)
	}
	assertRetiredTupleRange(t, replayed.state.retired, 2, nextIndex)

	fresh := reduceSessionInfo(sessionInfoState{}, reducerTestIndexedDescriptor(0))
	if fresh.disposition != sessionInfoDispositionInstalled || fresh.state.generation != 1 {
		t.Fatalf("fresh state replay = %#v", fresh)
	}
}

func TestReduceSessionInfoRetirementCursorWraps(t *testing.T) {
	const lastIndex = 2*sessionInfoRetiredTupleLimit + 17
	var state sessionInfoState
	for index := 0; index <= lastIndex; index++ {
		state = reduceSessionInfo(state, reducerTestIndexedDescriptor(index)).state
	}
	firstRetained := lastIndex - sessionInfoRetiredTupleLimit
	assertRetiredTupleRange(t, state.retired, firstRetained, lastIndex-1)

	for _, index := range []int{firstRetained, lastIndex - 1} {
		got := reduceSessionInfo(state, reducerTestIndexedDescriptor(index))
		if got.disposition != sessionInfoDispositionStale || got.state.identity != state.identity ||
			got.state.generation != state.generation {
			t.Fatalf("retained tuple %d replay = disposition %d, generation %d, identity %#v",
				index, got.disposition, got.state.generation, got.state.identity)
		}
	}

	evicted := reduceSessionInfo(state, reducerTestIndexedDescriptor(firstRetained-1))
	if evicted.disposition != sessionInfoDispositionReplaced ||
		evicted.state.identity != reducerTestIndexedIdentity(firstRetained-1) ||
		evicted.state.generation != state.generation+1 {
		t.Fatalf("evicted tuple replay = disposition %d, generation %d, identity %#v",
			evicted.disposition, evicted.state.generation, evicted.state.identity)
	}
}

func reducerTestIdentity(
	season int64,
	meetingKey int64,
	sessionType canonicalSessionType,
	sessionName canonicalSessionName,
) sessionInfoIdentity {
	return sessionInfoIdentity{
		season:      season,
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
	identity sessionInfoIdentity,
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

func reducerTestIndexedIdentity(index int) sessionInfoIdentity {
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
