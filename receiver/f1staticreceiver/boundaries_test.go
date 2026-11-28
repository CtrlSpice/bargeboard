package f1staticreceiver

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTimingLexicalBounds(t *testing.T) {
	for v, want := range map[string]time.Duration{"0:01.001": 1001 * time.Millisecond, "99.999": 99999 * time.Millisecond, "9223372036.854": 9223372036854 * time.Millisecond, "153722867:16.854": 9223372036854 * time.Millisecond, "9223372036.855": 0, "153722867:16.855": 0, "1:60.000": 0, "0.000": 0, "1.00": 0, " 3.000": 0, "1e3.000": 0, "-1.000": 0} {
		if got := duration(v); got != want {
			t.Errorf("duration(%q) = %s; want %s", v, got, want)
		}
	}
}

func TestPhaseEvidenceAfterConflict(t *testing.T) {
	streams := map[string][]StreamRecord{"SessionData": {{0, `{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`}},
		"TimingData": {{1e9, `{"Lines":{"17":{"SessionPart":1,"NumberOfLaps":1},"31":{"SessionPart":1,"NumberOfLaps":1}}}`}, {2e9, `{"Lines":{"17":{"SessionPart":2,"NumberOfLaps":2},"31":{"SessionPart":1}}}`}, {3e9, `{"Lines":{"17":{"NumberOfLaps":2}}}`}, {4e9, `{"Lines":{"17":{"SessionPart":2,"NumberOfLaps":1}}}`}}}
	p, err := freeze([]byte(strings.ReplaceAll(info, `"Race"`, `"Qualifying"`)), []byte(`{"17":{"Tla":"FIG"},"31":{"Tla":"PEA"}}`), streams)
	if err != nil {
		t.Fatal(err)
	}
	var phases []string
	for _, e := range reduce(p) {
		for _, p := range e.points {
			if p.name == "f1.driver.current_lap" && p.value != int64(1) {
				t.Fatal("timing escaped unresolved phase")
			}
		}
		for _, sp := range e.spans {
			if sp.name == "qualifying.phase" {
				phases = append(phases, fmt.Sprint(sp.a["f1.driver.number"], ":", sp.a["f1.session.phase"]))
			}
		}
	}
	if !reflect.DeepEqual(phases, []string{"17:q1", "31:q1", "17:q2"}) {
		t.Fatal(phases)
	}
}
func TestPublicationBound(t *testing.T) {
	table := fields{}
	mergePublication(table, object([]byte(`{"Message":"`+strings.Repeat("x", 4200)+` secret","unknown":"opaque"}`)), false)
	if text(table["Message"]) != "[redacted-message]" || table["unknown"] != nil || table["repair"] != nil || table["redacted"] == nil {
		t.Fatal("unsafe pending record")
	}
	rows := make(map[int64]fields, 4096)
	for i := int64(0); i < 4096; i++ {
		rows[i] = nil
	}
	rows[0] = fields{}
	s := session{records: recordState{rc: publicationTable{rows: rows}}, e: &effects{}}
	t0 := time.Unix(100, 0).UTC()
	s.publications("RaceControlMessages", object([]byte(`{"Messages":{"4096":{"Category":"Other","Message":"excess"}}}`)), t0)
	if !s.records.rc.overflow || len(rows) != 4096 || len(s.e.logs) != 0 {
		t.Fatal("overflow reopened index capacity")
	}
}
func TestTimingReferents(t *testing.T) {
	streams := map[string][]StreamRecord{"Heartbeat": {{0, `{"Utc":"2025-03-10T10:00:00Z"}`}}, "SessionData": {{0, `{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`}}, "LapCount": {{0, `{"CurrentLap":1,"TotalLaps":32}`}},
		"TimingData": {{1e9, `{"Lines":{"17":{"Position":"2","NumberOfLaps":1,"GapToLeader":"+1.125","IntervalToPositionAhead":{"Value":"+1.125"}},"31":{"Position":"1","GapToLeader":"LAP 1"}}}`}, {2e9, `{"Lines":{"17":{"NumberOfLaps":2,"GapToLeader":"+2.125","IntervalToPositionAhead":{"Value":"+2.125"}}}}`}, {3e9, `{"Lines":{"31":{"Position":"2"},"17":{"Position":"1","IntervalToPositionAhead":{"Value":"+99.000"}}}}`}, {4e9, `{"Lines":{"31":{"GapToLeader":"1 L","IntervalToPositionAhead":{"Value":"1L"}}}}`}}}
	p, err := freeze([]byte(info), []byte(`{"17":{"Tla":"FIG"},"31":{"Tla":"PEA"}}`), streams)
	if err != nil {
		t.Fatal(err)
	}
	observed := map[string][]any{}
	var lapAttrs attrs
	for _, e := range reduce(p) {
		for _, p := range e.points {
			if p.name == "f1.driver.gap_to_leader" || p.name == "f1.driver.interval_to_ahead" {
				observed[p.name] = append(observed[p.name], p.value)
			}
		}
		for _, sp := range e.spans {
			if sp.name == "lap" && sp.a["f1.lap.number"] == int64(1) {
				lapAttrs = sp.a
			}
		}
	}
	if !reflect.DeepEqual(observed, map[string][]any{"f1.driver.gap_to_leader": {1.125, 0., 2.125}, "f1.driver.interval_to_ahead": {1.125, 2.125}}) {
		t.Fatal(observed)
	}
	for _, prefix := range []string{"f1.lap.gap_to_leader.", "f1.lap.interval_to_ahead."} {
		if lapAttrs[prefix+"start_seconds"] != 1.125 || lapAttrs[prefix+"end_seconds"] != 2.125 || lapAttrs[prefix+"change_seconds"] != 1. {
			t.Fatal(lapAttrs)
		}
	}
}

func TestTerminalWatermarks(t *testing.T) {
	origin := time.Unix(100, 0).UTC()
	for _, tc := range []struct {
		name                             string
		final, ends, wantLap, wantSector time.Duration
		count                            uint64
	}{
		{"ends_over_finalised", 4e9, 7e9, 7e9, 7e9, 2}, {"finalised", 6e9, 0, 6e9, 6e9, 1}, {"per_instrument", 0, 0, 7e9, 3e9, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := session{start: origin.Add(time.Second), e: &effects{}}
			if tc.final != 0 {
				s.finalised = origin.Add(tc.final)
			}
			if tc.ends != 0 {
				s.ends = origin.Add(tc.ends)
			}
			d := driver{number: 17, acronym: "FIG"}
			s.durationPoints(&d, 0, origin.Add(2*time.Second), 60*time.Second)
			s.durationPoints(&d, 0, origin.Add(7*time.Second), 61*time.Second)
			s.durationPoints(&d, 1, origin.Add(3*time.Second), 30*time.Second)
			s.flushWindows()
			points := s.e.points[3:]
			if len(points) != 2 {
				t.Fatal(points)
			}
			for i, p := range points {
				h := p.histogram
				if h.start != s.start || p.at.Sub(origin) != []time.Duration{tc.wantLap, tc.wantSector}[i] || h.count != []uint64{tc.count, 1}[i] {
					t.Fatal(p, h)
				}
			}
		})
	}
	s := session{start: origin, e: &effects{}}
	d := driver{number: 17, acronym: "FIG"}
	for i, v := range []time.Duration{10e9, 10e9 + 1, 10e9 - 1} {
		s.ordinal = int64(i)
		s.durationPoints(&d, 0, origin.Add(v), 65*time.Second)
	}
	s.flushWindows()
	if pts := s.e.points[3:]; len(pts) != 2 || pts[0].histogram.count != 1 || pts[1].histogram.count != 1 {
		t.Fatal("late observation revised closed window")
	}
}

func TestExactHistogramBuckets(t *testing.T) {
	for _, tc := range []struct {
		sector   bool
		duration time.Duration
		bucket   int
	}{{false, 45 * time.Second, 0}, {false, 45*time.Second + 1, 1}, {false, 120 * time.Second, 63}, {false, 300*time.Second + 1, 74}, {true, 12500 * time.Millisecond, 1}, {true, 15500 * time.Millisecond, 3}, {true, 50 * time.Second, 72}, {true, 120*time.Second + 1, 78}} {
		b := bounds(tc.sector)
		h := population{bounds: b, buckets: make([]uint64, len(b)+1)}
		h.observe(tc.duration)
		if h.buckets[tc.bucket] != 1 || h.count != 1 || h.sum != tc.duration.Seconds() {
			t.Fatal(tc, h.buckets)
		}
	}
}
func TestPendingLapCutoff(t *testing.T) {
	for _, tc := range []struct {
		offset int64
		want   bool
	}{{5e9 - 1, true}, {5e9, true}, {5e9 + 1, false}} {
		t.Run(fmt.Sprint(tc.offset), func(t *testing.T) {
			e := reduceFixture(t, map[string][]StreamRecord{"SessionData": {{0, `{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`}}, "TimingData": {{0, `{"Lines":{"17":{"NumberOfLaps":1}}}`}, {3e9, `{"Lines":{"17":{"NumberOfLaps":2}}}`}, {3e9 + tc.offset, `{"Lines":{"17":{"LastLapTime":{"Value":"3.000"}}}}`}}})
			found := false
			for _, p := range e.points {
				if p.name == "f1.driver.lap_time" {
					found = true
					if p.histogram.sum != 3 {
						t.Fatal(p)
					}
				}
			}
			if found != tc.want {
				t.Fatal(found)
			}
		})
	}
}
func TestPositionBarriers(t *testing.T) {
	d := &driver{}
	s := session{drivers: map[string]*driver{"17": d}, keys: []string{"17"}}
	at := time.Unix(100, 0).UTC()
	frame := func(dt time.Duration, entries string) {
		s.positions(object([]byte(fmt.Sprintf(`{"Position":[{"Timestamp":%q,"Entries":%s}]}`, at.Add(dt).Format(time.RFC3339Nano), entries))))
	}
	frame(0, `{"17":{"X":1,"Y":-2,"Z":0}}`)
	frame(1, `{"17":{"X":100,"Y":0,"Z":0},"018":{}}`)
	for _, tc := range []struct {
		age  time.Duration
		want bool
	}{{-1, false}, {time.Second - 1, true}, {time.Second, true}, {time.Second + 1, false}} {
		owner := &span{}
		d.attach(owner, "f1.position.changed", at.Add(tc.age), attrs{})
		a := owner.events[0].a
		if (len(a) == 4) != tc.want {
			t.Fatal(tc, a)
		}
	}
	for _, bad := range []string{`{"17":{"X":0,"Y":0,"Z":0}}`, `{"17":{"X":1,"Z":2}}`, `{"17":{"X":-0,"Y":2,"Z":1}}`} {
		frame(1, bad)
		owner := &span{}
		d.attach(owner, "f1.position.changed", at.Add(2), attrs{})
		if len(owner.events[0].a) != 0 {
			t.Fatal("searched behind invalid sample")
		}
	}
	for i := range 20 {
		frame(time.Duration(20-i), `{"17":{"X":1,"Y":2,"Z":3},"241":{}}`)
	}
	if len(d.coords) != 16 || d.coords[0].at != at.Add(5) {
		t.Fatal("position retention")
	}
}

func TestStaticFinalisedAwaitsArchiveCompletion(t *testing.T) {
	e := reduceFixture(t, map[string][]StreamRecord{"SessionData": {{0, `{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`}, {1e9, `{"StatusSeries":{"1":{"Utc":"2025-03-10T10:00:01Z","SessionStatus":"Finalised"}}}`}}, "WeatherData": {{302e9, `{"WindDirection":"1"}`}}, "TeamRadio": {{303e9, `{"Captures":{"0":{"Path":"TeamRadio/FIG_17_20250310_100000.mp3","RacingNumber":"17","Utc":"2025-03-10T10:00:00Z"}}}`}}})
	if len(e.spans) != 1 || e.spans[0].end.Sub(e.spans[0].at) != time.Second || e.logs[len(e.logs)-1].owner != nil {
		t.Fatal("expected the root to remain provisional until archive completion")
	}
}
func TestHistoricalRepairDecisions(t *testing.T) {
	e := reduceFixture(t, map[string][]StreamRecord{"SessionData": {{0, `{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`}}, "TimingData": {{0, `{"Lines":{"17":{"NumberOfLaps":1}}}`}, {6e9, `{"Lines":{"17":{"NumberOfLaps":2,"LastLapTime":{"Value":"6.000"},"Sectors":[{"Value":"1.000"},{"Value":"2.000"},{"Value":"3.000"}]}}}`}, {12e9, `{"Lines":{"17":{"NumberOfLaps":3,"LastLapTime":{"Value":"6.000"},"Sectors":{"0":{"Value":"1.000"},"2":{"Value":"3.000"}}}}}`}, {18e9, `{"Lines":{"17":{"NumberOfLaps":4,"LastLapTime":{"Value":"5.999","OverallFastest":true},"Sectors":[{"Value":"1.000"},{"Value":"2.000"},{"Value":"3.000"}]}}}`}},
		"RaceControlMessages": {{13e9, `{"Messages":[{"Category":"Other","Message":"CAR 17 (FIG) TIME 0:06.000 DELETED - TRACK LIMITS AT TURN 9 LAP 3 10:00:12"},{"Category":"Other","Message":"CAR 17 (FIG) LAP 3 WILL BE REINSTATED"}]}`}}})
	var qualities []any
	var decisions []string
	for _, sp := range e.spans {
		if sp.name == "lap" {
			qualities = append(qualities, sp.a["f1.timing.value_quality"])
		}
		for _, ev := range sp.events {
			switch ev.name {
			case "f1.fastest_lap":
				t.Fatal("conflicting duration created fastest lap")
			case "f1.lap_time.deleted", "f1.lap_time.reinstated":
				decisions = append(decisions, ev.name)
				if sp.a["f1.lap.number"] != int64(2) || ev.a["f1.race_control.reason_lap"] != int64(3) {
					t.Fatal(ev)
				}
			}
		}
	}
	if !reflect.DeepEqual(qualities, []any{"reported", "reported", "conflict", nil}) || !reflect.DeepEqual(decisions, []string{"f1.lap_time.deleted", "f1.lap_time.reinstated"}) {
		t.Fatal(qualities, decisions)
	}
}
