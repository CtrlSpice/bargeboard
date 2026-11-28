package f1engine

const sessionInfoRetiredTupleLimit = 256

type sessionInfoLogicalTuple struct {
	season      int64
	meetingKey  int64
	sessionName canonicalSessionName
}

func (identity SessionInfoIdentity) logicalTuple() sessionInfoLogicalTuple {
	return sessionInfoLogicalTuple{
		season:      identity.Season,
		meetingKey:  identity.meetingKey,
		sessionName: identity.sessionName,
	}
}

type SessionInfoGeneration uint64

type sessionInfoRouteEpoch uint64

type sessionInfoRetiredTuples struct {
	tuples [sessionInfoRetiredTupleLimit]sessionInfoLogicalTuple
	start  uint16
	count  uint16
}

func (retired sessionInfoRetiredTuples) contains(tuple sessionInfoLogicalTuple) bool {
	for offset := 0; offset < int(retired.count); offset++ {
		index := (int(retired.start) + offset) % len(retired.tuples)
		if retired.tuples[index] == tuple {
			return true
		}
	}
	return false
}

func (retired *sessionInfoRetiredTuples) add(tuple sessionInfoLogicalTuple) {
	if int(retired.count) < len(retired.tuples) {
		index := (int(retired.start) + int(retired.count)) % len(retired.tuples)
		retired.tuples[index] = tuple
		retired.count++
		return
	}
	retired.tuples[retired.start] = tuple
	retired.start = uint16((int(retired.start) + 1) % len(retired.tuples))
}

type sessionInfoState struct {
	Identity          SessionInfoIdentity
	IdentityAvailable bool
	Synchronized      bool
	routeKey          int64
	routeAvailable    bool
	schedule          sessionInfoSchedule
	scheduleAvailable bool
	Generation        SessionInfoGeneration
	routeEpoch        sessionInfoRouteEpoch
	retired           sessionInfoRetiredTuples
}

type sessionInfoDisposition uint8

const (
	sessionInfoDispositionUnresolved sessionInfoDisposition = iota
	sessionInfoDispositionStale
	SessionInfoDispositionInstalled
	SessionInfoDispositionRefreshed
	SessionInfoDispositionReplaced
	sessionInfoDispositionTokenExhausted
)

type sessionInfoReduction struct {
	state           sessionInfoState
	disposition     sessionInfoDisposition
	routeTransition bool
	// Occurrences in this descriptor, not retained invalidity or state history.
	issues sessionInfoIssueSet
}

// reduceSessionInfo consumes descriptors produced by parseSessionInfo.
func reduceSessionInfo(
	state sessionInfoState,
	descriptor sessionInfoParseResult,
) sessionInfoReduction {
	if !descriptor.identityAvailable {
		reduction := unsynchronizeSessionInfo(state)
		reduction.issues = descriptor.issues
		return reduction
	}

	incomingTuple := descriptor.identity.logicalTuple()
	if state.retired.contains(incomingTuple) {
		state.Synchronized = false
		return sessionInfoReduction{
			state:       state,
			disposition: sessionInfoDispositionStale,
			issues:      descriptor.issues,
		}
	}

	if !state.IdentityAvailable {
		if state.Generation == ^SessionInfoGeneration(0) {
			return exhaustedSessionInfoReduction(state, descriptor.issues)
		}
		state.Identity = descriptor.identity
		state.IdentityAvailable = true
		state.Synchronized = true
		state.Generation++
		state.routeEpoch = 0
		replaceSessionInfoMetadata(&state, descriptor)
		return sessionInfoReduction{
			state:       state,
			disposition: SessionInfoDispositionInstalled,
			issues:      descriptor.issues,
		}
	}

	if state.Identity.logicalTuple() == incomingTuple {
		routeTransition := state.routeAvailable != descriptor.routeAvailable ||
			(state.routeAvailable && state.routeKey != descriptor.routeKey)
		if routeTransition && state.routeEpoch == ^sessionInfoRouteEpoch(0) {
			return exhaustedSessionInfoReduction(state, descriptor.issues)
		}
		state.Synchronized = true
		if routeTransition {
			state.routeEpoch++
		}
		replaceSessionInfoMetadata(&state, descriptor)
		return sessionInfoReduction{
			state:           state,
			disposition:     SessionInfoDispositionRefreshed,
			routeTransition: routeTransition,
			issues:          descriptor.issues,
		}
	}

	if state.Generation == ^SessionInfoGeneration(0) {
		return exhaustedSessionInfoReduction(state, descriptor.issues)
	}
	state.retired.add(state.Identity.logicalTuple())
	state.Identity = descriptor.identity
	state.Synchronized = true
	state.Generation++
	state.routeEpoch = 0
	replaceSessionInfoMetadata(&state, descriptor)
	return sessionInfoReduction{
		state:       state,
		disposition: SessionInfoDispositionReplaced,
		issues:      descriptor.issues,
	}
}

func unsynchronizeSessionInfo(state sessionInfoState) sessionInfoReduction {
	state.Synchronized = false
	return sessionInfoReduction{
		state:       state,
		disposition: sessionInfoDispositionUnresolved,
	}
}

func exhaustedSessionInfoReduction(state sessionInfoState, issues sessionInfoIssueSet) sessionInfoReduction {
	state.Synchronized = false
	return sessionInfoReduction{
		state:       state,
		disposition: sessionInfoDispositionTokenExhausted,
		issues:      issues,
	}
}

func replaceSessionInfoMetadata(state *sessionInfoState, descriptor sessionInfoParseResult) {
	state.routeKey = descriptor.routeKey
	state.routeAvailable = descriptor.routeAvailable
	if !state.routeAvailable {
		state.routeKey = 0
	}
	state.schedule = descriptor.schedule
	state.scheduleAvailable = descriptor.scheduleAvailable
	if !state.scheduleAvailable {
		state.schedule = sessionInfoSchedule{}
	}
}
