package f1staticreceiver

import (
	"maps"
	"regexp"
	"strings"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

const pos = `[1-9][0-9]*`
const lapDur = `[0-9]+:[0-5][0-9]\.[0-9]{3}`
const clockText = `(?:[0-9]|[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]`
const trackLimit = `TRACK LIMITS AT TURN ` + pos
const doubleYellow = `DOUBLE YELLOW AT TURN ` + pos
const reasonLap = ` LAP (` + pos + `)`
const timedLap = reasonLap + ` ` + clockText
const carHead = `^CAR (` + pos + `) \(([A-Z]{3})\) `

var deletesTime = regexp.MustCompile(carHead + `TIME (` + lapDur + `) DELETED - (?:` + trackLimit + `(?:` + timedLap + `|(?:` + reasonLap + `)? \(NEXT LAP\))|TRACK LIMITS AT PIT ENTRY` + timedLap + `|` + doubleYellow + `(?:` + timedLap + `)?)$`)
var deletesLap = regexp.MustCompile(carHead + `LAP DELETED - (?:` + trackLimit + `(?:` + timedLap + `(?: \(PIT\))?| \(NEXT LAP PIT\))|` + doubleYellow + `(?:` + timedLap + `(?: \(PIT\))?)?)$`)
var phaseDelete = regexp.MustCompile(`^CAR ([A-Z]{3}) TIME (` + lapDur + `) DELETED - ` + trackLimit + ` \(NEXT LAP\) \((Q[12])\)$`)
var pitDelete = regexp.MustCompile(`^CAR ([A-Z]{3}) TIME DELETED - ` + trackLimit + ` \(NEXT LAP PIT\) \(Q2\)$`)
var restoreTime = regexp.MustCompile(carHead + `TIME (` + lapDur + `) WILL BE REINSTATED$`)
var restoreLap = regexp.MustCompile(carHead + `LAP (` + pos + `) WILL BE REINSTATED$`)
var integers = regexp.MustCompile(`[0-9]+`)

type summary struct {
	phase    f1id.QualifyingPhase
	lap      int64
	duration time.Duration
	deletion *repairRecord
}
type repairRecord struct {
	car, acr, phase string
	label, index    int64
	duration        time.Duration
	restore         bool
}

func parseRepair(msg string) (r repairRecord, ok bool) {
	var dur, label string
	switch {
	case deletesTime.MatchString(msg):
		m := deletesTime.FindStringSubmatch(msg)
		r.car, r.acr, dur = m[1], m[2], m[3]
		label = strings.Join(m[4:], "")
	case deletesLap.MatchString(msg):
		m := deletesLap.FindStringSubmatch(msg)
		r.car, r.acr = m[1], m[2]
		label = strings.Join(m[3:], "")
	case phaseDelete.MatchString(msg):
		m := phaseDelete.FindStringSubmatch(msg)
		r.acr, dur, r.phase = m[1], m[2], strings.ToLower(m[3])
	case pitDelete.MatchString(msg):
		r.acr = pitDelete.FindStringSubmatch(msg)[1]
		r.phase = "q2"
	case restoreTime.MatchString(msg):
		m := restoreTime.FindStringSubmatch(msg)
		r.car, r.acr, dur, r.restore = m[1], m[2], m[3], true
	case restoreLap.MatchString(msg):
		m := restoreLap.FindStringSubmatch(msg)
		r.car, r.acr, r.restore = m[1], m[2], true
		label = m[3]
	default:
		return r, false
	}
	for _, v := range integers.FindAllString(msg, -1) {
		if _, ok := f1id.ParseNonNegativeCanonicalInt64([]byte(strings.TrimLeft(v, "0"))); !ok && strings.Trim(v, "0") != "" {
			return r, false
		}
	}
	if dur != "" {
		r.duration = duration(dur)
		if r.duration == 0 {
			return r, false
		}
	}
	if label != "" {
		r.label, _ = f1id.ParsePositiveCanonicalInt64([]byte(label))
	}
	return r, true
}
func (s *session) repair(index int64, msg, body string, row fields, a attrs, t time.Time) {
	r, ok := parseRepair(msg)
	if !ok {
		return
	}
	delete(a, "f1.driver.number")
	delete(a, "f1.driver.acronym")
	var d *driver
	for _, k := range s.keys {
		x := s.drivers[k]
		if x.acronym == r.acr {
			if d != nil {
				return
			}
			d = x
		}
	}
	if d == nil || (r.car != "" && r.car != decimal(d.number)) || (row["RacingNumber"] != nil && text(row["RacingNumber"]) != decimal(d.number)) {
		return
	}
	maps.Copy(a, d.attrs())
	if d.repairOverflow || r.duration == 0 && r.label == 0 {
		return
	}
	var target *summary
	for _, x := range d.summaries {
		if r.restore {
			old := x.deletion
			if old == nil || (r.label != 0 && old.label != r.label) || (r.duration != 0 && old.duration != r.duration) {
				continue
			}
		} else if (r.duration != 0 && x.duration != r.duration) || (r.label != 0 && r.label-1 != x.lap) || (r.phase != "" && string(x.phase) != r.phase) {
			continue
		}
		if target != nil {
			return
		}
		target = x
	}
	if target == nil {
		return
	}
	ev := maps.Clone(a)
	ev["f1.race_control.message"] = body
	ev["f1.lap.number"] = target.lap
	ev["f1.session.phase"] = string(target.phase)
	if r.label > 0 {
		ev["f1.race_control.reason_lap"] = r.label
	}
	if r.duration > 0 {
		ev["f1.lap.reported_duration"] = r.duration.Seconds()
	}
	name := "f1.lap_time.deleted"
	if r.restore {
		name = "f1.lap_time.reinstated"
		ev["f1.race_control.related_index"] = target.deletion.index
		target.deletion = nil
	} else {
		r.index = index
		target.deletion = &r
	}
	if l := d.pending; l != nil && l.summary == target {
		d.attach(l.span, name, t, ev)
	} else {
		d.event(name, t, ev)
	}
}
