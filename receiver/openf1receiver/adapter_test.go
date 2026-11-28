package openf1receiver

import (
	"math"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"
)

var origin = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func input() Session {
	return Session{Identity: race, RouteKey: 23, Enabled: true, Terminal: true, Drivers: []DriverConfig{{1, "ONE"}, {2, "TWO"}}}
}

type harness struct {
	t   *testing.T
	s   AdapterState
	now time.Time
}

func fresh(t *testing.T) *harness {
	s, _ := (AdapterState{}).Update(input(), origin)
	return &harness{t, s, origin}
}
func (h *harness) send(stage string) Request {
	h.t.Helper()
	at, ok := h.s.Next()
	if !ok {
		h.t.Fatal("unexpected stop", h.s)
	}
	h.now = later(at, h.now)
	var q Request
	h.s, q, ok = h.s.Dispatch(h.s.Epoch, h.now)
	if !ok || q.Stage != stage {
		h.t.Fatal("dispatch", q, stage)
	}
	return q
}
func (h *harness) reply(q Request, r Response) Effects {
	h.t.Helper()
	h.now = h.now.Add(125 * time.Millisecond)
	var e Effects
	h.s, e = h.s.Complete(q, r, h.now, h.now)
	return e
}
func success(body []byte) Response { return Response{Status: 200, Body: body} }
func frozen(t *testing.T) *harness {
	h := fresh(t)
	h.reply(h.send(sessions), success(array(session)))
	if at, _ := h.s.Next(); at != origin.Add(time.Second) {
		t.Fatal(at)
	}
	h.reply(h.send(drivers), success(array(change(driver(1), "name_acronym", `"ONE"`), change(driver(2), "name_acronym", `"TWO"`))))
	if at, _ := h.s.Next(); at.Sub(h.now) != 30*time.Second {
		t.Fatal(at)
	}
	return h
}
func TestResponsePolicy(t *testing.T) {
	for _, tc := range []struct {
		r        Response
		delay    time.Duration
		failures int
		diag     Diagnostic
	}{
		{Response{Status: 404}, 30 * time.Second, 0, DiagHTTPStatus}, {success(array()), 30 * time.Second, 0, ""},
		{Response{Status: 401}, 5 * time.Minute, 2, DiagHTTPStatus}, {Response{Status: 403}, 5 * time.Minute, 2, DiagHTTPStatus}, {Response{Status: 429, RetryAfter: "41"}, 41 * time.Second, 2, DiagRateLimit},
		{Response{Status: 429}, 2 * time.Minute, 3, DiagRateLimit}, {Response{Failure: DiagTransport}, 2 * time.Minute, 3, DiagTransport},
		{Response{Failure: DiagResponseSize}, 2 * time.Minute, 3, DiagResponseSize}, {success([]byte("null")), 2 * time.Minute, 3, DiagDecode}, {success(array("{}")), 2 * time.Minute, 3, DiagIdentity},
	} {
		h := fresh(t)
		h.s.failures = 2
		e := h.reply(h.send(sessions), tc.r)
		at, ok := h.s.Next()
		if !ok || at.Sub(h.now) != tc.delay || h.s.failures != tc.failures || (tc.diag != "" && !slices.Equal(e.Diagnostics, []Diagnostic{tc.diag})) {
			t.Fatal(tc, at, h.s, e)
		}
	}
	for _, status := range []int{100, 199, 201, 204, 206, 299, 300, 301, 307, 399, 400, 405, 408, 425, 499, 500, 503, 599} {
		h := fresh(t)
		e := h.reply(h.send(sessions), Response{Status: status})
		transient := status/100 == 2 || status/100 == 5 || status == 408 || status == 425
		at, ok := h.s.Next()
		if ok != transient || transient && (at.Sub(h.now) != 30*time.Second || h.s.failures != 1) || !slices.Equal(e.Diagnostics, []Diagnostic{DiagHTTPStatus}) {
			t.Fatal(status, h.s, e)
		}
	}
	h := fresh(t)
	for i, sec := range []int{30, 60, 120, 240, 300, 300, 300} {
		e := h.reply(h.send(sessions), Response{Failure: DiagTransport})
		at, _ := h.s.Next()
		if at.Sub(h.now) != time.Duration(sec)*time.Second || (len(e.Diagnostics) > 0) != (i == 0) {
			t.Fatal(i, h.s, e)
		}
	}
	h.reply(h.send(sessions), success(array(session)))
	h.reply(h.send(drivers), success(array(driver(1))))
	if h.s.stage != sessions || h.s.failures != 1 {
		t.Fatal(h.s)
	}
}
func TestRetryAfter(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": 0, "0": 0, "+1": 0, "01": 0, " 1": 0, "1.5": 0, "-1": 0, "9223372036854775808": 0, "9223372036854775807": 0, "1": 30 * time.Second, "31": 31 * time.Second,
		origin.Add(time.Minute).Format(http.TimeFormat): time.Minute, origin.Format(http.TimeFormat): 0, origin.Add(-time.Hour).Format(http.TimeFormat): 0, origin.Add(time.Hour).Format(time.RFC850): 0,
	} {
		if got := retryDelay(raw, origin); got != want {
			t.Errorf("%q: %v, want %v", raw, got, want)
		}
	}
	h := fresh(t)
	h.reply(h.send(sessions), Response{Status: 429, RetryAfter: "3601"})
	if _, ok := h.s.Next(); ok || !h.s.stopped {
		t.Fatal("rate limit beyond deadline")
	}
}
func TestDeadlineAndAttemptBounds(t *testing.T) {
	h := fresh(t)
	q := h.send(sessions)
	h.s, _ = h.s.Complete(q, success(array(session)), origin.Add(time.Hour), origin)
	if h.s.stage != sessions {
		t.Fatal("accepted late response")
	}
	if _, _, ok := h.s.Dispatch(h.s.Epoch, origin.Add(time.Hour)); ok {
		t.Fatal("dispatch at deadline")
	}
	h = fresh(t)
	// Coherent sessions take the short path. A 404 on drivers starts identity over.
	for i := 1; i <= 128; i++ {
		stage := sessions
		r := success(array(session))
		if i%2 == 0 {
			stage = drivers
			r = Response{Status: 404}
		}
		q := h.send(stage)
		if q.Attempt != i || h.s.attempts != i {
			t.Fatal(q)
		}
		e := h.reply(q, r)
		if slices.Contains(e.Diagnostics, DiagRequestBudget) != (i == 128) {
			t.Fatal(i, e)
		}
	}
	if _, ok := h.s.Next(); ok {
		t.Fatal("129th attempt")
	}
}
func TestEligibilityRouteReplacementAndStaleRateLimit(t *testing.T) {
	for _, edit := range []func(*Session){func(in *Session) { in.Enabled = false }, func(in *Session) { in.Terminal = false }, func(in *Session) { in.RouteKey = 0 }, func(in *Session) { in.Drivers = nil }, func(in *Session) { in.Identity.Season = 0 }} {
		in := input()
		edit(&in)
		s, _ := (AdapterState{}).Update(in, origin)
		if _, ok := s.Next(); ok || !s.deadline.IsZero() {
			t.Fatal(s)
		}
	}
	h := frozen(t)
	before := h.s
	in := input()
	in.Drivers = nil
	h.s, _ = h.s.Update(in, h.now)
	if h.s.Epoch != before.Epoch || !h.s.eligible || h.s.deadline != before.deadline || h.s.attempts != before.attempts {
		t.Fatal(h.s)
	}
	h.reply(h.send(results), success(array(result(1, 1, 2, ""), result(2, -1, 0, "dns"))))
	old := h.send(results)
	in.RouteKey = 0
	var cancel bool
	h.s, cancel = h.s.Update(in, h.now)
	if !cancel || h.s.frozen != nil || h.s.Snapshot.Revision() != 1 || h.s.Route == old.Route {
		t.Fatal(h.s)
	}
	in.RouteKey = 32
	in.Drivers = input().Drivers
	h.s, _ = h.s.Update(in, h.now)
	var e Effects
	h.s, e = h.s.Complete(old, success(array(result(1, 2, 5, ""), result(2, 1, 5, ""))), h.now, origin)
	if h.s.Snapshot.Revision() != 1 || !reflect.DeepEqual(e, Effects{}) || h.s.deadline != before.deadline {
		t.Fatal(h.s, e)
	}
	if q := h.send(sessions); q.RouteKey != 32 || q.Attempt != 5 {
		t.Fatal(q)
	}
}

func completeStaleForTest(s AdapterState, q Request, now time.Time) (AdapterState, Effects) {
	return s.Complete(q, Response{Status: 429, RetryAfter: "120"}, now, origin)
}

func TestReplacementWaitsForRoundTrip(t *testing.T) {
	h := fresh(t)
	q := h.send(sessions)
	in := input()
	in.Identity.MeetingKey++
	s, cancel := h.s.Update(in, origin)
	if !cancel || s.Generation == q.Generation || s.attempts != 0 || s.Snapshot.revision != 0 || s.globalSpacing != origin.Add(time.Second) {
		t.Fatal(s)
	}
	if _, ok := s.Next(); ok {
		t.Fatal("released before return")
	}
	now := origin.Add(2 * time.Second)
	s, e := completeStaleForTest(s, q, now)
	at, ok := s.Next()
	if !ok || at != now.Add(120*time.Second) || s.failures != 0 || s.attempts != 0 || !reflect.DeepEqual(e, Effects{}) {
		t.Fatal(s, e)
	}
	if _, _, ok = s.Dispatch(q.Epoch, at); ok {
		t.Fatal("accepted stale timer")
	}
	if _, _, ok = s.Dispatch(s.Epoch, at.Add(-1)); ok {
		t.Fatal("ignored global rate limit")
	}
	s, q, ok = s.Dispatch(s.Epoch, at)
	if !ok || q.Attempt != 1 || q.Stage != sessions {
		t.Fatal(q)
	}
	s, cancel = s.BeginShutdown()
	if !cancel {
		t.Fatal("missing cancel")
	}
	s, _ = s.Barrier()
	if s.flight != q {
		t.Fatal("early barrier")
	}
	s, e = s.Complete(q, success(array(session)), at, origin)
	if _, ok = s.Next(); ok || !reflect.DeepEqual(e, Effects{}) {
		t.Fatal(s, e)
	}
	s, _ = s.Barrier()
	s, _ = s.Update(input(), at)
	if _, ok = s.Next(); ok || !s.globalSpacing.IsZero() || !s.globalRateLimit.IsZero() {
		t.Fatal(s)
	}
}

func TestReconnectKeepsInFlightResultAndRoutingBackoff(t *testing.T) {
	h := frozen(t)
	q := h.send(results)
	in := input()
	in.Drivers = []DriverConfig{{1, "NEW"}}
	var cancel bool
	h.s, cancel = h.s.Update(in, h.now)
	if cancel || h.s.Epoch != q.Epoch {
		t.Fatal("same route invalidated work")
	}
	e := h.reply(q, success(array(result(1, 1, 3, ""), result(2, 2, 2, "dnf"))))
	if e.Observation.Revision != 1 || h.s.Snapshot.Driver(1).Acronym != "ONE" {
		t.Fatal(e, h.s)
	}
	h.reply(h.send(results), Response{Failure: DiagTransport})
	at, _ := h.s.Next()
	in.RouteKey++
	h.s, _ = h.s.Update(in, h.now)
	if next, ok := h.s.Next(); !ok || next != at || h.s.failures != 1 || h.s.Snapshot.Revision() != 1 || !h.s.diagnosed[DiagTransport] {
		t.Fatal(h.s)
	}
}

func TestEpochTransitions(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		in := input()
		in.HasExplicitEpoch = explicit
		in.Generation, in.Route = 12, 7
		s, _ := (AdapterState{}).Update(in, origin)
		want := Epoch{1, 0}
		if explicit {
			want = Epoch{12, 7}
		}
		if s.Epoch != want {
			t.Fatal(s.Epoch, want)
		}
		if explicit {
			in.Route += 4
			want.Route += 4
		} else {
			in.RouteKey++
			want.Route++
		}
		s, _ = s.Update(in, origin)
		if s.Epoch != want {
			t.Fatal(s.Epoch, want)
		}
		if explicit {
			in.Generation += 3
			want = Epoch{15, 7}
			in.Route = 7
		} else {
			in.Identity.MeetingKey++
			want = Epoch{2, 0}
		}
		s, _ = s.Update(in, origin)
		if s.Epoch != want {
			t.Fatal(s.Epoch, want)
		}
	}
}

func TestEpochExhaustion(t *testing.T) {
	for _, replace := range []bool{false, true} {
		in := input()
		in.HasExplicitEpoch = true
		in.Generation, in.Route = math.MaxUint64, math.MaxUint64
		s, _ := (AdapterState{}).Update(in, origin)
		s, q, _ := s.Dispatch(s.Epoch, origin)
		in.HasExplicitEpoch = false
		if replace {
			in.Identity.MeetingKey++
		} else {
			in.RouteKey++
		}
		s, cancel := s.Update(in, origin)
		s, e := s.Complete(q, success(array(session)), origin, origin)
		if !cancel || !s.stopped || s.Epoch != q.Epoch || s.stage != sessions || !reflect.DeepEqual(e, Effects{}) {
			t.Fatal(s, e)
		}
	}
}

func TestCompletionStartSpacing(t *testing.T) {
	for _, stale := range []bool{true, false} {
		h := fresh(t)
		q := h.send(sessions)
		if stale {
			in := input()
			in.Identity.MeetingKey++
			h.s, _ = h.s.Update(in, h.now)
		}
		r := success(array(session))
		r.StartedAt = origin.Add(3 * time.Second)
		h.s, _ = h.s.Complete(q, r, r.StartedAt.Add(time.Millisecond), origin)
		if at, ok := h.s.Next(); !ok || at != r.StartedAt.Add(time.Second) {
			t.Fatal(stale, at)
		}
	}
}

func TestRateLimitBounds(t *testing.T) {
	for _, raw := range []string{"9223372037", origin.AddDate(300, 0, 0).Format(http.TimeFormat)} {
		h := fresh(t)
		h.s.failures = 1
		h.reply(h.send(sessions), Response{Status: 429, RetryAfter: raw})
		if at, ok := h.s.Next(); !ok || at != h.now.Add(time.Minute) || !h.s.globalRateLimit.IsZero() {
			t.Fatal(raw, h.s)
		}
	}
	h := fresh(t)
	q := h.send(sessions)
	h.s, _ = h.s.Complete(q, Response{Status: 429, RetryAfter: "3600"}, origin, origin)
	if !h.s.stopped {
		t.Fatal("retry at deadline")
	}
	in := input()
	in.Identity.MeetingKey++
	s, _ := h.s.Update(in, origin.Add(time.Second))
	if at, ok := s.Next(); !ok || at != origin.Add(time.Hour) {
		t.Fatal(s)
	}
	for _, raw := range []string{"", "35"} {
		h := fresh(t)
		q := h.send(sessions)
		s, _ := h.s.Update(in, origin)
		prior := time.Time{}
		if raw != "" {
			prior = origin.Add(time.Minute)
		}
		s.globalRateLimit = prior
		s, _ = s.Complete(q, Response{Status: 429, RetryAfter: raw}, origin, origin)
		if s.globalRateLimit != prior {
			t.Fatal(raw, s.globalRateLimit)
		}
	}
}
