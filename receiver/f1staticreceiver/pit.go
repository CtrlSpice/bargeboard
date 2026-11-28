package f1staticreceiver

import (
	"regexp"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

type pitVisit struct {
	number, in, out      int64
	lane, stop, stopLane time.Duration
	reportLap            [2]int64
}
type pitIdentity struct {
	kind int
	lap  int64
}
type pitSample struct {
	at    time.Time
	value time.Duration
}

var pitTenths = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]$`)
var pitMillis = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]{3}$`)

func pitDuration(v string, millis bool) time.Duration {
	if millis {
		if !pitMillis.MatchString(v) {
			return 0
		}
	} else {
		if !pitTenths.MatchString(v) {
			return 0
		}
		v += "00"
	}
	return duration(v)
}
func (s *session) pitReports(topic string, f fields, t time.Time) {
	if topic == "PitStop" {
		if d := s.drivers[text(f["RacingNumber"])]; d != nil {
			s.acceptPit(d, 1, f, t)
		}
		return
	}
	times := object(f["PitTimes"])
	for _, k := range s.keys {
		row := object(times[k])
		if row == nil || (row["RacingNumber"] != nil && text(row["RacingNumber"]) != k) {
			continue
		}
		s.acceptPit(s.drivers[k], 0, row, t)
	}
}
func (s *session) acceptPit(d *driver, kind int, row fields, t time.Time) {
	value := pitDuration(text(row[[]string{"Duration", "PitStopTime"}[kind]]), false)
	if value == 0 {
		return
	}
	var lap int64
	if raw := row["Lap"]; raw != nil {
		v, err := f1id.DecodeLosslessString(raw)
		var ok bool
		lap, ok = f1id.ParsePositiveCanonicalInt64([]byte(v))
		if err != nil || v != "" && !ok {
			s.issue("invalid pit report lap")
			if kind == 0 {
				return
			}
		}
	}
	for _, v := range d.visits {
		prior := [2]time.Duration{v.lane, v.stop}[kind]
		if prior != 0 && (v.reportLap[kind] == lap || v.out == lap) {
			if prior != value {
				s.issue("pit report correction")
			}
			return
		}
	}
	id := pitIdentity{kind, lap}
	if d.pitSeen == nil {
		d.pitSeen = map[pitIdentity]bool{}
	}
	if d.pitSeen[id] {
		return
	}
	lane := pitDuration(text(row["PitLaneTime"]), true)
	paired := false
	for i := range d.visits {
		v := &d.visits[i]
		if lap != 0 && v.out != lap {
			continue
		}
		if kind == 0 {
			if v.lane != 0 || (v.stopLane != 0 && v.stopLane.Truncate(100*time.Millisecond) != value) {
				continue
			}
			v.lane = value
		} else {
			if v.stop != 0 || (lane != 0 && v.lane != 0 && lane.Truncate(100*time.Millisecond) != v.lane) {
				continue
			}
			v.stop = value
			v.stopLane = lane
			if v.number > 0 {
				a := pitAttrs(v.number)
				a["f1.pit.stop_duration"] = value.Seconds()
				if lane > 0 {
					a["f1.pit.lane_duration"] = lane.Seconds()
				}
				if lap > 0 {
					a["f1.lap.number"] = lap
				}
				d.event("f1.pit.stop", t, a)
			}
		}
		v.reportLap[kind] = lap
		paired = true
		break
	}
	if !paired {
		d.pitSeen[id] = true
	}
	if !s.start.IsZero() && !t.Before(s.start) {
		s.pitSamples[kind] = append(s.pitSamples[kind], pitSample{t, value})
	}
}
func pitBounds(kind int) []float64 {
	if kind == 0 {
		b := []float64{10, 12, 14, 16, 18}
		for n := 18.5; n <= 40; n += .5 {
			b = append(b, n)
		}
		return append(b, 45, 50, 60, 90, 120)
	}
	b := []float64{.5, 1}
	for n := 1.25; n <= 4; n += .25 {
		b = append(b, n)
	}
	return append(b, 4.5, 5, 6, 7, 8, 10, 12, 15, 20, 30, 60, 120)
}
func (s *session) foldPit() {
	for kind, samples := range s.pitSamples {
		groups := map[time.Time]*population{}
		var order []time.Time
		var watermark time.Time
		terminal := s.ends
		if terminal.IsZero() {
			terminal = s.finalised
		}
		for _, v := range samples {
			if !terminal.IsZero() && v.at.After(terminal) {
				continue
			}
			lo, hi := window(v.at, s.start)
			if watermark.After(hi) {
				s.issue("late pit histogram observation")
				continue
			}
			if v.at.After(watermark) {
				watermark = v.at
			}
			h := groups[hi]
			if h == nil {
				b := pitBounds(kind)
				h = &population{start: lo, bounds: b, buckets: make([]uint64, len(b)+1), min: v.value.Seconds(), max: v.value.Seconds()}
				groups[hi] = h
				order = append(order, hi)
			}
			h.observe(v.value)
		}
		if terminal.IsZero() {
			terminal = watermark
		}
		for _, hi := range order {
			h := groups[hi]
			if hi.After(terminal) {
				hi = terminal
			}
			if hi.After(h.start) {
				s.e.points = append(s.e.points, point{fact: fact{[]string{"f1.pit.lane_duration", "f1.pit.stop_duration"}[kind], hi, nil}, unit: "s", histogram: h})
			}
		}
	}
}
