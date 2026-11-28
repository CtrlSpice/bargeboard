package f1engine

import (
	"math/big"
	"reflect"
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pmetric"
)

func TestDurationExemplarExtremesAndTerminalClip(t *testing.T) {
	id := sessionFixture().Identity
	// Arrival order is deliberately different from measurement order. The terminal
	// boundary excludes the provisional maximum and requires a new selection.
	s := timingState{deltaStart: fieldTime(1)}
	for _, obs := range []struct{ sec, driver, lap, sector, value int64 }{{6, 1, 2, 2, 8}, {4, 7, 1, 1, 2}, {4, 1, 2, 1, 2}, {4, 1, 1, 3, 2}, {4, 1, 1, 1, 2}, {7, 1, 2, 1, 8}, {8, 1, 3, 1, 11}} {
		s.observeValue("f1.driver.sector_time", "s", id, fieldTime(obs.sec), big.NewRat(obs.value, 1), nil, observationOwner{driver: obs.driver, lap: obs.lap, sector: obs.sector, acronym: "ABC", lapResolved: true})
	}
	var e Effects
	s.drainWindows(fieldTime(7), true, &e)
	detached := e.append(e)
	detached.metrics[0].exemplars[0].exact.SetInt64(99)
	if e.metrics[0].exemplars[0].exact.Cmp(big.NewRat(2, 1)) != 0 {
		t.Fatal("detached effect mutated the original exemplar")
	}
	m := ProjectMetrics("test", e).ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().At(0)
	p := m.Histogram().DataPoints().At(0)
	xs := p.Exemplars()
	if p.Count() != 6 || xs.Len() != 2 {
		t.Fatal(p.Count(), xs.Len())
	}
	for i, w := range []struct {
		sec, lap, sector int64
		value            float64
	}{{4, 1, 1, 2}, {6, 2, 2, 8}} {
		x := xs.At(i)
		if !x.Timestamp().AsTime().Equal(fieldTime(w.sec)) || x.ValueType() != pmetric.ExemplarValueTypeDouble || x.DoubleValue() != w.value || x.TraceID() != DeterministicTraceID(id, 1) || x.SpanID() != DeterministicSectorSpanID(id, 1, "", w.lap, w.sector) || !reflect.DeepEqual(x.FilteredAttributes().AsRaw(), map[string]any{"f1.driver.number": int64(1), "f1.driver.acronym": "ABC", "f1.lap.number": w.lap, "f1.sector.number": w.sector}) {
			t.Fatal(i, x.FilteredAttributes().AsRaw(), x.DoubleValue(), x.Timestamp())
		}
	}
}

func TestCarDataExemplarsAtTerminalBoundary(t *testing.T) {
	o := sessionFixture()
	start := fieldTime(1)
	reduce := carReducer(t, o, start)
	s, e := reduce(carDataState{},
		carRow(fieldTime(6), fieldCar(1, `"2":60`)),
		carRow(fieldTime(7), fieldCar(1, `"2":70`)),
		carRow(fieldTime(8), fieldCar(1, `"2":80`)))
	if !e.empty() {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		skew   time.Duration
		values []int64
	}{{-time.Nanosecond, []int64{60}}, {0, []int64{60, 70}}, {time.Nanosecond, []int64{60, 70}}} {
		end := fieldTime(7).Add(tc.skew)
		_, e := flushCarData(s, o.Identity, start, end)
		if len(e.metrics) != 1 {
			t.Fatal(e)
		}
		m := e.metrics[0]
		if m.kind != metricExpHistogramDelta || m.startTime != start || m.timestamp != end || m.count != uint64(len(tc.values)) || len(m.exemplars) != len(tc.values) {
			t.Fatal(m)
		}
		for i, x := range m.exemplars {
			if !x.isInt || x.intVal != tc.values[i] || x.timestamp != fieldTime(tc.values[i]/10) || x.spanID != DeterministicRootSpanID(o.Identity, 1) {
				t.Fatal(x)
			}
		}
	}
}

func TestCarTickCrossingLapBoundary(t *testing.T) {
	s := identityGateTestState()
	s.drivers = fieldRoster()
	stateUpdate(t, &s, Feed, 1, "SessionData", indexedStatus(0, 1, "Started"))
	stateUpdate(t, &s, Feed, 2, "TimingData", `{"Lines":{"1":{"NumberOfLaps":1}}}`)
	stateUpdate(t, &s, Feed, 4, "CarData", `{"Entries":[`+carRow(fieldTime(4).Add(time.Millisecond), fieldCar(1, `"2":128`))+`]}`)
	prior := s
	stateUpdate(t, &s, Feed, 5, "TimingData", `{"Lines":{"1":{"NumberOfLaps":2}}}`)
	e := stateUpdate(t, &s, Feed, 12, "CarData", `{"Entries":[`+carRow(fieldTime(12), "")+`]}`)
	if len(e.metrics) != 1 {
		t.Fatal(e)
	}
	m := e.metrics[0]
	if m.count != 1 || len(m.exemplars) != 1 {
		t.Fatal(m)
	}
	x := m.exemplars[0]
	if !x.isInt || x.intVal != 128 || x.timestamp != fieldTime(5) || x.spanID != DeterministicLapSpanID(s.SessionInfo.Identity, 1, "", 2) || !slices.Contains(x.filteredAttrs, intAttr("f1.lap.number", 2)) {
		t.Fatal(x)
	}
	if prior.timing.cars.ticks[carTickKey{fieldTime(5), 1}].owner.lap != 1 {
		t.Fatal("reassigned an observation in prior state")
	}
}

func TestTimingExemplarsFallBackWithoutLapSpan(t *testing.T) {
	o := sessionFixture()
	s := timingState{deltaStart: fieldTime(1)}
	d := timingDriver{
		driverRegistryEntry: driverRegistryEntry{number: 1, tla: "ABC"},
		pending: lapTiming{
			span:     spanEffect{lapNumber: 2, endTime: fieldTime(8)},
			reported: int64(3 * time.Second),
			sector:   [3]int64{int64(time.Second)},
		},
	}
	var e Effects
	s.finishLap(&d, o, &e)
	s.drainWindows(fieldTime(9), true, &e)
	if len(e.spans) != 0 || len(e.metrics) != 4 {
		t.Fatal(e)
	}
	for _, m := range e.metrics[2:] {
		if m.count != 1 || len(m.exemplars) != 1 || m.exemplars[0].spanID != DeterministicRootSpanID(o.Identity, 1) {
			t.Fatal(m)
		}
	}
	o.Identity.sessionType = canonicalSessionTypeSprintQualifying
	o.Identity.sessionName = canonicalSessionNameSprintQualifying
	x := (observationOwner{driver: 1, lap: 2, sector: 1, lapResolved: true}).exemplar(o.Identity, fieldTime(8))
	if x.spanID.IsEmpty() || x.spanID != DeterministicRootSpanID(o.Identity, 1) {
		t.Fatal(x)
	}
}
