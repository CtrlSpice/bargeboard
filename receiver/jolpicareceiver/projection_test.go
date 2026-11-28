package jolpicareceiver

import (
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

func parsed(t *testing.T) snapshot {
	t.Helper()
	s := snapshot{}
	for _, k := range []table{race, sprint, wdc, wcc} {
		rows, err := parse(config(), k, []byte(fixture(t, k)))
		if err != nil {
			t.Fatal(err)
		}
		s[k] = rows
	}
	return s
}
func TestLogs(t *testing.T) {
	s := parsed(t)
	at := time.Unix(120, 123)
	ts := pcommon.NewTimestampFromTime(at)
	for _, c := range []Config{config(), func() Config { c := config(); c.MeetingKey = 55; return c }(), func() Config { c := config(); c.MeetingKey = 55; c.CorrelateTraces = true; return c }()} {
		out := projectLogs(c, s, "test", at)
		resource := out.ResourceLogs().At(0)
		scope := resource.ScopeLogs().At(0)
		if out.LogRecordCount() != 3 || scope.Scope().Name() != f1id.ScopeName || scope.Scope().Version() != "1.0.0" || resource.Resource().Attributes().AsRaw()["service.version"] != "test" {
			t.Fatal("logs resource, scope or count")
		}
		for i, input := range []struct {
			session  string
			r        row
			category string
		}{{"race", s[race][0], "finished"}, {"race", s[race][1], "mechanical"}, {"sprint", s[sprint][0], "finished"}} {
			r := input.r
			l := scope.LogRecords().At(i)
			if l.Timestamp() != ts || l.ObservedTimestamp() != ts || l.SeverityNumber() != plog.SeverityNumberInfo || l.SeverityText() != "INFO" || l.Body().Str() != "f1.classification.fia" {
				t.Fatal("incorrect log envelope")
			}
			want := map[string]any{"f1.season.year": int64(2025), "f1.round.number": int64(4), "f1.data.source": "jolpica", "f1.session.type": input.session, "f1.session.name": input.session, "f1.time.quality": "collector_observation", "f1.driver.id": r.driverID, "f1.driver.number": r.number, "f1.constructor.name": r.team, "f1.classification.status": r.status, "f1.classification.status_category": input.category, "f1.classification.grid": r.grid, "f1.classification.points": r.points, "f1.classification.laps_completed": r.laps, "f1.classification.position_text": r.positionText}
			if r.position > 0 {
				want["f1.classification.position"] = r.position
			}
			if r.acronym != "" {
				want["f1.driver.acronym"] = r.acronym
			}
			if c.MeetingKey > 0 {
				want["f1.meeting.key"] = c.MeetingKey
			}
			if got := l.Attributes().AsRaw(); !reflect.DeepEqual(got, want) {
				t.Errorf("got %v want %v", got, want)
			}
			var trace pcommon.TraceID
			var span pcommon.SpanID
			if c.CorrelateTraces {
				e := f1id.SessionIdentity{Season: 2025, MeetingKey: 55, SessionType: f1id.SessionType(input.session), SessionName: f1id.SessionName(input.session)}
				trace, _ = f1id.DriverTraceID(e, r.number)
				span, _ = f1id.DriverSessionRootSpanID(e, r.number)
			}
			if l.TraceID() != trace || l.SpanID() != span {
				t.Errorf("incorrect correlation on %s %d", input.session, r.number)
			}
		}
	}
}

func TestMetrics(t *testing.T) {
	c := config()
	c.MeetingKey = 55
	at := time.Unix(345, 0)
	out := projectMetrics(c, parsed(t), "test", at)
	scope := out.ResourceMetrics().At(0).ScopeMetrics().At(0)
	common := map[string]any{"f1.season.year": int64(2025), "f1.round.number": int64(4), "f1.meeting.key": int64(55), "f1.data.source": "jolpica"}
	attrs := func(extra map[string]any) map[string]any { m := maps.Clone(common); maps.Copy(m, extra); return m }
	r := attrs(map[string]any{"f1.session.type": "race", "f1.session.name": "race", "f1.driver.id": "elm", "f1.driver.number": int64(17), "f1.driver.acronym": "ELM"})
	r2 := attrs(map[string]any{"f1.session.type": "race", "f1.session.name": "race", "f1.driver.id": "ash", "f1.driver.number": int64(93)})
	sp := maps.Clone(r)
	sp["f1.session.type"] = "sprint"
	sp["f1.session.name"] = "sprint"
	d := attrs(map[string]any{"f1.driver.number": int64(17), "f1.driver.acronym": "ELM", "f1.driver.id": "elm"})
	historic := attrs(map[string]any{"f1.driver.id": "ash"})
	team := attrs(map[string]any{"f1.constructor.id": "copper", "f1.constructor.name": "Copper Works"})
	type point struct {
		value any
		attrs map[string]any
	}
	want := map[string]struct {
		unit   string
		points []point
	}{
		"f1.classification.points":             {"{point}", []point{{7.5, r}, {0.0, r2}, {8.0, sp}}},
		"f1.classification.grid_position":      {"{position}", []point{{int64(2), r2}, {int64(3), sp}}},
		"f1.championship.driver.points":        {"{point}", []point{{35.5, d}, {0.0, historic}}},
		"f1.championship.driver.position":      {"{position}", []point{{int64(1), d}}},
		"f1.championship.driver.wins":          {"{win}", []point{{int64(2), d}, {int64(0), historic}}},
		"f1.championship.constructor.points":   {"{point}", []point{{42.5, team}}},
		"f1.championship.constructor.position": {"{position}", []point{{int64(1), team}}},
		"f1.championship.constructor.wins":     {"{win}", []point{{int64(3), team}}},
	}
	for i := 0; i < scope.Metrics().Len(); i++ {
		m := scope.Metrics().At(i)
		w, ok := want[m.Name()]
		if !ok || m.Unit() != w.unit || m.Type() != pmetric.MetricTypeGauge || m.Gauge().DataPoints().Len() != len(w.points) {
			t.Fatalf("unexpected instrument %s", m.Name())
		}
		delete(want, m.Name())
		for j, expected := range w.points {
			p := m.Gauge().DataPoints().At(j)
			var v any = p.IntValue()
			if p.ValueType() == pmetric.NumberDataPointValueTypeDouble {
				v = p.DoubleValue()
			}
			if v != expected.value || !reflect.DeepEqual(p.Attributes().AsRaw(), expected.attrs) || p.Timestamp() != pcommon.NewTimestampFromTime(at) || p.StartTimestamp() != 0 {
				t.Errorf("%s: %v, %v", m.Name(), v, p.Attributes().AsRaw())
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing gauges: %v", want)
	}
}

func TestOptionalClassificationFields(t *testing.T) {
	for _, fields := range []string{"", `"position":null,"grid":null,"laps":null,`, `"grid":"0","laps":"0",`} {
		raw := strings.NewReplacer(`"position":"1",`, fields, `"grid":"3",`, "", `"laps":"6",`, "", `"Finished"`, `"Withdrew"`).Replace(fixture(t, sprint))
		rows, err := parse(config(), sprint, []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		s := snapshot{sprint: rows}
		a := projectLogs(config(), s, "test", time.Time{}).ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().AsRaw()
		for _, key := range []string{"grid", "laps_completed", "position", "pit_lane_start"} {
			v, ok := a["f1.classification."+key]
			want := strings.Contains(fields, `"0"`) && (key == "grid" || key == "laps_completed")
			if ok != want || ok && v != int64(0) {
				t.Errorf("%s: %s = %v, %v", fields, key, v, ok)
			}
		}
		if m := projectMetrics(config(), s, "test", time.Time{}); m.MetricCount() != 1 || m.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().At(0).Name() != "f1.classification.points" {
			t.Error("unexpected grid metric")
		}
	}
}
