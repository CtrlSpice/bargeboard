package f1staticreceiver

import (
	"maps"
	"slices"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/circuitmap"
	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type attrs map[string]any

func (a attrs) put(m pcommon.Map) {
	for _, k := range slices.Sorted(maps.Keys(a)) {
		switch v := a[k].(type) {
		case string:
			m.PutStr(k, v)
		case int64:
			m.PutInt(k, v)
		case float64:
			m.PutDouble(k, v)
		case bool:
			m.PutBool(k, v)
		}
	}
}

type fact struct {
	name string
	at   time.Time
	a    attrs
}
type span struct {
	fact
	end        time.Time
	id, parent pcommon.SpanID
	trace      pcommon.TraceID
	events     []fact
}
type point struct {
	fact
	unit      string
	value     any
	histogram *population
}
type effects struct {
	issues map[string]bool
	spans  []*span
	points []point
	logs   []logFact
}

func (s *session) issue(reason string) {
	if s.e.issues == nil {
		s.e.issues = map[string]bool{}
	}
	s.e.issues[reason] = true
}

type logFact struct {
	fact
	owner *span
}

func (s *session) log(f fact, d *driver) {
	var owner *span
	if d != nil && d.exportedRoot != nil && !f.at.Before(d.exportedRoot.at) && !f.at.After(d.exportedRoot.end) {
		owner = d.exportedRoot
	}
	s.e.logs = append(s.e.logs, logFact{f, owner})
}

func projectTraces(p FrozenReplayPlan, spans []*span, v string) ptrace.Traces {
	t := ptrace.NewTraces()
	r := t.ResourceSpans().AppendEmpty()
	f1id.InitResource(r.Resource(), v)
	s := r.ScopeSpans().AppendEmpty()
	f1id.InitScope(s.Scope())
	for _, src := range spans {
		x := s.Spans().AppendEmpty()
		x.SetName(src.name)
		x.SetKind(ptrace.SpanKindInternal)
		x.SetTraceID(src.trace)
		x.SetSpanID(src.id)
		x.SetParentSpanID(src.parent)
		x.SetStartTimestamp(pcommon.NewTimestampFromTime(src.at))
		x.SetEndTimestamp(pcommon.NewTimestampFromTime(src.end))
		f1id.PutCommonSessionAttributes(x.Attributes(), p.Identity, "livetiming")
		src.a.put(x.Attributes())
		for _, f := range src.events {
			e := x.Events().AppendEmpty()
			e.SetName(f.name)
			e.SetTimestamp(pcommon.NewTimestampFromTime(f.at))
			f.a.put(e.Attributes())
			p.Circuit.EnrichEvent(f.name, e.Attributes(), circuitmap.DefaultLookupOptions())
		}
	}
	return t
}
func projectLogs(p FrozenReplayPlan, logs []logFact, v string, now time.Time) plog.Logs {
	l := plog.NewLogs()
	r := l.ResourceLogs().AppendEmpty()
	f1id.InitResource(r.Resource(), v)
	s := r.ScopeLogs().AppendEmpty()
	f1id.InitScope(s.Scope())
	for _, f := range logs {
		x := s.LogRecords().AppendEmpty()
		x.Body().SetStr(f.name)
		if f.owner != nil {
			x.SetTraceID(f.owner.trace)
			x.SetSpanID(f.owner.id)
		}
		x.SetSeverityNumber(plog.SeverityNumberInfo)
		x.SetSeverityText("INFO")
		x.SetTimestamp(pcommon.NewTimestampFromTime(f.at))
		x.SetObservedTimestamp(pcommon.NewTimestampFromTime(now))
		f1id.PutCommonSessionAttributes(x.Attributes(), p.Identity, "livetiming")
		f.a.put(x.Attributes())
	}
	return l
}
func projectMetrics(p FrozenReplayPlan, points []point, v string) pmetric.Metrics {
	m := pmetric.NewMetrics()
	r := m.ResourceMetrics().AppendEmpty()
	f1id.InitResource(r.Resource(), v)
	s := r.ScopeMetrics().AppendEmpty()
	f1id.InitScope(s.Scope())
	instruments := map[string]pmetric.Metric{}
	for _, pt := range points {
		x, ok := instruments[pt.name]
		if !ok {
			x = s.Metrics().AppendEmpty()
			instruments[pt.name] = x
			x.SetName(pt.name)
			x.SetUnit(pt.unit)
			if v, ok := pt.value.(sum); ok {
				data := x.SetEmptySum()
				data.SetIsMonotonic(true)
				data.SetAggregationTemporality(v.temporality)
			} else if pt.histogram == nil {
				x.SetEmptyGauge()
			} else {
				x.SetEmptyHistogram().SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
			}
		}
		var a pcommon.Map
		if h := pt.histogram; h != nil {
			d := x.Histogram().DataPoints().AppendEmpty()
			a = d.Attributes()
			d.SetStartTimestamp(pcommon.NewTimestampFromTime(h.start))
			d.SetTimestamp(pcommon.NewTimestampFromTime(pt.at))
			d.SetCount(h.count)
			d.SetSum(h.sum)
			d.SetMin(h.min)
			d.SetMax(h.max)
			d.ExplicitBounds().FromRaw(h.bounds)
			d.BucketCounts().FromRaw(h.buckets)
		} else if v, ok := pt.value.(sum); ok {
			d := x.Sum().DataPoints().AppendEmpty()
			a = d.Attributes()
			d.SetStartTimestamp(pcommon.NewTimestampFromTime(v.start))
			d.SetTimestamp(pcommon.NewTimestampFromTime(pt.at))
			d.SetIntValue(v.value)
		} else {
			d := x.Gauge().DataPoints().AppendEmpty()
			a = d.Attributes()
			d.SetTimestamp(pcommon.NewTimestampFromTime(pt.at))
			switch v := pt.value.(type) {
			case int64:
				d.SetIntValue(v)
			case float64:
				d.SetDoubleValue(v)
			}
		}
		f1id.PutCommonSessionAttributes(a, p.Identity, "livetiming")
		pt.a.put(a)
	}
	return m
}
