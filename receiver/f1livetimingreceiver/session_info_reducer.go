package f1livetimingreceiver

const sessionInfoRetiredTupleLimit = 256

type sessionInfoLogicalTuple struct {
	season      int64
	meetingKey  int64
	sessionName canonicalSessionName
}

func (identity sessionInfoIdentity) logicalTuple() sessionInfoLogicalTuple {
	return sessionInfoLogicalTuple{
		season:      identity.season,
		meetingKey:  identity.meetingKey,
		sessionName: identity.sessionName,
	}
}

type sessionInfoGeneration uint64

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
	identity          sessionInfoIdentity
	identityAvailable bool
	synchronized      bool
	routeKey          int64
	routeAvailable    bool
	schedule          sessionInfoSchedule
	scheduleAvailable bool
	generation        sessionInfoGeneration
	routeEpoch        sessionInfoRouteEpoch
	retired           sessionInfoRetiredTuples
}

type sessionInfoDisposition uint8

const (
	sessionInfoDispositionUnresolved sessionInfoDisposition = iota
	sessionInfoDispositionStale
	sessionInfoDispositionInstalled
	sessionInfoDispositionRefreshed
	sessionInfoDispositionReplaced
	sessionInfoDispositionTokenExhausted
)

type sessionInfoReduction struct {
	state           sessionInfoState
	disposition     sessionInfoDisposition
	routeTransition bool
}

// reduceSessionInfo consumes descriptors produced by parseSessionInfo.
func reduceSessionInfo(
	state sessionInfoState,
	descriptor sessionInfoParseResult,
) sessionInfoReduction {
	if !descriptor.identityAvailable {
		return unsynchronizeSessionInfo(state)
	}

	incomingTuple := descriptor.identity.logicalTuple()
	if state.retired.contains(incomingTuple) {
		state.synchronized = false
		return sessionInfoReduction{
			state:       state,
			disposition: sessionInfoDispositionStale,
		}
	}

	if !state.identityAvailable {
		if state.generation == ^sessionInfoGeneration(0) {
			return exhaustedSessionInfoReduction(state)
		}
		state.identity = descriptor.identity
		state.identityAvailable = true
		state.synchronized = true
		state.generation++
		state.routeEpoch = 0
		replaceSessionInfoMetadata(&state, descriptor)
		return sessionInfoReduction{
			state:       state,
			disposition: sessionInfoDispositionInstalled,
		}
	}

	if state.identity.logicalTuple() == incomingTuple {
		routeTransition := state.routeAvailable != descriptor.routeAvailable ||
			(state.routeAvailable && state.routeKey != descriptor.routeKey)
		if routeTransition && state.routeEpoch == ^sessionInfoRouteEpoch(0) {
			return exhaustedSessionInfoReduction(state)
		}
		state.synchronized = true
		if routeTransition {
			state.routeEpoch++
		}
		replaceSessionInfoMetadata(&state, descriptor)
		return sessionInfoReduction{
			state:           state,
			disposition:     sessionInfoDispositionRefreshed,
			routeTransition: routeTransition,
		}
	}

	if state.generation == ^sessionInfoGeneration(0) {
		return exhaustedSessionInfoReduction(state)
	}
	state.retired.add(state.identity.logicalTuple())
	state.identity = descriptor.identity
	state.synchronized = true
	state.generation++
	state.routeEpoch = 0
	replaceSessionInfoMetadata(&state, descriptor)
	return sessionInfoReduction{
		state:       state,
		disposition: sessionInfoDispositionReplaced,
	}
}

func unsynchronizeSessionInfo(state sessionInfoState) sessionInfoReduction {
	state.synchronized = false
	return sessionInfoReduction{
		state:       state,
		disposition: sessionInfoDispositionUnresolved,
	}
}

func exhaustedSessionInfoReduction(state sessionInfoState) sessionInfoReduction {
	state.synchronized = false
	return sessionInfoReduction{
		state:       state,
		disposition: sessionInfoDispositionTokenExhausted,
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
