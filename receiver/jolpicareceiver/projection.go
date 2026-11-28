package jolpicareceiver

import (
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

func session(t table) string {
	if t == race {
		return "race"
	}
	return "sprint"
}
func attributes(a pcommon.Map, c Config, t table, r row) {
	a.PutInt("f1.season.year", c.Season)
	a.PutInt("f1.round.number", c.Round)
	a.PutStr("f1.data.source", "jolpica")
	if c.MeetingKey > 0 {
		a.PutInt("f1.meeting.key", c.MeetingKey)
	}
	if t.classification() {
		a.PutStr("f1.session.type", session(t))
		a.PutStr("f1.session.name", session(t))
	}
	if r.number > 0 {
		a.PutInt("f1.driver.number", r.number)
	}
	if r.acronym != "" {
		a.PutStr("f1.driver.acronym", r.acronym)
	}
	if t != wcc {
		a.PutStr("f1.driver.id", r.driverID)
	}
	if t == wcc {
		a.PutStr("f1.constructor.id", r.teamID)
		a.PutStr("f1.constructor.name", r.team)
	}
}

func projectLogs(c Config, s snapshot, version string, at time.Time) plog.Logs {
	out := plog.NewLogs()
	resource := out.ResourceLogs().AppendEmpty()
	f1id.InitResource(resource.Resource(), version)
	scope := resource.ScopeLogs().AppendEmpty()
	f1id.InitScope(scope.Scope())
	ts := pcommon.NewTimestampFromTime(at)
	for _, t := range []table{race, sprint} {
		for _, r := range s[t] {
			l := scope.LogRecords().AppendEmpty()
			l.Body().SetStr("f1.classification.fia")
			l.SetSeverityNumber(plog.SeverityNumberInfo)
			l.SetSeverityText("INFO")
			l.SetTimestamp(ts)
			l.SetObservedTimestamp(ts)
			a := l.Attributes()
			attributes(a, c, t, r)
			a.PutStr("f1.time.quality", "collector_observation")
			a.PutStr("f1.constructor.name", r.team)
			a.PutStr("f1.classification.position_text", r.positionText)
			if r.position > 0 {
				a.PutInt("f1.classification.position", r.position)
			}
			a.PutStr("f1.classification.status", r.status)
			a.PutStr("f1.classification.status_category", statusCategory(r.status))
			if r.hasGrid {
				a.PutInt("f1.classification.grid", r.grid)
			}
			if r.hasLaps {
				a.PutInt("f1.classification.laps_completed", r.laps)
			}
			a.PutDouble("f1.classification.points", r.points)
			if c.CorrelateTraces && c.MeetingKey > 0 {
				e := f1id.SessionIdentity{Season: c.Season, MeetingKey: c.MeetingKey, SessionType: f1id.SessionType(session(t)), SessionName: f1id.SessionName(session(t))}
				trace, _ := f1id.DriverTraceID(e, r.number)
				span, _ := f1id.DriverSessionRootSpanID(e, r.number)
				l.SetTraceID(trace)
				l.SetSpanID(span)
			}
		}
	}
	return out
}

func projectMetrics(c Config, s snapshot, version string, at time.Time) pmetric.Metrics {
	out := pmetric.NewMetrics()
	resource := out.ResourceMetrics().AppendEmpty()
	f1id.InitResource(resource.Resource(), version)
	scope := resource.ScopeMetrics().AppendEmpty()
	f1id.InitScope(scope.Scope())
	gauges := map[string]pmetric.Gauge{}
	ts := pcommon.NewTimestampFromTime(at)
	for _, t := range []table{race, sprint, wdc, wcc} {
		for _, r := range s[t] {
			point := func(name, unit string) pmetric.NumberDataPoint {
				g, ok := gauges[name]
				if !ok {
					m := scope.Metrics().AppendEmpty()
					m.SetName(name)
					m.SetUnit(unit)
					g = m.SetEmptyGauge()
					gauges[name] = g
				}
				p := g.DataPoints().AppendEmpty()
				p.SetTimestamp(ts)
				attributes(p.Attributes(), c, t, r)
				return p
			}
			if t.classification() {
				point("f1.classification.points", "{point}").SetDoubleValue(r.points)
				if r.hasGrid && r.grid > 0 {
					point("f1.classification.grid_position", "{position}").SetIntValue(r.grid)
				}
				continue
			}
			prefix := "f1.championship.driver."
			if t == wcc {
				prefix = "f1.championship.constructor."
			}
			point(prefix+"points", "{point}").SetDoubleValue(r.points)
			if r.position > 0 {
				point(prefix+"position", "{position}").SetIntValue(r.position)
			}
			point(prefix+"wins", "{win}").SetIntValue(r.wins)
		}
	}
	return out
}
