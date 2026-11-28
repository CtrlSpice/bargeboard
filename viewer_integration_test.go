package main

import (
	"bytes"
	"compress/flate"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
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

	"github.com/CtrlSpice/otel-desktop-viewer/desktopexporter/duckdbextension"
	"github.com/coder/websocket"
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
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

// Pipeline readiness and graceful shutdown provide synchronization; the test
// does not wait for a batch timeout or poll the database for eventual writes.
type viewerReadiness struct {
	ready  chan *duckdbextension.DuckDBExtension
	viewer *duckdbextension.DuckDBExtension
}

var _ extensioncapabilities.PipelineWatcher = (*viewerReadiness)(nil)
var _ extensioncapabilities.Dependent = (*viewerReadiness)(nil)

func (*viewerReadiness) Dependencies() []component.ID {
	return []component.ID{component.MustNewID("duckdb")}
}

func (r *viewerReadiness) Start(_ context.Context, host component.Host) error {
	var ok bool
	r.viewer, ok = host.GetExtensions()[component.MustNewID("duckdb")].(*duckdbextension.DuckDBExtension)
	if !ok {
		return fmt.Errorf("test readiness extension requires the DuckDB viewer")
	}
	return nil
}

func (*viewerReadiness) Shutdown(context.Context) error { return nil }
func (*viewerReadiness) NotReady() error                { return nil }
func (r *viewerReadiness) Ready() error {
	r.ready <- r.viewer
	return nil
}

func newPersistentViewerCollector(t *testing.T, db string) (*otelcol.Collector, string, string, <-chan *duckdbextension.DuckDBExtension) {
	t.Helper()
	endpoint := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return address
	}
	viewerEndpoint, otlpEndpoint := endpoint(), endpoint()
	config := fmt.Sprintf(`
receivers:
  otlp:
    protocols:
      http:
        endpoint: %s
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
  viewerready:
service:
  extensions: [duckdb, viewerready]
  telemetry:
    logs:
      level: error
    metrics:
      level: none
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [desktop]
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [desktop]
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [desktop]
`, otlpEndpoint, viewerEndpoint, db)
	collector, ready := viewerCollector(t, config, nil)
	return collector, "http://" + viewerEndpoint, "http://" + otlpEndpoint, ready
}

func viewerCollector(t *testing.T, config string, extra extension.Factory) (*otelcol.Collector, <-chan *duckdbextension.DuckDBExtension) {
	t.Helper()
	ready := make(chan *duckdbextension.DuckDBExtension, 1)
	collector, err := otelcol.NewCollector(otelcol.CollectorSettings{
		Factories: func() (otelcol.Factories, error) {
			factories, err := components()
			if err != nil {
				return factories, err
			}
			kind := component.MustNewType("viewerready")
			factories.Extensions[kind] = extension.NewFactory(kind,
				func() component.Config { return &struct{}{} },
				func(context.Context, extension.Settings, component.Config) (extension.Extension, error) {
					return &viewerReadiness{ready: ready}, nil
				},
				component.StabilityLevelDevelopment)
			if extra != nil {
				factories.Extensions[extra.Type()] = extra
			}
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
	return collector, ready
}

func startPersistentViewer(t *testing.T, db string) (string, string, *duckdbextension.DuckDBExtension, func()) {
	t.Helper()
	collector, viewerURL, otlpURL, ready := newPersistentViewerCollector(t, db)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	done := make(chan error, 1)
	go func() { done <- collector.Run(ctx) }()
	stop := sync.OnceFunc(func() {
		collector.Shutdown()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("shut down viewer Collector: %v", err)
		}
	})
	t.Cleanup(stop)
	select {
	case viewer := <-ready:
		return viewerURL, otlpURL, viewer, stop
	case err := <-done:
		// Preserve the single completion for cleanup.
		done <- err
		t.Fatalf("viewer Collector stopped before readiness: %v", err)
	case <-ctx.Done():
		t.Fatalf("viewer Collector did not become ready: %v", ctx.Err())
	}
	return "", "", nil, nil
}

func TestViewerRejectsUnusablePersistentDatabase(t *testing.T) {
	for _, kind := range []string{"directory", "invalid database"} {
		t.Run(kind, func(t *testing.T) {
			path := t.TempDir()
			want := []byte("synthetic file that is not a DuckDB database\n")
			if kind == "invalid database" {
				path = filepath.Join(path, "telemetry.duckdb")
				if err := os.WriteFile(path, want, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			collector, _, _, ready := newPersistentViewerCollector(t, path)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- collector.Run(ctx) }()
			select {
			case err := <-done:
				if err == nil || ctx.Err() != nil || !strings.Contains(err.Error(), "store initialization failed:") {
					t.Fatalf("startup error = %v, context = %v; want database startup failure", err, ctx.Err())
				}
			case <-ready:
				collector.Shutdown()
				cancel()
				<-done
				t.Fatal("Collector became ready with an unusable database")
			case <-ctx.Done():
				collector.Shutdown()
				<-done
				t.Fatal("Collector did not report the database startup failure")
			}
			if kind == "invalid database" {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("failed startup changed existing file: got %q, error %v", got, err)
				}
			}
		})
	}
}

func TestViewerPersistsOTLPAcrossCollectorRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "telemetry.duckdb")
	_, otlpURL, _, stop := startPersistentViewer(t, db)
	transport := &http.Transport{}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	t.Cleanup(transport.CloseIdleConnections)

	const timestamp = pcommon.Timestamp(1795750000123456789)
	const largeInteger = int64(-9007199254740993)
	traceID := pcommon.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := pcommon.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	resource := pcommon.NewResource()
	resource.Attributes().PutStr("service.name", "synthetic-persistence")
	scope := pcommon.NewInstrumentationScope()
	scope.SetName("synthetic.viewer.integration")
	scope.SetVersion("1.0.0")
	scope.Attributes().PutBool("synthetic", true)

	traces := ptrace.NewTraces()
	rt := traces.ResourceSpans().AppendEmpty()
	resource.CopyTo(rt.Resource())
	rt.SetSchemaUrl("https://example.invalid/resource-schema")
	st := rt.ScopeSpans().AppendEmpty()
	scope.CopyTo(st.Scope())
	st.SetSchemaUrl("https://example.invalid/scope-schema")
	span := st.Spans().AppendEmpty()
	span.SetName("persisted synthetic span")
	span.SetKind(ptrace.SpanKindInternal)
	span.SetTraceID(traceID)
	span.SetSpanID(spanID)
	span.SetStartTimestamp(timestamp)
	span.SetEndTimestamp(timestamp + 123456789)
	span.Attributes().PutInt("exact.integer", largeInteger)
	span.Status().SetCode(ptrace.StatusCodeOk)

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	resource.CopyTo(rl.Resource())
	sl := rl.ScopeLogs().AppendEmpty()
	scope.CopyTo(sl.Scope())
	log := sl.LogRecords().AppendEmpty()
	log.SetTimestamp(timestamp)
	log.SetObservedTimestamp(timestamp + 1)
	log.SetSeverityNumber(plog.SeverityNumberInfo)
	log.SetSeverityText("INFO")
	log.SetEventName("synthetic.persistence")
	log.SetTraceID(traceID)
	log.SetSpanID(spanID)
	body := log.Body().SetEmptyMap()
	body.PutBool("detected", false)
	body.PutInt("exact.integer", largeInteger)
	body.PutEmptyBytes("raw").FromRaw([]byte{0, 1, 255})
	log.Attributes().PutStr("synthetic", "log")

	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	resource.CopyTo(rm.Resource())
	sm := rm.ScopeMetrics().AppendEmpty()
	scope.CopyTo(sm.Scope())
	metric := sm.Metrics().AppendEmpty()
	metric.SetName("synthetic.persisted.integer")
	metric.SetDescription("An integer beyond binary64 precision")
	metric.SetUnit("1")
	point := metric.SetEmptyGauge().DataPoints().AppendEmpty()
	point.SetTimestamp(timestamp)
	point.SetIntValue(largeInteger)
	point.Attributes().PutBool("synthetic", true)

	for _, request := range []struct {
		signal  string
		marshal func() ([]byte, error)
	}{
		{"traces", ptraceotlp.NewExportRequestFromTraces(traces).MarshalProto},
		{"logs", plogotlp.NewExportRequestFromLogs(logs).MarshalProto},
		{"metrics", pmetricotlp.NewExportRequestFromMetrics(metrics).MarshalProto},
	} {
		payload, err := request.marshal()
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, otlpURL+"/v1/"+request.signal, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		responseBody, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
			t.Fatalf("%s OTLP response: status=%d body=%s read=%v close=%v",
				request.signal, response.StatusCode, responseBody, readErr, closeErr)
		}
	}
	stop()

	viewerURL, _, viewer, _ := startPersistentViewer(t, db)
	if got := viewer.Store().RetentionCap(); got != 0 {
		t.Fatalf("retention cap = %d, want disabled", got)
	}
	ids := map[string]string{"traces": traceID.String()}
	err := viewer.Store().WithDBRead(func(db *sql.DB) error {
		for _, signal := range []string{"traces", "logs", "metrics"} {
			table := map[string]string{"traces": "spans", "logs": "logs", "metrics": "metric_records"}[signal]
			var count int
			if err := db.QueryRowContext(t.Context(), "select count(*) from "+table).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("%s persisted rows = %d, want 1", signal, count)
			}
			if signal != "traces" {
				var id string
				if err := db.QueryRowContext(t.Context(), "select id::varchar from "+table).Scan(&id); err != nil {
					return err
				}
				ids[signal] = id
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, signal := range []string{"traces", "logs", "metrics"} {
		t.Run(signal, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, viewerURL+"/export/"+signal+"/"+ids[signal], nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
				t.Fatalf("viewer export: status=%d body=%s read=%v close=%v", response.StatusCode, data, readErr, closeErr)
			}
			var got, want []byte
			switch signal {
			case "traces":
				exported := ptraceotlp.NewExportRequest()
				err = exported.UnmarshalJSON(data)
				if err == nil {
					got, err = exported.MarshalJSON()
				}
				want, _ = ptraceotlp.NewExportRequestFromTraces(traces).MarshalJSON()
			case "logs":
				exported := plogotlp.NewExportRequest()
				err = exported.UnmarshalJSON(data)
				if err == nil {
					got, err = exported.MarshalJSON()
				}
				want, _ = plogotlp.NewExportRequestFromLogs(logs).MarshalJSON()
			case "metrics":
				exported := pmetricotlp.NewExportRequest()
				err = exported.UnmarshalJSON(data)
				if err == nil {
					got, err = exported.MarshalJSON()
				}
				want, _ = pmetricotlp.NewExportRequestFromMetrics(metrics).MarshalJSON()
			}
			if err != nil {
				t.Fatal(err)
			}
			var gotValue, wantValue any
			if err := json.Unmarshal(got, &gotValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(want, &wantValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Fatalf("persisted %s changed:\ngot  %s\nwant %s", signal, got, want)
			}
		})
	}
}

type terminalReceiverWatcher struct {
	component.StartFunc
	component.ShutdownFunc
	terminal func()
}

var _ extensioncapabilities.ComponentStatusWatcher = (*terminalReceiverWatcher)(nil)

func (w *terminalReceiverWatcher) Changed(id *componentstatus.InstanceID, event *componentstatus.Event) {
	if id.ComponentID() == component.MustNewID("f1livetiming") && event.Status() == componentstatus.StatusPermanentError {
		w.terminal()
	}
}

const viewerIncident = "CAR 1 NOTED FOR SYNTHETIC INCIDENT"

func viewerRaceRecords(t *testing.T) string {
	t.Helper()
	timestamp := func(sec int64) string { return time.Unix(sec, 0).UTC().Format(time.RFC3339) }
	var records strings.Builder
	records.WriteString(`{"type":3,"invocationId":"0","result":{"SessionInfo":{"Meeting":{"Key":1200,"Name":"Example Grand Prix"},"Key":7000,"Type":"Race","Name":"Race","StartDate":"2022-01-01T12:00:00","EndDate":"2022-01-01T14:00:00","GmtOffset":"00:00:00"},"DriverList":{"1":{"Tla":"ABC","RacingNumber":"1","FullName":"Example Driver","TeamName":"Example Team"}}}}` + "\x1e")
	feed := func(sec int64, topic, payload string) {
		fmt.Fprintf(&records, `{"type":1,"target":"feed","arguments":[%q,%s,%q]}`+"\x1e", topic, payload, timestamp(sec))
	}
	status := func(sec int64, index, value string) {
		feed(sec, "SessionData", fmt.Sprintf(`{"StatusSeries":{%q:{"SessionStatus":%q,"Utc":%q}}}`, index, value, timestamp(sec)))
	}
	timing := func(sec int64, fields string) { feed(sec, "TimingData", `{"Lines":{"1":{`+fields+`}}}`) }
	status(1, "0", "Started")
	feed(2, "LapCount", `{"CurrentLap":1,"TotalLaps":2}`)
	feed(3, "WeatherData", `{"AirTemp":"22.5","Rainfall":"0"}`)
	feed(4, "WeatherData", `{"Rainfall":"1"}`)
	feed(5, "TimingAppData", `{"Lines":{"1":{"Stints":{"0":{"Compound":"Soft","TotalLaps":0}}}}}`)
	var frames []string
	for _, sec := range []int64{8, 9, 11} {
		frames = append(frames, fmt.Sprintf(`{"Utc":%q,"Cars":{"1":{"Channels":{"2":280,"0":11000,"3":7,"4":100,"45":12}}}}`, timestamp(sec)))
	}
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, `{"Entries":[`+strings.Join(frames, ",")+`]}`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	timing(10, `"Position":"1","Stopped":false,"NumberOfLaps":1,"InPit":false`)
	feed(11, "CarData.z", fmt.Sprintf("%q", base64.StdEncoding.EncodeToString(compressed.Bytes())))
	timing(30, `"Sectors":{"0":{"Value":"20.000"}}`)
	feed(40, "RaceControlMessages", `{"Messages":{"0":{"Category":"CarEvent","RacingNumber":"1","Message":"`+viewerIncident+`","Utc":"1970-01-01T00:00:40"}}}`)
	timing(50, `"Sectors":{"1":{"Value":"20.000"}}`)
	timing(70, `"NumberOfLaps":2,"InPit":true,"NumberOfPitStops":1,"LastLapTime":{"Value":"1:00.000","PersonalFastest":true}`)
	timing(74, `"Sectors":{"2":{"Value":"20.000"}},"InPit":false`)
	status(80, "1", "Finalised")
	status(81, "2", "Ends")
	records.WriteString(`{"type":7}` + "\x1e")
	return records.String()
}

func startViewerRaceServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	records := viewerRaceRecords(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodOptions:
			http.SetCookie(w, &http.Cookie{Name: "AWSALBCORS", Value: "synthetic-affinity"})
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodPost:
			_, _ = io.WriteString(w, `{"negotiateVersion":1,"connectionId":"synthetic-id","connectionToken":"synthetic-token","availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]}`)
		case http.MethodGet:
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer ws.CloseNow()
			if _, _, err := ws.Read(ctx); err != nil {
				t.Error(err)
				return
			}
			if err := ws.Write(ctx, websocket.MessageText, []byte("{}\x1e")); err != nil {
				t.Error(err)
				return
			}
			_, sub, err := ws.Read(ctx)
			var invocation struct{ Target string }
			if err != nil || json.Unmarshal(bytes.TrimSuffix(sub, []byte{0x1e}), &invocation) != nil || invocation.Target != "Subscribe" {
				t.Errorf("expected Subscribe invocation: %v", err)
				return
			}
			if err := ws.Write(ctx, websocket.MessageText, []byte(records)); err != nil {
				t.Error(err)
				return
			}
			<-ctx.Done()
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(func() { cancel(); server.Close() })
	return server
}

func TestViewerPersistsF1LiveTimingPipeline(t *testing.T) {
	server := startViewerRaceServer(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "race.duckdb")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("synthetic-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`
receivers:
  f1livetiming:
    endpoint: %q
    negotiate_endpoint: %q
    auth: {token_file: %q}
processors:
  batch: {timeout: 1h}
exporters:
  desktop:
extensions:
  duckdb: {endpoint: "127.0.0.1:0", db: %q, db_max_size: "0"}
  viewerready:
  terminalwatcher:
service:
  extensions: [duckdb, viewerready, terminalwatcher]
  telemetry:
    metrics: {level: none}
  pipelines:
    traces: &pipeline {receivers: [f1livetiming], processors: [batch], exporters: [desktop]}
    metrics: *pipeline
    logs: *pipeline
`, "ws"+strings.TrimPrefix(server.URL, "http"), server.URL+"/negotiate", token, db)
	terminal := make(chan struct{})
	watcher := extension.NewFactory(component.MustNewType("terminalwatcher"), func() component.Config { return &struct{}{} },
		func(context.Context, extension.Settings, component.Config) (extension.Extension, error) {
			return &terminalReceiverWatcher{terminal: sync.OnceFunc(func() { close(terminal) })}, nil
		}, component.StabilityLevelDevelopment)
	col, ready := viewerCollector(t, config, watcher)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	var runErr error
	go func() { runErr = col.Run(ctx); close(done) }()
	stop := sync.OnceFunc(func() {
		col.Shutdown()
		<-done
		if runErr != nil {
			t.Errorf("Collector stopped: %v", runErr)
		}
	})
	defer stop()
	for ready != nil || terminal != nil {
		select {
		case <-ready:
			ready = nil
		case <-terminal:
			terminal = nil
		case <-done:
			t.Fatalf("Collector stopped before race completion: %v", runErr)
		case <-ctx.Done():
			t.Fatalf("waiting for viewer readiness (%t) or terminal receiver status (%t): %v", ready != nil, terminal != nil, ctx.Err())
		}
	}
	stop()
	viewerURL, _, viewer, _ := startPersistentViewer(t, db)
	verifyViewerRace(t, viewerURL, viewer, server.URL)
}

func verifyViewerRace(t *testing.T, viewerURL string, viewer *duckdbextension.DuckDBExtension, sourceURL string) {
	t.Helper()
	// Exports use the trace ID for a waterfall and individual record IDs for metrics and logs.
	ids := func(query string) []string {
		t.Helper()
		var result []string
		err := viewer.Store().WithDBRead(func(db *sql.DB) error {
			rows, err := db.QueryContext(t.Context(), query)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				result = append(result, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	client := &http.Client{Timeout: 10 * time.Second}
	export := func(signal, id string, unmarshal func([]byte) error) {
		t.Helper()
		response, err := client.Get(viewerURL + "/export/" + signal + "/" + id)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("export %s/%s: %s", signal, id, response.Status)
		}
		for _, private := range []string{"synthetic-only", "synthetic-affinity", "synthetic-token", sourceURL} {
			if bytes.Contains(data, []byte(private)) {
				t.Fatalf("private input in exported %s/%s", signal, id)
			}
		}
		if err := unmarshal(data); err != nil {
			t.Fatalf("decode exported %s/%s: %v", signal, id, err)
		}
	}
	traces := ids("select distinct replace(trace_id::varchar, '-', '') from spans")
	if len(traces) != 1 {
		t.Fatalf("persisted driver traces = %v, want one", traces)
	}
	trace := ptraceotlp.NewExportRequest()
	export("traces", traces[0], trace.UnmarshalJSON)
	spans := map[string]ptrace.Span{}
	events := map[string]bool{}
	uniqueIDs := map[pcommon.SpanID]bool{}
	for _, rs := range trace.Traces().ResourceSpans().All() {
		for _, ss := range rs.ScopeSpans().All() {
			if s := ss.Scope(); s.Name() != "github.com/CtrlSpice/bargeboard/f1" || s.Version() != "1.0.0" {
				t.Fatalf("unexpected trace scope %s %s", s.Name(), s.Version())
			}
			for _, span := range ss.Spans().All() {
				if _, exists := spans[span.Name()]; exists || span.SpanID().IsEmpty() || uniqueIDs[span.SpanID()] || span.TraceID().IsEmpty() || span.TraceID().String() != traces[0] {
					t.Fatalf("unexpected span identity: %s %s/%s", span.Name(), span.TraceID(), span.SpanID())
				}
				spans[span.Name()] = span
				uniqueIDs[span.SpanID()] = true
				for _, event := range span.Events().All() {
					events[event.Name()] = true
				}
			}
		}
	}
	const root = "driver.session ABC"
	parents := map[string]string{root: "", "stint 1": root, "stint 2": root, "lap 1": "stint 1", "lap 2": "stint 2",
		"sector 1": "lap 1", "sector 2": "lap 1", "sector 3": "lap 1"}
	if len(spans) != len(parents) {
		t.Errorf("exported %d spans, want %d", len(spans), len(parents))
	}
	for name, parent := range parents {
		span, ok := spans[name]
		if !ok {
			t.Fatalf("missing span %q", name)
		}
		var want pcommon.SpanID
		if parent != "" {
			p, ok := spans[parent]
			if !ok {
				t.Fatalf("missing parent span %q", parent)
			}
			want = p.SpanID()
		}
		if span.ParentSpanID() != want {
			t.Errorf("%s parent = %s, want %s (%q)", name, span.ParentSpanID(), want, parent)
		}
	}
	for _, name := range []string{"f1.personal_best", "f1.pit.entry", "f1.pit.exit", "f1.race_control.driver_notice"} {
		if !events[name] {
			t.Errorf("missing event %s", name)
		}
	}
	metrics := map[string]bool{}
	for _, id := range ids("select id::varchar from metric_records") {
		req := pmetricotlp.NewExportRequest()
		export("metrics", id, req.UnmarshalJSON)
		for _, rm := range req.Metrics().ResourceMetrics().All() {
			for _, sm := range rm.ScopeMetrics().All() {
				for _, m := range sm.Metrics().All() {
					metrics[m.Name()] = true
					if m.Name() == "f1.field.speed" || m.Name() == "f1.field.engine_speed" {
						checkViewerCarHistogram(t, m, spans[root].TraceID(), func(id pcommon.SpanID) bool { return id == spans[root].SpanID() || id == spans["lap 1"].SpanID() })
					}
				}
			}
		}
	}
	for _, name := range []string{"f1.session.current_lap", "f1.session.intended_total_laps", "f1.session.cars_running",
		"f1.session.weather.air_temperature", "f1.session.weather.rainfall_detected", "f1.driver.tyre.age",
		"f1.driver.last_lap_time", "f1.driver.lap_time", "f1.driver.sector_time", "f1.field.speed", "f1.field.engine_speed", "f1.field.drs.active_time_ratio"} {
		if !metrics[name] {
			t.Errorf("missing metric %s", name)
		}
	}
	logs := map[string]bool{}
	lap := spans["lap 1"]
	for _, id := range ids("select id::varchar from logs") {
		req := plogotlp.NewExportRequest()
		export("logs", id, req.UnmarshalJSON)
		for _, rl := range req.Logs().ResourceLogs().All() {
			for _, sl := range rl.ScopeLogs().All() {
				for _, log := range sl.LogRecords().All() {
					body := log.Body().Str()
					logs[body] = true
					if body == viewerIncident && (log.TraceID() != lap.TraceID() || log.SpanID() != lap.SpanID()) {
						t.Errorf("incident linked to %s/%s, want lap 1 (%s/%s)", log.TraceID(), log.SpanID(), lap.TraceID(), lap.SpanID())
					}
				}
			}
		}
	}
	for _, body := range []string{viewerIncident, "f1.weather.rainfall.changed", "f1.session.status.changed"} {
		if !logs[body] {
			t.Errorf("missing log %q", body)
		}
	}
}

func checkViewerCarHistogram(t *testing.T, m pmetric.Metric, trace pcommon.TraceID, validSpan func(pcommon.SpanID) bool) {
	t.Helper()
	if m.Type() != pmetric.MetricTypeExponentialHistogram {
		t.Fatal(m.Name(), m.Type())
	}
	h := m.ExponentialHistogram()
	if h.AggregationTemporality() != pmetric.AggregationTemporalityDelta || h.DataPoints().Len() == 0 {
		t.Fatal(m.Name(), "missing delta histogram")
	}
	for _, p := range h.DataPoints().All() {
		if p.Scale() != 4 || p.ZeroThreshold() != 0 || p.Exemplars().Len() < 1 || p.Exemplars().Len() > 2 {
			t.Fatal(m.Name(), "invalid scale or exemplars")
		}
		for _, x := range p.Exemplars().All() {
			if x.ValueType() != pmetric.ExemplarValueTypeInt || x.TraceID() != trace || !validSpan(x.SpanID()) || x.SpanID().IsEmpty() {
				t.Fatal(m.Name(), x.TraceID(), x.SpanID(), x.ValueType())
			}
		}
	}
}
