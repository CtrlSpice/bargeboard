package openf1receiver

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestDeliveryFailureAcrossRootGate(t *testing.T) {
	h := frozen(t)
	win := result(1, 1, 3, "")
	o := h.reply(h.send(results), success(array(win, result(2, 2, 2, "dnf")))).Observation
	fail := errors.New("downstream unavailable")
	logSink, _ := consumer.NewLogs(func(context.Context, plog.Logs) error { return fail })
	if err := logSink.ConsumeLogs(t.Context(), o.Logs("", nil)); err != fail {
		t.Fatal(err)
	}
	g := RootGate{Generation: h.s.Generation}
	roots := []DriverRoot{{Number: 2, Exists: true, Start: origin}}
	g, effects, _ := g.Close(h.s.Generation, Ends, origin.Add(time.Minute), h.now, race, h.s.Snapshot, &origin, roots)
	if len(effects) != 1 || effects[0].Description != "classified did not finish" {
		t.Fatal("log failure suppressed root", effects)
	}
	traces := ptrace.NewTraces()
	span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	effects[0].Attributes.CopyTo(span.Attributes())
	traceSink, _ := consumer.NewTraces(func(context.Context, ptrace.Traces) error { return fail })
	if err := traceSink.ConsumeTraces(t.Context(), traces); err != fail {
		t.Fatal(err)
	}
	revised := h.reply(h.send(results), success(array(win, result(2, 2, 3, "")))).Observation
	if revised.Revision != 2 || revised.Logs("", nil).LogRecordCount() != 1 {
		t.Fatal("trace failure suppressed revision")
	}
	if _, again, _ := g.Close(h.s.Generation, Shutdown, time.Time{}, h.now, race, h.s.Snapshot, &origin, roots); len(again) != 0 {
		t.Fatal("retried trace")
	}
}

func TestRevisionStream(t *testing.T) {
	h := frozen(t)
	a, b := result(1, 1, 12, ""), result(2, 2, 0, "dnf")
	poll := func(body []byte) Effects { return h.reply(h.send(results), success(body)) }
	first := poll(array(b, a))
	if first.Observation.Revision != 1 || len(first.Observation.Changes) != 2 || first.Observation.Changes[0].Current.DriverNumber != 1 {
		t.Fatal(first)
	}
	for _, r := range []Response{success(array(change(a, "gap_to_leader", "null"), change(b, "number_of_laps", "10"))), success(array()), {Status: 404}, success(array(a, b, b))} {
		e := h.reply(h.send(results), r)
		if len(e.Observation.Changes) != 0 || h.s.Snapshot.Driver(2).Outcome != OutcomeClassifiedDNF || h.s.Snapshot.Revision() != 1 {
			t.Fatal(e)
		}
	}
	c := result(2, -1, 0, "dns")
	for i := 2; i <= 64; i++ {
		row := b
		prev, out := OutcomeDNS, OutcomeClassifiedDNF
		if i%2 == 0 {
			row = c
			prev, out = out, prev
		}
		e := poll(array(a, row))
		changes := e.Observation.Changes
		if len(changes) != 1 || changes[0].Current.Outcome != out || changes[0].Previous.Outcome != prev || e.Observation.Revision != int64(i) {
			t.Fatal(i, e)
		}
	}
	if e := poll(array(c, a)); len(e.Observation.Changes) != 0 {
		t.Fatal(e)
	}
	e := poll(array(b, a))
	if !slices.Equal(e.Diagnostics, []Diagnostic{DiagRevisionOverflow}) || len(e.Observation.Changes) != 0 || h.s.Snapshot.Driver(2).Outcome != OutcomeDNS || h.s.Snapshot.Revision() != 64 {
		t.Fatal(e, h.s)
	}
	if _, ok := h.s.Next(); ok {
		t.Fatal("overflow did not stop")
	}
}
func TestExactLogs(t *testing.T) {
	for _, correlate := range []bool{true, false} {
		for _, tc := range []struct {
			rev           int64
			current, prev Row
			body          string
		}{
			{1, Row{2, "TWO", 2, OutcomeFinished}, Row{}, "f1.driver.result.observed"},
			{2, Row{2, "TWO", 0, OutcomeDSQ}, Row{2, "TWO", 2, OutcomeFinished}, "f1.driver.result.revised"},
			{3, Row{2, "TWO", 2, OutcomeFinished}, Row{2, "TWO", 0, OutcomeDSQ}, "f1.driver.result.revised"},
		} {
			o := Observation{race, tc.rev, origin, []Change{{tc.current, tc.prev}}}
			l := o.Logs("test", func(n int64) bool { return correlate && n == 2 })
			if l.LogRecordCount() != 1 {
				t.Fatal(l.LogRecordCount())
			}
			scope := l.ResourceLogs().At(0).ScopeLogs().At(0)
			r := scope.LogRecords().At(0)
			want := map[string]any{"f1.season.year": int64(2042), "f1.meeting.key": int64(17), "f1.session.type": "race", "f1.session.name": "race", "f1.data.source": "openf1", "f1.time.quality": "collector_observation", "f1.driver.number": int64(2), "f1.driver.acronym": "TWO", "f1.result.snapshot.revision": tc.rev, "f1.result.outcome": string(tc.current.Outcome)}
			if tc.current.Position != 0 {
				want["f1.result.position"] = tc.current.Position
			}
			if tc.rev > 1 {
				want["f1.result.previous_outcome"] = string(tc.prev.Outcome)
				if tc.prev.Position != 0 {
					want["f1.result.previous_position"] = tc.prev.Position
				}
			}
			var trace pcommon.TraceID
			var span pcommon.SpanID
			if correlate {
				trace, _ = f1id.DriverTraceID(race, 2)
				span, _ = f1id.DriverSessionRootSpanID(race, 2)
			}
			if r.TraceID() != trace || r.SpanID() != span || r.Body().Str() != tc.body || r.SeverityNumber() != plog.SeverityNumberInfo || r.SeverityText() != "INFO" || r.Timestamp() != pcommon.NewTimestampFromTime(origin) || r.Timestamp() != r.ObservedTimestamp() || !reflect.DeepEqual(r.Attributes().AsRaw(), want) {
				t.Fatal(tc, r.Attributes().AsRaw())
			}
		}
	}
}
func TestRootOutcomes(t *testing.T) {
	for _, tc := range []struct {
		o      Outcome
		p      int64
		status ptrace.StatusCode
		desc   string
	}{
		{OutcomeFinished, 1, ptrace.StatusCodeOk, ""}, {OutcomeDNS, 0, ptrace.StatusCodeError, "did not start"}, {OutcomeDSQ, 0, ptrace.StatusCodeError, "disqualified"}, {OutcomeClassifiedDNF, 2, ptrace.StatusCodeError, "classified did not finish"}, {OutcomeUnclassifiedDNF, 0, ptrace.StatusCodeError, "did not finish"}, {OutcomeUnresolved, 0, ptrace.StatusCodeUnset, ""},
	} {
		row := AcceptedRow{Row{2, "TWO", tc.p, tc.o}, 4}
		end := origin.Add(time.Hour)
		r := ResolveRootOutcome(race, 2, &row, false, &origin, end)
		want := map[string]any{"f1.result.outcome": string(tc.o), "f1.result.source": "openf1", "f1.result.snapshot.revision": int64(4)}
		if tc.p != 0 {
			want["f1.result.position"] = tc.p
		}
		if tc.o == OutcomeDNS {
			end = origin
		}
		if !reflect.DeepEqual(r.Attributes.AsRaw(), want) || r.End != end || r.Description != tc.desc || r.Status != tc.status || r.DNSPoint != (tc.o == OutcomeDNS) || r.Suppress {
			t.Fatal(r)
		}
	}
	r := ResolveRootOutcome(race, 2, nil, false, nil, origin)
	if !reflect.DeepEqual(r.Attributes.AsRaw(), map[string]any{"f1.result.outcome": "unresolved"}) || r.Status != ptrace.StatusCodeUnset {
		t.Fatal(r)
	}
}

func TestParticipationEvidence(t *testing.T) {
	for _, set := range []func(*ParticipationEvidence){
		func(p *ParticipationEvidence) { p.LapCountTransition = true }, func(p *ParticipationEvidence) { p.LapCreated = true }, func(p *ParticipationEvidence) { p.SectorCreated = true }, func(p *ParticipationEvidence) { p.PitEntry = true }, func(p *ParticipationEvidence) { p.PitExit = true },
	} {
		for _, live := range []bool{true, false} {
			for _, started := range []bool{true, false} {
				p := ParticipationEvidence{Live: live, AfterCompetitiveStart: started}
				set(&p)
				if p.Participated() != (live && started) {
					t.Fatal(p)
				}
			}
		}
	}
	// A static post-start Position update produced no competitive effect.
	if (ParticipationEvidence{Live: true, AfterCompetitiveStart: true}).Participated() {
		t.Fatal("position inferred participation")
	}
}
func TestRootGateOrdering(t *testing.T) {
	h := frozen(t)
	win := result(1, 1, 9, "")
	h.reply(h.send(results), success(array(win, result(2, -1, 1, "dnf"))))
	final := origin.Add(2 * time.Minute)
	received := origin.Add(15 * time.Minute)
	g := RootGate{Generation: 4}
	g.Closure = g.Finalised(final, received, time.Minute)
	g.Closure = g.Finalised(received, received.Add(time.Hour), time.Minute)
	if g.FinalisedUTC != final || g.GraceDeadline != received.Add(time.Minute) {
		t.Fatal(g)
	}
	roots := []DriverRoot{{Number: 1, Exists: true, Start: origin, GreatestOwned: final.Add(time.Second)}, {Number: 2, Exists: true, Start: origin, GreatestOwned: final}}
	for _, boundary := range []RootBoundary{Ends, GraceExpiry, Replacement, Shutdown} {
		observed := g.GraceDeadline
		ends := final.Add(time.Minute)
		_, out, _ := g.Close(4, boundary, ends, observed, race, h.s.Snapshot, &origin, roots)
		want := roots[0].GreatestOwned
		if boundary == Ends {
			want = ends
		}
		if len(out) != 2 || out[0].End != want || out[1].Description != "did not finish" || out[1].Start != origin {
			t.Fatal(boundary, out)
		}
	}
	for _, tc := range []struct {
		gen uint64
		now time.Time
	}{{3, g.GraceDeadline}, {4, g.GraceDeadline.Add(-1)}} {
		if next, out, _ := g.Close(tc.gen, GraceExpiry, time.Time{}, tc.now, race, h.s.Snapshot, &origin, roots); next.closed || len(out) != 0 {
			t.Fatal(next, out)
		}
	}
	// Static coordinates do not set Participated. Only the caller's accepted
	// post-start lap, sector, or pit evidence can change that bit.
	h.reply(h.send(results), success(array(win, result(2, -1, 0, "dns"))))
	for _, exists := range []bool{true, false} {
		roots[1].Exists = exists
		_, out, d := g.Close(4, Ends, final, received, race, h.s.Snapshot, &origin, roots)
		if d != "" || len(out) != 2 || !out[1].DNSPoint || out[1].Start != origin || out[1].End != origin {
			t.Fatal(out, d)
		}
		_, out, _ = g.Close(4, Shutdown, time.Time{}, received, race, h.s.Snapshot, nil, roots)
		if len(out) != 1 {
			t.Fatal("no observed start", out)
		}
	}
	roots[1].Exists = true
	roots[1].Participated = true
	closed, out, d := g.Close(4, GraceExpiry, time.Time{}, g.GraceDeadline, race, h.s.Snapshot, &origin, roots)
	want := map[string]any{"f1.result.outcome": "unresolved", "f1.time.quality": "estimated"}
	if d != DiagParticipationConflict || len(out) != 2 || out[1].DNSPoint || out[1].End != final || out[1].Status != ptrace.StatusCodeUnset || !reflect.DeepEqual(out[1].Attributes.AsRaw(), want) {
		t.Fatal(out, d)
	}
	e := h.reply(h.send(results), success(array(win, result(2, 2, 9, ""))))
	if e.Observation.Revision != 3 || len(e.Observation.Changes) != 1 {
		t.Fatal(e)
	}
	if _, again, _ := closed.Close(4, Shutdown, time.Time{}, received, race, h.s.Snapshot, &origin, roots); len(again) != 0 || !reflect.DeepEqual(out[1].Attributes.AsRaw(), want) {
		t.Fatal("reopened roots")
	}
	roots[1].Exists = false
	if _, out, _ := g.Close(4, Replacement, time.Time{}, received, race, h.s.Snapshot, &origin, roots); len(out) != 1 {
		t.Fatal("created ordinary root")
	}
	if end := g.FallbackEnd(origin.Add(-time.Minute), origin, time.Time{}, false); end != origin {
		t.Fatal(end)
	}
}
