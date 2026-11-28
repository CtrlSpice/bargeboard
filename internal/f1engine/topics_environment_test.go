package f1engine

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWeatherSnapshotAndSparseUpdates(t *testing.T) {
	o := sessionFixture()
	o.source = Snapshot
	s, e, err := reduceWeatherData(weatherState{}, o, []byte(`{"AirTemp":"-1.7","TrackTemp":"0.0","Humidity":"33.5","Pressure":"759.9","WindSpeed":"0.0","WindDirection":"359","Rainfall":"0"}`))
	if err != nil || len(e.metrics) != 7 || len(e.logs) != 0 {
		t.Fatal(e, err)
	}
	for i, k := range []string{"air_temperature", "track_temperature", "relative_humidity", "air_pressure", "wind_speed", "wind_direction", "rainfall_detected"} {
		m := e.metrics[i]
		if m.name != "f1.session.weather."+k || m.unit != []string{"Cel", "Cel", "%", "hPa", "m/s", "deg", "1"}[i] || m.timestamp != o.observed || !m.startTime.IsZero() || len(m.attrs) != 0 || m.identity != o.Identity {
			t.Fatal(m)
		}
		if i < 5 {
			if m.kind != metricDoubleGauge || m.floatVal != []float64{-1.7, 0, 33.5, 759.9, 0}[i] {
				t.Fatal(m)
			}
		} else if m.kind != metricIntGauge || m.intVal != []int64{359, 0}[i-5] {
			t.Fatal(m)
		}
	}
	base := s
	o.source = Feed
	o.publication = o.observed
	o.monotonic = time.Second
	s, e, err = reduceWeatherData(s, o, []byte(`{"Rainfall":false}`))
	if !e.empty() || err != nil || !s.baseline || s.fields != base.fields {
		t.Fatal(s, e, err)
	}
	o.publication = o.publication.Add(1)
	s, e, err = reduceWeatherData(s, o, []byte(`{"Humidity":"100.1","Rainfall":"1"}`))
	want := recordLog(o, "f1.weather.rainfall.changed", o.publication, "publication_time", []keyValue{boolAttr("f1.weather.rainfall_detected", true), boolAttr("f1.weather.previous_rainfall_detected", false)})
	if err != nil || !reflect.DeepEqual(e.logs, []logEffect{want}) || len(e.metrics) != 1 || !base.baseline || len(base.seen) != 0 {
		t.Fatal(e, err)
	}
	if !tickWeather(s, 125*time.Second-1).fields[0].fresh || tickWeather(s, 125*time.Second).fields[0].fresh || !tickWeather(s, 125*time.Second).baseline || tickWeather(s, 126*time.Second).baseline {
		t.Fatal("per-field expiry")
	}
	// Restore an older baseline and replay the already consumed transition.
	recovered := omitWeather(disconnectWeather(s))
	o.source = Snapshot
	recovered, _, _ = reduceWeatherData(recovered, o, []byte(`{"Rainfall":"0"}`))
	o.source = Feed
	_, e, _ = reduceWeatherData(recovered, o, []byte(`{"Rainfall":"1"}`))
	if len(e.metrics) != 1 || len(e.logs) != 0 {
		t.Fatal(e)
	}
	o.publication = o.publication.Add(1)
	s, e, _ = reduceWeatherData(s, o, []byte(`{"Rainfall":"0"}`))
	if len(e.logs) != 1 {
		t.Fatal(e)
	}
	o.publication = o.publication.Add(1)
	s, e, _ = reduceWeatherData(s, o, []byte(`{"Rainfall":null}`))
	if !e.empty() || s.baseline {
		t.Fatal(s, e)
	}
	s, e, _ = reduceWeatherData(s, o, []byte(`{"Rainfall":"1"}`))
	if len(e.logs) != 0 || !s.baseline {
		t.Fatal(e)
	}
	o.source = Snapshot
	bad, e, _ := reduceWeatherData(s, o, []byte(`{"AirTemp":"12.3","Rainfall":2}`))
	if !bad.unsynchronized || bad.fields != s.fields || !e.empty() {
		t.Fatal(bad, e)
	}
	o.source = Feed
	_, e, _ = reduceWeatherData(bad, o, []byte(`{"AirTemp":"44.4"}`))
	if !e.empty() {
		t.Fatal(e)
	}
	o.source = Snapshot
	bad, e, _ = reduceWeatherData(bad, o, []byte(`{}`))
	if bad.unsynchronized || bad.fields != [7]weatherReading{} || !e.empty() {
		t.Fatal(bad, e)
	}
}
func TestWeatherLexicalDomain(t *testing.T) {
	for _, s := range []string{"", " 1.2", "1.2 ", "-0.0", "+0.1", "00.1", "-.1", "1.", "1.20", "1e2", "1,0", "NaN", "Inf", "１.０", "922337203685477580.8", "-922337203685477580.9"} {
		if _, ok := weatherValue(0, s); ok {
			t.Fatal(s)
		}
	}
	if v, ok := weatherValue(0, "-922337203685477580.8"); !ok || v != -1<<63 {
		t.Fatal(v, ok)
	}
	for _, tc := range []struct {
		i  int
		s  string
		ok bool
	}{{2, "0.0", true}, {2, "100.0", true}, {2, "100.1", false}, {3, "0.0", false}, {4, "-0.1", false}, {5, "0", true}, {5, "-1", false}, {5, "360", false}, {6, "01", false}, {6, "2", false}} {
		if _, ok := weatherValue(tc.i, tc.s); ok != tc.ok {
			t.Fatal(tc)
		}
	}
}
func radioPatch(t *testing.T, s teamRadioState, o sessionObservation, captures string) (teamRadioState, Effects) {
	t.Helper()
	s, e, err := reduceTeamRadio(s, o, []byte(`{"Captures":`+captures+`}`), fieldRoster(), func(n int64, at time.Time) bool { return n == 1 && !at.Before(fieldTime(0)) })
	if err != nil {
		t.Fatal(err)
	}
	return s, e
}
func TestLiveRadioRejectsInitialFeedArray(t *testing.T) {
	o := sessionFixture()
	o.source = Feed
	const capture = `{"RacingNumber":"1","Path":"TeamRadio/ABC_1_20250101_000000.mp3"}`
	s, e := radioPatch(t, teamRadioState{}, o, `[`+capture+`]`)
	if !e.empty() || s.rows != nil || s.issues != recordShape {
		t.Fatal(s, e)
	}
	_, e = radioPatch(t, s, o, `{"0":`+capture+`}`)
	if len(e.logs) != 1 {
		t.Fatal(e)
	}
}

func TestRadioIdentityAndMetadata(t *testing.T) {
	o := sessionFixture()
	o.source = Snapshot
	s, e, err := reduceTeamRadio(teamRadioState{}, o, []byte(`{"_kf":true,"Captures":[null]}`), fieldRoster(), nil)
	if err != nil || s.unsynchronized || !e.empty() {
		t.Fatal(s, e, err)
	}
	o.source = Feed
	const path = "TeamRadio/ABCXYZ23_1_12345678_765432.mp3"
	s, e = radioPatch(t, s, o, `{"1":{"Path":"`+path+`"},"0":{"Path":"ignored"}}`)
	if !e.empty() {
		t.Fatal(e)
	}
	base := s
	s, e = radioPatch(t, s, o, `{"2":{"Path":"TeamRadio/ABC_9_00000000_000000.mp3","RacingNumber":"9"},"01":{},"1":{"RacingNumber":"1","Utc":"2025-01-01T00:00:00.1234567Z"}}`)
	if len(e.logs) != 2 || len(e.metrics)+len(e.spans) != 0 {
		t.Fatal(e)
	}
	l := e.logs[0]
	if !l.hasTrace || l.traceID != DeterministicTraceID(o.Identity, 1) || l.spanID != DeterministicRootSpanID(o.Identity, 1) || l.body != "f1.team_radio.available" || l.timestamp.Nanosecond() != 123456700 || l.timeQuality != "observed" || l.observedTimestamp != o.observed || !slices.Equal(l.attrs, []keyValue{intAttr("f1.team_radio.index", 1), strAttr("f1.team_radio.path", path), intAttr("f1.driver.number", 1), strAttr("f1.driver.acronym", "ABC")}) {
		t.Fatal(l)
	}
	l = e.logs[1]
	if l.hasTrace || len(l.attrs) != 2 || l.timestamp != o.publication || l.timeQuality != "publication_time" || base.rows[1] == nil {
		t.Fatal(l)
	}
	_, e = radioPatch(t, omitTeamRadio(disconnectTeamRadio(s)), o, `{"1":{"Path":"`+path+`","RacingNumber":"1"}}`)
	if !e.empty() {
		t.Fatal(e)
	}
	for _, p := range []string{"../" + path, path + "?q", strings.Replace(path, "_1_", "_7_", 1), strings.Replace(path, "XYZ23", "xyz23", 1), strings.Replace(path, ".mp3", ".mp4", 1)} {
		bad, e := radioPatch(t, base, o, `{"1":{"Path":"`+p+`","RacingNumber":"1"}}`)
		if !e.empty() || bad.rows[1] != nil {
			t.Fatal(p, e)
		}
	}
	for _, s := range []string{"2025-02-30T00:00:00Z", "2025-01-01T00:00:00+00:00", "2025-01-01T00:00:00.12345678Z"} {
		if recordUTC(s, true).valid {
			t.Fatal(s)
		}
	}
}
func TestRecordLimitsAndRecovery(t *testing.T) {
	o := sessionFixture()
	s := teamRadioState{rows: map[int64]*captureRecord{}}
	for i := int64(1); i <= 4096; i++ {
		s.rows[i] = nil
	}
	s.rows[2] = &captureRecord{number: "1", hasNumber: true}
	o.source = Snapshot
	bad, e := radioPatch(t, s, o, `[{}]`)
	if !bad.unsynchronized || bad.overflow || !e.empty() || len(bad.rows) != 4096 {
		t.Fatal("snapshot union")
	}
	o.source = Feed
	bad, e = radioPatch(t, bad, o, `{"2":{"Path":"TeamRadio/ABC_1_00000000_000000.mp3"}}`)
	if !e.empty() {
		t.Fatal("unsynchronized feed")
	}
	s, e = radioPatch(t, s, o, `{"0":{},"2":{"Path":"TeamRadio/ABC_1_00000000_000000.mp3"}}`)
	if !s.overflow || len(s.rows) != 4096 || len(e.logs) != 1 {
		t.Fatal("radio overflow")
	}
	state := raceControlState{}
	state.rows = map[int64]*controlRecord{9: {category: "other"}}
	state.rows[8192] = nil
	rc := controlPatch(t, state, o, `[`+strings.Repeat(`{},`, 4095)+`{}]`)
	if !rc.state.overflow || len(rc.state.rows) != 2 || rc.state.rows[9].category != "other" || !rc.effects.empty() {
		t.Fatal("feed replacement changed collection on overflow")
	}
}
func controlPatch(t *testing.T, s raceControlState, o sessionObservation, rows string) raceControlReduction {
	t.Helper()
	r, err := reduceRaceControlMessages(s, o, []byte(`{"Messages":`+rows+`}`), fieldRoster(), fieldTime(3), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestRaceControlSanitization(t *testing.T) {
	if m := sanitizeControl(" \x01 café\u200b\u0009\u202e🚗\u00a0 "); m.body != "café 🚗" || m.redacted || m.truncated {
		t.Fatal(m)
	}
	for _, term := range []string{"HtTp://", "hTtPs://", "Authorization", "access\tToken", "access_token", "access-token", "coOKie", "SecrEt", "signature", "API KEY", "api_key", "api-key"} {
		m := sanitizeControl(strings.Repeat("a", 4097) + term)
		if m.body != "[redacted-message]" || !m.redacted || m.truncated {
			t.Fatal(term, m)
		}
	}
	m := sanitizeControl(strings.Repeat("a", 4095) + "é")
	if len(m.body) != 4095 || !m.truncated {
		t.Fatal(len(m.body), m.truncated)
	}
}
func TestControlEventsAndTerminalFold(t *testing.T) {
	o := sessionFixture()
	const blue = `"Category":"Flag","Scope":"Driver","Flag":"BLUE","RacingNumber":"7","Message":"  blue ","Sector":2,"Lap":0`
	r := controlPatch(t, raceControlState{}, o, `{"10":{`+blue+`,"Utc":"1970-01-01T00:00:12.000"},"3":{`+blue+`,"Utc":"1970-01-01T00:00:10"},"7":{"Category":"CarEvent","RacingNumber":"1"},"2":{`+blue+`,"Utc":"1970-01-01T00:00:03"},"8":{`+blue+`,"Utc":"1970-01-01T00:00:13"}}`)
	if len(r.events) != 4 || len(r.effects.logs) != 4 || len(r.effects.metrics) != 0 {
		t.Fatal(r)
	}
	for j, i := range []int64{2, 3, 8, 10} {
		log := r.effects.logs[j]
		ev := r.events[j]
		if ev.driver != 7 || ev.event.name != "f1.blue_flag.shown" || log.timeQuality != "observed" || log.body != "blue" || !slices.Equal(log.attrs, []keyValue{intAttr("f1.race_control.index", i), strAttr("f1.race_control.category", "flag"), intAttr("f1.race_control.lap", 0), intAttr("f1.race_control.sector", 2), strAttr("f1.race_control.flag", "blue"), strAttr("f1.race_control.scope", "driver"), intAttr("f1.driver.number", 7), strAttr("f1.driver.acronym", "XYZ")}) {
			t.Fatal(log, ev)
		}
	}
	r = controlPatch(t, r.state, o, `{"3":{"Message":"corrected"},"7":{"Message":"api-key","Status":"No - Action","Sector":1.2},"12":{"Category":"SafetyCar","Message":"Notice"}}`)
	if len(r.events) != 1 || r.events[0].event.name != "f1.race_control.driver_notice" || len(r.effects.logs) != 2 || r.effects.logs[0].body != "[redacted-message]" || r.effects.logs[0].timestamp != o.publication || !slices.Contains(r.effects.logs[0].attrs, strAttr("f1.race_control.status", "no_action")) {
		t.Fatal(r)
	}
	state, e := foldBlueFlags(omitRaceControlMessages(r.state), o.Identity, fieldTime(3), fieldTime(12))
	if len(e.metrics) != 2 || len(state.blue) != 0 || len(r.state.blue) != 4 {
		t.Fatal(e)
	}
	for i, m := range e.metrics {
		if m.name != "f1.driver.blue_flags" || m.unit != "{flag}" || !m.monotonic || m.kind != metricIntSumDelta || m.intVal != []int64{2, 1}[i] || m.startTime != fieldTime([]int64{3, 10}[i]) || m.timestamp != fieldTime([]int64{10, 12}[i]) {
			t.Fatal(m)
		}
	}
}
func TestControlRepairCorrelation(t *testing.T) {
	o := sessionFixture()
	o.Identity = SessionInfoIdentity{2026, 42, canonicalSessionTypeQualifying, canonicalSessionNameQualifying}
	s := retainLapRepairSummary(raceControlState{}, lapRepairSummary{1, 4, 5, 60001, "q1", true, true})
	s = retainLapRepairSummary(s, lapRepairSummary{driver: 1, lap: 4, boundary: 5, phase: "q1"})
	const del = "CAR 1 (ABC) TIME 1:00.001 DELETED - TRACK LIMITS AT TURN 3 LAP 5 1:09:03"
	const rein = "CAR 1 (ABC) LAP 5 WILL BE REINSTATED"
	row := func(message string) string { return fmt.Sprintf(`{"Category":"Other","Message":%q}`, message) }
	o.source = Snapshot
	r := controlPatch(t, s, o, `[`+row(del)+`]`)
	if !r.effects.empty() || len(r.state.deleted) != 1 || len(s.deleted) != 0 {
		t.Fatal(r)
	}
	o.source = Feed
	r = controlPatch(t, r.state, o, `[`+row(del)+`,`+row(rein)+`,`+row(rein)+`,`+row(del)+`]`)
	if len(r.effects.logs) != 3 || len(r.events) != 2 || r.events[0].event.name != "f1.lap_time.reinstated" || !slices.Contains(r.events[0].event.attrs, intAttr("f1.race_control.related_index", 0)) || r.events[1].event.name != "f1.lap_time.deleted" || !slices.Contains(r.events[1].event.attrs, intAttr("f1.lap.number", 4)) {
		t.Fatal(r)
	}
	roots := map[int64]rootInterval{1: {fieldTime(1), o.publication}}
	for _, open := range []bool{false, true} {
		logs := slices.Clone(r.effects.logs)
		var timing timingState
		if open {
			timing = timingStart(o)
			timing.phase = "q1"
			timing, _ = timingPatch(t, timing, o, 10, `"1":{"NumberOfLaps":1}`)
		}
		attachRaceControlEvents(timing, r.events, logs, roots)
		for i, l := range logs {
			want := DeterministicLapSpanID(o.Identity, 1, "q1", 4)
			if i == 1 {
				want = DeterministicRootSpanID(o.Identity, 1)
			} else if open {
				want = DeterministicLapSpanID(o.Identity, 1, "q1", 1)
			}
			if !l.hasTrace || l.traceID != DeterministicTraceID(o.Identity, 1) || l.spanID != want {
				t.Fatal(i, open, l)
			}
		}
	}
	r = controlPatch(t, r.state, o, `{"5":{"Category":"Other","RacingNumber":"7","Message":"`+del+`"}}`)
	if len(r.events) != 0 || len(r.effects.logs) != 1 || len(r.effects.logs[0].attrs) != 2 {
		t.Fatal(r)
	}
	s = retainLapRepairSummary(s, lapRepairSummary{1, 4, 5, 60001, "q2", true, false})
	r = controlPatch(t, s, o, `[`+row(del)+`,`+row("CAR ABC TIME 1:00.001 DELETED - TRACK LIMITS AT TURN 2 (NEXT LAP) (Q2)")+`]`)
	if len(r.events) != 1 || !slices.Contains(r.events[0].event.attrs, strAttr("f1.session.phase", "q2")) {
		t.Fatal(r)
	}
	for i := int64(5); i < 260; i++ {
		s = retainLapRepairSummary(s, lapRepairSummary{driver: 1, lap: i})
	}
	if !s.repairOverflow[1] || len(s.summaries[1]) != 256 {
		t.Fatal("lap retention limit")
	}
	for _, message := range []string{del + " ", strings.Replace(del, "TURN 3", "TURN 03", 1), strings.Replace(del, "TURN 3", "TURN\u200b3", 1), strings.Replace(del, "1:00.001", "0:00.000", 1), strings.Replace(del, "1:00.001", "153722868:00.000", 1), strings.Replace(del, "1:09:03", "24:09:03", 1)} {
		if c := parseRepair(message); c.acronym != "" {
			t.Fatal(message, c)
		}
	}
}
func TestSemanticInputsAreNotInvariantErrors(t *testing.T) {
	o := sessionFixture()
	o.source = Snapshot
	for _, tc := range []struct {
		raw json.RawMessage
		err error
	}{{[]byte(`null`), nil}, {[]byte(`4`), nil}, {[]byte(`[]`), nil}, {[]byte(`{"x":`), ErrJSONSyntax}, {[]byte{0xff}, ErrJSONUTF8}} {
		w, _, ew := reduceWeatherData(weatherState{}, o, tc.raw)
		radio, _, er := reduceTeamRadio(teamRadioState{}, o, tc.raw, fieldRoster(), nil)
		rc, ec := reduceRaceControlMessages(raceControlState{}, o, tc.raw, fieldRoster(), time.Time{}, time.Time{})
		p, ep := reducePosition(positionState{}, o, tc.raw, fieldRoster())
		c, _, ed := reduceCarData(carDataState{}, o, tc.raw, fieldRoster(), fieldTime(0), nil)
		if ew != tc.err || er != tc.err || ec != tc.err || ep != tc.err || ed != tc.err || tc.err == nil && (!w.unsynchronized || !radio.unsynchronized || !rc.state.unsynchronized || !p.unsynchronized || !c.unsynchronized) {
			t.Fatal(string(tc.raw), ew, er, ec, ep, ed)
		}
	}
}

func TestRainfallCallbackCoalescing(t *testing.T) {
	o := sessionFixture()
	s, _, _ := reduceWeatherData(weatherState{}, o, []byte(`{"Rainfall":"0"}`))
	o.publication = o.publication.Add(time.Second)
	s, e, _ := reduceWeatherData(s, o, []byte(`{"Rainfall":"1"}`), []byte(`{"Rainfall":"0","WindDirection":"0"}`))
	if len(e.metrics) != 2 || e.metrics[1].intVal != 0 || len(e.logs) != 0 {
		t.Fatal(e)
	}
	s, e, _ = reduceWeatherData(s, o, []byte(`{"Rainfall":"1"}`))
	if !e.empty() || s.fields[6].value != 0 || s.source[6].value != 1 {
		t.Fatal("replay changed projected baseline")
	}
	o.source = Snapshot
	s, e, _ = reduceWeatherData(s, o, []byte(`{"Rainfall":false,"Rainfall":"0"}`))
	if !s.unsynchronized || !e.empty() {
		t.Fatal("invalid snapshot member hidden")
	}
	s, e, _ = reduceWeatherData(s.withSessionStatus("Ends"), o, []byte(`{"Rainfall":"1"}`))
	if !s.ended || !e.empty() {
		t.Fatal("terminal weather projection")
	}
}
func TestRepairGrammarBranches(t *testing.T) {
	for _, tc := range []struct {
		head    string
		reasons []string
	}{
		{"TIME 01:01.001", []string{"TRACK LIMITS AT TURN 11 LAP 3 00:00:02", "TRACK LIMITS AT TURN 11 (NEXT LAP)", "TRACK LIMITS AT TURN 11 LAP 3 (NEXT LAP)", "TRACK LIMITS AT PIT ENTRY LAP 3 23:59:59", "DOUBLE YELLOW AT TURN 11", "DOUBLE YELLOW AT TURN 11 LAP 3 0:00:00"}},
		{"LAP", []string{"TRACK LIMITS AT TURN 11 LAP 3 9:05:00", "TRACK LIMITS AT TURN 11 LAP 3 9:05:00 (PIT)", "TRACK LIMITS AT TURN 11 (NEXT LAP PIT)", "DOUBLE YELLOW AT TURN 11", "DOUBLE YELLOW AT TURN 11 LAP 3 0:00:00", "DOUBLE YELLOW AT TURN 11 LAP 3 0:00:00 (PIT)"}},
	} {
		for _, reason := range tc.reasons {
			s := "CAR 7 (XYZ) " + tc.head + " DELETED - " + reason
			c := parseRepair(s)
			if c.number != 7 || c.acronym != "XYZ" || c.reinstate || (c.reasonLap == 3) != strings.Contains(reason, " LAP 3") || c.hasDuration != (tc.head != "LAP") || c.hasDuration && c.durationMS != 61001 {
				t.Fatal(s, c)
			}
		}
	}
	for _, s := range []string{"CAR XYZ TIME 1:00.000 DELETED - TRACK LIMITS AT TURN 4 (NEXT LAP) (Q1)", "CAR XYZ TIME DELETED - TRACK LIMITS AT TURN 4 (NEXT LAP PIT) (Q2)", "CAR 7 (XYZ) TIME 1:00.000 WILL BE REINSTATED", "CAR 7 (XYZ) LAP 3 WILL BE REINSTATED"} {
		if c := parseRepair(s); c.acronym != "XYZ" {
			t.Fatal(s, c)
		}
	}
}
func TestControlPartialReplacementAndDeletion(t *testing.T) {
	o := sessionFixture()
	r := controlPatch(t, raceControlState{}, o, `{"1":{"Category":"CarEvent","RacingNumber":"7"},"2":{"Category":"Drs"}}`)
	r = controlPatch(t, r.state, o, `{"1":{"_deleted":["RacingNumber"],"Message":"Notice"},"_deleted":["2"],"2":{"Message":"partial"}}`)
	if len(r.effects.logs) != 1 || len(r.events) != 0 || len(r.effects.logs[0].attrs) != 2 || r.state.rows[2].category != "" {
		t.Fatal(r)
	}
	r = controlPatch(t, r.state, o, `[{},{"Category":"Flag","Message":"replay"},{"Category":"SafetyCar"}]`)
	if !r.effects.empty() || r.state.rows[2].hasMessage {
		t.Fatal(r)
	}
	o.source = Snapshot
	bad := controlPatch(t, r.state, o, `{"01":{}}`)
	if !bad.state.unsynchronized || !reflect.DeepEqual(bad.state.rows, r.state.rows) {
		t.Fatal(bad)
	}
	r = controlPatch(t, bad.state, o, `{"2":{}}`)
	o.source = Feed
	r = controlPatch(t, r.state, o, `{"2":{"Message":"replay","Category":"Other"},"3":{"Message":"new","Category":"x"}}`)
	if len(r.effects.logs) != 1 || !slices.Contains(r.effects.logs[0].attrs, strAttr("f1.race_control.category", "unknown")) {
		t.Fatal(r)
	}
}
