package f1livetimingreceiver

import (
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func resource(r pcommon.Resource, s pcommon.InstrumentationScope, version string) {
	r.Attributes().PutStr("service.namespace", "github.com/CtrlSpice")
	r.Attributes().PutStr("service.name", "bargeboard")
	if version != "" {
		r.Attributes().PutStr("service.version", version)
	}
	s.SetName("github.com/CtrlSpice/bargeboard/f1")
	s.SetVersion("1.0.0")
}

func sessionAttributes(a pcommon.Map, id sessionInfoIdentity, dataSource string) {
	a.PutInt("f1.season.year", id.season)
	a.PutInt("f1.meeting.key", id.meetingKey)
	a.PutStr("f1.session.type", string(id.sessionType))
	a.PutStr("f1.session.name", string(id.sessionName))
	if dataSource == "" {
		dataSource = "livetiming"
	}
	a.PutStr("f1.data.source", dataSource)
}

func attributes(a pcommon.Map, attrs []keyValue) {
	for _, kv := range attrs {
		v := kv.value
		switch v.kind {
		case attrInt:
			a.PutInt(kv.key, v.intVal)
		case attrFloat:
			a.PutDouble(kv.key, v.floatVal)
		case attrString:
			a.PutStr(kv.key, v.strVal)
		case attrBool:
			a.PutBool(kv.key, v.boolVal)
		}
	}
}

func timestamp(t time.Time) pcommon.Timestamp {
	if t.IsZero() {
		return 0
	}
	return pcommon.NewTimestampFromTime(t)
}

// Register ancestors even when a child finishes before its parent.
type spanEntity struct {
	session        sessionInfoIdentity
	driver         int64
	kind           spanKindType
	phase          string
	number, sector int64
}

func spanPath(s spanEffect, seen map[pcommon.SpanID]spanEntity) (id, parent pcommon.SpanID, label string, ok bool) {
	e, n := s.identity, s.driverNumber
	entity := spanEntity{session: e, driver: n}
	add := func(next pcommon.SpanID, kind spanKindType, labelValue string) bool {
		entity.kind = kind
		if previous, exists := seen[next]; exists && previous != entity {
			return false
		}
		seen[next] = entity
		parent, id, label = id, next, labelValue
		return true
	}
	ok = add(deterministicRootSpanID(e, n), spanDriverSession, s.driverAcronym)
	if !ok || s.kind == spanDriverSession {
		return
	}
	entity.phase = phaseOrNone(s.phase)
	if s.phase != "" {
		ok = add(deterministicPhaseSpanID(e, n, s.phase), spanQualifyingPhase, strings.ToUpper(s.phase))
	}
	if !ok || s.kind == spanQualifyingPhase {
		return
	}
	entity.number = s.stintOrdinal
	ok = add(deterministicStintSpanID(e, n, s.phase, s.stintOrdinal), spanStint, decimal(s.stintOrdinal))
	if !ok || s.kind == spanStint {
		return
	}
	entity.number = s.lapNumber
	ok = add(deterministicLapSpanID(e, n, s.phase, s.lapNumber), spanLap, decimal(s.lapNumber))
	if !ok || s.kind == spanLap {
		return
	}
	entity.sector = s.sectorNumber
	ok = add(deterministicSectorSpanID(e, n, s.phase, s.lapNumber, s.sectorNumber), spanSector, decimal(s.sectorNumber))
	return
}

func projectTraces(buildVersion string, effects liveTimingEffects) ptrace.Traces {
	out := ptrace.NewTraces()
	if len(effects.spans) == 0 {
		return out
	}
	r := out.ResourceSpans().AppendEmpty()
	scope := r.ScopeSpans().AppendEmpty()
	resource(r.Resource(), scope.Scope(), buildVersion)
	seen := make(map[pcommon.SpanID]spanEntity)
	for _, s := range effects.spans {
		id, parent, label, ok := spanPath(s, seen)
		if !ok {
			continue
		}
		p := scope.Spans().AppendEmpty()
		p.SetTraceID(deterministicTraceID(s.identity, s.driverNumber))
		p.SetSpanID(id)
		p.SetParentSpanID(parent)
		p.SetName(string(s.kind) + " " + label)
		p.SetKind(ptrace.SpanKindInternal)
		p.SetStartTimestamp(timestamp(s.startTime))
		p.SetEndTimestamp(timestamp(s.endTime))
		p.Status().SetCode(ptrace.StatusCode(s.status.code))
		p.Status().SetMessage(s.status.message)
		a := p.Attributes()
		sessionAttributes(a, s.identity, "livetiming")
		a.PutInt("f1.driver.number", s.driverNumber)
		a.PutStr("f1.driver.acronym", s.driverAcronym)
		a.PutStr("f1.time.quality", s.timeQuality)
		if s.phase != "" {
			a.PutStr("f1.session.phase", s.phase)
		}
		if s.kind == spanStint || s.kind == spanLap || s.kind == spanSector {
			a.PutInt("f1.stint.number", s.stintOrdinal)
		}
		if s.kind == spanLap || s.kind == spanSector {
			a.PutInt("f1.lap.number", s.lapNumber)
		}
		if s.kind == spanSector {
			a.PutInt("f1.sector.number", s.sectorNumber)
		}
		attributes(a, s.attrs)
		for _, ev := range s.events {
			p := p.Events().AppendEmpty()
			p.SetName(ev.name)
			p.SetTimestamp(timestamp(ev.timestamp))
			p.Attributes().PutStr("f1.time.quality", ev.timeQuality)
			attributes(p.Attributes(), ev.attrs)
		}
	}
	return out
}

func projectLogs(buildVersion string, effects liveTimingEffects) plog.Logs {
	out := plog.NewLogs()
	if len(effects.logs) == 0 {
		return out
	}
	r := out.ResourceLogs().AppendEmpty()
	scope := r.ScopeLogs().AppendEmpty()
	resource(r.Resource(), scope.Scope(), buildVersion)
	for _, l := range effects.logs {
		p := scope.LogRecords().AppendEmpty()
		p.SetTimestamp(timestamp(l.timestamp))
		p.SetObservedTimestamp(timestamp(l.observedTimestamp))
		p.SetSeverityNumber(plog.SeverityNumberInfo)
		p.SetSeverityText("INFO")
		p.Body().SetStr(l.body)
		if l.hasTrace {
			p.SetTraceID(l.traceID)
			p.SetSpanID(l.spanID)
		}
		sessionAttributes(p.Attributes(), l.identity, l.dataSource)
		attributes(p.Attributes(), l.attrs)
		p.Attributes().PutStr("f1.time.quality", l.timeQuality)
	}
	return out
}

type metricKey struct {
	name, unit string
	kind       metricKind
	monotonic  bool
}

func initMetric(p pmetric.Metric, m metricEffect) {
	p.SetName(m.name)
	p.SetDescription(m.description)
	p.SetUnit(m.unit)
	switch m.kind {
	case metricIntGauge, metricDoubleGauge:
		p.SetEmptyGauge()
	case metricIntSumCumulative, metricIntSumDelta:
		s := p.SetEmptySum()
		s.SetIsMonotonic(m.monotonic)
		s.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
		if m.kind == metricIntSumCumulative {
			s.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
		}
	case metricExplicitHistogramDelta:
		p.SetEmptyHistogram().SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	case metricExpHistogramDelta:
		p.SetEmptyExponentialHistogram().SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	}
}

type racingPoint interface {
	SetStartTimestamp(pcommon.Timestamp)
	SetTimestamp(pcommon.Timestamp)
	Attributes() pcommon.Map
	Exemplars() pmetric.ExemplarSlice
}

type histogramPoint interface {
	racingPoint
	SetCount(uint64)
	SetSum(float64)
	SetMin(float64)
	SetMax(float64)
}

func histogram(p histogramPoint, m metricEffect) {
	p.SetCount(m.count)
	p.SetSum(m.sum)
	if m.hasMinMax {
		p.SetMin(m.min)
		p.SetMax(m.max)
	}
}

func projectMetrics(buildVersion string, effects liveTimingEffects) pmetric.Metrics {
	out := pmetric.NewMetrics()
	if len(effects.metrics) == 0 {
		return out
	}
	r := out.ResourceMetrics().AppendEmpty()
	scope := r.ScopeMetrics().AppendEmpty()
	resource(r.Resource(), scope.Scope(), buildVersion)
	groups := make(map[metricKey]pmetric.Metric)
	for _, m := range effects.metrics {
		key := metricKey{m.name, m.unit, m.kind, m.monotonic}
		p, ok := groups[key]
		if !ok {
			p = scope.Metrics().AppendEmpty()
			initMetric(p, m)
			groups[key] = p
		}
		var point racingPoint
		switch m.kind {
		case metricExplicitHistogramDelta:
			h := p.Histogram().DataPoints().AppendEmpty()
			histogram(h, m)
			h.ExplicitBounds().FromRaw(m.bounds)
			h.BucketCounts().FromRaw(m.bucketCounts)
			point = h
		case metricExpHistogramDelta:
			h := p.ExponentialHistogram().DataPoints().AppendEmpty()
			histogram(h, m)
			scale := m.expScale
			if scale == 0 {
				scale = 4
			}
			h.SetScale(scale)
			h.SetZeroCount(m.expZeroCount)
			h.Positive().SetOffset(m.expPositiveOffset)
			h.Positive().BucketCounts().FromRaw(m.expPositiveBuckets)
			point = h
		default:
			var n pmetric.NumberDataPoint
			if m.kind == metricIntGauge || m.kind == metricDoubleGauge {
				n = p.Gauge().DataPoints().AppendEmpty()
			} else {
				n = p.Sum().DataPoints().AppendEmpty()
			}
			if m.kind == metricDoubleGauge {
				n.SetDoubleValue(m.floatVal)
			} else {
				n.SetIntValue(m.intVal)
			}
			point = n
		}
		point.SetTimestamp(timestamp(m.timestamp))
		point.SetStartTimestamp(timestamp(m.startTime))
		sessionAttributes(point.Attributes(), m.identity, m.dataSource)
		attributes(point.Attributes(), m.attrs)
		point.Attributes().Remove("f1.time.quality")
		for _, v := range m.exemplars {
			ex := point.Exemplars().AppendEmpty()
			ex.SetTimestamp(timestamp(v.timestamp))
			ex.SetSpanID(v.spanID)
			ex.SetTraceID(v.traceID)
			if v.isInt {
				ex.SetIntValue(v.intVal)
			} else {
				ex.SetDoubleValue(v.floatVal)
			}
			attributes(ex.FilteredAttributes(), v.filteredAttrs)
		}
	}
	return out
}
