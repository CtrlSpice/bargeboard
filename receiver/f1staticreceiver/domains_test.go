package f1staticreceiver

import (
	"context"
	"net/http"
	"net/http/httptest"

	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

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

type replaySignals struct {
	spans   []ptrace.Span
	logs    []plog.LogRecord
	metrics map[string][]pmetric.Metric
}

func runPlan(t *testing.T, p FrozenReplayPlan) replaySignals {
	t.Helper()
	out := replaySignals{metrics: map[string][]pmetric.Metric{}}
	r := newReceiver(DefaultConfig(), receiver.Settings{})
	r.traces, _ = consumer.NewTraces(func(_ context.Context, tr ptrace.Traces) error {
		for _, sp := range tr.ResourceSpans().At(0).ScopeSpans().At(0).Spans().All() {
			out.spans = append(out.spans, sp)
		}
		return nil
	})
	r.logs, _ = consumer.NewLogs(func(_ context.Context, logs plog.Logs) error {
		for _, l := range logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().All() {
			out.logs = append(out.logs, l)
		}
		return nil
	})
	r.metrics, _ = consumer.NewMetrics(func(_ context.Context, metrics pmetric.Metrics) error {
		for _, m := range metrics.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().All() {
			out.metrics[m.Name()] = append(out.metrics[m.Name()], m)
		}
		return nil
	})
	if err := r.play(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	return out
}
func replayFixture(t *testing.T, streams map[string][]StreamRecord) replaySignals {
	t.Helper()
	streams["Heartbeat"] = []StreamRecord{{0, `{"Utc":"2025-03-10T10:00:00Z"}`}}
	p, err := freeze([]byte(info), []byte(drivers), streams)
	if err != nil {
		t.Fatal(err)
	}
	return runPlan(t, p)
}
func TestSourceOwnership(t *testing.T) {
	e := replayFixture(t, map[string][]StreamRecord{
		"SessionData":           {{0, `{"StatusSeries":[{"SessionStatus":"Started","Utc":"2025-03-10T10:00:00Z"}]}`}, {12e9, `{"StatusSeries":{"1":{"SessionStatus":"Ends","Utc":"2025-03-10T10:00:12Z"}}}`}},
		"TimingAppData":         {{0, `{"Lines":{"17":{"Stints":{"0":{"Compound":" SOFT ","TotalLaps":3}}}}}`}},
		"TimingData":            {{1e9, `{"Lines":{"17":{"InPit":false,"NumberOfPitStops":0,"NumberOfLaps":1,"Stopped":false}}}`}, {2e9, `{"Lines":{"17":{"InPit":true,"NumberOfPitStops":1}}}`}, {5e9, `{"Lines":{"17":{"NumberOfLaps":2,"InPit":false,"LastLapTime":{"Value":"4.000","PersonalFastest":true}}}}`}, {10e9, `{"Lines":{"17":{"Stopped":true}}}`}},
		"PitStopSeries":         {{5e9, `{"PitTimes":{"17":[{"PitStop":{"PitStopTime":"2.5"}}]}}`}},
		"PitLaneTimeCollection": {{6e9, `{"PitTimes":{"17":{"Duration":"12.3","Lap":"2"}}}`}},
		"PitStop":               {{7e9, `{"RacingNumber":"17","Lap":"2","PitStopTime":"2.5","PitLaneTime":"12.399"}`}, {8e9, `{"RacingNumber":"17","Lap":"2","PitStopTime":"2.6"}`}},
	})
	for name, want := range map[string][]int64{"f1.driver.tyre.age": {3}, "f1.session.cars_running": {1, 0}, "f1.driver.current_lap": {1, 2}} {
		var got []int64
		for _, m := range e.metrics[name] {
			for _, p := range m.Gauge().DataPoints().All() {
				got = append(got, p.IntValue())
			}
		}
		if !slices.Equal(want, got) {
			t.Fatal(name, got)
		}
	}
	for name, want := range map[string]float64{"f1.driver.lap_time": 4, "f1.pit.lane_duration": 12.3, "f1.pit.stop_duration": 2.5} {
		ms := e.metrics[name]
		if len(ms) != 1 {
			t.Fatal(name, len(ms))
		}
		p := ms[0].Histogram().DataPoints().At(0)
		if p.Count() != 1 || p.Sum() != want {
			t.Fatal(name, p.Count(), p.Sum())
		}
		if name == "f1.driver.lap_time" {
			v, _ := p.Attributes().Get("f1.tyre.compound")
			if v.Str() != "soft" {
				t.Fatal(v.Str())
			}
		}
	}
	var owners = map[string]int64{}
	for _, sp := range e.spans {
		for _, ev := range sp.Events().All() {
			if strings.HasPrefix(ev.Name(), "f1.pit.") || ev.Name() == "f1.personal_best" {
				n, _ := sp.Attributes().Get("f1.lap.number")
				owners[ev.Name()] = n.Int()
			}
		}
	}
	if !reflect.DeepEqual(owners, map[string]int64{"f1.pit.entry": 1, "f1.pit.exit": 2, "f1.pit.stop": 2, "f1.personal_best": 1}) {
		t.Fatal(owners)
	}
}
func TestWeatherContinuity(t *testing.T) {
	e := replayFixture(t, map[string][]StreamRecord{"WeatherData": {{0, `{"Rainfall":"0","AirTemp":"-0.1","WindDirection":"360","Pressure":"0.0"}`}, {1e9, `{"Rainfall":"1","WindSpeed":"0.0","Humidity":"100.1"}`}, {1e9, `{"Rainfall":"0"}`}, {2e9, `{"Rainfall":"0"}`}, {127e9, `{"Rainfall":"1"}`}, {128e9, `{"Rainfall":false}`}, {129e9, `{"Rainfall":"0"}`}}})
	if len(e.logs) != 2 {
		t.Fatal(len(e.logs))
	}
	a, _ := e.logs[0].Attributes().Get("f1.weather.rainfall_detected")
	b, _ := e.logs[1].Attributes().Get("f1.weather.previous_rainfall_detected")
	if !a.Bool() || !b.Bool() {
		t.Fatal(a, b)
	}
	if v := e.metrics["f1.session.weather.air_temperature"][0].Gauge().DataPoints().At(0).DoubleValue(); v != -0.1 {
		t.Fatal(v)
	}
}
func TestIndexedFactsAndSparseClock(t *testing.T) {
	e := replayFixture(t, map[string][]StreamRecord{
		"SessionData":         {{0, `{"StatusSeries":{"0":{"SessionStatus":"Started"}}}`}, {1e9, `{"StatusSeries":{"0":{"Utc":"2025-03-10T10:00:01Z"}}}`}, {11e9, `{"StatusSeries":{"1":{"Utc":"2025-03-10T10:00:10.500Z","SessionStatus":"Ends"}}}`}},
		"RaceControlMessages": {{2e9, `{"Messages":{"8":{"Category":"Flag","Scope":"Driver","Flag":"BLUE","RacingNumber":"17"}}}`}, {4e9, `{"Messages":{"8":{"Message":"  API_KEY\nnever emit this"},"9":{"Category":"Flag","Scope":"Driver","Flag":"BLUE","RacingNumber":"17","Message":"Second","Utc":"2025-03-10T10:00:10.500"}}}`}, {5e9, `{"Messages":{"8":{"Message":"duplicate"}}}`}},
		"TeamRadio":           {{3e9, `{"Captures":{"0":{"RacingNumber":"17","Path":"../unsafe"},"1":{"Path":"TeamRadio/ABC_17_20250310_100003.mp3"}}}`}, {12e9, `{"Captures":{"0":{"Path":"TeamRadio/ABC_17_20250310_100003.mp3"},"1":{"RacingNumber":"17","Utc":"2025-03-10T10:00:04.1Z"}}}`}},
		"ExtrapolatedClock":   {{0, `{"Utc":"2025-03-10T10:00:00Z","Remaining":"00:00:11"}`}, {1e9, `{"Extrapolating":true}`}, {2e9, `{"Utc":"2025-03-10T10:00:02Z","Remaining":"00:00:08"}`}, {3e9, `{"Utc":"2025-03-10T10:00:02Z","Remaining":"00:00:07"}`}},
	})
	var bodies []string
	for _, l := range e.logs {
		bodies = append(bodies, l.Body().Str())
	}
	if !slices.Equal(bodies, []string{"f1.session.status.changed", "[redacted-message]", "Second", "f1.session.status.changed", "f1.team_radio.available"}) {
		t.Fatal(bodies)
	}
	radio := e.logs[4]
	if radio.TraceID().IsEmpty() || radio.SpanID().IsEmpty() || radio.Timestamp().AsTime().Nanosecond() != 100000000 {
		t.Fatal("radio correlation")
	}
	if len(e.spans) != 1 || e.spans[0].Events().Len() != 4 {
		t.Fatal("root event ownership")
	}
}
func TestConflictingLapKeepsSectorMeasurements(t *testing.T) {
	for _, boundary := range []int64{2e9, 6e9} {
		e := replayFixture(t, map[string][]StreamRecord{
			"SessionData": {{0, `{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`}},
			"TimingData":  {{0, `{"Lines":{"17":{"NumberOfLaps":1}}}`}, {boundary, `{"Lines":{"17":{"NumberOfLaps":2,"LastLapTime":{"Value":"5.999"},"Sectors":[{"Value":"1.000"},{"Value":"2.000"},{"Value":"3.000"}]}}}`}},
		})
		if len(e.metrics["f1.driver.lap_time"]) != 0 || len(e.metrics["f1.driver.last_lap_time"]) != 0 {
			t.Fatal("accepted conflicting lap")
		}
		var gauges, hist []float64
		for _, m := range e.metrics["f1.driver.last_sector_time"] {
			for _, p := range m.Gauge().DataPoints().All() {
				gauges = append(gauges, p.DoubleValue())
			}
		}
		for _, m := range e.metrics["f1.driver.sector_time"] {
			for _, p := range m.Histogram().DataPoints().All() {
				if p.Count() != 1 {
					t.Fatal(p.Count())
				}
				hist = append(hist, p.Sum())
			}
		}
		for _, got := range [][]float64{gauges, hist} {
			if !slices.Equal(got, []float64{1, 2, 3}) {
				t.Fatal(boundary, got)
			}
		}
	}
}
func TestPendingLapCutoff(t *testing.T) {
	for _, tc := range []struct {
		offset int64
		want   bool
	}{{5e9 - 1, true}, {5e9, true}, {5e9 + 1, false}} {
		t.Run(fmt.Sprint(tc.offset), func(t *testing.T) {
			e := replayFixture(t, map[string][]StreamRecord{"SessionData": {{0, `{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`}}, "TimingData": {{0, `{"Lines":{"17":{"NumberOfLaps":1}}}`}, {3e9, `{"Lines":{"17":{"NumberOfLaps":2}}}`}, {3e9 + tc.offset, `{"Lines":{"17":{"LastLapTime":{"Value":"3.000"}}}}`}}})
			ms := e.metrics["f1.driver.lap_time"]
			if (len(ms) != 0) != tc.want {
				t.Fatal(tc, len(ms))
			}
			if tc.want && ms[0].Histogram().DataPoints().At(0).Sum() != 3 {
				t.Fatal("wrong lap duration")
			}
		})
	}
}
func TestHistoricalRepairDecisions(t *testing.T) {
	e := replayFixture(t, map[string][]StreamRecord{"SessionData": {{0, `{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`}}, "TimingData": {{0, `{"Lines":{"17":{"NumberOfLaps":1}}}`}, {6e9, `{"Lines":{"17":{"NumberOfLaps":2,"LastLapTime":{"Value":"6.000"},"Sectors":[{"Value":"1.000"},{"Value":"2.000"},{"Value":"3.000"}]}}}`}, {12e9, `{"Lines":{"17":{"NumberOfLaps":3,"LastLapTime":{"Value":"6.000"},"Sectors":{"0":{"Value":"1.000"},"2":{"Value":"3.000"}}}}}`}, {18e9, `{"Lines":{"17":{"NumberOfLaps":4,"LastLapTime":{"Value":"5.999","OverallFastest":true},"Sectors":[{"Value":"1.000"},{"Value":"2.000"},{"Value":"3.000"}]}}}`}},
		"RaceControlMessages": {{13e9, `{"Messages":[{"Category":"Other","Message":"CAR 17 (FIG) TIME 0:06.000 DELETED - TRACK LIMITS AT TURN 9 LAP 3 10:00:12"},{"Category":"Other","Message":"CAR 17 (FIG) LAP 3 WILL BE REINSTATED"}]}`}}})
	var qualities []any
	var decisions []string
	for _, sp := range e.spans {
		if strings.HasPrefix(sp.Name(), "lap ") {
			qualities = append(qualities, sp.Attributes().AsRaw()["f1.timing.value_quality"])
		}
		for _, ev := range sp.Events().All() {
			switch ev.Name() {
			case "f1.fastest_lap":
				t.Fatal("conflicting duration created fastest lap")
			case "f1.lap_time.deleted", "f1.lap_time.reinstated":
				decisions = append(decisions, ev.Name())
				if sp.Attributes().AsRaw()["f1.lap.number"] != int64(2) || ev.Attributes().AsRaw()["f1.race_control.reason_lap"] != int64(3) {
					t.Fatal(ev.Name())
				}
			}
		}
	}
	if !reflect.DeepEqual(qualities, []any{"reported", "reported", "conflict", nil}) || !slices.Equal(decisions, []string{"f1.lap_time.deleted", "f1.lap_time.reinstated"}) {
		t.Fatal(qualities, decisions)
	}
}
