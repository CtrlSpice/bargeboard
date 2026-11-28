package f1staticreceiver

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

var durationPattern = regexp.MustCompile(`^(?:([0-9]+):)?([0-9]+)\.([0-9]{3})$`)

func duration(v string) time.Duration {
	m := durationPattern.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	sec, err := strconv.ParseUint(m[2], 10, 63)
	if err != nil || sec > 9223372036 {
		return 0
	}
	if m[1] != "" {
		min, err := strconv.ParseUint(m[1], 10, 63)
		if err != nil || min > 153722867 || sec > 59 {
			return 0
		}
		sec += 60 * min
	}
	ms, _ := strconv.Atoi(m[3])
	total := sec*1000 + uint64(ms)
	if total > 9223372036854 {
		return 0
	}
	return time.Duration(total) * time.Millisecond
}

type lap struct {
	*span
	number                    int64
	boundary                  time.Time
	startCoord, boundaryCoord int64
	reported                  time.Duration
	empty                     bool
	explicit                  bool
	personal, fastest         bool
	summary                   *summary
	sectors                   [3]time.Duration
	stamps                    [3]time.Time
}

func (s *session) finishChildren(d *driver, t time.Time) {
	s.finalize(d)
	if d.lap != nil {
		s.close(d.lap.span, t)
		d.lap = nil
	}
	s.close(d.stint, t)
	s.close(d.phaseSpan, t)
	d.stint = nil
	d.phaseSpan = nil
}
func (s *session) open(d *driver, n int64, t time.Time) {
	if d.root == nil || d.phase == "" || t.Before(d.root.at) {
		return
	}
	parent := d.root
	if d.phase != f1id.PhaseNone {
		if d.phaseSpan == nil {
			id, _ := f1id.QualifyingPhaseSpanID(s.p.Identity, d.number, d.phase)
			d.phaseSpan = s.newSpan(d, "qualifying.phase", t, d.timingAttrs(), id, parent)
		}
		parent = d.phaseSpan
	}
	if d.stint == nil || d.split {
		s.close(d.stint, t)
		d.stintNumber++
		d.split = false
		id, _ := f1id.StintSpanID(s.p.Identity, d.number, d.phase, d.stintNumber)
		a := d.timingAttrs()
		a["f1.stint.number"] = d.stintNumber
		d.stint = s.newSpan(d, "stint", t, a, id, parent)
	}
	id, _ := f1id.LapSpanID(s.p.Identity, d.number, d.phase, n)
	a := maps.Clone(d.stint.a)
	a["f1.lap.number"] = n
	d.lap = &lap{span: s.newSpan(d, "lap", t, a, id, d.stint), number: n, startCoord: s.coord}
}
func (s *session) finalize(d *driver) {
	l := d.pending
	if l == nil {
		return
	}
	defer func() { d.pending = nil }()
	end := l.boundary
	repaired := -1
	for i, v := range l.sectors {
		if v == 0 {
			other1, other2 := l.sectors[(i+1)%3], l.sectors[(i+2)%3]
			if other1 > 0 && other2 > 0 && l.reported > other1 && l.reported-other1 > other2 && l.reported-other1-other2 == d.priorSectors[i] {
				l.sectors[i] = d.priorSectors[i]
				repaired = i
			}
			break
		}
	}
	total := time.Duration(0)
	complete := true
	for _, dur := range l.sectors {
		if dur == 0 || dur > time.Duration(1<<63-1)-total {
			complete = false
			break
		}
		total += dur
	}
	quality := "reported"
	timeQuality := "publication_time"
	dur := l.reported
	if complete {
		if dur == 0 && (l.empty || (!l.explicit && d.priorLap == total)) {
			dur = total
			quality = "reconstructed"
		} else if dur != 0 && dur != total {
			dur = 0
			quality = "conflict"
		}
	}
	if complete && end.Sub(l.at) >= total {
		timeQuality = "estimated"
		remaining := total
		for i, sec := range l.sectors {
			remaining -= sec
			if !l.stamps[i].IsZero() {
				candidate := l.stamps[i].Add(remaining)
				if candidate.Before(end) && candidate.Sub(l.at) >= total {
					end = candidate
				}
			}
		}
	}
	if dur > 0 {
		s.durationPoints(d, 0, end, dur)
		for i, v := range []bool{l.personal, l.fastest} {
			if v {
				d.attach(l.span, []string{"f1.personal_best", "f1.fastest_lap"}[i], end, attrs{"f1.lap.number": l.number, "f1.time.quality": timeQuality})
			}
		}
	}
	d.priorLap = dur
	d.boundaryGaps(l, end)
	l.a["f1.time.quality"] = timeQuality
	if l.summary != nil {
		l.summary.duration = dur
	}
	d.priorSectors = l.sectors
	if dur > 0 || quality == "conflict" {
		l.a["f1.timing.value_quality"] = quality
	}
	cursor := l.at
	if complete && end.Sub(l.at) >= total {
		cursor = end.Add(-total)
	}
	for i, sec := range l.sectors {
		if sec <= 0 {
			continue
		}
		finish := cursor.Add(sec)
		if !complete && !l.stamps[i].IsZero() {
			finish = l.stamps[i]
		}
		if complete && end.Sub(l.at) < total || finish.After(end) || finish.Before(cursor) {
			at := l.stamps[i]
			if at.IsZero() {
				at = l.boundary
			}
			s.durationPoints(d, int64(i+1), at, sec)
			continue
		}
		n := int64(i + 1)
		a := d.timingAttrs()
		a["f1.stint.number"] = l.a["f1.stint.number"]
		a["f1.lap.number"] = l.number
		a["f1.sector.number"] = n
		a["f1.timing.value_quality"] = "reported"
		if i == repaired {
			a["f1.timing.value_quality"] = "reconstructed"
		}
		a["f1.time.quality"] = timeQuality
		id, _ := f1id.SectorSpanID(s.p.Identity, d.number, d.phase, l.number, n)
		x := s.newSpan(d, "sector", cursor, a, id, l.span)
		s.close(x, finish)
		s.durationPoints(d, n, finish, sec)
		cursor = finish
	}
	s.close(l.span, end)
}
func (s *session) timing(f fields, t time.Time) {
	lines := object(f["Lines"])
	if lines == nil && f["SessionPart"] == nil {
		return
	}
	part := s.part
	changed := false
	qualifying := s.p.Identity.SessionType == f1id.SessionTypeQualifying || s.p.Identity.SessionType == f1id.SessionTypeSprintQualifying
	if raw := f["SessionPart"]; raw != nil && qualifying {
		n, ok := f1id.ParsePositiveCanonicalInt64(raw)
		if !ok || n > 3 {
			part = -1
		} else {
			part = n
		}
		changed = true
	}
	for _, k := range s.keys {
		raw := object(lines[k])["SessionPart"]
		if raw == nil || !qualifying {
			continue
		}
		n, ok := f1id.ParsePositiveCanonicalInt64(raw)
		if !ok || n > 3 || (changed && n != part) {
			part = -1
			changed = true
			break
		}
		part = n
		changed = true
	}
	if s.p.Identity.SessionType != f1id.SessionTypeQualifying && s.p.Identity.SessionType != f1id.SessionTypeSprintQualifying {
		part = 0
	}
	phase, _ := f1id.ResolveQualifyingPhase(s.p.Identity.SessionType, part)
	transition := part != s.part
	if qualifying && part >= 1 && part < s.highPart {
		s.issue("regressing qualifying phase")
		part = -1
		phase = ""
	}
	s.highPart = max(s.highPart, part)
	s.part = part
	accepted := changed
	for _, k := range s.keys {
		d := s.drivers[k]
		if phase != "" && d.phase != phase {
			s.finishChildren(d, t)
			d.phase = phase
			d.stintNumber = 0
			d.priorLap = 0
			d.bestLap = 0
			d.singularBest = false
			d.priorSectors = [3]time.Duration{}
		}
		row := object(lines[k])
		if row == nil {
			continue
		}
		if rn, ok := row["RacingNumber"]; ok && text(rn) != k {
			d.stopped = ""
			continue
		}
		validTiming := timingEvidence(row)
		accepted = accepted || validTiming
		if d.pending != nil && t.After(d.pending.boundary.Add(5*time.Second)) && validTiming {
			s.finalize(d)
		}
		if v, ok := row["Stopped"]; ok {
			d.stopped = string(v)
			d.stoppedAt = t
		}
		if pos, ok := f1id.ParsePositiveCanonicalInt64([]byte(text(row["Position"]))); ok && pos != d.position {
			prev := d.position
			d.position = pos
			s.gauge("f1.driver.position", "{position}", t, d.attrs(), pos)
			edge := positionEdge{t, prev, pos}
			if prev != 0 && !d.positionEdges[edge] {
				d.positionEdges[edge] = true
				d.event("f1.position.changed", t, attrs{"f1.position.previous": prev, "f1.position.current": pos, "f1.position.delta": pos - prev, "f1.position.change.kind": "classification_update", "f1.time.quality": "publication_time"})
			}
		}
		exit := s.pitEntry(d, row, t)
		if phase != "" {
			s.lapTiming(d, row, t, transition)
			s.bestLapTiming(d, row, t)
			if !transition {
				s.speeds(d, object(row["Speeds"]), t, false)
			}
		}
		if exit {
			if d.visit != 0 {
				d.event("f1.pit.exit", t, pitAttrs(d.visit))
			}
			v := *d.openVisit
			if d.lap == nil || d.lap.number != v.out {
				v.out = 0
			}
			d.visits = append(d.visits, v)
			d.openVisit = nil
			d.visit = 0
		}
	}
	if accepted && t.After(s.timingWater) {
		s.timingWater = t
	}
	s.cars(t, false)
	s.elapsed(lines, t)
	for _, k := range s.keys {
		d := s.drivers[k]
		if d.pending != nil && !s.timingWater.Before(d.pending.boundary.Add(5*time.Second)) {
			s.finalize(d)
		}
	}
}
func timingEvidence(row fields) bool {
	for _, key := range []string{"NumberOfLaps", "NumberOfPitStops", "SessionPart"} {
		if _, ok := f1id.ParseNonNegativeCanonicalInt64(row[key]); ok {
			return true
		}
	}
	if _, ok := f1id.ParsePositiveCanonicalInt64([]byte(text(row["Position"]))); ok {
		return true
	}
	for _, key := range []string{"InPit", "Stopped", "Retired", "PitOut"} {
		if v := string(row[key]); v == "true" || v == "false" {
			return true
		}
	}
	for _, v := range []fields{object(row["LastLapTime"]), object(row["BestLapTime"])} {
		if str, err := f1id.DecodeLosslessString(v["Value"]); err == nil && (str == "" || duration(str) > 0) {
			return true
		}
	}
	for i, v := range numbered(row["Sectors"]) {
		if i <= 2 && duration(text(object(v)["Value"])) > 0 {
			return true
		}
	}
	for _, key := range []string{"I1", "I2", "FL", "ST"} {
		if n, err := strconv.ParseUint(text(object(object(row["Speeds"])[key])["Value"]), 10, 63); err == nil && n > 0 {
			return true
		}
	}
	return false
}
func pitAttrs(n int64) attrs {
	return attrs{"f1.pit.visit.number": n, "f1.time.quality": "publication_time"}
}
func (s *session) changed(d *driver, name, unit string, t time.Time, a attrs, v any, key string) {
	if d.values[name+key] != v {
		s.gauge(name, unit, t, a, v)
		d.values[name+key] = v
	}
}
func (s *session) best(d *driver, name string, t time.Time, a attrs, v, key string) {
	if v == "" {
		delete(d.values, name+key)
		return
	}
	if sec := duration(v); sec > 0 {
		s.changed(d, name, "s", t, a, sec.Seconds(), key)
	}
}
func (s *session) speeds(d *driver, table fields, t time.Time, best bool) {
	for _, k := range []string{"I1", "I2", "FL", "ST"} {
		v := text(object(table[k])["Value"])
		n, err := strconv.ParseUint(v, 10, 63)
		if err != nil || n == 0 {
			continue
		}
		a := d.timingAttrs()
		name := "f1.driver.speed_trap"
		if best {
			a = d.attrs()
			name = "f1.driver.best_speed_trap"
		}
		a["f1.speed_trap.location"] = strings.ToLower(k)
		if best {
			s.changed(d, name, "km/h", t, a, int64(n), k)
		} else {
			s.gauge(name, "km/h", t, a, int64(n))
			l := d.lap
			if k != "ST" && d.pending != nil {
				l = d.pending
			}
			if l != nil {
				l.a["f1.lap.speed_trap."+strings.ToLower(k)+"_kph"] = int64(n)
			}
		}
	}
}
func (s *session) stats(f fields, t time.Time) {
	lines := object(f["Lines"])
	for _, k := range s.keys {
		d := s.drivers[k]
		row := object(lines[k])
		tab := numbered(row["BestSectors"])
		for _, i := range slices.Sorted(maps.Keys(tab)) {
			if i > 2 {
				continue
			}
			if v, ok := object(tab[i])["Value"]; ok {
				a := d.attrs()
				a["f1.sector.number"] = i + 1
				s.best(d, "f1.driver.best_sector_time", t, a, text(v), decimal(i))
			}
		}
		s.speeds(d, object(row["BestSpeeds"]), t, true)
	}
}

func (s *session) pitEntry(d *driver, row fields, t time.Time) bool {
	pit := string(row["InPit"])
	exit := false
	if pit != "" && d.root != nil {
		if (pit == "true" || pit == "false") && t.After(d.pitAt) {
			if d.pit == "false" && pit == "true" {
				count, ok := f1id.ParsePositiveCanonicalInt64(row["NumberOfPitStops"])
				if !ok || count > d.pitHigh {
					d.openVisit = &pitVisit{number: count}
					if d.lap != nil {
						d.openVisit.in = d.lap.number
					}
					d.split = d.stint != nil
					if ok {
						d.visit = count
						d.entryAt = t
						d.event("f1.pit.entry", t, pitAttrs(count))
					}
				}
			}
			exit = d.pit == "true" && pit == "false" && d.openVisit != nil
			d.pit = pit
			d.pitAt = t
		} else {
			d.pit = ""
			d.visit = 0
			d.openVisit = nil
		}
	}
	if v, ok := row["NumberOfPitStops"]; ok {
		if n, ok := f1id.ParseNonNegativeCanonicalInt64(v); ok {
			s.countVisit(d, n, t)
		} else {
			d.pitCountKnown = false
			d.pitReported = false
		}
	}
	return exit
}
func (s *session) lapTiming(d *driver, row fields, t time.Time, transition bool) {
	phase := d.phase
	n, ok := f1id.ParsePositiveCanonicalInt64(row["NumberOfLaps"])
	boundary := ok && d.lap != nil && n-1 == d.lap.number && !t.Before(d.lap.at)
	if row["NumberOfLaps"] != nil && (!ok || d.lap != nil && !boundary && (n != d.lap.number || t.Before(d.lap.at))) {
		return
	}
	if boundary {
		s.finalize(d)
		d.pending = d.lap
		d.pending.boundary = t
		d.pending.boundaryCoord = s.coord
		if len(d.summaries) < 256 && !d.repairOverflow {
			summary := &summary{phase: d.phase, lap: d.pending.number}
			d.summaries = append(d.summaries, summary)
			d.pending.summary = summary
		} else {
			if !d.repairOverflow {
				s.issue("lap summary limit")
			}
			d.repairOverflow = true
		}
		d.lap = nil
	}
	if ok && (boundary || d.lap == nil) && !(d.pit == "true" && d.stint == nil) {
		s.open(d, n, t)
		if visit := d.openVisit; visit != nil && visit.out == 0 && (visit.in == 0 || visit.in == n-1) {
			visit.out = n
		}
		s.changed(d, "f1.driver.current_lap", "{lap}", t, d.timingAttrs(), n, string(phase))
	}
	if l := d.pending; l != nil {
		last := object(row["LastLapTime"])
		if !transition {
			if v := last["PersonalFastest"]; v != nil {
				l.personal = string(v) == "true"
			}
			if v := last["OverallFastest"]; v != nil {
				l.fastest = string(v) == "true"
			}
		}
		if v, ok := last["Value"]; ok {
			l.explicit = true
			str, err := f1id.DecodeLosslessString(v)
			if err == nil {
				l.reported = duration(str)
				l.empty = str == ""
			}
		}
	}
	for i, raw := range numbered(row["Sectors"]) {
		if i > 2 {
			continue
		}
		sec := duration(text(object(raw)["Value"]))
		if sec == 0 {
			continue
		}
		owner := d.lap
		if d.pending != nil && (boundary || (i == 2 && d.pending.sectors[2] == 0 && d.pending.boundary.Sub(d.pending.at) >= sec)) {
			owner = d.pending
		}
		if owner != nil && !t.Before(owner.at) {
			owner.sectors[i] = sec
			owner.stamps[i] = t
			d.own(t)
		}
	}
	if l := d.pending; l != nil && l.summary != nil {
		l.summary.duration = l.reported
		if l.sectors[0] > 0 && l.sectors[1] > 0 && l.sectors[2] > 0 {
			total := l.sectors[0] + l.sectors[1] + l.sectors[2]
			if l.empty || (!l.explicit && total == d.priorLap) {
				l.summary.duration = total
			} else if total != l.reported {
				l.summary.duration = 0
			}
		}
	}
}
func (s *session) bestLapTiming(d *driver, row fields, t time.Time) {
	phase := d.phase
	singular := object(row["BestLapTime"])
	if v, ok := singular["Value"]; ok {
		if str, err := f1id.DecodeLosslessString(v); err == nil && (str == "" || duration(str) > 0) {
			d.singularBest = str != ""
		}
		name := "f1.driver.best_lap_time" + string(phase)
		if prev, ok := d.values[name].(float64); ok {
			if sec := duration(text(v)).Seconds(); sec > prev {
				a := attrs{"f1.personal_best.previous_duration": prev, "f1.personal_best.current_duration": sec}
				a["f1.time.quality"] = "publication_time"
				if d.bestLap > 0 {
					a["f1.personal_best.previous_lap"] = d.bestLap
				}
				if n, ok := f1id.ParsePositiveCanonicalInt64(singular["Lap"]); ok {
					a["f1.personal_best.current_lap"] = n
				}
				d.event("f1.personal_best.revised", t, a)
			}
		}
		if val, ok := d.values[name].(float64); !ok || val != duration(text(v)).Seconds() {
			d.bestLap = 0
		}
		s.best(d, "f1.driver.best_lap_time", t, d.timingAttrs(), text(v), string(phase))
	}
	if n, ok := f1id.ParsePositiveCanonicalInt64(singular["Lap"]); ok {
		d.bestLap = n
	}
	plural := numbered(row["BestLapTimes"])
	for _, i := range slices.Sorted(maps.Keys(plural)) {
		p, ok := f1id.ResolveQualifyingPhase(s.p.Identity.SessionType, i+1)
		if !ok || (p == phase && (d.singularBest || singular["Value"] != nil)) {
			continue
		}
		if v, ok := object(plural[i])["Value"]; ok {
			a := d.attrs()
			a["f1.session.phase"] = string(p)
			s.best(d, "f1.driver.best_lap_time", t, a, text(v), string(p))
		}
	}
}
