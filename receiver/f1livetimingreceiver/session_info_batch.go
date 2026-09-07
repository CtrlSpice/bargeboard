package f1livetimingreceiver

import "errors"

var errInvalidNormalizedSessionInfoBatch = errors.New("invalid normalized F1 SessionInfo batch")

// reduceSessionInfoBatch reports false when the batch has no authoritative SessionInfo outcome.
func reduceSessionInfoBatch(
	state sessionInfoState,
	batch normalizedLiveTimingBatch,
) (sessionInfoReduction, bool, error) {
	const sessionInfoTopic = "SessionInfo"
	updateIndex := -1

	switch batch.source {
	case liveTimingUpdateSourceFeed:
		if len(batch.requestedTopics) != 0 || len(batch.presentTopics) != 0 || len(batch.updates) != 1 {
			return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
		}
		update := batch.updates[0]
		if update.topic == "" || update.source != batch.source {
			return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
		}
		if update.topic != sessionInfoTopic {
			return sessionInfoReduction{}, false, nil
		}
		updateIndex = 0

	case liveTimingUpdateSourceSnapshot:
		requestedTopics, requestedValid := liveTimingTopicSet(batch.requestedTopics)
		if len(batch.requestedTopics) == 0 || !requestedValid || len(batch.presentTopics) != len(batch.updates) {
			return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
		}
		if _, presentValid := liveTimingTopicSet(batch.presentTopics); !presentValid {
			return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
		}
		for index, topic := range batch.presentTopics {
			update := batch.updates[index]
			if update.topic != topic || update.source != batch.source {
				return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
			}
			if _, requested := requestedTopics[topic]; !requested {
				return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
			}
			if topic == sessionInfoTopic {
				updateIndex = index
			}
		}

		if _, requested := requestedTopics[sessionInfoTopic]; !requested {
			return sessionInfoReduction{}, false, nil
		}
		if updateIndex == -1 {
			return unsynchronizeSessionInfo(state), true, nil
		}

	default:
		return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
	}

	descriptor, err := parseSessionInfo(batch.updates[updateIndex].payload)
	if err != nil {
		return sessionInfoReduction{}, false, err
	}
	return reduceSessionInfo(state, descriptor), true, nil
}
