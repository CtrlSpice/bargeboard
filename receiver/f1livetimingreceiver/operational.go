package f1livetimingreceiver

import "time"

// operationalState describes input observations, never racing projection or export.
// Times are supplied process-monotonic samples; no payload or episode history is retained.
type operationalState struct {
	started, outageStarted, lastUpdate, retryAt time.Time
	connection, subscription, connectionData    bool
	ready, outage, stopped, summarized          bool
	outages, attempts, recoveries, updates      int64
	consumerFailures                            int64
	closedOutageDuration                        time.Duration
}

type operationalEvent uint8

const (
	opStart operationalEvent = iota
	opConnected
	opBatch
	opOutage
	opSchedule
	opAttempt
	opConsumerFailure
	opInvalid
	opSourceStopped
	opFinish
	opTick
)

type operationalInput struct {
	event    operationalEvent
	at       time.Time
	delay    time.Duration
	updates  int
	snapshot bool
}

type operationalNotice uint8

const (
	noticeNone operationalNotice = iota
	noticeStarting
	noticeWaiting
	noticeFirstData
	noticeOutage
	noticeProgress
	noticeRecovered
	noticeConsumerFailure
	noticeInvalid
	noticeSourceStopped
	noticeSummary
	noticeStopped
)

func reduceOperational(s operationalState, in operationalInput) (operationalState, operationalNotice) {
	if s.stopped && in.event != opTick && in.event != opFinish {
		return s, noticeNone
	}
	notice := noticeNone
	switch in.event {
	case opStart:
		s.started = in.at
		notice = noticeStarting
	case opConnected:
		s.connection, s.subscription, s.connectionData = true, false, false
		s.retryAt = time.Time{}
	case opBatch:
		if in.snapshot {
			s.subscription = true
		}
		if in.updates > 0 {
			s.updates += int64(in.updates)
			s.lastUpdate, s.connectionData = in.at, true
		}
		if s.subscription && s.connectionData {
			if s.outage {
				s.closedOutageDuration += s.currentOutageDuration(in.at)
				s.outage = false
				s.outageStarted = time.Time{}
				s.recoveries++
				notice = noticeRecovered
			}
			// ready latches whether input has ever met both readiness conditions.
			if !s.ready {
				notice = noticeFirstData
			}
			s.ready = true
		}
	case opOutage, opInvalid, opSourceStopped:
		s.connection, s.subscription, s.connectionData = false, false, false
		s.retryAt = time.Time{}
		if !s.outage {
			s.outage, s.outageStarted = true, in.at
			s.outages++
			notice = noticeOutage
		}
		if in.event == opInvalid {
			s.stopped, notice = true, noticeInvalid
		} else if in.event == opSourceStopped {
			s.stopped, notice = true, noticeSourceStopped
		}
	case opSchedule:
		s.retryAt = in.at.Add(in.delay)
		notice = noticeProgress
	case opAttempt:
		s.attempts++
		s.retryAt = time.Time{}
	case opConsumerFailure:
		s.consumerFailures++
		if s.consumerFailures == 1 {
			notice = noticeConsumerFailure
		}
	case opFinish:
		if !s.summarized {
			s.connection, s.subscription, s.connectionData = false, false, false
			s.retryAt = time.Time{}
			s.stopped, s.summarized = true, true
			notice = noticeSummary
		}
	case opTick:
		switch {
		case s.stopped:
			notice = noticeStopped
		case s.outage:
			notice = noticeProgress
		case !s.ready:
			notice = noticeWaiting
		}
	}
	return s, notice
}

func (s operationalState) currentOutageDuration(at time.Time) time.Duration {
	if s.outage {
		return max(0, at.Sub(s.outageStarted))
	}
	return 0
}

type operationalDurations struct {
	run, outage, totalOutage time.Duration
}

// The transition's outage duration is current, or just closed for first-data or
// recovery notices. Summary totals include the unresolved gap as of this sample.
func operationalTiming(before, after operationalState, at time.Time) operationalDurations {
	current := after.currentOutageDuration(at)
	gap := current
	if before.outage && !after.outage {
		gap = after.closedOutageDuration - before.closedOutageDuration
	}
	return operationalDurations{
		run:         max(0, at.Sub(after.started)),
		outage:      gap,
		totalOutage: after.closedOutageDuration + current,
	}
}

func (s operationalState) nextDelay(at time.Time) time.Duration {
	if s.retryAt.IsZero() {
		return 0
	}
	return max(0, s.retryAt.Sub(at))
}
