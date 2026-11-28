package openf1receiver

import (
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type Change struct{ Current, Previous Row }
type Snapshot struct {
	revision int64
	rows     []Row
	overflow bool
}

func (s Snapshot) Revision() int64 { return s.revision }

type AcceptedRow struct {
	Row
	Revision int64
}

func (s Snapshot) Driver(number int64) *AcceptedRow {
	for _, r := range s.rows {
		if r.DriverNumber == number {
			return &AcceptedRow{r, s.revision}
		}
	}
	return nil
}
func (s Snapshot) accept(rows []Row) (Snapshot, []Change, Diagnostic) {
	if s.overflow {
		return s, nil, ""
	}
	var changes []Change
	for _, r := range rows {
		var prev Row
		if prior := s.Driver(r.DriverNumber); prior != nil {
			prev = prior.Row
		}
		if r.Position != prev.Position || r.Outcome != prev.Outcome {
			changes = append(changes, Change{r, prev})
		}
	}
	if len(changes) == 0 {
		return s, nil, ""
	}
	if s.revision == 64 {
		s.overflow = true
		return s, nil, DiagRevisionOverflow
	}
	return Snapshot{revision: s.revision + 1, rows: rows}, changes, ""
}

type Observation struct {
	Identity f1id.SessionIdentity
	Revision int64
	At       time.Time
	Changes  []Change
}

func (o Observation) Logs(version string, hasRoot func(int64) bool) plog.Logs {
	logs := plog.NewLogs()
	if len(o.Changes) == 0 {
		return logs
	}
	res := logs.ResourceLogs().AppendEmpty()
	f1id.InitResource(res.Resource(), version)
	scope := res.ScopeLogs().AppendEmpty()
	f1id.InitScope(scope.Scope())
	for _, c := range o.Changes {
		r := scope.LogRecords().AppendEmpty()
		ts := pcommon.NewTimestampFromTime(o.At)
		r.SetTimestamp(ts)
		r.SetObservedTimestamp(ts)
		r.SetSeverityNumber(plog.SeverityNumberInfo)
		r.SetSeverityText("INFO")
		a := r.Attributes()
		f1id.PutCommonSessionAttributes(a, o.Identity, "openf1")
		a.PutInt("f1.driver.number", c.Current.DriverNumber)
		a.PutStr("f1.driver.acronym", c.Current.Acronym)
		a.PutStr("f1.time.quality", "collector_observation")
		putRow(a, c.Current, o.Revision)
		if o.Revision == 1 {
			r.Body().SetStr("f1.driver.result.observed")
		} else {
			r.Body().SetStr("f1.driver.result.revised")
			a.PutStr("f1.result.previous_outcome", string(c.Previous.Outcome))
			if c.Previous.Position != 0 {
				a.PutInt("f1.result.previous_position", c.Previous.Position)
			}
		}
		if hasRoot != nil && hasRoot(c.Current.DriverNumber) {
			t, _ := f1id.DriverTraceID(o.Identity, c.Current.DriverNumber)
			s, _ := f1id.DriverSessionRootSpanID(o.Identity, c.Current.DriverNumber)
			r.SetTraceID(t)
			r.SetSpanID(s)
		}
	}
	return logs
}
func putRow(a pcommon.Map, r Row, revision int64) {
	a.PutStr("f1.result.outcome", string(r.Outcome))
	if revision > 0 {
		a.PutInt("f1.result.snapshot.revision", revision)
	}
	if r.Position != 0 {
		a.PutInt("f1.result.position", r.Position)
	}
}

// RootOutcome enriches an existing tree unless DNS requests a childless point
// root. The owner takes this value once, at its ordinary root export gate.
type RootOutcome struct {
	Attributes         pcommon.Map
	Status             ptrace.StatusCode
	Description        string
	End                time.Time
	DNSPoint, Suppress bool
	Diagnostic         Diagnostic
}

func ResolveRootOutcome(e f1id.SessionIdentity, driver int64, row *AcceptedRow, participated bool, start *time.Time, fallbackEnd time.Time) RootOutcome {
	r := RootOutcome{Attributes: pcommon.NewMap(), End: fallbackEnd}
	if !raceLike(e) || driver <= 0 {
		return r
	}
	current := Row{Outcome: OutcomeUnresolved}
	var rev int64
	if row != nil && row.DriverNumber == driver {
		if row.Outcome == OutcomeDNS && participated {
			r.Diagnostic = DiagParticipationConflict
		} else {
			current = row.Row
			rev = row.Revision
			r.Attributes.PutStr("f1.result.source", "openf1")
		}
	}
	putRow(r.Attributes, current, rev)
	switch current.Outcome {
	case OutcomeFinished:
		r.Status = ptrace.StatusCodeOk
	case OutcomeDNS:
		r.Description = "did not start"
		if start == nil {
			r.Suppress = true
		} else {
			r.DNSPoint = true
			r.End = *start
		}
	case OutcomeClassifiedDNF:
		r.Description = "classified did not finish"
	case OutcomeUnclassifiedDNF:
		r.Description = "did not finish"
	case OutcomeDSQ:
		r.Description = "disqualified"
	}
	if r.Description != "" {
		r.Status = ptrace.StatusCodeError
	}
	return r
}

// Closure retains both the source end candidate and the Collector timer
// deadline. Zero means that Finalised has not been observed.
type Closure struct{ FinalisedUTC, GraceDeadline time.Time }

func (c Closure) Finalised(source, observed time.Time, grace time.Duration) Closure {
	if c.GraceDeadline.IsZero() {
		return Closure{source, observed.Add(grace)}
	}
	return c
}
func (c Closure) FallbackEnd(ends, rootStart, greatestOwned time.Time, graceExpired bool) time.Time {
	if ends.IsZero() && graceExpired {
		ends = c.FinalisedUTC
	}
	return later(later(rootStart, greatestOwned), ends)
}
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
