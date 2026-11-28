package f1livetimingreceiver

import (
	"encoding/json"
	"maps"
	"slices"
	"time"
)

type pitReportKind uint8

const (
	laneReport pitReportKind = iota
	stopReport
	seriesReport
)

type pitSignature struct {
	driver, lap int64
	kind        pitReportKind
}
type pitReport struct{ driver, lap, stop, lane int64 }
type pitSeriesRow struct {
	report    pitReport
	timestamp time.Time
}
type pitReports struct {
	unsynchronized  [3]bool
	invalidSnapshot [3]bool
	seen            map[pitSignature]bool
	lanes           map[int64]pitReport
	stop            pitReport
	series          map[int64]map[int64]pitSeriesRow
}

func omitPitTopic(s timingState, k pitReportKind) timingState {
	s.pits.unsynchronized[k] = false
	s.pits.invalidSnapshot[k] = false
	switch k {
	case laneReport:
		s.pits.lanes = nil
	case stopReport:
		s.pits.stop = pitReport{}
	case seriesReport:
		s.pits.series = nil
	}
	return s
}
func pitFields(r pitReport, f topicFields, k pitReportKind) pitReport {
	for _, key := range deletedNames(f) {
		switch key {
		case "Lap":
			r.lap = 0
		case "RacingNumber":
			r.driver = 0
		case "Duration", "PitLaneTime":
			r.lane = 0
		case "PitStopTime":
			r.stop = 0
		}
	}
	if v := f["RacingNumber"]; v != nil {
		r.driver, _ = parseCanonicalDriverNumber(sessionText(v))
	}
	if v := f["Lap"]; v != nil {
		r.lap, _ = parseCanonicalDriverNumber(sessionText(v))
	}
	key, precision := "PitLaneTime", 3
	if k == laneReport {
		key, precision = "Duration", 1
	}
	if v := f[key]; v != nil {
		r.lane, _ = fixedSeconds(sessionText(v), precision)
	}
	if v := f["PitStopTime"]; v != nil {
		r.stop, _ = fixedSeconds(sessionText(v), 1)
	}
	return r
}
func laneCompatible(coarse, fine int64) bool {
	return coarse == 0 || fine == 0 || fine/1e8*1e8 == coarse
}

func invalidPitDuration(raw json.RawMessage, precision int) bool {
	if raw == nil {
		return false
	}
	t, err := decodeLosslessJSONString(raw)
	if err != nil {
		return true
	}
	n, ok := fixedSeconds(t, precision)
	return t != "" && (!ok || n <= 0)
}
func (s *timingState) acceptReport(o sessionObservation, k pitReportKind, r pitReport, reg driverRegistryState) {
	if _, ok := resolvedDriver(reg, r.driver); !ok {
		return
	}
	if k == seriesReport {
		if !o.snapshot() {
			return
		}
		k = stopReport
	}
	ns, name := r.stop, "f1.pit.stop_duration"
	if k == laneReport {
		ns, name = r.lane, "f1.pit.lane_duration"
	}
	if ns <= 0 {
		return
	}
	key := pitSignature{r.driver, r.lap, k}
	if o.snapshot() && k == stopReport && r.lane > 0 {
		s.pits.seen[pitSignature{r.driver, r.lap, laneReport}] = true
	}
	if s.pits.seen[key] {
		return
	}
	s.pits.seen[key] = true
	if o.snapshot() {
		return
	}
	s.observe(name, o.identity, o.publication, ns, nil)
	d := s.drivers[r.driver]
	for i, v := range d.visits {
		if r.lap != 0 && r.lap != v.out {
			continue
		}
		if k == laneReport {
			if v.lane != 0 || !laneCompatible(r.lane, v.stopLane) {
				continue
			}
			v.lane = r.lane
		} else {
			if v.stop != 0 || !laneCompatible(v.lane, r.lane) {
				continue
			}
			v.stop, v.stopLane = r.stop, r.lane
			if v.number != 0 {
				a := []keyValue{intAttr("f1.pit.visit.number", v.number), floatAttr("f1.pit.stop_duration", float64(ns)/1e9)}
				if r.lane > 0 {
					a = append(a, floatAttr("f1.pit.lane_duration", float64(r.lane)/1e9))
				}
				if r.lap > 0 {
					a = append(a, intAttr("f1.lap.number", r.lap))
				}
				d.attach(eventAt("f1.pit.stop", o.publication, a...), s.position)
			}
		}
		d.visits = slices.Clone(d.visits)
		d.visits[i] = v
		s.drivers[r.driver] = d
		break
	}
}
func reducePitTopic(s timingState, o sessionObservation, raw json.RawMessage, reg driverRegistryState, k pitReportKind) (timingState, liveTimingEffects, error) {
	var e liveTimingEffects
	f, err := topicObject(raw)
	if err != nil {
		return s, e, err
	}
	old := s
	bad := func() (timingState, liveTimingEffects, error) {
		if o.snapshot() {
			old.pits.unsynchronized[k] = true
			old.pits.invalidSnapshot[k] = true
		}
		return old, e, nil
	}
	if f == nil {
		return bad()
	}
	if s.sessionEnded || s.pits.unsynchronized[k] && !o.snapshot() {
		return s, e, nil
	}
	if o.snapshot() {
		s = omitPitTopic(s, k)
	}
	s.drivers = copyMap(s.drivers)
	s.pits.seen = copyMap(s.pits.seen)
	if k == stopReport {
		s.pits.stop = pitFields(s.pits.stop, f, k)
		r := pitFields(pitReport{}, f, k)
		if o.snapshot() && (f["RacingNumber"] != nil && r.driver <= 0 || invalidPitDuration(f["PitStopTime"], 1)) {
			return bad()
		}
		s.acceptReport(o, k, r, reg)
	} else {
		lines := nestedObject(f["PitTimes"])
		if lines == nil {
			return bad()
		}
		s.pits.lanes = copyMap(s.pits.lanes)
		s.pits.series = copyMap(s.pits.series)
		for _, key := range deletedNames(lines) {
			n, _ := parseCanonicalDriverNumber(key)
			if k == laneReport {
				delete(s.pits.lanes, n)
			} else {
				delete(s.pits.series, n)
			}
		}
		for _, key := range slices.Sorted(maps.Keys(lines)) {
			if key == "_deleted" {
				continue
			}
			n, ok := parseCanonicalDriverNumber(key)
			if !ok {
				if o.snapshot() {
					return bad()
				}
				continue
			}
			v := lines[key]
			if k == laneReport {
				fields := nestedObject(v)
				r := pitFields(pitReport{driver: n}, fields, k)
				if fields == nil || r.driver != n || invalidPitDuration(fields["Duration"], 1) {
					if o.snapshot() {
						return bad()
					}
					continue
				}
				prev := s.pits.lanes[n]
				prev.driver = n
				s.pits.lanes[n] = pitFields(prev, fields, k)
				s.acceptReport(o, k, r, reg)
				continue
			}
			rows, ok := recordPatches(v, true, true, true)
			if !ok {
				if o.snapshot() {
					return bad()
				}
				continue
			}
			series := copyMap(s.pits.series[n])
			if _, array := topicArray(v); array {
				clear(series)
			}
			for _, p := range rows {
				fields := nestedObject(p.raw)
				if p.remove || fields == nil {
					if o.snapshot() && !p.remove {
						return bad()
					}
					delete(series, p.index)
					continue
				}
				row := series[p.index]
				row.report.driver = n
				if slices.Contains(deletedNames(fields), "PitStop") {
					row.report = pitReport{driver: n}
				}
				stop := nestedObject(fields["PitStop"])
				if fields["PitStop"] != nil && stop == nil || invalidPitDuration(stop["PitStopTime"], 1) {
					if o.snapshot() {
						return bad()
					}
					delete(series, p.index)
					continue
				}
				row.report = pitFields(row.report, stop, stopReport)
				if v := fields["Timestamp"]; v != nil {
					var ok bool
					row.timestamp, ok = sessionUTC(v)
					if !ok && o.snapshot() {
						return bad()
					}
				}
				if row.report.driver != n {
					if o.snapshot() {
						return bad()
					}
					delete(series, p.index)
					continue
				}
				series[p.index] = row
				if !row.timestamp.IsZero() {
					s.acceptReport(o, k, row.report, reg)
				}
			}
			s.pits.series[n] = series
		}
	}
	if !o.snapshot() {
		s.drainWindows(time.Time{}, false, &e)
	}
	return s, e, nil
}

func reducePitStop(s timingState, o sessionObservation, raw json.RawMessage, reg driverRegistryState) (timingState, liveTimingEffects, error) {
	return reducePitTopic(s, o, raw, reg, stopReport)
}
func reducePitStopSeries(s timingState, o sessionObservation, raw json.RawMessage, reg driverRegistryState) (timingState, liveTimingEffects, error) {
	return reducePitTopic(s, o, raw, reg, seriesReport)
}
func reducePitLaneTimeCollection(s timingState, o sessionObservation, raw json.RawMessage, reg driverRegistryState) (timingState, liveTimingEffects, error) {
	return reducePitTopic(s, o, raw, reg, laneReport)
}

func (s *timingState) updatePit(d *timingDriver, o sessionObservation, f topicFields, e *liveTimingEffects) (exit bool) {
	at := measurementTime(o)
	n, valid := canonicalIndex(string(f["NumberOfPitStops"]))
	advances := valid && n > d.count
	if f["NumberOfPitStops"] != nil {
		restored := !d.countKnown
		if valid && n < d.count {
			d.countRegressed = true
		}
		if valid && n > d.count {
			d.countRegressed = false
		}
		d.countKnown = valid && n >= d.count && (!d.countRegressed || o.snapshot())
		if d.countKnown {
			d.count = n
			d.countDeferred = d.countDeferred || restored || !d.countSent || d.countSentValue < n
			if !s.start.IsZero() && d.countDeferred && at.After(d.countAt) && !at.Before(s.start) {
				m := driverGauge(o, d.driverRegistryEntry, "pit_lane_visits", "{visit}", n)
				m.kind, m.monotonic, m.startTime = metricIntSumCumulative, true, s.start
				e.metrics = append(e.metrics, m)
				d.countAt, d.countSentValue, d.countSent = at, n, true
				d.countDeferred = false
			}
		}
	}
	if f["InPit"] == nil {
		return false
	}
	prev := d.inPit
	d.inPit = boolState(f["InPit"])
	if d.inPit == 0 || !o.snapshot() && prev != d.inPit && !at.After(d.pitAt) {
		d.inPit = 0
		d.visit = timingVisit{}
		d.newStint = false
		return false
	}
	if at.After(d.pitAt) {
		d.pitAt = at
	}
	if o.snapshot() || !containsSpan(d.root, at) || prev == 0 || prev == d.inPit {
		return false
	}
	if d.inPit == 1 {
		return !d.visit.entry.IsZero()
	}
	if valid && !advances {
		return false
	}
	d.visit = timingVisit{entry: at, in: d.current}
	d.newStint = true
	if advances {
		d.visit.number = n
		d.attach(eventAt("f1.pit.entry", at, intAttr("f1.pit.visit.number", n)), s.position)
	}
	return false
}
