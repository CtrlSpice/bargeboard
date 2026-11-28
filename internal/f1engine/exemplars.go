package f1engine

import (
	"cmp"
	"time"
)

// observationOwner identifies the observation independently of its metric attributes.
type observationOwner struct {
	driver, lap, sector int64
	acronym, phase      string
	lapResolved         bool
}

func (o observationOwner) at(d timingDriver, t time.Time) observationOwner {
	for _, lap := range []*spanEffect{&d.lap.span, &d.pending.span} {
		if containsSpan(*lap, t) {
			o.phase, o.lap, o.lapResolved = lap.phase, lap.lapNumber, true
			break
		}
	}
	return o
}

// A telemetry sample can supply a tick one second ahead of its own timestamp.
// A subsequent lap boundary can therefore resolve a different owner for that tick.
func (s *carDataState) resolveTicks(driver timingDriver) {
	copied := false
	for k, tick := range s.ticks {
		if k.driver != driver.number {
			continue
		}
		owner := tick.owner.at(driver, k.second)
		if owner != tick.owner {
			if !copied {
				s.ticks = copyMap(s.ticks)
				copied = true
			}
			tick.owner = owner
			s.ticks[k] = tick
		}
	}
}

func (o observationOwner) exemplar(id SessionInfoIdentity, at time.Time) exemplarEffect {
	x := exemplarEffect{timestamp: at, traceID: DeterministicTraceID(id, o.driver), spanID: DeterministicRootSpanID(id, o.driver), filteredAttrs: []keyValue{intAttr("f1.driver.number", o.driver), strAttr("f1.driver.acronym", o.acronym)}, order: [3]int64{o.driver, o.lap, o.sector}}
	if o.lap > 0 {
		x.filteredAttrs = append(x.filteredAttrs, intAttr("f1.lap.number", o.lap))
	}
	if o.sector > 0 {
		x.filteredAttrs = append(x.filteredAttrs, intAttr("f1.sector.number", o.sector))
	}
	if o.lapResolved {
		span := DeterministicLapSpanID(id, o.driver, o.phase, o.lap)
		if o.sector > 0 {
			span = DeterministicSectorSpanID(id, o.driver, o.phase, o.lap, o.sector)
		}
		if !span.IsEmpty() {
			x.spanID = span
		}
	}
	return x
}
func (a exemplarEffect) before(b exemplarEffect) bool {
	if !a.timestamp.Equal(b.timestamp) {
		return a.timestamp.Before(b.timestamp)
	}
	for i, n := range a.order {
		if n != b.order[i] {
			return n < b.order[i]
		}
	}
	return false
}
func (a exemplarEffect) compare(b exemplarEffect) int {
	if a.exact != nil && b.exact != nil {
		return a.exact.Cmp(b.exact)
	}
	if a.isInt {
		return cmp.Compare(a.intVal, b.intVal)
	}
	return cmp.Compare(a.floatVal, b.floatVal)
}

// Retain only the two extremes. Replacement slices keep earlier states immutable.
func (m *metricEffect) selectExemplar(x exemplarEffect) {
	if len(m.exemplars) == 0 {
		m.exemplars = []exemplarEffect{x}
		return
	}
	lo, hi := m.exemplars[0], m.exemplars[len(m.exemplars)-1]
	if c := x.compare(lo); c < 0 || c == 0 && x.before(lo) {
		lo = x
	}
	if c := x.compare(hi); c > 0 || c == 0 && x.before(hi) {
		hi = x
	}
	if lo.compare(hi) == 0 {
		m.exemplars = []exemplarEffect{lo}
	} else {
		m.exemplars = []exemplarEffect{lo, hi}
	}
}
