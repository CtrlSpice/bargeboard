package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CtrlSpice/otel-desktop-viewer/desktopexporter/duckdbextension"
	"go.opentelemetry.io/collector/component"
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
	return collector, "http://" + viewerEndpoint, "http://" + otlpEndpoint, ready
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
