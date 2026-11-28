package f1livetimingreceiver

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"
)

func sessionFixture() sessionObservation {
	return sessionObservation{identity: sessionInfoIdentity{2026, 42, "race", "race"}, observed: time.Unix(100, 0).UTC(), publication: time.Unix(90, 0).UTC(), source: liveTimingUpdateSourceFeed}
}

func TestClockSnapshotRequiresCompleteValidBaseline(t *testing.T) {
	const good = `{"Utc":"1970-01-01T00:01:30Z","Remaining":"00:02:00","Extrapolating":true}`
	feed := sessionFixture()
	snap := feed
	snap.source = liveTimingUpdateSourceSnapshot
	prior, _, err := reduceExtrapolatedClock(extrapolatedClockState{}, feed, []byte(good))
	if err != nil || !prior.scheduled {
		t.Fatal(prior, err)
	}
	for _, bad := range []string{
		`{"Remaining":"00:02:00","Extrapolating":true}`,
		`{"Utc":"1970-01-01T00:01:30Z","Extrapolating":false}`,
		`{"Utc":"1970-01-01T00:01:30Z","Remaining":"00:02:00"}`,
		`{"Utc":"bad","Remaining":"00:02:00","Extrapolating":true}`,
		`{"Utc":"1970-01-01T00:01:30Z","Remaining":"00:60:00","Extrapolating":true}`,
		`{"Utc":"1970-01-01T00:01:30Z","Remaining":"00:02:00","Extrapolating":"true"}`,
	} {
		want := prior.suspend()
		want.unsynchronized = true
		s, e, err := reduceExtrapolatedClock(prior, snap, []byte(bad))
		if s != want || !e.empty() || err != nil {
			t.Fatal(bad, s, e, err)
		}
		s, e, err = reduceExtrapolatedClock(s, feed, []byte(good))
		if s != want || !e.empty() || err != nil {
			t.Fatal(bad, s, e, err)
		}
		s, e, err = reduceExtrapolatedClock(s, snap, []byte(good))
		if s.unsynchronized || !s.scheduled || err != nil || len(e.metrics) != 1 || e.metrics[0].intVal != 110 {
			t.Fatal(bad, s, e, err)
		}
	}
}

func TestInvalidLapCountSnapshotRequiresRecovery(t *testing.T) {
	feed := sessionFixture()
	snap := feed
	snap.source = liveTimingUpdateSourceSnapshot
	prior, _, err := reduceLapCount(lapCountState{}, feed, []byte(`{"CurrentLap":2,"TotalLaps":20}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"CurrentLap":3,"TotalLaps":null}`, `{"CurrentLap":"3","TotalLaps":25}`} {
		want := prior
		want.unsynchronized = true
		s, e, err := reduceLapCount(prior, snap, []byte(bad))
		if s != want || !e.empty() || err != nil {
			t.Fatal(bad, s, e, err)
		}
		const good = `{"CurrentLap":3,"TotalLaps":25}`
		s, e, err = reduceLapCount(s, feed, []byte(good))
		if s != want || !e.empty() || err != nil {
			t.Fatal(s, e, err)
		}
		s, e, err = reduceLapCount(s, snap, []byte(good))
		if s.unsynchronized || len(e.metrics) != 2 || err != nil || e.metrics[0].intVal != 3 || e.metrics[1].intVal != 25 {
			t.Fatal(s, e, err)
		}
	}
}

func TestSessionDataOrdering(t *testing.T) {
	o := sessionFixture()
	at := time.Unix(60, 0).UTC()
	reduce := func(s sessionDataState, source liveTimingUpdateSource, series string) sessionDataReduction {
		t.Helper()
		o.source = source
		out, err := reduceSessionData(s, o, json.RawMessage(`{"StatusSeries":`+series+`}`))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	f, snap := liveTimingUpdateSourceFeed, liveTimingUpdateSourceSnapshot
	for _, series := range []string{`[{"Utc":"1970-01-01T00:01:00Z","SessionStatus":"Inactive"}]`, `{"0":{"Utc":"1970-01-01T01:01:00+01:00","SessionStatus":"Inactive"}}`} {
		base := reduce(sessionDataState{}, snap, series)
		if !base.effects.empty() || len(base.sessions)+len(base.tracks) != 0 {
			t.Fatal(base)
		}
		partial := reduce(base.state, f, `{"2":{"SessionStatus":"Ends"},"0":{"SessionStatus":"Finished","TrackStatus":"Yellow"},"1":{"SessionStatus":"Finalised","TrackStatus":"Red"}}`)
		wantTrack := trackStatusTransition{0, at, trackCondition{}, trackCondition{2, "yellow"}}
		if !reflect.DeepEqual(partial.tracks, []trackStatusTransition{wantTrack}) || len(partial.sessions) != 0 || base.state.records[0].present[trackField] {
			t.Fatal(partial)
		}
		out := reduce(partial.state, f, `{"2":{"Utc":"1970-01-01T00:01:00Z"},"1":{"Utc":"1970-01-01T00:01:00Z"}}`)
		if !reflect.DeepEqual(out.sessions, []sessionStatusTransition{{1, at, "Inactive", "Finalised"}, {2, at, "Finalised", "Ends"}}) ||
			!reflect.DeepEqual(out.tracks, []trackStatusTransition{{1, at, trackCondition{2, "yellow"}, trackCondition{5, "red"}}}) {
			t.Fatal(out)
		}
		common := logEffect{timestamp: at, observedTimestamp: o.observed, identity: o.identity, dataSource: "livetiming", timeQuality: "observed"}
		track, final, ends := common, common, common
		track.body = "f1.track_status.changed"
		track.attrs = []keyValue{intAttr("f1.track_status.code", 5), strAttr("f1.track_status.state", "red"), intAttr("f1.track_status.previous_code", 2), strAttr("f1.track_status.previous_state", "yellow")}
		final.body = "f1.session.status.changed"
		final.attrs = []keyValue{strAttr("f1.session.status", "Finalised"), strAttr("f1.session.previous_status", "Inactive")}
		ends.body = final.body
		ends.attrs = []keyValue{strAttr("f1.session.status", "Ends"), strAttr("f1.session.previous_status", "Finalised")}
		if !reflect.DeepEqual(out.effects, liveTimingEffects{logs: []logEffect{track, final, ends}}) {
			t.Fatal(out.effects)
		}
		replay := reduce(out.state, f, `[{"Utc":"1970-01-01T00:02:00Z","SessionStatus":"Started"}]`)
		if !replay.effects.empty() || len(replay.state.records) != 1 || replay.state.history[sessionField].current != "Ends" {
			t.Fatal(replay)
		}
	}
}

func TestSessionDataEdges(t *testing.T) {
	o := sessionFixture()
	var s sessionDataState
	for i, c := range []struct {
		utc, session, track string
		logs                []string
		unknown             bool
	}{
		{"10", "Started", "AllClear", []string{"f1.track_status.changed", "f1.session.status.changed"}, false},
		{"10", "Aborted", "Yellow", []string{"f1.track_status.changed", "f1.session.status.changed"}, false},
		{"10", "Started", "AllClear", []string{"f1.track_status.changed", "f1.session.status.changed"}, false},
		{"10", "Aborted", "Yellow", nil, false}, {"10", "Started", "AllClear", nil, false},
		{"12", "Started", "AllClear", nil, false}, {"11", "Finished", "Red", nil, false},
		{"13", "unknown", `\udc00`, nil, true}, {"14", "Inactive", "SCDeployed", nil, false},
		{"15", "Finished", "Red", []string{"f1.track_status.changed", "f1.session.status.changed"}, false},
	} {
		raw := `{"StatusSeries":{"` + decimal(int64(i)) + `":{"Utc":"2026-01-01T00:00:` + c.utc + `Z","SessionStatus":"` + c.session + `","TrackStatus":"` + c.track + `"}}}`
		out, err := reduceSessionData(s, o, []byte(raw))
		var bodies []string
		for _, l := range out.effects.logs {
			bodies = append(bodies, l.body)
		}
		if err != nil || !slices.Equal(bodies, c.logs) || out.unknownStatus != c.unknown {
			t.Fatalf("index %d: %+v %v", i, out, err)
		}
		s = out.state
	}
	partial, err := reduceSessionData(s, o, []byte(`{"StatusSeries":{"02":{"SessionStatus":"Ends"},"\ud800":{"SessionStatus":"Ends"},"10":{"Utc":"2026-01-01T00:00:00+24:00","SessionStatus":"Ends"}}}`))
	if err != nil || !partial.effects.empty() || partial.state.records[10].hasUTC || len(partial.state.records) != 11 {
		t.Fatal(partial, err)
	}
}

func TestSessionStatusMirror(t *testing.T) {
	o := sessionFixture()
	for _, status := range []string{"Inactive", "Started", "Aborted", "Finished", "Finalised", "Ends"} {
		m, err := reduceSessionStatus(sessionStatusMirror{}, o, []byte(`{"Status":"`+status+`","Started":"yes"}`))
		if err != nil || m != (sessionStatusMirror{status, "yes", false}) {
			t.Fatal(m, err)
		}
		m, err = reduceSessionStatus(m, o, []byte(`{"Started":"\ud800","Sta\ud800tus":"Ends"}`))
		if err != nil || m != (sessionStatusMirror{status, "", false}) {
			t.Fatal(m, err)
		}
	}
	o.source = liveTimingUpdateSourceSnapshot
	m, err := reduceSessionStatus(sessionStatusMirror{"Started", "yes", false}, o, []byte(`{"Status":"started"}`))
	if err != nil || m != (sessionStatusMirror{}) {
		t.Fatal(m, err)
	}
}
func TestTrackStatusMirror(t *testing.T) {
	o := sessionFixture()
	for _, c := range []struct{ code, message, state string }{
		{"1", "AllClear", "all_clear"}, {"2", "Yellow", "yellow"}, {"4", "SCDeployed", "safety_car"}, {"5", "Red", "red"}, {"6", "VSCDeployed", "virtual_safety_car"}, {"7", "VSCEnding", "virtual_safety_car_ending"}, {"3", "AllClear", ""}, {"2", "Red", ""}, {"5", `\ud800`, ""},
	} {
		m, err := reduceTrackStatus(trackStatusMirror{code: c.code}, o, []byte(`{"Message":"`+c.message+`"}`))
		if err != nil || m.condition().state != c.state {
			t.Fatal(c, m, err)
		}
	}
	o.source = liveTimingUpdateSourceSnapshot
	m, err := reduceTrackStatus(trackStatusMirror{"5", "Red", false}, o, []byte(`{"Message":"Red"}`))
	if err != nil || m.condition() != (trackCondition{}) {
		t.Fatal(m, err)
	}
	base, err := reduceSessionData(sessionDataState{}, o, []byte(`{"StatusSeries":[{"Utc":"2026-01-01T00:00:00Z","SessionStatus":"Started","TrackStatus":"Red"}]}`))
	if err != nil || !base.state.mirrorsAgree(sessionStatusMirror{status: "Started"}, trackStatusMirror{"5", "Red", false}) || base.state.mirrorsAgree(sessionStatusMirror{status: "Started"}, m) {
		t.Fatal(base, err)
	}
}

func assertSessionMetrics(t *testing.T, out liveTimingEffects, o sessionObservation, at time.Time, values ...keyValue) {
	t.Helper()
	var want liveTimingEffects
	for _, v := range values {
		unit := "{lap}"
		if v.key == "f1.session.clock.remaining" {
			unit = "s"
		}
		want.metrics = append(want.metrics, metricEffect{kind: metricIntGauge, name: v.key, intVal: v.value.intVal, unit: unit, timestamp: at, identity: o.identity, dataSource: "livetiming"})
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %+v, want %+v", out, want)
	}
}

func TestLapCountProjection(t *testing.T) {
	for _, typ := range []canonicalSessionType{"race", "sprint", "qualifying", "practice", "testing", "sprint_qualifying"} {
		o := sessionFixture()
		o.identity.sessionType = typ
		var s lapCountState
		for _, c := range []struct {
			raw      string
			snapshot bool
			values   []keyValue
		}{
			{`{"CurrentLap":0}`, false, nil},
			{`{"TotalLaps":40}`, true, []keyValue{intAttr(totalLapsMetric, 40)}},
			{`{"CurrentLap":0}`, false, []keyValue{intAttr(currentLapMetric, 0)}},
			{`{"CurrentLap":12}`, false, []keyValue{intAttr(currentLapMetric, 12)}},
			{`{"CurrentLap":13,"TotalLaps":10}`, false, nil},
			{`{"TotalLaps":0}`, false, nil}, {`{"CurrentLap":0}`, false, nil},
			{`{"CurrentLap":8,"TotalLaps":36}`, false, []keyValue{intAttr(currentLapMetric, 8), intAttr(totalLapsMetric, 36)}},
			{`{"CurrentLap":8}`, true, []keyValue{intAttr(currentLapMetric, 8)}},
			{`{"CurrentLap":0}`, false, nil},
			{`{"TotalLaps":0}`, true, nil},
		} {
			o.publication = o.observed.Add(time.Second)
			o.observed = o.publication.Add(time.Second)
			at := o.publication
			o.source = liveTimingUpdateSourceFeed
			if c.snapshot {
				o.source = liveTimingUpdateSourceSnapshot
				at = o.observed
			}
			next, out, err := reduceLapCount(s, o, []byte(c.raw))
			if err != nil {
				t.Fatal(err)
			}
			if typ != "race" && typ != "sprint" {
				c.values = nil
			}
			assertSessionMetrics(t, out, o, at, c.values...)
			s = next
		}
	}
	for _, v := range []string{`"8"`, `-0`, `-1`, `1.0`, `1e1`, `9223372036854775808`, `"\ud800"`, `"８"`} {
		s, out, err := reduceLapCount(lapCountState{}, sessionFixture(), []byte(`{"CurrentLap":`+v+`}`))
		if err != nil || !out.empty() || s.current.known {
			t.Fatal(v, s, out, err)
		}
	}
}

func TestLapCountHeartbeat(t *testing.T) {
	o := sessionFixture()
	s := lapCountState{}.withSessionStatus("Started", false, 0)
	s, _, err := reduceLapCount(s, o, []byte(`{"CurrentLap":2,"TotalLaps":20}`))
	if err != nil {
		t.Fatal(err)
	}
	s, out, err := reduceLapCount(s, o, []byte(`{"CurrentLap":3,"TotalLaps":19}`))
	if err != nil || !out.empty() {
		t.Fatal(out, err)
	}
	s = s.withSessionStatus("Aborted", false, time.Second).withSessionStatus("Inactive", false, 2*time.Second)
	o.monotonic = 10*time.Second - 1
	if _, out = tickLapCount(s, o); !out.empty() {
		t.Fatal(out)
	}
	o.monotonic++
	s, out = tickLapCount(s, o)
	assertSessionMetrics(t, out, o, o.observed, intAttr(currentLapMetric, 3))
	o.monotonic += 31 * time.Second
	o.observed = o.observed.Add(-time.Second)
	s, out = tickLapCount(s, o)
	if !out.empty() || s.deadline != 51*time.Second {
		t.Fatal(out, s)
	}
	o.publication = o.observed.Add(2 * time.Second)
	s, out, err = reduceLapCount(s, o, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	assertSessionMetrics(t, out, o, o.publication, intAttr(currentLapMetric, 3), intAttr(totalLapsMetric, 19))
	for _, status := range []string{"Finished", "Finalised", "Ends"} {
		if s.withSessionStatus(status, false, 0).active {
			t.Fatal(status)
		}
	}
	if (lapCountState{}).withSessionStatus("Inactive", true, 0).active || !(lapCountState{}).withSessionStatus("Aborted", true, 0).active {
		t.Fatal("snapshot activation")
	}
}

func TestExtrapolatedClockAnchors(t *testing.T) {
	o := sessionFixture()
	a := time.Unix(90, 0).UTC()
	o.observed = a.Add(2300 * time.Millisecond)
	o.source = liveTimingUpdateSourceSnapshot
	anchor := []byte(`{"Utc":"1970-01-01T00:01:30Z","Remaining":"00:00:08","Extrapolating":true}`)
	s, out, err := reduceExtrapolatedClock(extrapolatedClockState{}, o, anchor)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionMetrics(t, out, o, o.observed, intAttr(clockMetric, 6))
	s, out = tickExtrapolatedClock(s, o.identity, a.Add(2700*time.Millisecond))
	if !out.empty() {
		t.Fatal(out)
	}
	s, out = tickExtrapolatedClock(s, o.identity, a.Add(5700*time.Millisecond))
	assertSessionMetrics(t, out, o, a.Add(5*time.Second), intAttr(clockMetric, 3))
	o.source = liveTimingUpdateSourceFeed
	s, out, err = reduceExtrapolatedClock(s, o, []byte(`{"Utc":"1970-01-01T00:01:35Z","Remaining":"00:00:12"}`))
	if err != nil || !out.empty() {
		t.Fatal(out, err)
	}
	s, out = tickExtrapolatedClock(s, o.identity, a.Add(6*time.Second))
	assertSessionMetrics(t, out, o, a.Add(6*time.Second), intAttr(clockMetric, 11))
	s, out, err = reduceExtrapolatedClock(s, o, anchor)
	if err != nil || !out.empty() {
		t.Fatal(out, err)
	}
	s, out = tickExtrapolatedClock(s, o.identity, a.Add(20*time.Second))
	assertSessionMetrics(t, out, o, a.Add(20*time.Second), intAttr(clockMetric, 0))
	if _, out = tickExtrapolatedClock(s, o.identity, a.Add(40*time.Second)); !out.empty() {
		t.Fatal(out)
	}
}

func TestExtrapolatedClockSparseAndFuture(t *testing.T) {
	o := sessionFixture()
	a := time.Unix(110, 0).UTC()
	o.source = liveTimingUpdateSourceSnapshot
	s, out, err := reduceExtrapolatedClock(extrapolatedClockState{}, o, []byte(`{"Utc":"1970-01-01T00:01:50Z","Remaining":"01:02:03","Extrapolating":false}`))
	if err != nil || !out.empty() {
		t.Fatal(out, err)
	}
	s, out = tickExtrapolatedClock(s, o.identity, a.Add(time.Second))
	assertSessionMetrics(t, out, o, a, intAttr(clockMetric, 3723))
	if _, out = tickExtrapolatedClock(s, o.identity, a.Add(10*time.Second)); !out.empty() {
		t.Fatal(out)
	}
	o.source = liveTimingUpdateSourceFeed
	for _, raw := range []string{`{"Extrapolating":true}`, `{"Remaining":"00:00:15"}`, `{"Utc":"1970-01-01T00:02:00Z"}`} {
		s, out, err = reduceExtrapolatedClock(s, o, []byte(raw))
		if err != nil || s.scheduled || !out.empty() {
			t.Fatal(raw, s, out, err)
		}
	}
	s, out, err = reduceExtrapolatedClock(s, o, []byte(`{"Utc":"1970-01-01T01:02:00+01:00","Remaining":"99:59:59"}`))
	if err != nil {
		t.Fatal(err)
	}
	assertSessionMetrics(t, out, o, time.Unix(120, 0).UTC(), intAttr(clockMetric, 359999))
	o.source = liveTimingUpdateSourceSnapshot
	for _, raw := range []string{
		`{"Utc":"1970-01-01T00:00:00Z","Remaining":"00:00:01"}`,
		`{"Utc":"1970-01-01T00:00:00Z","Remaining":"00:00:01","Extrapolating":"true"}`,
		`{"Utc":"1970-01-01T00:00:00,1Z","Remaining":"00:00:01","Extrapolating":true}`,
		`{"Utc":"1970-01-01T00:00:00Z","Remaining":"00:00:01","Extrapolatin\ud800g":true}`,
	} {
		next, out, err := reduceExtrapolatedClock(s, o, []byte(raw))
		if err != nil || next.scheduled || !out.empty() {
			t.Fatal(raw, next, out, err)
		}
	}
	for _, v := range []string{"100:00:00", "9:00:00", "00:60:00", "00:59:60", "-1:00:00", "00:00:０", "00:00:00.1"} {
		if _, ok := remainingSeconds(v); ok {
			t.Fatal(v)
		}
	}
}

func TestLapCountReconnectSchedule(t *testing.T) {
	s := lapCountState{}.withSessionStatus("Started", false, 0).heartbeatActive(false, time.Second)
	o := sessionFixture()
	o.monotonic = time.Hour
	if _, out := tickLapCount(s, o); !out.empty() {
		t.Fatal(out)
	}
	s = s.withSessionStatus("Inactive", true, o.monotonic)
	if !s.active || s.deadline != time.Hour+10*time.Second {
		t.Fatal(s)
	}
}

func TestExtrapolatedClockSuspend(t *testing.T) {
	o := sessionFixture()
	raw := []byte(`{"Utc":"1970-01-01T00:01:30Z","Remaining":"00:01:00","Extrapolating":true}`)
	s, _, err := reduceExtrapolatedClock(extrapolatedClockState{}, o, raw)
	if err != nil {
		t.Fatal(err)
	}
	s = s.suspend()
	if _, out := tickExtrapolatedClock(s, o.identity, o.observed); !out.empty() {
		t.Fatal(out)
	}
	o.source = liveTimingUpdateSourceSnapshot
	s, out, err := reduceExtrapolatedClock(s, o, raw)
	if err != nil || !s.scheduled {
		t.Fatal(s, err)
	}
	assertSessionMetrics(t, out, o, o.observed, intAttr(clockMetric, 50))
}

func TestSessionTopicShapeAndRecovery(t *testing.T) {
	for _, tc := range []struct{ topic, payload string }{
		{"SessionData", `{"StatusSeries":[{"SessionStatus":"Started","Utc":"1970-01-01T00:00:03Z"}]}`},
		{"SessionStatus", `{"Status":"Started"}`}, {"TrackStatus", `{"Status":"2","Message":"Yellow"}`},
		{"LapCount", `{"CurrentLap":1}`}, {"ExtrapolatedClock", `{"Utc":"1970-01-01T00:00:03Z","Remaining":"00:01:00","Extrapolating":true}`},
	} {
		t.Run(tc.topic, func(t *testing.T) {
			var state liveTimingState
			o := sessionFixture()
			o.source = liveTimingUpdateSourceSnapshot
			u := normalizedLiveTimingUpdate{topic: tc.topic, payload: []byte(tc.payload)}
			if _, err := state.apply(o, u); err != nil {
				t.Fatal(err)
			}
			want := state
			switch tc.topic {
			case "SessionData":
				want.series.unsynchronized = true
			case "SessionStatus":
				want.status.unsynchronized = true
			case "TrackStatus":
				want.track.unsynchronized = true
			case "LapCount":
				want.laps.unsynchronized = true
			case "ExtrapolatedClock":
				want.clock = want.clock.suspend()
				want.clock.unsynchronized = true
			}
			for _, raw := range []string{"false", "null", "[]"} {
				u.payload = []byte(raw)
				if e, err := state.apply(o, u); err != nil || !e.empty() || !reflect.DeepEqual(state, want) {
					t.Fatal("snapshot shape did not preserve recovery state", err, e)
				}
			}
			o.source = liveTimingUpdateSourceFeed
			u.payload = []byte(tc.payload)
			if e, err := state.apply(o, u); err != nil || !e.empty() || !reflect.DeepEqual(state, want) {
				t.Fatal("feed resumed before snapshot", err, e)
			}
			for _, invalid := range []struct {
				raw []byte
				err error
			}{{[]byte("{"), errJSONSyntax}, {[]byte{0xff}, errJSONUTF8}} {
				u.payload = invalid.raw
				if _, err := state.apply(o, u); err != invalid.err {
					t.Fatal(err, invalid.err)
				}
			}
		})
	}
}

func TestSessionDataEffectsOwnNestedSlices(t *testing.T) {
	attrs := []keyValue{boolAttr("flag", true)}
	in := liveTimingEffects{
		spans:   []spanEffect{{attrs: attrs, events: []spanEventEffect{{attrs: attrs}}}},
		metrics: []metricEffect{{attrs: attrs, bounds: []float64{0.5}, bucketCounts: []uint64{2}, expPositiveBuckets: []uint64{3}, exemplars: []exemplarEffect{{filteredAttrs: attrs}}}},
		logs:    []logEffect{{attrs: attrs}},
	}
	out := in.append(in)
	replacement := floatAttr("new", 1.5)
	for i := range out.spans {
		out.spans[i].attrs[0] = replacement
		out.spans[i].events[0].attrs[0] = replacement
		out.spans[i].events[0].name = "new"
		m := &out.metrics[i]
		m.attrs[0] = replacement
		m.bounds[0], m.bucketCounts[0], m.expPositiveBuckets[0] = 1, 1, 1
		m.exemplars[0].filteredAttrs[0] = replacement
		m.exemplars[0].intVal = 42
		out.logs[i].attrs[0] = replacement
	}
	m := in.metrics[0]
	if attrs[0] != boolAttr("flag", true) || in.spans[0].events[0].name != "" || m.bounds[0] != 0.5 || m.bucketCounts[0] != 2 || m.expPositiveBuckets[0] != 3 || m.exemplars[0].intVal != 0 {
		t.Fatal(in)
	}
}
