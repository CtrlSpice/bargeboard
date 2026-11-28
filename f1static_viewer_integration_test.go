package main

import (
	"bytes"
	"compress/flate"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"github.com/CtrlSpice/otel-desktop-viewer/desktopexporter/duckdbextension"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/yamlprovider"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/extension/extensioncapabilities"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"go.opentelemetry.io/collector/pipeline"
)

// staticReplayCompletion synchronizes on both Collector pipeline readiness and
// f1static reaching StatusOK across all three signal pipelines before shutdown
// flushes batched signals to DuckDB. No sleeps or database polls are used.
type staticReplayCompletion struct {
	mu          sync.Mutex
	viewer      *duckdbextension.DuckDBExtension
	ready       bool
	okPipelines map[pipeline.ID]bool
	sendOnce    sync.Once
	completed   chan *duckdbextension.DuckDBExtension
	failed      chan string
}

var _ extensioncapabilities.PipelineWatcher = (*staticReplayCompletion)(nil)
var _ extensioncapabilities.ComponentStatusWatcher = (*staticReplayCompletion)(nil)
var _ extensioncapabilities.Dependent = (*staticReplayCompletion)(nil)

func (*staticReplayCompletion) Dependencies() []component.ID {
	return []component.ID{component.MustNewID("duckdb")}
}

func (c *staticReplayCompletion) Start(_ context.Context, host component.Host) error {
	viewer, ok := host.GetExtensions()[component.MustNewID("duckdb")].(*duckdbextension.DuckDBExtension)
	if !ok {
		return fmt.Errorf("test completion extension requires the DuckDB viewer")
	}
	c.mu.Lock()
	c.viewer = viewer
	c.mu.Unlock()
	return nil
}

func (*staticReplayCompletion) Shutdown(context.Context) error { return nil }
func (*staticReplayCompletion) NotReady() error                { return nil }

func (c *staticReplayCompletion) Ready() error {
	c.mu.Lock()
	c.ready = true
	c.maybeCompleteLocked()
	c.mu.Unlock()
	return nil
}

func (c *staticReplayCompletion) Changed(id *componentstatus.InstanceID, event *componentstatus.Event) {
	if id.ComponentID().String() != "f1static" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch event.Status() {
	case componentstatus.StatusOK:
		id.AllPipelineIDs(func(pid pipeline.ID) bool {
			c.okPipelines[pid] = true
			return true
		})
		c.maybeCompleteLocked()
	case componentstatus.StatusRecoverableError:
		id.AllPipelineIDs(func(pid pipeline.ID) bool {
			delete(c.okPipelines, pid)
			return true
		})
	case componentstatus.StatusPermanentError, componentstatus.StatusFatalError:
		select {
		case c.failed <- event.Status().String():
		default:
		}
	}
}

func (c *staticReplayCompletion) maybeCompleteLocked() {
	if !c.ready || c.viewer == nil || len(c.okPipelines) < 3 {
		return
	}
	viewer := c.viewer
	c.sendOnce.Do(func() {
		c.completed <- viewer
	})
}

func deflateBase64JSONLine(t *testing.T, offset, rawJSON string) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, rawJSON); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s%q", offset, base64.StdEncoding.EncodeToString(buf.Bytes()))
}

func TestViewerPersistsF1StaticReplayWaterfall(t *testing.T) {
	const (
		sessionPath  = "2025/2025-05-04_Invented_Grand_Prix/2025-05-04_Race"
		archiveToken = "synthetic-archive-bearer-token"
	)

	pos1 := deflateBase64JSONLine(t, "00:00:30.000",
		`{"Position":[{"Timestamp":"2025-05-04T15:00:30Z","Entries":{"17":{"X":1250,"Y":-340,"Z":15},"31":{"X":800,"Y":200,"Z":10}}}]}`)
	pos2 := deflateBase64JSONLine(t, "00:01:30.000",
		`{"Position":[{"Timestamp":"2025-05-04T15:01:30Z","Entries":{"17":{"X":1260,"Y":-340,"Z":15},"31":{"X":810,"Y":210,"Z":10}}}]}`)

	archiveFiles := map[string]string{
		"SessionInfo.json":     `{"Key":9101,"Type":"Race","Name":"Race","Meeting":{"Key":1304,"Name":"Invented Grand Prix","Circuit":{"Key":55,"ShortName":"Invented Ring"}},"StartDate":"2025-05-04T15:00:00","EndDate":"2025-05-04T17:00:00","GmtOffset":"00:00:00"}`,
		"DriverList.json":      `{"17":{"RacingNumber":"17","Tla":"FIG","FullName":"Figaro Valenti","TeamName":"Synthetic Corsa"},"31":{"RacingNumber":"31","Tla":"PEA","FullName":"Pearl Renard","TeamName":"Synthetic Corsa"}}`,
		"Heartbeat.jsonStream": `00:00:00.000{"Utc":"2025-05-04T15:00:00Z"}`,
		"SessionData.jsonStream": strings.Join([]string{
			`00:00:00.000{"StatusSeries":[{"Utc":"2025-05-04T15:00:00Z","SessionStatus":"Started","TrackStatus":"AllClear"}]}`,
			`00:01:40.000{"StatusSeries":{"1":{"Utc":"2025-05-04T15:01:40Z","SessionStatus":"Finished"}}}`,
			`00:01:45.000{"StatusSeries":{"2":{"Utc":"2025-05-04T15:01:45Z","SessionStatus":"Finalised"}}}`,
			`00:01:50.000{"StatusSeries":{"3":{"Utc":"2025-05-04T15:01:50Z","SessionStatus":"Ends"}}}`,
		}, "\n"),
		"LapCount.jsonStream": strings.Join([]string{
			`00:00:01.000{"CurrentLap":1,"TotalLaps":2}`,
			`00:01:30.000{"CurrentLap":2,"TotalLaps":2}`,
		}, "\n"),
		"TimingAppData.jsonStream": `00:00:01.000{"Lines":{"17":{"Stints":{"0":{"Compound":"MEDIUM","New":"true","StartLaps":0,"TotalLaps":0}}},"31":{"Stints":{"0":{"Compound":"HARD","New":"true","StartLaps":0,"TotalLaps":0}}}}}`,
		"CarData.z.jsonStream":     deflateBase64JSONLine(t, "00:00:05.000", `{"Entries":[{"Utc":"2025-05-04T15:00:05Z","Cars":{"17":{"Channels":{"0":10240,"2":256}}}}]}`),
		"Position.z.jsonStream":    pos1 + "\n" + pos2,
		"TimingData.jsonStream": strings.Join([]string{
			`00:00:01.000{"Lines":{"17":{"Position":"2","NumberOfLaps":1,"InPit":false,"NumberOfPitStops":0},"31":{"Position":"1","NumberOfLaps":1,"InPit":false,"NumberOfPitStops":0}}}`,
			`00:00:30.000{"Lines":{"17":{"Position":"1","Sectors":{"0":{"Value":"29.000"}}},"31":{"Position":"2"}}}`,
			`00:01:00.000{"Lines":{"17":{"Sectors":{"1":{"Value":"30.000"}}}}}`,
			`00:01:30.000{"Lines":{"17":{"NumberOfLaps":2,"Sectors":{"2":{"Value":"30.000"}},"LastLapTime":{"Value":"1:29.000","PersonalFastest":true,"OverallFastest":true},"BestLapTime":{"Value":"1:29.000","Lap":1}}}}`,
			`00:01:36.000{"Lines":{"31":{"Speeds":{"ST":{"Value":"312"}}}}}`,
		}, "\n"),
		"RaceControlMessages.jsonStream": strings.Join([]string{
			`00:00:02.000{"Messages":{"0":{"Utc":"2025-05-04T15:00:02.000","Category":"Flag","Scope":"Track","Flag":"GREEN","Message":"GREEN LIGHT - PIT EXIT OPEN"}}}`,
			`00:00:30.000{"Messages":{"1":{"Utc":"2025-05-04T15:00:30.000","Category":"Flag","Scope":"Driver","Flag":"BLUE","RacingNumber":"31","Lap":1,"Message":"WAVED BLUE FLAG FOR CAR 31 (PEA) TIMED AT 15:00:30"}}}`,
		}, "\n"),
	}

	archiveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+archiveToken {
			t.Errorf("archive Authorization = %q, want Bearer token", r.Header.Get("Authorization"))
		}
		name := strings.TrimPrefix(r.URL.Path, "/"+sessionPath+"/")
		if body, ok := archiveFiles[name]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(archiveServer.Close)

	circuitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("archive credential leaked to circuit server: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/55/2025" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"year":2025,"circuitKey":55,"circuitName":"Invented Ring","corners":[{"number":4,"letter":"A","trackPosition":{"x":1250,"y":-340}}],"marshalSectors":[{"number":2,"trackPosition":{"x":1250,"y":-340}}]}`)
	}))
	t.Cleanup(circuitServer.Close)

	tmpDir := t.TempDir()
	tokenFile := filepath.Join(tmpDir, "archive-token")
	if err := os.WriteFile(tokenFile, []byte(archiveToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(tmpDir, "f1-replay.duckdb")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	viewerEndpoint := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	config := fmt.Sprintf(`
receivers:
  f1static:
    enabled: true
    endpoint: %q
    session_path: %q
    pace: instant
    auth:
      token_file: %q
    circuit_enrichment:
      enabled: true
      endpoint: %q
processors:
  batch:
    send_batch_size: 8192
    send_batch_max_size: 20000
    timeout: 1h
exporters:
  desktop:
extensions:
  duckdb:
    endpoint: %s
    db: %q
    db_max_size: "0"
  staticdone:
service:
  extensions: [duckdb, staticdone]
  telemetry:
    logs:
      level: error
    metrics:
      level: none
  pipelines:
    traces:
      receivers: [f1static]
      processors: [batch]
      exporters: [desktop]
    metrics:
      receivers: [f1static]
      processors: [batch]
      exporters: [desktop]
    logs:
      receivers: [f1static]
      processors: [batch]
      exporters: [desktop]
`, archiveServer.URL, sessionPath, tokenFile, circuitServer.URL, viewerEndpoint, dbPath)

	completed := make(chan *duckdbextension.DuckDBExtension, 1)
	failed := make(chan string, 1)
	collector, err := otelcol.NewCollector(otelcol.CollectorSettings{
		Factories: func() (otelcol.Factories, error) {
			factories, err := components()
			if err != nil {
				return factories, err
			}
			kind := component.MustNewType("staticdone")
			factories.Extensions[kind] = extension.NewFactory(kind,
				func() component.Config { return &struct{}{} },
				func(context.Context, extension.Settings, component.Config) (extension.Extension, error) {
					return &staticReplayCompletion{
						okPipelines: map[pipeline.ID]bool{},
						completed:   completed,
						failed:      failed,
					}, nil
				},
				component.StabilityLevelDevelopment)
			return factories, nil
		},
		DisableGracefulShutdown: true,
		SkipSettingGRPCLogger:   true,
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				URIs:              []string{"yaml:" + config},
				ProviderFactories: []confmap.ProviderFactory{yamlprovider.NewFactory()},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	runStart := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- collector.Run(ctx) }()

	stopReplay := sync.OnceFunc(func() {
		collector.Shutdown()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("shut down replay Collector: %v", err)
		}
	})
	t.Cleanup(stopReplay)

	select {
	case <-completed:
	case status := <-failed:
		stopReplay()
		t.Fatalf("f1static reported failure status %s", status)
	case err := <-done:
		done <- err
		t.Fatalf("Collector exited before replay completed: %v", err)
	case <-ctx.Done():
		stopReplay()
		t.Fatalf("timed out waiting for f1static replay completion: %v", ctx.Err())
	}

	stopReplay()

	// Reopen the persisted DuckDB file in a fresh Collector instance to verify
	// durable waterfall spans, corner enrichment, metrics, and race-control logs.
	viewerURL, _, viewer, _ := startPersistentViewer(t, dbPath)
	client := &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	identity := f1id.SessionIdentity{
		Season:      2025,
		MeetingKey:  1304,
		SessionType: f1id.SessionTypeRace,
		SessionName: "race",
	}
	driver17TraceID, err := f1id.DriverTraceID(identity, 17)
	if err != nil {
		t.Fatal(err)
	}
	wantRootID, _ := f1id.DriverSessionRootSpanID(identity, 17)
	wantStint1ID, _ := f1id.StintSpanID(identity, 17, f1id.PhaseNone, 1)
	wantLap1ID, _ := f1id.LapSpanID(identity, 17, f1id.PhaseNone, 1)
	wantLap2ID, _ := f1id.LapSpanID(identity, 17, f1id.PhaseNone, 2)
	wantSector1ID, _ := f1id.SectorSpanID(identity, 17, f1id.PhaseNone, 1, 1)
	wantSector2ID, _ := f1id.SectorSpanID(identity, 17, f1id.PhaseNone, 1, 2)
	wantSector3ID, _ := f1id.SectorSpanID(identity, 17, f1id.PhaseNone, 1, 3)

	var logIDs, metricIDs []string
	if err := viewer.Store().WithDBRead(func(db *sql.DB) error {
		var spanCount int
		if err := db.QueryRowContext(t.Context(), "select count(*) from spans").Scan(&spanCount); err != nil {
			return err
		}
		if spanCount == 0 {
			return fmt.Errorf("persisted spans = 0")
		}
		collectIDs := func(table string) ([]string, error) {
			rows, err := db.QueryContext(t.Context(), "select id::varchar from "+table)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var out []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return nil, err
				}
				out = append(out, id)
			}
			return out, rows.Err()
		}
		var err error
		if logIDs, err = collectIDs("logs"); err != nil {
			return err
		}
		if metricIDs, err = collectIDs("metric_records"); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(logIDs) == 0 || len(metricIDs) == 0 {
		t.Fatalf("persisted logs=%d metrics=%d, want both non-empty", len(logIDs), len(metricIDs))
	}

	fetchExport := func(path string) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, viewerURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if resp.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
			t.Fatalf("GET %s: status=%d read=%v close=%v body=%s", path, resp.StatusCode, readErr, closeErr, body)
		}
		if bytes.Contains(body, []byte(archiveToken)) {
			t.Fatalf("exported payload leaked archive credential")
		}
		return body
	}

	// 1. Verify driver #17 waterfall spans (driver.session -> stint -> lap -> sector)
	// and MultiViewer corner / marshal-sector coordinate enrichment.
	traceExport := ptraceotlp.NewExportRequest()
	if err := traceExport.UnmarshalJSON(fetchExport("/export/traces/" + driver17TraceID.String())); err != nil {
		t.Fatal(err)
	}
	type spanLink struct {
		name   string
		parent pcommon.SpanID
	}
	gotSpans := map[pcommon.SpanID]spanLink{}
	var gotPositionCorner, gotFastestLapCorner bool
	rs := traceExport.Traces().ResourceSpans()
	for i := 0; i < rs.Len(); i++ {
		resAttrs := rs.At(i).Resource().Attributes().AsRaw()
		if resAttrs["service.name"] != "bargeboard" {
			t.Fatalf("resource service.name = %v, want bargeboard", resAttrs["service.name"])
		}
		ss := rs.At(i).ScopeSpans()
		for j := 0; j < ss.Len(); j++ {
			if ss.At(j).Scope().Name() != f1id.ScopeName || ss.At(j).Scope().Version() != f1id.ScopeVersion {
				t.Fatalf("scope = %s@%s", ss.At(j).Scope().Name(), ss.At(j).Scope().Version())
			}
			spans := ss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				sp := spans.At(k)
				if sp.TraceID() != driver17TraceID {
					t.Fatalf("span %s traceID = %s, want %s", sp.Name(), sp.TraceID(), driver17TraceID)
				}
				gotSpans[sp.SpanID()] = spanLink{name: sp.Name(), parent: sp.ParentSpanID()}
				if sp.SpanID() == wantLap1ID {
					if sp.EndTimestamp()-sp.StartTimestamp() != pcommon.Timestamp(89*time.Second) {
						t.Fatalf("lap 1 duration = %d ns, want 89s", sp.EndTimestamp()-sp.StartTimestamp())
					}
					for e := 0; e < sp.Events().Len(); e++ {
						ev := sp.Events().At(e)
						attrs := ev.Attributes().AsRaw()
						switch ev.Name() {
						case "f1.position.changed":
							gotPositionCorner = attrs["f1.position.x"] == 125.0 &&
								attrs["f1.position.y"] == -34.0 &&
								attrs["f1.position.z"] == 1.5 &&
								attrs["f1.position.sample_age"] == 0.0 &&
								attrs["f1.circuit.corner.number"] == int64(4) &&
								attrs["f1.circuit.corner.label"] == "4A" &&
								attrs["f1.circuit.corner.distance"] == 0.0 &&
								attrs["f1.circuit.marshal_sector"] == int64(2)
						case "f1.fastest_lap":
							gotFastestLapCorner = attrs["f1.position.x"] == 126.0 &&
								attrs["f1.position.y"] == -34.0 &&
								attrs["f1.circuit.corner.number"] == int64(4) &&
								attrs["f1.circuit.corner.label"] == "4A" &&
								attrs["f1.circuit.corner.distance"] == 1.0 &&
								attrs["f1.circuit.marshal_sector"] == int64(2)
						}
					}
				}
			}
		}
	}
	wantSpans := map[pcommon.SpanID]spanLink{
		wantRootID:    {name: "driver.session FIG", parent: pcommon.SpanID{}},
		wantStint1ID:  {name: "stint 1", parent: wantRootID},
		wantLap1ID:    {name: "lap 1", parent: wantStint1ID},
		wantLap2ID:    {name: "lap 2", parent: wantStint1ID},
		wantSector1ID: {name: "sector 1", parent: wantLap1ID},
		wantSector2ID: {name: "sector 2", parent: wantLap1ID},
		wantSector3ID: {name: "sector 3", parent: wantLap1ID},
	}
	if !reflect.DeepEqual(gotSpans, wantSpans) {
		t.Fatalf("driver 17 waterfall hierarchy:\ngot  %+v\nwant %+v", gotSpans, wantSpans)
	}
	if !gotPositionCorner || !gotFastestLapCorner {
		t.Fatalf("corner enrichment missing: positionChanged=%v fastestLap=%v", gotPositionCorner, gotFastestLapCorner)
	}

	// 2. Verify persisted F1 metrics (Delta histograms and Gauges, CarData ExponentialHistograms).
	metricNames := map[string]bool{}
	var lapHistChecked, sectorHistChecked bool
	for _, id := range metricIDs {
		exported := pmetricotlp.NewExportRequest()
		if err := exported.UnmarshalJSON(fetchExport("/export/metrics/" + id)); err != nil {
			t.Fatal(err)
		}
		rms := exported.Metrics().ResourceMetrics()
		for i := 0; i < rms.Len(); i++ {
			sms := rms.At(i).ScopeMetrics()
			for j := 0; j < sms.Len(); j++ {
				ms := sms.At(j).Metrics()
				for k := 0; k < ms.Len(); k++ {
					m := ms.At(k)
					metricNames[m.Name()] = true
					if m.Name() == "f1.field.speed" || m.Name() == "f1.field.engine_speed" {
						checkViewerCarHistogram(t, m, driver17TraceID, func(id pcommon.SpanID) bool { return id == wantLap1ID })
					}
					if strings.HasPrefix(m.Name(), "f1.car.") {
						t.Fatalf("unexpected YELLOW CarData metric persisted: %s", m.Name())
					}
					switch m.Name() {
					case "f1.driver.lap_time":
						dp := m.Histogram().DataPoints().At(0)
						if m.Histogram().AggregationTemporality() != pmetric.AggregationTemporalityDelta ||
							dp.Count() != 1 || dp.Sum() != 89.0 || dp.ExplicitBounds().Len() != 74 {
							t.Fatalf("unexpected f1.driver.lap_time datapoint: count=%d sum=%v bounds=%d",
								dp.Count(), dp.Sum(), dp.ExplicitBounds().Len())
						}
						lapHistChecked = true
					case "f1.driver.sector_time":
						dp := m.Histogram().DataPoints().At(0)
						if m.Histogram().AggregationTemporality() != pmetric.AggregationTemporalityDelta ||
							dp.ExplicitBounds().Len() != 78 {
							t.Fatalf("unexpected f1.driver.sector_time bounds=%d", dp.ExplicitBounds().Len())
						}
						sectorHistChecked = true
					}
				}
			}
		}
	}
	for _, wantMetric := range []string{"f1.field.speed", "f1.field.engine_speed",
		"f1.driver.lap_time",
		"f1.driver.sector_time",
		"f1.driver.best_lap_time",
		"f1.driver.position",
		"f1.driver.tyre.age",
		"f1.session.current_lap",
		"f1.session.intended_total_laps",
	} {
		if !metricNames[wantMetric] {
			t.Fatalf("missing persisted metric %q in %v", wantMetric, metricNames)
		}
	}
	if !lapHistChecked || !sectorHistChecked {
		t.Fatalf("histograms not verified: lap=%v sector=%v", lapHistChecked, sectorHistChecked)
	}

	// 3. Verify persisted race-control and session-status logs retain original
	// historical 2025 timestamps distinct from process ObservedTimestamp.
	var gotGreenFlagLog, gotBlueFlagLog, gotSessionStatusLog bool
	for _, id := range logIDs {
		exported := plogotlp.NewExportRequest()
		if err := exported.UnmarshalJSON(fetchExport("/export/logs/" + id)); err != nil {
			t.Fatal(err)
		}
		rls := exported.Logs().ResourceLogs()
		for i := 0; i < rls.Len(); i++ {
			sls := rls.At(i).ScopeLogs()
			for j := 0; j < sls.Len(); j++ {
				lrs := sls.At(j).LogRecords()
				for k := 0; k < lrs.Len(); k++ {
					lr := lrs.At(k)
					if lr.SeverityNumber() != plog.SeverityNumberInfo {
						t.Fatalf("log severity = %v, want INFO", lr.SeverityNumber())
					}
					eventTime := lr.Timestamp().AsTime()
					observedTime := lr.ObservedTimestamp().AsTime()
					if eventTime.Year() != 2025 || observedTime.Before(runStart.Add(-time.Second)) {
						t.Fatalf("log timestamps: event=%s observed=%s runStart=%s", eventTime, observedTime, runStart)
					}
					attrs := lr.Attributes().AsRaw()
					switch lr.Body().Str() {
					case "GREEN LIGHT - PIT EXIT OPEN":
						gotGreenFlagLog = attrs["f1.race_control.category"] == "flag" &&
							attrs["f1.race_control.scope"] == "track" &&
							attrs["f1.race_control.flag"] == "green" &&
							attrs["f1.data.source"] == "livetiming"
					case "WAVED BLUE FLAG FOR CAR 31 (PEA) TIMED AT 15:00:30":
						gotBlueFlagLog = attrs["f1.race_control.category"] == "flag" &&
							attrs["f1.race_control.scope"] == "driver" &&
							attrs["f1.race_control.flag"] == "blue" &&
							attrs["f1.driver.number"] == int64(31) &&
							attrs["f1.driver.acronym"] == "PEA"
					case "f1.session.status.changed":
						if attrs["f1.session.status"] == "Started" {
							gotSessionStatusLog = true
						}
					}
				}
			}
		}
	}
	if !gotGreenFlagLog || !gotBlueFlagLog || !gotSessionStatusLog {
		t.Fatalf("missing persisted logs: greenFlag=%v blueFlag=%v sessionStatus=%v",
			gotGreenFlagLog, gotBlueFlagLog, gotSessionStatusLog)
	}
}
