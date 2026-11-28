package f1staticreceiver

import (
	"slices"
	"time"
)

type population struct {
	start         time.Time
	bounds        []float64
	buckets       []uint64
	count         uint64
	sum, min, max float64
	seconds       uint64
	nanos         uint64
}
type seriesWindow struct {
	end             time.Time
	driver, sector  int64
	phase, compound string
}
type timingSample struct {
	ordinal int64
	at      time.Time
	value   time.Duration
	key     seriesWindow
	attrs   attrs
}
type windowState struct {
	samples []timingSample
	water   [2]time.Time
	groups  map[seriesWindow]*point
	order   []seriesWindow
}

func bounds(sector bool) []float64 {
	if sector {
		b := []float64{10, 12.5, 15}
		for n := 15.5; n <= 50; n += 0.5 {
			b = append(b, n)
		}
		return append(b, 55, 60, 75, 90, 120)
	}
	b := []float64{45, 50, 55, 60}
	for n := 61.; n <= 120; n++ {
		b = append(b, n)
	}
	return append(b, 125, 130, 135, 140, 150, 165, 180, 210, 240, 300)
}
func window(t, start time.Time) (time.Time, time.Time) {
	end := t.Truncate(10 * time.Second)
	if end.Before(t) || t.Equal(start) {
		end = end.Add(10 * time.Second)
	}
	begin := end.Add(-10 * time.Second)
	if begin.Before(start) {
		begin = start
	}
	return begin, end
}
func (s *session) durationPoints(d *driver, sector int64, t time.Time, v time.Duration) {
	a := d.timingAttrs()
	kind := "lap_time"
	if sector != 0 {
		kind = "sector_time"
		a["f1.sector.number"] = sector
	}
	s.gauge("f1.driver.last_"+kind, "s", t, a, v.Seconds())
	if s.start.IsZero() || t.Before(s.start) {
		return
	}
	k := seriesWindow{driver: d.number, sector: sector, phase: string(d.phase)}
	a = d.timingAttrs()
	if sector == 0 {
		k.compound = "unknown"
		if d.pending != nil {
			k.compound = d.compound(d.pending)
		}
		a["f1.tyre.compound"] = k.compound
	} else {
		a["f1.sector.number"] = sector
	}
	s.windows.samples = append(s.windows.samples, timingSample{s.ordinal, t, v, k, a})
}
func (s *session) admitTiming(v timingSample) {
	k := v.key
	idx := 0
	kind := "lap_time"
	if k.sector != 0 {
		kind = "sector_time"
		idx = 1
	}
	start, end := window(v.at, s.start)
	k.end = end
	w := &s.windows
	if w.water[idx].After(end) {
		s.issue("late timing histogram observation")
		return
	}
	if v.at.After(w.water[idx]) {
		w.water[idx] = v.at
	}
	if w.groups == nil {
		w.groups = map[seriesWindow]*point{}
	}
	p := w.groups[k]
	if p == nil {
		b := bounds(k.sector != 0)
		p = &point{fact: fact{"f1.driver." + kind, end, v.attrs}, unit: "s", histogram: &population{start: start, bounds: b, buckets: make([]uint64, len(b)+1), min: v.value.Seconds(), max: v.value.Seconds()}}
		w.groups[k] = p
		w.order = append(w.order, k)
	}
	p.histogram.observe(v.value)
}
func (h *population) observe(duration time.Duration) {
	n := duration.Seconds()
	i, _ := slices.BinarySearch(h.bounds, n)
	h.buckets[i]++
	h.count++
	h.min = min(h.min, n)
	h.max = max(h.max, n)
	h.nanos += uint64(duration % time.Second)
	h.seconds += uint64(duration/time.Second) + h.nanos/1_000_000_000
	h.nanos %= 1_000_000_000
	h.sum = float64(h.seconds) + float64(h.nanos)/1e9
}
func (s *session) flushWindows() {
	w := &s.windows
	terminal := s.ends
	if terminal.IsZero() {
		terminal = s.finalised
	}
	slices.SortStableFunc(w.samples, func(a, b timingSample) int {
		if a.ordinal < b.ordinal {
			return -1
		}
		if a.ordinal > b.ordinal {
			return 1
		}
		return a.at.Compare(b.at)
	})
	for _, v := range w.samples {
		if terminal.IsZero() || !v.at.After(terminal) {
			s.admitTiming(v)
		}
	}
	for _, k := range w.order {
		p := w.groups[k]
		end := s.ends
		if end.IsZero() {
			end = s.finalised
		}
		if end.IsZero() {
			idx := 0
			if k.sector != 0 {
				idx = 1
			}
			end = w.water[idx]
		}
		if end.Before(p.at) {
			p.at = end
		}
		if p.at.After(p.histogram.start) {
			s.e.points = append(s.e.points, *p)
		}
	}
}
