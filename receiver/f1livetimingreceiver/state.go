package f1livetimingreceiver

type liveTimingState struct {
	sessionInfo sessionInfoState
}

type liveTimingReduction struct {
	state                       liveTimingState
	sessionInfoDisposition      sessionInfoDisposition
	sessionInfoAuthoritative    bool
	sessionInfoRouteTransition  bool
	sessionScopedUpdatesAllowed bool
}

func reduceLiveTimingBatch(
	state liveTimingState,
	batch normalizedLiveTimingBatch,
) (liveTimingReduction, error) {
	sessionInfo, authoritative, err := reduceSessionInfoBatch(state.sessionInfo, batch)
	if err != nil {
		return liveTimingReduction{}, err
	}

	reduction := liveTimingReduction{
		state:                    state,
		sessionInfoAuthoritative: authoritative,
	}
	if authoritative {
		reduction.state.sessionInfo = sessionInfo.state
		reduction.sessionInfoDisposition = sessionInfo.disposition
		reduction.sessionInfoRouteTransition = sessionInfo.routeTransition
	}
	reduction.sessionScopedUpdatesAllowed =
		reduction.state.sessionInfo.identityAvailable &&
			reduction.state.sessionInfo.synchronized
	return reduction, nil
}
