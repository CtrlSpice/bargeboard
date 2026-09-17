package f1livetimingreceiver

import "errors"

var errInvalidDriverRegistryUpdate = errors.New("invalid DriverList registry update")

type driverRegistryEntry struct {
	number            int64
	tla               string
	tlaState          driverListFieldState
	racingNumberState driverListFieldState
}

type driverRegistryState struct {
	drivers      [maxDriverRegistryEntries]driverRegistryEntry
	count        uint8
	frozen       bool
	synchronized bool
}

type driverRegistryDisposition uint8

const (
	driverRegistryDispositionNoUpdate driverRegistryDisposition = iota
	driverRegistryDispositionStaged
	driverRegistryDispositionFrozen
	driverRegistryDispositionRefreshed
	driverRegistryDispositionUnavailable
	driverRegistryDispositionConflict
)

type driverRegistryReduction struct {
	state       driverRegistryState
	disposition driverRegistryDisposition
	issues      driverListIssueSet
}

func reduceDriverRegistry(
	state driverRegistryState,
	source liveTimingUpdateSource,
	parsed driverListParseResult,
) (driverRegistryReduction, error) {
	switch source {
	case liveTimingUpdateSourceFeed:
		return reduceDriverRegistryFeed(state, parsed), nil
	case liveTimingUpdateSourceSnapshot:
		return reduceDriverRegistrySnapshot(state, parsed), nil
	default:
		return driverRegistryReduction{}, errInvalidDriverRegistryUpdate
	}
}

func reduceDriverRegistryFeed(
	state driverRegistryState,
	parsed driverListParseResult,
) driverRegistryReduction {
	reduction := driverRegistryReduction{state: state, issues: parsed.issues}
	if !parsed.object {
		reduction.state.synchronized = false
		reduction.disposition = driverRegistryDispositionUnavailable
		return reduction
	}
	if !state.frozen && parsed.issues&driverListIssueLimit != 0 {
		return reduction
	}

	if state.frozen {
		conflict := parsed.issues&driverListIssueLimit != 0
		for index := 0; index < int(parsed.count); index++ {
			patch := parsed.entries[index]
			stateIndex, found := driverRegistryEntryIndex(state, patch.number)
			if !found || !patch.object || patch.duplicate {
				conflict = true
				continue
			}
			frozen := state.drivers[stateIndex]
			if patch.tlaState == driverListFieldInvalid ||
				(patch.tlaState == driverListFieldValid && patch.tla != frozen.tla) ||
				patch.racingNumberState == driverListFieldInvalid {
				conflict = true
			}
		}
		if conflict {
			reduction.state.synchronized = false
			reduction.disposition = driverRegistryDispositionConflict
			reduction.issues |= driverListIssueConflict
		}
		return reduction
	}

	changed := false
	for index := 0; index < int(parsed.count); index++ {
		var applied bool
		var limited bool
		reduction.state, applied, limited = applyStagedDriverPatch(reduction.state, parsed.entries[index])
		changed = changed || applied
		if limited {
			reduction.issues |= driverListIssueLimit
		}
	}
	if changed {
		reduction.disposition = driverRegistryDispositionStaged
	}
	return reduction
}

func reduceDriverRegistrySnapshot(
	state driverRegistryState,
	parsed driverListParseResult,
) driverRegistryReduction {
	reduction := driverRegistryReduction{state: state, issues: parsed.issues}
	if !coherentDriverListSnapshot(parsed) {
		if parsed.object && parsed.issues&driverListIssueLimit == 0 {
			if parsed.count == 0 {
				reduction.issues |= driverListIssueIdentity
			}
			for index := 0; index < int(parsed.count); index++ {
				entry := parsed.entries[index]
				if !entry.object || entry.duplicate || entry.tlaState != driverListFieldValid ||
					entry.racingNumberState == driverListFieldInvalid {
					reduction.issues |= driverListIssueIdentity
				}
			}
		}
		reduction.state.synchronized = false
		reduction.disposition = driverRegistryDispositionUnavailable
		return reduction
	}

	candidate := frozenDriverRegistry(parsed)
	if !state.frozen {
		reduction.state = candidate
		reduction.disposition = driverRegistryDispositionFrozen
		return reduction
	}
	if sameFrozenDriverRegistry(state, candidate) {
		reduction.state.synchronized = true
		reduction.disposition = driverRegistryDispositionRefreshed
		return reduction
	}
	reduction.state.synchronized = false
	reduction.disposition = driverRegistryDispositionConflict
	reduction.issues |= driverListIssueConflict
	return reduction
}

func applyStagedDriverPatch(
	state driverRegistryState,
	patch driverListEntryPatch,
) (driverRegistryState, bool, bool) {
	index, found := driverRegistryEntryIndex(state, patch.number)
	if !found {
		if int(state.count) == len(state.drivers) {
			return state, false, true
		}
		copy(state.drivers[index+1:int(state.count)+1], state.drivers[index:int(state.count)])
		state.drivers[index] = driverRegistryEntry{number: patch.number}
		state.count++
	}

	before := state.drivers[index]
	entry := before
	if patch.duplicate {
		entry.tla = ""
		entry.tlaState = driverListFieldInvalid
		entry.racingNumberState = driverListFieldInvalid
	} else if !patch.object {
		entry.tla = ""
		entry.tlaState = driverListFieldInvalid
	} else {
		if patch.tlaState != driverListFieldAbsent {
			entry.tla = patch.tla
			entry.tlaState = patch.tlaState
		}
		if patch.racingNumberState != driverListFieldAbsent {
			entry.racingNumberState = patch.racingNumberState
		}
	}
	state.drivers[index] = entry
	return state, !found || entry != before, false
}

func coherentDriverListSnapshot(parsed driverListParseResult) bool {
	if !parsed.object || parsed.count == 0 || parsed.issues&driverListIssueLimit != 0 {
		return false
	}
	for index := 0; index < int(parsed.count); index++ {
		entry := parsed.entries[index]
		if !entry.object || entry.duplicate || entry.tlaState != driverListFieldValid ||
			entry.racingNumberState == driverListFieldInvalid {
			return false
		}
	}
	return true
}

func frozenDriverRegistry(parsed driverListParseResult) driverRegistryState {
	state := driverRegistryState{
		frozen:       true,
		synchronized: true,
	}
	for index := 0; index < int(parsed.count); index++ {
		patch := parsed.entries[index]
		insert, _ := driverRegistryEntryIndex(state, patch.number)
		copy(state.drivers[insert+1:int(state.count)+1], state.drivers[insert:int(state.count)])
		state.drivers[insert] = driverRegistryEntry{
			number:            patch.number,
			tla:               patch.tla,
			tlaState:          driverListFieldValid,
			racingNumberState: driverListFieldValid,
		}
		state.count++
	}
	return state
}

func sameFrozenDriverRegistry(left, right driverRegistryState) bool {
	if left.count != right.count {
		return false
	}
	for index := 0; index < int(left.count); index++ {
		if left.drivers[index].number != right.drivers[index].number ||
			left.drivers[index].tla != right.drivers[index].tla {
			return false
		}
	}
	return true
}

func driverRegistryEntryIndex(state driverRegistryState, number int64) (int, bool) {
	for index := 0; index < int(state.count); index++ {
		if state.drivers[index].number == number {
			return index, true
		}
		if state.drivers[index].number > number {
			return index, false
		}
	}
	return int(state.count), false
}
