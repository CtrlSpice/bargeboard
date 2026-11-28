package f1staticreceiver

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

type driver struct {
	meta                                  attrs
	exportedRoot                          *span
	owned                                 time.Time
	positionEdges                         map[positionEdge]bool
	bestLap                               int64
	singularBest                          bool
	runs                                  map[int64]fields
	ageRun                                int64
	ageKnown                              bool
	priorSectors                          [3]time.Duration
	priorLap                              time.Duration
	stopped                               string
	stoppedAt, pitCountAt, entryAt        time.Time
	pitCountKnown, pitReported            bool
	gaps                                  [2]gap
	gapHistory                            [2][]gapChange
	visits                                []pitVisit
	openVisit                             *pitVisit
	pitSeen                               map[pitIdentity]bool
	summaries                             []*summary
	repairOverflow                        bool
	number                                int64
	acronym                               string
	root, phaseSpan, stint                *span
	lap, pending                          *lap
	phase                                 f1id.QualifyingPhase
	stintNumber, position, visit, pitHigh int64
	pit                                   string
	pitAt                                 time.Time
	split                                 bool
	coords                                []sample
	values                                map[string]any
}
type sample struct {
	at    time.Time
	xyz   [3]int64
	valid bool
}
type session struct {
	highPart                                       int64
	ordinal                                        int64
	meeting                                        attrs
	coord                                          int64
	running                                        *int64
	runningAt                                      time.Time
	runningTerminal                                bool
	blue                                           []blueFlag
	pitSamples                                     [2][]pitSample
	p                                              FrozenReplayPlan
	drivers                                        map[string]*driver
	keys                                           []string
	start, ends, finalised, lastClock, timingWater time.Time
	clockKnown                                     bool
	e                                              *effects
	statusRows                                     map[int64]fields
	consumed                                       map[[2]int64]bool
	status                                         [2]string
	water                                          [2]time.Time
	edges                                          map[edge]bool
	part                                           int64
	windows                                        windowState
	records                                        recordState
	weather                                        weatherState
	lapCounts                                      [2]*int64
	reported                                       [2]*int64
	lapAt                                          [2]time.Time
}
type edge struct {
	kind     int
	at       time.Time
	from, to string
}
type positionEdge struct {
	at         time.Time
	prev, next int64
}

// reduce folds a frozen archive without process clocks, consumers, or network I/O.
func reduce(p FrozenReplayPlan) []effects {
	s := newSession(p)
	out := make([]effects, len(p.batches))
	for i, b := range p.batches {
		out[i] = s.apply(b, i == len(out)-1)
	}
	return out
}
func newSession(p FrozenReplayPlan) *session {
	return &session{p: p, drivers: map[string]*driver{}, statusRows: map[int64]fields{}, consumed: map[[2]int64]bool{}, edges: map[edge]bool{}}
}
func (s *session) apply(b FrozenBatch, last bool) effects {
	s.ordinal = b.Ordinal
	s.e = &effects{}
	s.coord = b.CoordNs

	for _, r := range b.records {
		f := object([]byte(r.Payload))
		if f == nil {
			continue
		}
		t := r.PublicationUTC
		switch r.Topic {
		case "SessionInfo":
			m := object(f["Meeting"])
			s.meeting = attrs{}
			for k, v := range map[string]string{"f1.meeting.name": text(m["Name"]), "f1.circuit.name": text(object(m["Circuit"])["ShortName"])} {
				if v != "" {
					s.meeting[k] = v
				}
			}
		case "DriverList":
			for _, k := range slices.Sorted(maps.Keys(f)) {
				n, ok := f1id.ParsePositiveCanonicalInt64([]byte(k))
				if ok {
					s.keys = append(s.keys, k)
					row := object(f[k])
					a := maps.Clone(s.meeting)
					if a == nil {
						a = attrs{}
					}
					for key, v := range map[string]string{"f1.driver.name": text(row["FullName"]), "f1.constructor.name": text(row["TeamName"])} {
						if v != "" {
							a[key] = v
						}
					}
					s.drivers[k] = &driver{number: n, acronym: text(row["Tla"]), values: map[string]any{}, meta: a, positionEdges: map[positionEdge]bool{}}
				}
			}
			slices.SortFunc(s.keys, func(a, b string) int { return cmp.Compare(s.drivers[a].number, s.drivers[b].number) })
		case "SessionData":
			s.transitions(f)
		case "RaceControlMessages", "TeamRadio":
			s.publications(r.Topic, f, t)
		default:
			if !s.ends.IsZero() {
				continue
			}
			switch r.Topic {
			case "Position":
				s.positions(f)
			case "TimingData":
				s.timing(f, t)
			case "TimingStats":
				s.stats(f, t)
			case "TimingAppData":
				s.tyres(f, t)
			case "PitLaneTimeCollection", "PitStop":
				s.pitReports(r.Topic, f, t)
			case "ExtrapolatedClock":
				if v, ok := f["Extrapolating"]; ok {
					s.clockKnown = string(v) == "true" || string(v) == "false"
				}
				a, ok := f1id.ParseStrictRFC3339(text(f["Utc"]))
				ns, valid := parseOffset([]byte(text(f["Remaining"]) + ".000"))
				if s.clockKnown && ok && validReplayTime(a) && valid && a.After(s.lastClock) {
					s.gauge("f1.session.clock.remaining", "s", a, nil, ns/int64(time.Second))
					s.lastClock = a
				}
			case "WeatherData":
				s.weatherPatch(f, t, b.CoordNs)
			case "LapCount":
				s.sessionLaps(f, t)
			}
		}
	}
	if last {
		s.closeRoots(s.finalised)
		s.flushWindows()
		s.foldBlue()
		s.foldPit()
	}
	return *s.e
}
func (s *session) raceLike() bool {
	return s.p.Identity.SessionType == f1id.SessionTypeRace || s.p.Identity.SessionType == f1id.SessionTypeSprint
}
func (s *session) sessionLaps(f fields, t time.Time) {
	if !s.raceLike() {
		return
	}
	for i, k := range []string{"CurrentLap", "TotalLaps"} {
		if v, ok := f[k]; ok {
			if n, valid := f1id.ParseNonNegativeCanonicalInt64(v); valid {
				s.lapCounts[i] = &n
			}
		}
	}
	a, b := s.lapCounts[0], s.lapCounts[1]
	for i, v := range s.lapCounts {
		if v == nil || (*v == 0 && (a == nil || b == nil || *b == 0)) || (a != nil && b != nil && *a > *b) {
			s.reported[i] = nil
			continue
		}
		if t.After(s.lapAt[i]) && (s.reported[i] == nil || *s.reported[i] != *v) {
			s.gauge([]string{"f1.session.current_lap", "f1.session.intended_total_laps"}[i], "{lap}", t, nil, *v)
			s.reported[i] = v
			s.lapAt[i] = t
		}
	}
}
func (s *session) gauge(name, unit string, t time.Time, a attrs, v any) {
	s.e.points = append(s.e.points, point{fact{name, t, a}, unit, v, nil})
}
func (d *driver) attrs() attrs {
	return attrs{"f1.driver.number": d.number, "f1.driver.acronym": d.acronym}
}
func (d *driver) timingAttrs() attrs {
	a := d.attrs()
	if d.phase != "" && d.phase != f1id.PhaseNone {
		a["f1.session.phase"] = string(d.phase)
	}
	return a
}
func (s *session) newSpan(d *driver, name string, t time.Time, a attrs, id pcommon.SpanID, parent *span) *span {
	if _, ok := a["f1.time.quality"]; !ok {
		a["f1.time.quality"] = "publication_time"
	}
	if name == "driver.session" {
		a["f1.time.quality"] = "observed"
	}
	d.own(t)
	tr, _ := f1id.DriverTraceID(s.p.Identity, d.number)
	x := &span{fact: fact{name, t, a}, id: id, trace: tr}
	if parent != nil {
		x.parent = parent.id
	}
	return x
}
func (s *session) close(x *span, t time.Time) {
	if x != nil && x.end.IsZero() {
		if t.Before(x.at) {
			t = x.at
		}
		x.end = t
		s.e.spans = append(s.e.spans, x)
	}
}
func (s *session) closeRoots(t time.Time) {
	for _, k := range s.keys {
		d := s.drivers[k]
		if d.root != nil {
			if d.visit != 0 {
				a := pitAttrs(d.visit)
				a["f1.pit.visit.incomplete_reason"] = "session_end"
				if s.ends.IsZero() && s.finalised.IsZero() {
					a["f1.pit.visit.incomplete_reason"] = "shutdown"
				}
				d.event("f1.pit.visit.incomplete", d.entryAt, a)
				d.visit = 0
			}
			if s.raceLike() {
				d.root.a["f1.result.outcome"] = "unresolved"
				d.root.a["f1.time.quality"] = "estimated"
			}
			end := t
			if d.owned.After(end) {
				end = d.owned
			}
			s.finishChildren(d, end)
			s.close(d.root, end)
			d.exportedRoot = d.root
			d.root = nil
		}
	}
}
func numbered(raw []byte) map[int64]json.RawMessage {
	m := map[int64]json.RawMessage{}
	if len(raw) > 0 && raw[0] == '[' {
		for i, v := range indexed(raw) {
			m[int64(i)] = v
		}
	} else {
		for k, v := range object(raw) {
			if n, ok := f1id.ParseNonNegativeCanonicalInt64([]byte(k)); ok {
				m[n] = v
			}
		}
	}
	return m
}

var tracks = map[string]struct {
	code  int64
	state string
}{"AllClear": {1, "all_clear"}, "Yellow": {2, "yellow"}, "SCDeployed": {4, "safety_car"}, "Red": {5, "red"}, "VSCDeployed": {6, "virtual_safety_car"}, "VSCEnding": {7, "virtual_safety_car_ending"}}

func (s *session) transitions(f fields) {
	rows := numbered(f["StatusSeries"])
	if raw := f["StatusSeries"]; len(raw) > 0 && raw[0] == '[' {
		clear(s.statusRows)
	}
	for _, i := range slices.Sorted(maps.Keys(rows)) {
		row := s.statusRows[i]
		if row == nil {
			row = fields{}
			s.statusRows[i] = row
		}
		maps.Copy(row, object(rows[i]))
		t, ok := f1id.ParseStrictRFC3339(text(row["Utc"]))
		if !ok || !validReplayTime(t) {
			continue
		}
		for kind, key := range []string{"TrackStatus", "SessionStatus"} {
			v, present := row[key]
			id := [2]int64{i, int64(kind)}
			if !present || s.consumed[id] {
				continue
			}
			s.consumed[id] = true
			if t.Before(s.water[kind]) {
				continue
			}
			s.water[kind] = t
			cur := text(v)
			prev := s.status[kind]
			a := attrs{"f1.time.quality": "observed"}
			name := "f1.track_status.changed"
			if kind == 0 {
				tr, ok := tracks[cur]
				if !ok {
					s.status[kind] = "?"
					continue
				}
				a["f1.track_status.code"] = tr.code
				a["f1.track_status.state"] = tr.state
				if p, ok := tracks[prev]; ok {
					a["f1.track_status.previous_code"] = p.code
					a["f1.track_status.previous_state"] = p.state
				}
			} else {
				name = "f1.session.status.changed"
				if !slices.Contains([]string{"Inactive", "Started", "Aborted", "Finished", "Finalised", "Ends"}, cur) {
					s.status[kind] = "?"
					continue
				}
				a["f1.session.status"] = cur
				if prev != "" {
					a["f1.session.previous_status"] = prev
				}
			}
			s.status[kind] = cur
			e := edge{kind, t, prev, cur}
			if cur == prev || prev == "?" || s.edges[e] {
				continue
			}
			s.edges[e] = true
			if kind == 1 && cur == "Started" && s.start.IsZero() && s.ends.IsZero() {
				s.start = t
				for _, k := range s.keys {
					d := s.drivers[k]
					id, _ := f1id.DriverSessionRootSpanID(s.p.Identity, d.number)
					d.root = s.newSpan(d, "driver.session", t, d.attrs(), id, nil)
					maps.Copy(d.root.a, d.meta)
					if d.pitCountKnown {
						s.countVisit(d, d.pitHigh, t)
					}
				}
				s.cars(t, true)
			}
			ev := fact{name, t, a}
			s.log(ev, nil)
			for _, k := range s.keys {
				d := s.drivers[k]
				owner := d.root
				if kind == 0 {
					owner = nil
					if d.lap != nil {
						owner = d.lap.span
					}
				}
				if owner != nil && !t.Before(owner.at) {
					d.own(t)
					owner.events = append(owner.events, ev)
				}
			}
			if kind == 1 {
				if cur == "Finished" || cur == "Finalised" || cur == "Ends" {
					s.runningTerminal = true
				}
				switch cur {
				case "Finalised":
					if s.finalised.IsZero() {
						s.finalised = t
					}
					if !s.raceLike() {
						s.closeRoots(t)
					}
				case "Ends":
					if !s.ends.IsZero() {
						continue
					}
					s.ends = t
					s.closeRoots(t)
					s.foldBlue()
					s.blue = nil
				}
			}
		}
	}
}
func (s *session) positions(f fields) {
	var frames []json.RawMessage
	if json.Unmarshal(f["Position"], &frames) != nil {
		return
	}
	for _, raw := range frames {
		fr := object(raw)
		t, ok := f1id.ParseStrictRFC3339(text(fr["Timestamp"]))
		entries := object(fr["Entries"])
		if !ok || !validReplayTime(t) || entries == nil {
			continue
		}
		valid := true
		for k := range entries {
			if s.drivers[k] == nil && k != "241" && k != "242" && k != "243" {
				valid = false
			}
		}
		for _, k := range s.keys {
			if entries[k] == nil {
				valid = false
			}
		}
		if !valid {
			continue
		}
		for _, k := range s.keys {
			d := s.drivers[k]
			xyz := object(entries[k])
			p := sample{at: t, valid: true}
			for i, key := range []string{"X", "Y", "Z"} {
				v, ok := f1id.ParseSignedCanonicalInt64(xyz[key])
				p.xyz[i] = v
				p.valid = p.valid && ok
			}
			p.valid = p.valid && p.xyz != [3]int64{}
			i, found := slices.BinarySearchFunc(d.coords, t, func(x sample, t time.Time) int { return x.at.Compare(t) })
			if found {
				d.coords[i] = p
			} else {
				d.coords = slices.Insert(d.coords, i, p)
			}
			if len(d.coords) > 16 {
				d.coords = d.coords[1:]
			}
		}
	}
}
func (d *driver) event(name string, t time.Time, a attrs) {
	owner := d.root
	if d.pending != nil && !t.Before(d.pending.at) && !t.After(d.pending.boundary) {
		owner = d.pending.span
	}
	if d.lap != nil && !t.Before(d.lap.at) {
		owner = d.lap.span
	}
	if owner == nil || t.Before(owner.at) {
		return
	}
	d.attach(owner, name, t, a)
}
func (d *driver) attach(owner *span, name string, t time.Time, a attrs) {
	d.own(t)
	for i := len(d.coords) - 1; i >= 0; i-- {
		p := d.coords[i]
		age := t.Sub(p.at)
		if age < 0 {
			continue
		}
		if age <= time.Second && p.valid {
			for j, k := range []string{"f1.position.x", "f1.position.y", "f1.position.z"} {
				a[k] = float64(p.xyz[j]) / 10
			}
			a["f1.position.sample_age"] = age.Seconds()
		}
		break
	}
	owner.events = append(owner.events, fact{name, t, a})
}
func (d *driver) own(t time.Time) {
	if t.After(d.owned) {
		d.owned = t
	}
}
func decimal(n int64) string { return strconv.FormatInt(n, 10) }
