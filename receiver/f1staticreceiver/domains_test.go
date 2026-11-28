package f1staticreceiver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver"
)

func TestSharedCollectorFactory(t *testing.T) {
	f := NewFactory()
	c := f.CreateDefaultConfig().(*Config)
	if c.Enabled || f.Type().String() != "f1static" {
		t.Fatal(f.Type(), c.Enabled)
	}
	tr, _ := consumer.NewTraces(func(context.Context, ptrace.Traces) error { return nil })
	me, _ := consumer.NewMetrics(func(context.Context, pmetric.Metrics) error { return nil })
	lo, _ := consumer.NewLogs(func(context.Context, plog.Logs) error { return nil })
	set := receiver.Settings{ID: component.NewID(Type)}
	a, err := f.CreateTraces(t.Context(), set, c, tr)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.CreateMetrics(t.Context(), set, c, me)
	if err != nil {
		t.Fatal(err)
	}
	l, err := f.CreateLogs(t.Context(), set, c, lo)
	if err != nil || a != b || b != l {
		t.Fatal("factories did not share receiver", err)
	}
	if err := a.Start(t.Context(), nil); err != nil || a.(*staticReceiver).done != nil {
		t.Fatal("disabled receiver started", err)
	}
	if err := a.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSharedSingleRun(t *testing.T) {
	var bootstraps atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/s/SessionInfo.json":
			bootstraps.Add(1)
			w.Write([]byte(info))
		case "/s/DriverList.json":
			w.Write([]byte(drivers))
		case "/s/SessionData.jsonStream":
			w.Write([]byte(`00:00:00.000{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := DefaultConfig()
	c.Enabled = true
	c.Endpoint = server.URL
	c.SessionPath = "s"
	f := NewFactory()
	set := receiver.Settings{ID: component.NewID(Type)}
	tr, _ := consumer.NewTraces(func(context.Context, ptrace.Traces) error { return nil })
	lo, _ := consumer.NewLogs(func(context.Context, plog.Logs) error { return nil })
	a, err := f.CreateTraces(t.Context(), set, &c, tr)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.CreateLogs(t.Context(), set, &c, lo)
	if err != nil {
		t.Fatal(err)
	}
	dupCfg := c
	if _, err := f.CreateTraces(t.Context(), receiver.Settings{ID: component.NewIDWithName(Type, "dup")}, &dupCfg, tr); err == nil {
		t.Fatal("accepted duplicate enabled session_path across instances")
	}
	for _, r := range []component.Component{a, b} {
		if err := r.Start(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
	}
	<-a.(*staticReceiver).done
	if err := b.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range []component.Component{b, a} {
		if err := r.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if bootstraps.Load() != 1 {
		t.Fatal("replayed archive more than once")
	}
}
func reduceFixture(t *testing.T, streams map[string][]StreamRecord) effects {
	t.Helper()
	streams["Heartbeat"] = []StreamRecord{{0, `{"Utc":"2025-03-10T10:00:00Z"}`}}
	p, err := freeze([]byte(info), []byte(drivers), streams)
	if err != nil {
		t.Fatal(err)
	}
	var total effects
	for _, e := range reduce(p) {
		total.logs = append(total.logs, e.logs...)
		total.spans = append(total.spans, e.spans...)
		total.points = append(total.points, e.points...)
	}
	return total
}
func TestIndexedFactsAndSparseClock(t *testing.T) {
	e := reduceFixture(t, map[string][]StreamRecord{
		"SessionData":         {{0, `{"StatusSeries":{"0":{"SessionStatus":"Started"}}}`}, {1e9, `{"StatusSeries":{"0":{"Utc":"2025-03-10T10:00:01Z"}}}`}, {11e9, `{"StatusSeries":{"1":{"Utc":"2025-03-10T10:00:10.500Z","SessionStatus":"Ends"}}}`}},
		"RaceControlMessages": {{2e9, `{"Messages":{"8":{"Category":"Flag","Scope":"Driver","Flag":"BLUE","RacingNumber":"17"}}}`}, {4e9, `{"Messages":{"8":{"Message":"  API_KEY\nnever emit this"},"9":{"Category":"Flag","Scope":"Driver","Flag":"BLUE","RacingNumber":"17","Message":"Second","Utc":"2025-03-10T10:00:10.500"}}}`}, {5e9, `{"Messages":{"8":{"Message":"duplicate"}}}`}},
		"TeamRadio":           {{3e9, `{"Captures":{"0":{"RacingNumber":"17","Path":"../unsafe"},"1":{"Path":"TeamRadio/ABC_17_20250310_100003.mp3"}}}`}, {12e9, `{"Captures":{"0":{"Path":"TeamRadio/ABC_17_20250310_100003.mp3"},"1":{"RacingNumber":"17","Utc":"2025-03-10T10:00:04.1Z"}}}`}},
		"ExtrapolatedClock":   {{0, `{"Utc":"2025-03-10T10:00:00Z","Remaining":"00:00:11"}`}, {1e9, `{"Extrapolating":true}`}, {2e9, `{"Utc":"2025-03-10T10:00:02Z","Remaining":"00:00:08"}`}, {3e9, `{"Utc":"2025-03-10T10:00:02Z","Remaining":"00:00:07"}`}},
	})
	var bodies []string
	for _, l := range e.logs {
		bodies = append(bodies, l.name)
	}
	if !reflect.DeepEqual(bodies, []string{"f1.session.status.changed", "[redacted-message]", "Second", "f1.session.status.changed", "f1.team_radio.available"}) {
		t.Fatal(bodies)
	}
	if e.logs[1].a["f1.race_control.message_redacted"] != true || e.logs[4].owner == nil || e.logs[4].at.Nanosecond() != 100000000 {
		t.Fatal("log metadata")
	}
	if len(e.spans) != 1 || len(e.spans[0].events) != 4 || e.spans[0].a["f1.result.outcome"] != "unresolved" {
		t.Fatal("root event ownership")
	}
	if len(e.points) != 3 || e.points[0].value != int64(8) {
		t.Fatal(e.points)
	}
	for i, p := range e.points[1:] {
		v := p.value.(sum)
		if v.temporality != pmetric.AggregationTemporalityDelta || v.value != 1 || p.at.Sub(v.start) != []time.Duration{9 * time.Second, 500 * time.Millisecond}[i] {
			t.Fatal(p)
		}
	}
}
func TestSourceOwnership(t *testing.T) {
	e := reduceFixture(t, map[string][]StreamRecord{
		"SessionData":           {{0, `{"StatusSeries":[{"SessionStatus":"Started","Utc":"2025-03-10T10:00:00Z"}]}`}, {12e9, `{"StatusSeries":{"1":{"SessionStatus":"Ends","Utc":"2025-03-10T10:00:12Z"}}}`}},
		"TimingAppData":         {{0, `{"Lines":{"17":{"Stints":{"0":{"Compound":" SOFT ","TotalLaps":3}}}}}`}},
		"TimingData":            {{1e9, `{"Lines":{"17":{"InPit":false,"NumberOfPitStops":0,"NumberOfLaps":1,"Stopped":false}}}`}, {2e9, `{"Lines":{"17":{"InPit":true,"NumberOfPitStops":1}}}`}, {5e9, `{"Lines":{"17":{"NumberOfLaps":2,"InPit":false,"LastLapTime":{"Value":"4.000","PersonalFastest":true}}}}`}, {10e9, `{"Lines":{"17":{"Stopped":true}}}`}},
		"PitStopSeries":         {{5e9, `{"PitTimes":{"17":[{"PitStop":{"PitStopTime":"2.5"}}]}}`}},
		"PitLaneTimeCollection": {{6e9, `{"PitTimes":{"17":{"Duration":"12.3","Lap":"2"}}}`}},
		"PitStop":               {{7e9, `{"RacingNumber":"17","Lap":"2","PitStopTime":"2.5","PitLaneTime":"12.399"}`}, {8e9, `{"RacingNumber":"17","Lap":"2","PitStopTime":"2.6"}`}},
	})
	byName := map[string][]point{}
	for _, p := range e.points {
		byName[p.name] = append(byName[p.name], p)
	}
	for name, want := range map[string][]any{"f1.driver.tyre.age": {int64(3)}, "f1.session.cars_running": {int64(1), int64(0)}, "f1.driver.current_lap": {int64(1), int64(2)}} {
		var got []any
		for _, p := range byName[name] {
			got = append(got, p.value)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal(name, got)
		}
	}
	for name, v := range map[string]float64{"f1.driver.lap_time": 4, "f1.pit.lane_duration": 12.3, "f1.pit.stop_duration": 2.5} {
		ps := byName[name]
		if len(ps) != 1 || ps[0].histogram.sum != v || ps[0].histogram.count != 1 {
			t.Fatal(name, ps)
		}
	}
	if byName["f1.driver.lap_time"][0].a["f1.tyre.compound"] != "soft" {
		t.Fatal("in-lap used the next stint")
	}
	pits := byName["f1.driver.pit_lane_visits"]
	if len(pits) != 2 || pits[1].value.(sum).value != 1 || pits[0].value.(sum).start != pits[1].value.(sum).start {
		t.Fatal(pits)
	}
	got := map[string]any{}
	for _, sp := range e.spans {
		for _, ev := range sp.events {
			if strings.HasPrefix(ev.name, "f1.pit.") || ev.name == "f1.personal_best" {
				got[ev.name] = sp.a["f1.lap.number"]
			}
		}
	}
	if !reflect.DeepEqual(got, map[string]any{"f1.pit.entry": int64(1), "f1.pit.exit": int64(2), "f1.pit.stop": int64(2), "f1.personal_best": int64(1)}) {
		t.Fatal(got)
	}
}
func TestWeatherContinuity(t *testing.T) {
	e := reduceFixture(t, map[string][]StreamRecord{"WeatherData": {{0, `{"Rainfall":"0","AirTemp":"-0.1","WindDirection":"360","Pressure":"0.0"}`}, {1e9, `{"Rainfall":"1","WindSpeed":"0.0","Humidity":"100.1"}`}, {1e9, `{"Rainfall":"0"}`}, {2e9, `{"Rainfall":"0"}`}, {127e9, `{"Rainfall":"1"}`}, {128e9, `{"Rainfall":false}`}, {129e9, `{"Rainfall":"0"}`}}})
	if len(e.points) != 7 || len(e.logs) != 2 {
		t.Fatal(e.points, e.logs)
	}
	if e.points[0].value != -0.1 || e.logs[0].a["f1.weather.rainfall_detected"] != true || e.logs[1].a["f1.weather.previous_rainfall_detected"] != true {
		t.Fatal("weather values")
	}
}
