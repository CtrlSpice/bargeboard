package f1engine

import (
	"reflect"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func checkProjectionHeader(t *testing.T, r pcommon.Resource, s pcommon.InstrumentationScope, resourceURL, scopeURL string) {
	t.Helper()
	if !reflect.DeepEqual(r.Attributes().AsRaw(), map[string]any{"service.namespace": "github.com/CtrlSpice", "service.name": "bargeboard", "service.version": "test"}) || s.Name() != "github.com/CtrlSpice/bargeboard/f1" || s.Version() != "1.0.0" || s.Attributes().Len() != 0 || resourceURL != "" || scopeURL != "" {
		t.Fatal(r.Attributes().AsRaw(), s.Name(), s.Version(), resourceURL, scopeURL)
	}
}

func TestProjectionWaterfall(t *testing.T) {
	var e Effects
	if projectTraces("", e).ResourceSpans().Len() != 0 || ProjectMetrics("", e).ResourceMetrics().Len() != 0 || ProjectLogs("", e).ResourceLogs().Len() != 0 {
		t.Fatal("empty resource")
	}
	id := SessionInfoIdentity{2026, 42, canonicalSessionTypeQualifying, canonicalSessionNameQualifying}
	for _, k := range []spanKindType{spanDriverSession, spanQualifyingPhase, spanStint, spanLap, spanSector} {
		s := spanEffect{identity: id, kind: k, driverNumber: 7, driverAcronym: "XYZ", startTime: fieldTime(10), endTime: fieldTime(20), timeQuality: "observed", status: spanStatusEffect{spanStatusError, "retired"}, attrs: []keyValue{boolAttr("test", true)}, events: []spanEventEffect{eventAt("example", fieldTime(12), floatAttr("value", 2.5))}}
		if k != spanDriverSession {
			s.phase = "q2"
		}
		s.stintOrdinal, s.lapNumber, s.sectorNumber = 2, 3, 1
		e.spans = append(e.spans, s)
	}
	r := projectTraces("test", e).ResourceSpans().At(0)
	sc := r.ScopeSpans().At(0)
	checkProjectionHeader(t, r.Resource(), sc.Scope(), r.SchemaUrl(), sc.SchemaUrl())
	ids := []pcommon.SpanID{DeterministicRootSpanID(id, 7), deterministicPhaseSpanID(id, 7, "q2"), DeterministicStintSpanID(id, 7, "q2", 2), DeterministicLapSpanID(id, 7, "q2", 3), DeterministicSectorSpanID(id, 7, "q2", 3, 1)}
	var parent pcommon.SpanID
	a := map[string]any{"f1.season.year": id.Season, "f1.meeting.key": id.meetingKey, "f1.session.type": string(id.sessionType), "f1.session.name": string(id.sessionName), "f1.data.source": "livetiming", "f1.driver.number": int64(7), "f1.driver.acronym": "XYZ", "f1.time.quality": "observed", "test": true}
	for i, name := range []string{"driver.session XYZ", "qualifying.phase Q2", "stint 2", "lap 3", "sector 1"} {
		s := sc.Spans().At(i)
		switch i {
		case 1:
			a["f1.session.phase"] = "q2"
		case 2:
			a["f1.stint.number"] = int64(2)
		case 3:
			a["f1.lap.number"] = int64(3)
		case 4:
			a["f1.sector.number"] = int64(1)
		}
		if s.TraceID() != DeterministicTraceID(id, 7) || s.SpanID() != ids[i] || s.ParentSpanID() != parent || s.Name() != name || s.StartTimestamp() != 10e9 || s.EndTimestamp() != 20e9 || s.Status().Code() != ptrace.StatusCodeError || s.Status().Message() != "retired" || s.Kind() != ptrace.SpanKindInternal || !reflect.DeepEqual(a, s.Attributes().AsRaw()) {
			t.Fatalf("%s: %v", s.Name(), s.Attributes().AsRaw())
		}
		ev := s.Events().At(0)
		if ev.Name() != "example" || ev.Timestamp() != 12e9 || !reflect.DeepEqual(ev.Attributes().AsRaw(), map[string]any{"f1.time.quality": "publication_time", "value": 2.5}) {
			t.Fatal(ev.Attributes().AsRaw())
		}
		parent = s.SpanID()
	}
	// An existing, unrelated claim on the phase ID suppresses its sector.
	seen := map[pcommon.SpanID]spanEntity{ids[1]: {driver: 8}}
	if _, _, _, ok := spanPath(e.spans[4], seen); ok {
		t.Fatal("accepted descendant of colliding entity")
	}
	if _, _, _, ok := spanPath(spanEffect{identity: id, kind: spanQualifyingPhase, driverNumber: 7, driverAcronym: "XYZ"}, map[pcommon.SpanID]spanEntity{}); ok {
		t.Fatal("accepted unresolved qualifying phase")
	}
}

func TestProjectionMetricKinds(t *testing.T) {
	id := SessionInfoIdentity{2026, 42, canonicalSessionTypeQualifying, canonicalSessionNameQualifying}
	var e Effects
	for _, k := range []metricKind{metricIntGauge, metricDoubleGauge, metricIntSumCumulative, metricIntSumDelta, metricExplicitHistogramDelta, metricExpHistogramDelta, metricIntGauge} {
		e.metrics = append(e.metrics, metricEffect{kind: k, name: "measurement", description: "description", unit: "s", monotonic: true, identity: id, attrs: []keyValue{strAttr("extra", "ok")}, startTime: fieldTime(1), timestamp: fieldTime(2), intVal: 8, floatVal: 2.5, bounds: []float64{2}, bucketCounts: []uint64{1, 2}, count: 3, sum: 6, hasMinMax: true, min: 0, max: 4, expZeroCount: 1, expPositiveOffset: -2, expPositiveBuckets: []uint64{0, 2}, exemplars: []exemplarEffect{{timestamp: fieldTime(2), isInt: true, intVal: 4, spanID: pcommon.SpanID{9}, traceID: pcommon.TraceID{7}, filteredAttrs: []keyValue{strAttr("key", "value")}}}})
	}
	r := ProjectMetrics("test", e).ResourceMetrics().At(0)
	sc := r.ScopeMetrics().At(0)
	checkProjectionHeader(t, r.Resource(), sc.Scope(), r.SchemaUrl(), sc.SchemaUrl())
	ms := sc.Metrics()
	if ms.Len() != 6 || ms.At(0).Gauge().DataPoints().Len() != 2 {
		t.Fatal("incorrect grouping")
	}
	for i := 0; i < ms.Len(); i++ {
		m := ms.At(i)
		if m.Name() != "measurement" || m.Unit() != "s" || m.Description() != "description" {
			t.Fatal(m.Name())
		}
		var p racingPoint
		switch i {
		case 0, 1:
			n := m.Gauge().DataPoints().At(0)
			p = n
			if i == 0 && n.IntValue() != 8 || i == 1 && n.DoubleValue() != 2.5 {
				t.Fatal(n.ValueType())
			}
		case 2, 3:
			s := m.Sum()
			n := s.DataPoints().At(0)
			p = n
			if n.IntValue() != 8 || !s.IsMonotonic() || i == 2 && s.AggregationTemporality() != pmetric.AggregationTemporalityCumulative || i == 3 && s.AggregationTemporality() != pmetric.AggregationTemporalityDelta {
				t.Fatal(s.AggregationTemporality())
			}
		case 4:
			h := m.Histogram()
			n := h.DataPoints().At(0)
			p = n
			if h.AggregationTemporality() != pmetric.AggregationTemporalityDelta || n.Count() != 3 || n.Sum() != 6 || !n.HasMin() || n.Min() != 0 || n.Max() != 4 || !reflect.DeepEqual(n.ExplicitBounds().AsRaw(), []float64{2}) || !reflect.DeepEqual(n.BucketCounts().AsRaw(), []uint64{1, 2}) {
				t.Fatal("explicit histogram")
			}
		case 5:
			h := m.ExponentialHistogram()
			n := h.DataPoints().At(0)
			p = n
			if h.AggregationTemporality() != pmetric.AggregationTemporalityDelta || n.Count() != 3 || n.Sum() != 6 || n.Min() != 0 || n.Max() != 4 || n.Scale() != 4 || n.ZeroThreshold() != 0 || n.ZeroCount() != 1 || n.Positive().Offset() != -2 || !reflect.DeepEqual(n.Positive().BucketCounts().AsRaw(), []uint64{0, 2}) {
				t.Fatal("exponential histogram")
			}
		}
		if !reflect.DeepEqual(p.Attributes().AsRaw(), map[string]any{"f1.season.year": id.Season, "f1.meeting.key": id.meetingKey, "f1.session.type": string(id.sessionType), "f1.session.name": string(id.sessionName), "f1.data.source": "livetiming", "extra": "ok"}) {
			t.Fatal(p.Attributes().AsRaw())
		}
		ex := p.Exemplars().At(0)
		if ex.IntValue() != 4 || ex.Timestamp() != 2e9 || ex.SpanID() != (pcommon.SpanID{9}) || ex.TraceID() != (pcommon.TraceID{7}) || !reflect.DeepEqual(ex.FilteredAttributes().AsRaw(), map[string]any{"key": "value"}) {
			t.Fatal("exemplar")
		}
	}
}

func TestProjectionLogs(t *testing.T) {
	id := SessionInfoIdentity{2026, 42, canonicalSessionTypeQualifying, canonicalSessionNameQualifying}
	l := logEffect{identity: id, body: "yellow", timeQuality: "observed", timestamp: fieldTime(1), observedTimestamp: fieldTime(5), traceID: pcommon.TraceID{1}, spanID: pcommon.SpanID{2}, attrs: []keyValue{intAttr("index", 3)}}
	e := Effects{logs: []logEffect{l, l}}
	e.logs[1].hasTrace = true
	e.logs[1].dataSource = "archive"
	r := ProjectLogs("test", e).ResourceLogs().At(0)
	sc := r.ScopeLogs().At(0)
	checkProjectionHeader(t, r.Resource(), sc.Scope(), r.SchemaUrl(), sc.SchemaUrl())
	for i := 0; i < 2; i++ {
		p := sc.LogRecords().At(i)
		if source, _ := p.Attributes().Get("f1.data.source"); source.Str() != []string{"livetiming", "archive"}[i] {
			t.Fatal(source)
		}
		if p.Body().Str() != "yellow" || p.Timestamp() != 1e9 || p.ObservedTimestamp() != 5e9 || p.SeverityNumber() != plog.SeverityNumberInfo || p.SeverityText() != "INFO" || p.Attributes().Len() != 7 || i == 0 && (!p.SpanID().IsEmpty() || !p.TraceID().IsEmpty()) || i == 1 && (p.TraceID() != l.traceID || p.SpanID() != l.spanID) {
			t.Fatal(p.Body().Str(), p.Attributes().AsRaw())
		}
	}
}

func TestMetricProjectionPreservesDeclaredSourceAndScale(t *testing.T) {
	e := Effects{metrics: []metricEffect{{kind: metricExpHistogramDelta, name: "test", expScale: -2, dataSource: "archive"}}}
	p := ProjectMetrics("test", e).ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().At(0).ExponentialHistogram().DataPoints().At(0)
	if v, ok := p.Attributes().Get("f1.data.source"); !ok || v.Str() != "archive" || p.Scale() != -2 {
		t.Fatal(v, p.Scale())
	}
}
