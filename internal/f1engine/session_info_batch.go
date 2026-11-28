package f1engine

import "errors"

var errInvalidNormalizedSessionInfoBatch = errors.New("invalid normalized F1 SessionInfo batch")

// reduceSessionInfoBatch reports false when the batch has no authoritative
// SessionInfo outcome. Issues describe occurrences in this batch only. The
// normalized contract permits one feed update or one atomic snapshot with at
// most one SessionInfo; callers combining feed results must OR issues separately
// from the last state/disposition, which can recover later in wire order.
func reduceSessionInfoBatch(
	state sessionInfoState,
	batch Batch,
) (sessionInfoReduction, bool, error) {
	const sessionInfoTopic = "SessionInfo"
	updateIndex := -1

	switch batch.Source {
	case Feed:
		if len(batch.RequestedTopics) != 0 || len(batch.PresentTopics) != 0 || len(batch.Updates) != 1 {
			return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
		}
		update := batch.Updates[0]
		if update.Topic == "" || update.Source != batch.Source {
			return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
		}
		if update.Topic != sessionInfoTopic {
			return sessionInfoReduction{}, false, nil
		}
		updateIndex = 0

	case Snapshot:
		requestedTopics, requestedValid := LiveTimingTopicSet(batch.RequestedTopics)
		if len(batch.RequestedTopics) == 0 || !requestedValid || len(batch.PresentTopics) != len(batch.Updates) {
			return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
		}
		if _, presentValid := LiveTimingTopicSet(batch.PresentTopics); !presentValid {
			return sessionInfoReduction{}, false, errInvalidNormalizedSessionInfoBatch
		}
		for index, topic := range batch.PresentTopics {
			update := batch.Updates[index]
			if update.Topic != topic || update.Source != batch.Source {
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

	descriptor, err := parseSessionInfo(batch.Updates[updateIndex].Payload)
	if err != nil {
		return sessionInfoReduction{}, false, err
	}
	return reduceSessionInfo(state, descriptor), true, nil
}
