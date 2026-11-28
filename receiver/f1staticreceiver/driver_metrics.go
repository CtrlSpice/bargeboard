package f1staticreceiver

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

type sum struct {
	start       time.Time
	value       int64
	temporality pmetric.AggregationTemporality
}
type blueFlag struct {
	at     time.Time
	driver *driver
}

func (s *session) foldBlue() {
	end := s.ends
	slices.SortStableFunc(s.blue, func(a, b blueFlag) int { return a.at.Compare(b.at) })
	if len(s.blue) == 0 {
		return
	}
	if end.IsZero() {
		end = s.blue[len(s.blue)-1].at
	}
	type key struct {
		begin, end time.Time
		d          *driver
	}
	counts := map[key]int64{}
	var order []key
	for _, f := range s.blue {
		if f.at.After(end) {
			continue
		}
		lo, hi := window(f.at, s.start)
		if hi.After(end) {
			hi = end
		}
		if !hi.After(lo) {
			continue
		}
		k := key{lo, hi, f.driver}
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	for _, k := range order {
		s.gauge("f1.driver.blue_flags", "{flag}", k.end, k.d.attrs(), sum{k.begin, counts[k], pmetric.AggregationTemporalityDelta})
	}
}
func (s *session) countVisit(d *driver, n int64, t time.Time) {
	if n < d.pitHigh {
		return
	}
	d.pitCountKnown = true
	d.pitHigh = n
	if s.start.IsZero() || t.Before(s.start) || !t.After(d.pitCountAt) {
		return
	}
	if d.pitReported && d.values["pit_count"] == n {
		return
	}
	d.pitReported = true
	d.pitCountAt = t
	d.values["pit_count"] = n
	s.gauge("f1.driver.pit_lane_visits", "{visit}", t, d.attrs(), sum{s.start, n, pmetric.AggregationTemporalityCumulative})
}
func (s *session) cars(t time.Time, activation bool) {
	if !s.raceLike() || s.start.IsZero() || s.runningTerminal {
		return
	}
	var count int64
	for _, k := range s.keys {
		d := s.drivers[k]
		switch d.stopped {
		case "false":
			count++
		case "true":
		default:
			s.running = nil
			return
		}
		if activation && d.stoppedAt.After(t) {
			t = d.stoppedAt
		}
	}
	if t.Before(s.start) || !t.After(s.runningAt) {
		if s.running == nil || *s.running != count {
			s.running = nil
		}
		return
	}
	if s.running == nil || *s.running != count {
		s.running = &count
		s.runningAt = t
		s.gauge("f1.session.cars_running", "{car}", t, nil, count)
	}
}
func (s *session) tyres(f fields, t time.Time) {
	lines := object(f["Lines"])
	for _, k := range s.keys {
		d := s.drivers[k]
		updates := object(object(lines[k])["Stints"])
		if updates == nil {
			continue
		}
		if d.runs == nil {
			d.runs = map[int64]fields{}
		}
		for key, v := range updates {
			idx, ok := f1id.ParseNonNegativeCanonicalInt64([]byte(key))
			if !ok {
				continue
			}
			row := object(v)
			if row == nil || string(row["_deleted"]) == "true" {
				d.runs[idx] = nil
				continue
			}
			if d.runs[idx] == nil {
				d.runs[idx] = fields{}
			}
			maps.Copy(d.runs[idx], row)
		}
		idx := int64(len(d.runs) - 1)
		row := d.run(idx)
		n, ok := f1id.ParseNonNegativeCanonicalInt64(row["TotalLaps"])
		if !ok {
			d.ageKnown = false
			continue
		}
		if !d.ageKnown || d.ageRun != idx || d.values["age"] != n {
			s.gauge("f1.driver.tyre.age", "{lap}", t, d.attrs(), n)
			d.values["age"] = n
			d.ageRun = idx
			d.ageKnown = true
		}
	}
}
func (d *driver) run(idx int64) fields {
	if idx < 0 || idx >= int64(len(d.runs)) {
		return nil
	}
	for i := range idx + 1 {
		if d.runs[i] == nil {
			return nil
		}
	}
	row := d.runs[idx]
	if raw := row["Compound"]; raw != nil {
		if _, err := f1id.DecodeLosslessString(raw); err != nil {
			return nil
		}
	}
	for _, k := range []string{"StartLaps", "TotalLaps", "LapNumber"} {
		if raw := row[k]; raw != nil {
			if _, ok := f1id.ParseNonNegativeCanonicalInt64(raw); !ok {
				return nil
			}
		}
	}
	if v := row["New"]; v != nil && text(v) != "true" && text(v) != "false" {
		return nil
	}
	if v := row["TyresNotChanged"]; v != nil && text(v) != "0" && text(v) != "1" {
		return nil
	}
	return row
}
func (d *driver) compound(l *lap) string {
	idx, _ := l.a["f1.stint.number"].(int64)
	row := d.run(idx - 1)
	if n, ok := f1id.ParsePositiveCanonicalInt64(row["LapNumber"]); ok && n > l.number {
		return "unknown"
	}
	v := strings.Trim(text(row["Compound"]), " \t\r\n\f\v")
	for _, r := range v {
		if r > 127 {
			return "other"
		}
	}
	v = strings.ToLower(v)
	switch v {
	case "", "unknown":
		return "unknown"
	case "soft", "medium", "hard", "wet", "intermediate", "test":
		return v
	}
	if strings.HasPrefix(v, "test-") || strings.HasPrefix(v, "test_") {
		return "test"
	}
	return "other"
}

type gap struct {
	referent *driver
	coord    int64
	value    time.Duration
}
type gapChange struct {
	at    time.Time
	coord int64
	state gap
}

func (d *driver) boundaryGaps(l *lap, end time.Time) {
	for i, name := range []string{"gap_to_leader", "interval_to_ahead"} {
		history := d.gapHistory[i]
		valueAt := func(t time.Time, coord int64) gap {
			var latest time.Time
			var result gap
			for _, v := range history {
				if v.coord <= coord && !v.at.After(t) && !v.at.Before(latest) {
					latest = v.at
					result = v.state
				}
			}
			if coord-result.coord >= int64(15*time.Second) {
				return gap{}
			}
			return result
		}
		a, b := valueAt(l.at, l.startCoord), valueAt(end, l.boundaryCoord)
		prefix := "f1.lap." + name + "."
		if a.referent != nil {
			l.a[prefix+"start_seconds"] = a.value.Seconds()
		}
		if b.referent != nil {
			l.a[prefix+"end_seconds"] = b.value.Seconds()
		}
		if a.referent != nil && a.referent == b.referent {
			l.a[prefix+"change_seconds"] = (b.value - a.value).Seconds()
		}
		// Later contiguous laps cannot select a publication before this expiry.
		d.gapHistory[i] = slices.DeleteFunc(history, func(v gapChange) bool { return l.boundaryCoord-v.coord >= int64(15*time.Second) })
	}
}

func (s *session) elapsed(lines fields, t time.Time) {
	if !s.raceLike() {
		return
	}
	positions := map[int64]*driver{}
	for _, k := range s.keys {
		d := s.drivers[k]
		if _, dup := positions[d.position]; dup {
			positions[d.position] = nil
		} else {
			positions[d.position] = d
		}
	}
	for _, k := range s.keys {
		d := s.drivers[k]
		row := object(lines[k])
		if rn := row["RacingNumber"]; rn != nil && text(rn) != k {
			continue
		}
		for i, name := range []string{"gap_to_leader", "interval_to_ahead"} {
			ref := positions[1]
			raw := row["GapToLeader"]
			if i == 1 {
				raw = object(row["IntervalToPositionAhead"])["Value"]
				ref = positions[d.position-1]
				if d.position <= 1 {
					ref = nil
				}
			}
			g := &d.gaps[i]
			before := *g
			if g.referent != ref || s.coord-g.coord >= int64(15*time.Second) {
				*g = gap{}
			}
			if raw == nil || ref == nil {
				if before != *g {
					d.gapHistory[i] = append(d.gapHistory[i], gapChange{t, s.coord, *g})
				}
				continue
			}
			v := text(raw)
			var dur time.Duration
			if i == 0 && ref == d {
				if s.lapCounts[0] == nil || *s.lapCounts[0] <= 0 || v != "LAP "+decimal(*s.lapCounts[0]) {
					*g = gap{}
					d.gapHistory[i] = append(d.gapHistory[i], gapChange{t, s.coord, *g})
					continue
				}
			} else {
				if !strings.HasPrefix(v, "+") || strings.Contains(v, ":") || !durationPattern.MatchString(v[1:]) {
					*g = gap{}
					d.gapHistory[i] = append(d.gapHistory[i], gapChange{t, s.coord, *g})
					continue
				}
				dur = duration(v[1:])
				if dur == 0 {
					if n, err := strconv.ParseUint(strings.ReplaceAll(v[1:], ".", ""), 10, 64); err != nil || n != 0 {
						*g = gap{}
						d.gapHistory[i] = append(d.gapHistory[i], gapChange{t, s.coord, *g})
						continue
					}
				}
			}
			*g = gap{ref, s.coord, dur}
			d.gapHistory[i] = append(d.gapHistory[i], gapChange{t, s.coord, *g})
			s.gauge("f1.driver."+name, "s", t, d.attrs(), dur.Seconds())
		}
	}
}
