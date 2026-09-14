package f1livetimingreceiver

import (
	"errors"
	"time"
)

const (
	hubKeepaliveInterval = 15 * time.Second
	hubServerTimeout     = 30 * time.Second
	hubPingRecord        = "{\"type\":6}\x1e"
)

var (
	errSignalRReceiveTimeout      = errors.New("SignalR receive timeout")
	errSignalRSubscriptionTimeout = errors.New("SignalR subscription completion timeout")
)

// serverWaitBudget counts only elapsed server wait supplied by the read owner.
// Local processing has no transition; it cannot spend either remaining budget.
type serverWaitBudget struct {
	receive      time.Duration
	subscription time.Duration
	pending      bool
}

func newServerWaitBudget(pending bool) serverWaitBudget {
	state := serverWaitBudget{receive: hubServerTimeout, pending: pending}
	if pending {
		state.subscription = hubServerTimeout
	}
	return state
}

func (s serverWaitBudget) wait(elapsed time.Duration) serverWaitBudget {
	elapsed = max(0, elapsed)
	s.receive = max(0, s.receive-elapsed)
	if s.pending {
		s.subscription = max(0, s.subscription-elapsed)
	}
	return s
}

func (s serverWaitBudget) accepted(completion bool) serverWaitBudget {
	s.receive = hubServerTimeout
	if completion {
		s.pending = false
		s.subscription = 0
	}
	return s
}

func (s serverWaitBudget) remaining() time.Duration {
	if s.pending {
		return min(s.receive, s.subscription)
	}
	return s.receive
}

func (s serverWaitBudget) expired() error {
	if s.pending && s.subscription == 0 {
		return errSignalRSubscriptionTimeout
	}
	if s.receive == 0 {
		return errSignalRReceiveTimeout
	}
	return nil
}

func hubPingDelay(lastOutbound, now time.Time) time.Duration {
	return max(0, hubKeepaliveInterval-now.Sub(lastOutbound))
}
