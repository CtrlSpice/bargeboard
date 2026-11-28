package f1livetimingreceiver

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestTimingLexicalBoundaries(t *testing.T) {
	for text, want := range map[string]int64{"1:02.003": 62003000000, "000:0.001": 1000000, "9223372036.854": 9223372036854000000} {
		if n, ok := timingDuration(text); !ok || n != want {
			t.Errorf("duration %q: %d %t", text, n, ok)
		}
	}
	for _, text := range []string{"", "0.000", "00:60.000", " 1.000", "-1.000", "+1.000", "1.2", "1.0000", "1e2.000", "9223372036.855", "9223372036854775808:00.001", "١.000"} {
		if _, ok := timingDuration(text); ok {
			t.Errorf("accepted %q", text)
		}
	}
	for _, tc := range []struct {
		s string
		k gapKind
		n int64
	}{{"", Cleared, 0}, {"+00.000", ElapsedMilliseconds, 0}, {"+44.163", ElapsedMilliseconds, 44163000000}, {"1 L", LiveLapDeficit, 1}, {"LAP 0", LeaderLap, 0}, {"1L", TerminalLapToken, 1}, {"01L", Unknown, 0}, {"LAP 01", Unknown, 0}, {"0 L", Unknown, 0}, {"+9223372036.855", Unknown, 0}} {
		if k, n := gapToken(tc.s); k != tc.k || n != tc.n {
			t.Errorf("gap %q: %v %d", tc.s, k, n)
		}
	}
	for text, want := range map[string]string{" \tSOFT\r": "soft", "TeSt-X": "test", "test_": "test", "TEST": "test", "testing": "other", "UnKnown": "unknown", "": "unknown", "\u00a0wet": "other", "ſoft": "other"} {
		if got := normalizedCompound(text); got != want {
			t.Errorf("compound %q: %s", text, got)
		}
	}
}

func TestTimingSourceRunReplacement(t *testing.T) {
	r, o := fieldRoster(), sessionFixture()
	o.source = liveTimingUpdateSourceSnapshot
	s, e, err := reduceTimingAppData(timingAppState{}, o, []byte(`{"Lines":{"1":{"Stints":[{"Compound":"Soft","TotalLaps":0,"New":"true","StartLaps":0}]}}}`), r)
	want := driverGauge(o, r.drivers[0], "tyre.age", "{lap}", 0)
	if err != nil || !reflect.DeepEqual(e.metrics, []metricEffect{want}) || stintCompound(s, 1, 1, 1, 3) != "soft" {
		t.Fatal(s, e, err)
	}
	o.source = liveTimingUpdateSourceFeed
	next, e, err := reduceTimingAppData(s, o, []byte(`{"Lines":{"1":{"Stints":{"1":{"TotalLaps":0,"LapNumber":4,"Compound":"Wet","TyresNotChanged":"0"}}}}}`), r)
	want.timestamp = o.publication
	if err != nil || !reflect.DeepEqual(e.metrics, []metricEffect{want}) || stintCompound(next, 1, 2, 4, 5) != "wet" || stintCompound(next, 1, 2, 1, 2) != "unknown" || len(s.lines[1].runs) != 1 {
		t.Fatal(next, e, err)
	}
	o.source = liveTimingUpdateSourceSnapshot
	bad, e, err := reduceTimingAppData(next, o, []byte(`{"Lines":{"1":{"Stints":{"0":{"TotalLaps":false}}}}}`), r)
	if err != nil || !bad.unsynchronized || !e.empty() || !reflect.DeepEqual(bad.lines, next.lines) || stintCompound(bad, 1, 1, 1, 1) != "unknown" {
		t.Fatal(bad, e, err)
	}
	o.source = liveTimingUpdateSourceFeed
	bad, e, err = reduceTimingAppData(bad, o, []byte(`{"Lines":{"1":{"Stints":{"2":{"TotalLaps":4}}}}}`), r)
	if err != nil || !e.empty() || !reflect.DeepEqual(bad.lines, next.lines) {
		t.Fatal(bad, e, err)
	}
}

func TestTimingStatsRankedValues(t *testing.T) {
	o, r := sessionFixture(), fieldRoster()
	o.source = liveTimingUpdateSourceSnapshot
	s, e, err := reduceTimingStats(timingStatsState{}, o, []byte(`{"Lines":{"1":{"BestSectors":[{"Value":"20.001"},{"Value":"25.000"},{"Value":"19.100"}],"BestSpeeds":{"I1":{"Value":"299"},"I2":{"Value":"300"},"FL":{"Value":"301"},"ST":{"Value":"302"}}}}}`), r)
	var want []metricEffect
	for i, n := range []int64{20001000000, 25e9, 19100000000} {
		want = append(want, durationGauge(o, r.drivers[0], "best_sector_time", n, intAttr("f1.sector.number", int64(i+1))))
	}
	for i, k := range []string{"i1", "i2", "fl", "st"} {
		want = append(want, driverGauge(o, r.drivers[0], "best_speed_trap", "km/h", int64(299+i), strAttr("f1.speed_trap.location", k)))
	}
	if err != nil || !reflect.DeepEqual(e.metrics, want) {
		t.Fatal(e, err)
	}
	o.source = liveTimingUpdateSourceFeed
	next, e, err := reduceTimingStats(s, o, []byte(`{"Lines":{"1":{"BestSectors":{"2":{"Position":2}},"BestSpeeds":{"I1":{"Value":"288"},"ST":{"Value":""}}}}}`), r)
	if err != nil || !reflect.DeepEqual(e.metrics, []metricEffect{driverGauge(o, r.drivers[0], "best_speed_trap", "km/h", 288, strAttr("f1.speed_trap.location", "i1"))}) || next.lines[1][6] != 0 || s.lines[1][6] != 302 {
		t.Fatal(next, e, err)
	}
}

func timingPatch(t *testing.T, s timingState, o sessionObservation, sec int64, lines string) (timingState, liveTimingEffects) {
	t.Helper()
	o.publication = fieldTime(sec)
	o.monotonic = time.Duration(sec) * time.Second
	next, e, err := reduceTimingData(s, o, json.RawMessage(`{"Lines":{`+lines+`}}`), fieldRoster())
	if err != nil {
		t.Fatal(err)
	}
	return next, e
}
func timingStart(o sessionObservation) timingState {
	s, _ := applySessionStatusTransition(timingState{}, o, sessionStatusTransition{utc: fieldTime(1), current: "Started"}, fieldRoster())
	return s
}

func TestTimingDataRegistryGateAndMalformedLines(t *testing.T) {
	o := sessionFixture()
	s, _ := timingPatch(t, timingStart(o), o, 10, `"1":{"Position":"1","Stopped":false},"7":{"Stopped":false}`)
	if !s.runningKnown {
		t.Fatal("missing initial count")
	}
	for _, r := range []driverRegistryState{{}, fieldRoster()} {
		r.synchronized = false
		const good = `{"Lines":{"1":{"Position":"2","Stopped":true}}}`
		next, e, err := reduceTimingData(s, o, []byte(good), r)
		if err != nil || !e.empty() || next.runningKnown || !reflect.DeepEqual(next.drivers, s.drivers) {
			t.Fatal(next, e, err)
		}
		snap := o
		snap.source = liveTimingUpdateSourceSnapshot
		for _, bad := range []string{`{"Lines":[]}`, `{"Lines":{"1":null}}`, `{"Lines":{"1":{"RacingNumber":"7"}}}`, `{"Lines":{"1":{"Stopped":3}}}`} {
			next, e, err = reduceTimingData(s, snap, []byte(bad), r)
			if err != nil || !e.empty() || !next.invalidSnapshot || !next.unsynchronized || next.runningKnown {
				t.Fatal(bad, next, e, err)
			}
		}
		next, e, err = reduceTimingData(next, snap, []byte(good), r)
		if err != nil || !e.empty() || next.unsynchronized || next.invalidSnapshot || next.drivers[1].position != 0 || next.drivers[1].stopped != 0 {
			t.Fatal(next, e, err)
		}
	}
	for _, bad := range []string{`null`, `{"RacingNumber":"7","Stopped":true}`} {
		next, e := timingPatch(t, s, o, 11, `"1":`+bad+`,"7":{"Stopped":true}`)
		if next.runningKnown || next.drivers[1].stopped != 0 || next.drivers[1].position != s.drivers[1].position || !e.empty() {
			t.Fatal(next, e)
		}
		next, e = timingPatch(t, next, o, 12, `"1":{"Position":"2"}`)
		if next.runningKnown || len(e.metrics) != 1 || e.metrics[0].name != "f1.driver.position" {
			t.Fatal(e)
		}
		_, e = timingPatch(t, next, o, 13, `"1":{"Stopped":false}`)
		if len(e.metrics) != 1 || e.metrics[0].name != "f1.session.cars_running" || e.metrics[0].intVal != 1 {
			t.Fatal(e)
		}
	}
}

func TestSnapshotPhaseClosesOnlyObservedSpans(t *testing.T) {
	o := sessionFixture()
	o.identity.sessionType = "qualifying"
	s, _ := timingPatch(t, timingStart(o), o, 2, `"1":{"SessionPart":1,"NumberOfLaps":1}`)
	s, _ = timingPatch(t, s, o, 20, `"1":{"NumberOfLaps":2,"LastLapTime":{"Value":"18.000"},"Sectors":[{"Value":"6.000"}]}`)
	o.source = liveTimingUpdateSourceSnapshot
	s, e := timingPatch(t, s, o, 100, `"1":{"SessionPart":2}`)
	if len(e.metrics) != 0 || len(s.windows) != 0 || len(e.spans) != 4 || s.drivers[1].pending.span.lapNumber != 0 {
		t.Fatal(e)
	}
	for i, k := range []spanKindType{spanLap, spanLap, spanStint, spanQualifyingPhase} {
		if e.spans[i].kind != k || e.spans[i].endTime != fieldTime(20) {
			t.Fatal(e.spans[i])
		}
	}
}

func TestColdStintRequiresSourceOrdinal(t *testing.T) {
	o := sessionFixture()
	o.source = liveTimingUpdateSourceSnapshot
	s, _ := applySessionStatusTransition(timingState{}, o, sessionStatusTransition{current: "Started", utc: fieldTime(1)}, fieldRoster())
	s, _ = timingPatch(t, s, o, 100, `"1":{"NumberOfLaps":5}`)
	o.source = liveTimingUpdateSourceFeed
	s, _ = timingPatch(t, s, o, 110, `"1":{"NumberOfLaps":6}`)
	d := s.drivers[1]
	if d.ordinal != 0 || d.root.kind == "" || d.stint.kind != "" || d.lap.span.kind != "" {
		t.Fatal(d)
	}
	d.newStint = true
	s.drivers[1] = d
	s, _ = timingPatch(t, s, o, 120, `"1":{"NumberOfLaps":7,"LastLapTime":{"Value":"10.000"}}`)
	s, e := timingPatch(t, s, o, 125, `"1":{}`)
	if s.drivers[1].ordinal != 0 || len(e.spans) != 0 || len(e.metrics) != 1 || e.metrics[0].name != "f1.driver.last_lap_time" {
		t.Fatal(e)
	}
}

func TestObservedLapDRSHistogram(t *testing.T) {
	o := sessionFixture()
	s, _ := timingPatch(t, timingStart(o), o, 10, `"1":{"NumberOfLaps":1}`)
	feed := carReducer(t, o, s.deltaStart)
	s.cars, _ = feed(s.cars, carRow(fieldTime(10), fieldCar(1, `"45":12`)), carRow(fieldTime(10).Add(125*time.Millisecond), fieldCar(1, `"45":0`)), carRow(fieldTime(11), fieldCar(1, `"45":0`)))
	s, _ = timingPatch(t, s, o, 11, `"1":{"NumberOfLaps":2,"LastLapTime":{"Value":"1.000"}}`)
	s, _ = timingPatch(t, s, o, 16, `"1":{}`)
	_, e := flushTimingTerminal(s, o, fieldTime(17), "session_end")
	for _, m := range e.metrics {
		if m.name != "f1.field.lap.drs_active_time_ratio" {
			continue
		}
		if m.kind != metricExplicitHistogramDelta || m.unit != "1" || len(m.attrs) != 0 || m.count != 1 || m.sum != .125 || m.min != .125 || m.max != .125 || !slices.Equal(m.bounds, []float64{0, .01, .02, .05, .1, .15, .2, .3, .5, 1}) || !slices.Equal(m.bucketCounts, []uint64{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0}) {
			t.Fatal(m)
		}
		return
	}
	t.Fatal("no lap DRS histogram", e)
}
func TestWaterfallLapBuffer(t *testing.T) {
	o := sessionFixture()
	s := timingStart(o)
	s, _ = timingPatch(t, s, o, 10, `"1":{"NumberOfLaps":1,"InPit":false}`)
	s, _ = timingPatch(t, s, o, 30, `"1":{"Sectors":{"0":{"Value":"20.000"}}}`)
	s, _ = timingPatch(t, s, o, 50, `"1":{"Sectors":{"1":{"Value":"20.000"}}}`)
	s, e := timingPatch(t, s, o, 70, `"1":{"NumberOfLaps":2,"InPit":true,"NumberOfPitStops":1,"LastLapTime":{"Value":"1:00.000","PersonalFastest":true},"Speeds":{"ST":{"Value":"310"},"FL":{"Value":"300"}}}`)
	d := s.drivers[1]
	if len(e.spans) != 1 || e.spans[0].kind != spanStint || d.pending.span.stintOrdinal != 1 || d.lap.span.stintOrdinal != 2 || d.pending.trap[2] != 300 || d.lap.trap[3] != 310 {
		t.Fatal(e, d)
	}
	s, e = timingPatch(t, s, o, 74, `"1":{"Sectors":{"2":{"Value":"20.000"}},"InPit":false}`)
	if len(e.spans) != 0 || s.drivers[1].visits[0].out != 2 {
		t.Fatal(e, s.drivers[1])
	}
	before := s
	s, e = timingPatch(t, s, o, 75, `"7":{}`)
	if len(e.spans) != 4 || len(before.drivers[1].pending.span.events) != 1 || s.drivers[1].pending.span.lapNumber != 0 {
		t.Fatal(e, s)
	}
	for i, sp := range e.spans[:3] {
		if sp.sectorNumber != int64(i+1) || sp.kind != spanSector || sp.startTime != fieldTime(int64(10+20*i)) || sp.endTime != fieldTime(int64(30+20*i)) || sp.timeQuality != "estimated" {
			t.Fatal(sp)
		}
	}
	l := e.spans[3]
	if l.events[len(l.events)-1].timeQuality != "estimated" {
		t.Fatal("lap event lost estimated boundary quality", l.events)
	}
	if l.events[len(l.events)-1].timeQuality != "estimated" {
		t.Fatal(l.events)
	}
	if l.kind != spanLap || l.startTime != fieldTime(10) || l.endTime != fieldTime(70) || !slices.Contains(l.attrs, intAttr("f1.lap.speed_trap.fl_kph", 300)) || len(l.events) != 2 || l.events[0].name != "f1.pit.entry" || l.events[1].name != "f1.personal_best" {
		t.Fatal(l)
	}
	if summaries := s.control.summaries[1]; !reflect.DeepEqual(summaries, []lapRepairSummary{{1, 1, 2, 60000, "", true, true}}) {
		t.Fatal(summaries)
	}
	_, terminal := flushTimingTerminal(s, o, fieldTime(76), "shutdown")
	all := append(e.metrics, terminal.metrics...)
	found := false
	for _, m := range all {
		if m.name == "f1.driver.lap_time" {
			found = true
			if m.count != 1 || m.sum != 60 || m.min != 60 || m.max != 60 || m.startTime != fieldTime(60) || m.timestamp != fieldTime(70) || !slices.Contains(m.attrs, strAttr("f1.tyre.compound", "unknown")) {
				t.Fatal(m)
			}
		}
	}
	if !found {
		t.Fatal(all)
	}
}

func TestTimingRepairAndConflict(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		sector      [3]int64
		prior       int64
		quality     string
		accepted    bool
	}{
		{"missing sector", "1:00.000", [3]int64{20e9, 0, 20e9}, 20e9, "reported", true},
		{"explicit empty", "", [3]int64{20e9, 20e9, 20e9}, 0, "reconstructed", true},
		{"conflict", "59.999", [3]int64{20e9, 20e9, 20e9}, 0, "conflict", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := sessionFixture()
			s := timingStart(o)
			d := s.drivers[1]
			d.openLap(o, "", 3, fieldTime(10))
			d.pending = d.lap
			d.pending.span.endTime = fieldTime(70)
			d.pending.reported, _ = timingDuration(tc.value)
			d.pending.empty = tc.value == ""
			d.pending.valuePresent = true
			d.pending.sector = tc.sector
			d.lastSectors[1] = tc.prior
			var e liveTimingEffects
			s.finishLap(&d, o, &e)
			if len(e.spans) != 4 || !slices.Contains(e.spans[3].attrs, strAttr("f1.timing.value_quality", tc.quality)) {
				t.Fatal(e)
			}
			if tc.prior != 0 && !slices.Contains(e.spans[1].attrs, strAttr("f1.timing.value_quality", "reconstructed")) {
				t.Fatal(e.spans[1])
			}
			if got := slices.ContainsFunc(e.metrics, func(m metricEffect) bool { return m.name == "f1.driver.last_lap_time" }); got != tc.accepted {
				t.Fatal(e.metrics)
			}
		})
	}
}

func TestTimingClassificationFreshness(t *testing.T) {
	o := sessionFixture()
	s := timingStart(o)
	s.currentSessionLap = 1
	s, e := timingPatch(t, s, o, 10, `"1":{"Position":"1","GapToLeader":"LAP 1","Stopped":false},"7":{"Position":"2","GapToLeader":"+1.001","IntervalToPositionAhead":{"Value":"+1.001"},"Stopped":true}`)
	if !s.runningKnown || s.running != 1 || len(e.metrics) != 6 {
		t.Fatal(e, s)
	}
	o.monotonic = 20 * time.Second
	s, e = tickTiming(s, o)
	if len(e.metrics) != 5 {
		t.Fatal(e)
	}
	for _, m := range e.metrics {
		if m.timestamp != o.observed || !m.startTime.IsZero() {
			t.Fatal(m)
		}
	}
	o.monotonic = 30 * time.Second
	_, e = tickTiming(s, o)
	if len(e.metrics) != 2 {
		t.Fatal(e)
	}
	s, _ = timingPatch(t, s, o, 31, `"7":{"Position":"1","Stopped":false},"1":{"Position":"2"}`)
	if s.running != 2 || s.drivers[7].elapsed[1][1].ref != 0 || s.drivers[1].root.events[1].name != "f1.position.changed" {
		t.Fatal(s)
	}
	// A malformed known-driver line suppresses Cars Running until explicit Stopped
	// recovery while preserving valid prior position, pit visit, and elapsed state.
	s, _ = timingPatch(t, s, o, 32, `"1":{"InPit":false}`)
	s, _ = timingPatch(t, s, o, 33, `"1":{"InPit":true,"NumberOfPitStops":1,"GapToLeader":"+2.000"}`)
	s, e = timingPatch(t, s, o, 34, `"1":null,"7":{"Stopped":true}`)
	d1 := s.drivers[1]
	if s.runningKnown || d1.position != 2 || d1.inPit != 2 || d1.visit.number != 1 || !d1.newStint || len(d1.elapsed[0]) == 0 {
		t.Fatal("malformed line failed to suppress Cars Running or erased prior state", s.runningKnown, d1)
	}
	s, _ = timingPatch(t, s, o, 35, `"1":{"Position":"2"}`)
	if s.runningKnown {
		t.Fatal("unrelated omission restored Cars Running without explicit Stopped")
	}
	s, e = timingPatch(t, s, o, 36, `"1":{"Stopped":false,"InPit":false}`)
	if !s.runningKnown || s.running != 1 || d1.visit.number != 1 || s.drivers[1].root.events[len(s.drivers[1].root.events)-1].name != "f1.pit.exit" || len(e.metrics) != 1 || e.metrics[0].name != "f1.session.cars_running" {
		t.Fatal("explicit Stopped recovery or preserved pit exit failed", s, e)
	}
}

func TestPitReportsAreIndependent(t *testing.T) {
	o, r := sessionFixture(), fieldRoster()
	s := timingStart(o)
	s, _ = timingPatch(t, s, o, 10, `"1":{"InPit":false,"NumberOfLaps":1}`)
	s, _ = timingPatch(t, s, o, 20, `"1":{"InPit":true,"NumberOfPitStops":1,"NumberOfLaps":2}`)
	s, _ = timingPatch(t, s, o, 21, `"1":{"InPit":false}`)
	o.publication = fieldTime(22)
	s, _, _ = reducePitLaneTimeCollection(s, o, []byte(`{"PitTimes":{"1":{"Lap":"2","Duration":"21.4"}}}`), r)
	stop := []byte(`{"RacingNumber":"1","Lap":"2","PitStopTime":"2.3","PitLaneTime":"21.499"}`)
	s, _, _ = reducePitStop(s, o, stop, r)
	replay, _, _ := reducePitStop(s, o, stop, r)
	if len(s.windows) != 2 || !reflect.DeepEqual(s.windows, replay.windows) || len(s.drivers[1].lap.span.events) != 2 {
		t.Fatal(s)
	}
	ev := s.drivers[1].lap.span.events[1]
	if ev.name != "f1.pit.stop" || ev.timestamp != o.publication || !slices.Contains(ev.attrs, floatAttr("f1.pit.lane_duration", 21.499)) {
		t.Fatal(ev)
	}
	for _, m := range s.windows {
		if len(m.attrs) != 0 || m.startTime != fieldTime(20) || m.timestamp != fieldTime(30) || m.count != 1 {
			t.Fatal(m)
		}
	}
	if laneCompatible(21400000000, 21500000000) {
		t.Fatal("rounded a neighbouring tenth")
	}
	o.source = liveTimingUpdateSourceSnapshot
	s, _, _ = reducePitStopSeries(timingState{}, o, []byte(`{"PitTimes":{"1":[{"Timestamp":"2026-01-01T00:00:00Z","PitStop":`+string(stop)+`}]}}`), r)
	o.source = liveTimingUpdateSourceFeed
	s, e, _ := reducePitStop(s, o, stop, r)
	if !e.empty() || len(s.windows) != 0 || len(s.pits.seen) != 2 {
		t.Fatal(s, e)
	}
}

func TestQualifyingPhaseAndRollback(t *testing.T) {
	o := sessionFixture()
	o.identity.sessionType = canonicalSessionTypeSprintQualifying
	s := timingStart(o)
	s, e := timingPatch(t, s, o, 10, `"1":{"SessionPart":1,"NumberOfLaps":1,"BestLapTimes":{"0":{"Value":"55.000"},"2":{"Value":"58.000"}},"BestLapTime":{"Value":"52.000","Lap":1},"Speeds":{"ST":{"Value":"300"}}}`)
	if d := s.drivers[1]; d.phaseSpan.phase != "sq1" || d.best["sq1"] != (bestCandidate{52e9, 1}) || d.best["sq3"].duration != 58e9 || len(e.metrics) != 3 {
		t.Fatal(d, e)
	}
	s, e = timingPatch(t, s, o, 12, `"1":{"BestLapTime":{"Value":"53.000"},"Speeds":{"ST":{"Value":"300"}}}`)
	d := s.drivers[1]
	if len(e.metrics) != 2 || d.best["sq1"].lap != 0 || len(d.lap.span.events) != 1 {
		t.Fatal(d, e)
	}
	want := eventAt("f1.personal_best.revised", fieldTime(12), floatAttr("f1.personal_best.previous_duration", 52), floatAttr("f1.personal_best.current_duration", 53), intAttr("f1.personal_best.previous_lap", 1))
	if !reflect.DeepEqual(d.lap.span.events[0], want) {
		t.Fatal(d.lap.span.events)
	}
	s, e = timingPatch(t, s, o, 20, `"1":{"SessionPart":2,"NumberOfLaps":1,"Speeds":{"I1":{"Value":"310"}}},"7":{"Speeds":{"ST":{"Value":"305"}}}`)
	if s.phase != "sq2" || len(e.spans) != 3 || len(e.metrics) != 1 || s.drivers[7].phaseSpan.kind != "" {
		t.Fatal(s, e)
	}
	s, e = applySessionStatusTransition(s, o, sessionStatusTransition{utc: fieldTime(25), current: "Finalised"}, fieldRoster())
	if !s.tracesEnded || len(e.spans) != 5 || e.spans[3].events[len(e.spans[3].events)-1].name != "f1.session.status.changed" {
		t.Fatal(e)
	}
}

func TestTimingColdSnapshotAndOmission(t *testing.T) {
	o := sessionFixture()
	o.source = liveTimingUpdateSourceSnapshot
	r := fieldRoster()
	series := sessionDataState{}
	series.history[sessionField] = statusHistory{current: "Aborted", watermark: fieldTime(12)}
	s, _ := applyTimingStatusSnapshot(timingState{}, o, series, sessionStatusMirror{status: "Aborted"}, r)
	b := normalizedLiveTimingBatch{source: o.source, observationTime: o.observed, requestedTopics: []string{"TimingData", "TimingAppData"}, presentTopics: []string{"TimingData", "TimingAppData"}, updates: []normalizedLiveTimingUpdate{
		{topic: "TimingData", payload: []byte(`{"Lines":{"1":{"NumberOfLaps":5}}}`)}, {topic: "TimingAppData", payload: []byte(`{"Lines":{"1":{"Stints":[{},{"Compound":"MEDIUM"}]}}}`)},
	}}
	s, e, err := reduceTimingBatch(s, o, b, r)
	if err != nil || !s.late || s.drivers[1].root.kind != "" || s.drivers[1].ordinal != 2 || len(e.spans) != 0 || e.metrics[0].timestamp != o.observed || b.updates[0].topic != "TimingData" {
		t.Fatal(s, e, err)
	}
	bad, e, err := reduceTimingData(s, o, []byte(`{"Lines":{"1":{"NumberOfLaps":6},"7":{"Stopped":null}}}`), r)
	if err != nil || !bad.unsynchronized || !e.empty() || !reflect.DeepEqual(bad.drivers, s.drivers) {
		t.Fatal(bad, e, err)
	}
	for _, tc := range []struct {
		raw []byte
		err error
	}{{[]byte("{"), errJSONSyntax}, {[]byte{255}, errJSONUTF8}, {[]byte("false"), nil}} {
		_, e, err := reduceTimingData(s, o, tc.raw, r)
		if err != tc.err || !e.empty() {
			t.Fatal(e, err)
		}
	}
	b.updates, b.presentTopics = nil, nil
	reset, _, err := reduceTimingBatch(bad, o, b, r)
	if err != nil || reset.unsynchronized || reset.drivers[1].current != 0 || len(reset.app.lines) != 0 {
		t.Fatal(reset, err)
	}
	o.source = liveTimingUpdateSourceFeed
	s, e = timingPatch(t, s, o, 101, `"1":{"NumberOfLaps":5}`)
	if s.drivers[1].root.startTime != fieldTime(101) || s.drivers[1].lap.span.kind != "" || !e.empty() {
		t.Fatal(e, s)
	}
	s, _ = timingPatch(t, s, o, 110, `"1":{"NumberOfLaps":6,"LastLapTime":{"Value":"54.000","OverallFastest":true}}`)
	s, e = timingPatch(t, s, o, 115, `"7":{}`)
	if len(e.spans) != 0 || len(e.metrics) != 1 || e.metrics[0].name != "f1.driver.last_lap_time" || len(s.windows) != 1 || !slices.Contains(s.windows[0].attrs, strAttr("f1.tyre.compound", "medium")) {
		t.Fatal(s, e)
	}
}

func TestTimingDisconnectAndGrace(t *testing.T) {
	o := sessionFixture()
	s := timingStart(o)
	s, _ = timingPatch(t, s, o, 10, `"1":{"InPit":false}`)
	s, e := timingPatch(t, s, o, 11, `"1":{"NumberOfPitStops":2,"InPit":true}`)
	if len(e.metrics) != 1 || e.metrics[0].kind != metricIntSumCumulative || !e.metrics[0].monotonic || e.metrics[0].startTime != fieldTime(1) || e.metrics[0].intVal != 2 {
		t.Fatal(e)
	}
	s.position = positionState{history: map[int64][]positionSample{1: {{at: fieldTime(11), available: true, xyz: [3]int64{10, 20, 30}}}}}
	s, _ = disconnectTiming(s)
	s, _ = disconnectTiming(s)
	ev := s.drivers[1].root.events
	if len(ev) != 3 || len(ev[2].attrs) != 2 {
		t.Fatal(ev)
	}
	if len(ev) != 3 || ev[2].timestamp != fieldTime(11) || !slices.Contains(ev[2].attrs, strAttr("f1.pit.visit.incomplete_reason", "disconnect")) {
		t.Fatal(ev)
	}
	s, e = applySessionStatusTransition(s, o, sessionStatusTransition{utc: fieldTime(15), current: "Finalised"}, fieldRoster())
	if s.tracesEnded || !e.empty() {
		t.Fatal(s, e)
	}
	o.monotonic = 5*time.Minute - 1
	s, e = tickTiming(s, o)
	if s.tracesEnded || !e.empty() {
		t.Fatal(e)
	}
	o.monotonic++
	s, e = tickTiming(s, o)
	if !s.tracesEnded || len(e.spans) != 2 || e.spans[0].status.code != spanStatusUnset || e.spans[0].endTime != fieldTime(15) || e.spans[0].timeQuality != "estimated" {
		t.Fatal(s, e)
	}
}

func TestTerminalBoundsBelongToEachDriver(t *testing.T) {
	o := sessionFixture()
	s := timingStart(o)
	s, _ = timingPatch(t, s, o, 10, `"1":{"NumberOfLaps":1}`)
	s = attachRaceControlEvents(s, []controlEventCandidate{{driver: 1, event: eventAt("notice", fieldTime(18))}}, nil, nil)
	for _, tc := range []struct {
		reason     string
		at, second int64
	}{{"session_replacement", 100, 1}, {"shutdown", 100, 1}, {"session_end", 14, 14}} {
		_, e := flushTimingTerminal(s, o, fieldTime(tc.at), tc.reason)
		for _, span := range e.spans {
			want := int64(18)
			if span.driverNumber == 7 {
				want = tc.second
			}
			if span.endTime != fieldTime(want) || span.kind == spanDriverSession && span.timeQuality != "estimated" {
				t.Fatal(tc, span)
			}
		}
		if len(e.spans) != 4 {
			t.Fatal(e.spans)
		}
	}
}

func TestTimingPitWindowWatermark(t *testing.T) {
	o := sessionFixture()
	s := timingStart(o)
	r := fieldRoster()
	apply := func(at time.Time, payload string) liveTimingEffects {
		t.Helper()
		o.publication = at
		var e liveTimingEffects
		var err error
		s, e, err = reducePitStop(s, o, []byte(payload), r)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	apply(fieldTime(20).Add(-1), `{"RacingNumber":"1","Lap":"1","PitStopTime":"2.0"}`)
	if e := apply(fieldTime(20), `{"RacingNumber":"7","Lap":"1","PitStopTime":"3.0"}`); !e.empty() {
		t.Fatal(e)
	}
	e := apply(fieldTime(20).Add(1), `{"RacingNumber":"1","Lap":"2","PitStopTime":"5.0"}`)
	if len(e.metrics) != 1 {
		t.Fatal(e)
	}
	m := e.metrics[0]
	if m.startTime != fieldTime(10) || m.timestamp != fieldTime(20) || m.sum != 5 || m.count != 2 || m.min != 2 || m.max != 3 {
		t.Fatal(m)
	}
	if e := apply(fieldTime(19), `{"RacingNumber":"1","Lap":"3","PitStopTime":"4.0"}`); !e.empty() || len(s.windows) != 1 {
		t.Fatal(s, e)
	}
	_, e = flushTimingTerminal(s, o, fieldTime(45), "shutdown")
	if len(e.metrics) != 1 || e.metrics[0].sum != 5 || e.metrics[0].timestamp != fieldTime(20).Add(1) {
		t.Fatal(e)
	}
}

func TestPendingLapEventOwnerAndCoordinateAge(t *testing.T) {
	o := sessionFixture()
	s := timingStart(o)
	s, _ = timingPatch(t, s, o, 10, `"1":{"NumberOfLaps":1}`)
	s, _ = timingPatch(t, s, o, 60, `"1":{"NumberOfLaps":2,"LastLapTime":{"Value":"50.000"}}`)
	s.position = positionState{history: map[int64][]positionSample{1: {{at: fieldTime(61), xyz: [3]int64{10, 20, -30}, available: true}}}}
	s = applyTrackStatusTransition(s, trackStatusTransition{utc: fieldTime(61), current: trackStatus("Yellow")})
	event := eventAt("f1.lap_time.deleted", fieldTime(62), intAttr("f1.lap.number", 1), strAttr("f1.session.phase", "none"))
	s = attachRaceControlEvents(s, []controlEventCandidate{{driver: 1, event: event}}, nil, nil)
	d := s.drivers[1]
	if len(d.pending.span.events) != 1 || !slices.Contains(d.pending.span.events[0].attrs, floatAttr("f1.position.sample_age", 1)) || len(d.lap.span.events) != 1 || d.lap.span.events[0].name != "f1.track_status.changed" {
		t.Fatal(d)
	}
	for _, dt := range []time.Duration{-1, 0, 1} {
		o.publication = fieldTime(65).Add(dt)
		next, e, err := reduceTimingData(s, o, []byte(`{"Lines":{}}`), fieldRoster())
		if err != nil {
			t.Fatal(err)
		}
		if dt < 0 {
			if len(e.spans) != 0 || next.drivers[1].pending.span.kind == "" {
				t.Fatal(e)
			}
			continue
		}
		if len(e.spans) != 1 || e.spans[0].events[0].name != event.name {
			t.Fatal(e)
		}
		next = attachRaceControlEvents(next, []controlEventCandidate{{driver: 1, event: eventAt("f1.blue_flag.shown", fieldTime(62).Add(1))}}, nil, nil)
		x := next.drivers[1].lap.span.events[1]
		if len(x.attrs) != 0 {
			t.Fatal(x)
		}
	}
}

func TestEventOwnerFallbackAndDisconnectPosition(t *testing.T) {
	o := sessionFixture()
	s := timingStart(o)
	d := s.drivers[1]
	d.stint = spanAt(spanStint, o, d, fieldTime(2))
	d.stint.stintOrdinal = 1
	for _, tc := range []struct {
		name  string
		owner spanKindType
	}{{"f1.pit.stop", spanStint}, {"f1.blue_flag.shown", spanDriverSession}, {"f1.race_control.driver_notice", spanDriverSession}, {"f1.position.changed", spanDriverSession}} {
		driver := d
		_, ok := driver.attach(eventAt(tc.name, fieldTime(4)), positionState{})
		if !ok || (len(driver.root.events) > len(d.root.events)) != (tc.owner == spanDriverSession) || (len(driver.stint.events) == 1) != (tc.owner == spanStint) {
			t.Fatal(tc, driver)
		}
	}
	d.visit.number, d.visit.entry = 1, fieldTime(4)
	s.drivers[1] = d
	s.position.history = map[int64][]positionSample{1: {{at: fieldTime(4), available: true, xyz: [3]int64{10, 20, 30}}}}
	s, _ = disconnectTiming(s)
	e := s.drivers[1].stint.events
	if len(e) != 1 || e[0].name != "f1.pit.visit.incomplete" || len(e[0].attrs) != 2 {
		t.Fatal(e)
	}
}
