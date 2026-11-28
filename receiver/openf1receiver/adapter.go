package openf1receiver

import (
	"maps"
	"math"
	"net/http"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

const (
	sessions = "sessions"
	drivers  = "drivers"
	results  = "session_result"
)

type Epoch struct{ Generation, Route uint64 }
type Request struct {
	Epoch
	Attempt      int
	RouteKey     int64
	Stage        string
	Deadline     time.Time
	DispatchedAt time.Time
}
type Response struct {
	StartedAt  time.Time
	Err        error
	Status     int
	RetryAfter string
	Body       []byte
	Failure    Diagnostic
}
type Effects struct {
	Observation Observation
	Diagnostics []Diagnostic
}

// Session is supplied by the owner after reducing a source update. Terminal
// means an authoritative Finished, Finalised or Ends has been accepted.
// DiscoverRoster is only for a standalone post-session receiver.
type Session struct {
	Generation, Route                 uint64
	HasExplicitEpoch                  bool
	Identity                          f1id.SessionIdentity
	RouteKey                          int64
	Enabled, Terminal, DiscoverRoster bool
	Drivers                           []DriverConfig
}
type AdapterState struct {
	Epoch
	Snapshot                                       Snapshot
	id                                             f1id.SessionIdentity
	key                                            int64
	eligible, stopped, closing                     bool
	epochExhausted                                 bool
	expected, frozen                               map[int64]string
	stage                                          string
	deadline, next, globalSpacing, globalRateLimit time.Time
	attempts, failures                             int
	flight                                         Request
	diagnosed                                      map[Diagnostic]bool
}

// Update invalidates old work without releasing its slot. On replacement the
// owner captures its old root effects before applying this transition.
func (s AdapterState) Update(in Session, now time.Time) (AdapterState, bool) {
	if s.closing || s.epochExhausted {
		return s, false
	}
	cancel := false
	if in.Identity != s.id || in.HasExplicitEpoch && in.Generation != s.Generation {
		cancel = s.flight.Attempt != 0
		epoch := Epoch{in.Generation, in.Route}
		if !in.HasExplicitEpoch {
			if s.Generation == math.MaxUint64 {
				s.stopped, s.epochExhausted = true, true
				return s, cancel
			}
			epoch = Epoch{Generation: s.Generation + 1}
		}
		s = AdapterState{Epoch: epoch, id: in.Identity, key: in.RouteKey, globalSpacing: s.globalSpacing, globalRateLimit: s.globalRateLimit, flight: s.flight, stage: sessions}
	} else if in.RouteKey != s.key || in.HasExplicitEpoch && in.Route != s.Route {
		cancel = s.flight.Attempt != 0
		if in.HasExplicitEpoch {
			s.Route = in.Route
		} else if s.Route == math.MaxUint64 {
			s.stopped, s.epochExhausted = true, true
			return s, cancel
		} else {
			s.Route++
		}
		s.key = in.RouteKey
		s.frozen = nil
		s.stage = sessions
	}
	expected, ok := roster(in.Drivers)
	s.expected = expected
	s.eligible = in.Enabled && raceLike(in.Identity) && in.Terminal && s.key > 0 && (s.frozen != nil || ok && (len(expected) > 0 || in.DiscoverRoster))
	if s.eligible && s.deadline.IsZero() {
		s.deadline = now.Add(time.Hour)
	}
	return s, cancel
}
func (s AdapterState) Next() (time.Time, bool) {
	at := later(s.next, later(s.globalSpacing, s.globalRateLimit))
	return at, s.eligible && !s.closing && !s.stopped && s.flight.Attempt == 0 && s.attempts < 128 && at.Before(s.deadline)
}

// Dispatch consumes a timer token and charges one attempt before HTTP starts.
func (s AdapterState) Dispatch(token Epoch, now time.Time) (AdapterState, Request, bool) {
	at, ok := s.Next()
	if !ok || token != s.Epoch || now.Before(at) || !now.Before(s.deadline) {
		return s, Request{}, false
	}
	s.attempts++
	s.flight = Request{Epoch: s.Epoch, Attempt: s.attempts, RouteKey: s.key, Stage: s.stage, Deadline: s.deadline, DispatchedAt: now}
	s.globalSpacing = now.Add(time.Second)
	return s, s.flight, true
}
func retryDelay(raw string, wall time.Time) time.Duration {
	if n, ok := f1id.ParseNonNegativeCanonicalInt64([]byte(raw)); ok {
		if n == 0 {
			return 0
		}
		if n > math.MaxInt64/int64(time.Second) {
			return 0
		}
		return max(30*time.Second, time.Duration(n)*time.Second)
	}
	t, err := time.Parse(http.TimeFormat, raw)
	if err != nil || t.Format(http.TimeFormat) != raw || !t.After(wall) {
		return 0
	}
	delay := t.Sub(wall)
	if !wall.Add(delay).Equal(t) {
		return 0
	}
	return max(30*time.Second, delay)
}
func (s *AdapterState) diagnose(e *Effects, d Diagnostic) {
	if d != "" && !s.diagnosed[d] {
		s.diagnosed = maps.Clone(s.diagnosed)
		if s.diagnosed == nil {
			s.diagnosed = make(map[Diagnostic]bool)
		}
		s.diagnosed[d] = true
		e.Diagnostics = append(e.Diagnostics, d)
	}
}

// Complete receives the response-completion monotonic and wall clocks, not
// the later time at which the owner happens to drain its queue.
func (s AdapterState) Complete(q Request, r Response, now, wall time.Time) (AdapterState, Effects) {
	var e Effects
	if q.Attempt == 0 || q != s.flight {
		return s, e
	}
	s.flight = Request{}
	dispatch := q.DispatchedAt
	if dispatch.IsZero() {
		dispatch = now
	}
	s.globalSpacing = later(s.globalSpacing, later(dispatch, r.StartedAt).Add(time.Second))
	var retry time.Duration
	if r.Status == 429 {
		retry = retryDelay(r.RetryAfter, wall)
		if retry > 0 {
			s.globalRateLimit = later(s.globalRateLimit, now.Add(retry))
		}
	}
	if s.closing || s.epochExhausted || q.Epoch != s.Epoch {
		return s, e
	}
	if s.frozen == nil {
		s.stage = sessions
	}
	if !now.Before(s.deadline) {
		s.stopped = true
		return s, e
	}
	if retry > 0 && !now.Add(retry).Before(s.deadline) {
		s.stopped = true
	}
	if !s.eligible {
		return s, e
	}
	delay := 30 * time.Second
	d := r.Failure
	transient := d != ""
	switch {
	case transient:
	case r.Status == 200:
		expected := s.expected
		if q.Stage == results {
			expected = s.frozen
		}
		p, problem := parse(r.Body, q.Stage, s.id, s.key, expected)
		if problem != "" {
			transient = true
			d = problem
			break
		}
		s.failures = 0
		if !p.empty {
			switch q.Stage {
			case sessions:
				s.stage = drivers
				delay = 0
			case drivers:
				s.stage = results
				s.frozen = p.drivers
			case results:
				var changes []Change
				s.Snapshot, changes, d = s.Snapshot.accept(p.rows)
				if len(changes) > 0 {
					e.Observation = Observation{s.id, s.Snapshot.revision, wall.UTC(), changes}
				}
				s.stopped = s.Snapshot.overflow
			}
		}
	case r.Status == 404:
		s.failures = 0
		d = DiagHTTPStatus
	case r.Status == 401 || r.Status == 403:
		delay = 5 * time.Minute
		d = DiagHTTPStatus
	case r.Status == 429:
		d = DiagRateLimit
		delay = retry
		transient = retry == 0
	case r.Status == 408 || r.Status == 425 || r.Status >= 500 && r.Status < 600 || r.Status >= 200 && r.Status < 300:
		transient = true
		d = DiagHTTPStatus
	default:
		d = DiagHTTPStatus
		s.stopped = true
	}
	if transient {
		s.failures = min(s.failures+1, 5)
		delay = min(30*time.Second<<(s.failures-1), 5*time.Minute)
	}
	s.next = now.Add(delay)
	s.diagnose(&e, d)
	if s.attempts == 128 {
		s.diagnose(&e, DiagRequestBudget)
	}
	return s, e
}

func (s AdapterState) BeginShutdown() (AdapterState, bool) {
	s.closing = true
	return s, s.flight.Attempt != 0
}

// Barrier runs after request workers join. Capture roots from the returned
// snapshot; no subsequent action can reopen this adapter.
func (s AdapterState) Barrier() (AdapterState, Snapshot) {
	if !s.closing || s.flight.Attempt != 0 {
		return s, Snapshot{}
	}
	return AdapterState{closing: true}, s.Snapshot
}
