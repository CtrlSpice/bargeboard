package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/fileprovider"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/featuregate"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/pipeline"
	"go.uber.org/zap"
)

// Test-only status watcher observes the pinned service FSM, including automatic
// Starting/OK handling, without adding an extension to the shipped distribution.
type syntheticStatusWatcher struct{ logger *zap.Logger }

func (*syntheticStatusWatcher) Start(context.Context, component.Host) error { return nil }
func (*syntheticStatusWatcher) Shutdown(context.Context) error              { return nil }
func (w *syntheticStatusWatcher) ComponentStatusChanged(id *componentstatus.InstanceID, event *componentstatus.Event) {
	if id.ComponentID().String() != "f1livetiming" {
		return
	}
	id.AllPipelineIDs(func(id pipeline.ID) bool {
		w.logger.Info(fmt.Sprintf("synthetic input status %s %s", id, event.Status()))
		return true
	})
}

func syntheticCollectorComponents() (otelcol.Factories, error) {
	factories, err := components()
	if err != nil {
		return factories, err
	}
	kind := component.MustNewType("syntheticstatus")
	factories.Extensions = map[component.Type]extension.Factory{kind: extension.NewFactory(kind,
		func() component.Config { return &struct{}{} },
		func(_ context.Context, settings extension.Settings, _ component.Config) (extension.Extension, error) {
			return &syntheticStatusWatcher{settings.Logger}, nil
		},
		component.StabilityLevelDevelopment)}
	return factories, nil
}

func scanForegroundStderr(ctx context.Context, reader io.Reader, lines chan<- string) error {
	defer close(lines)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		select {
		case lines <- scanner.Text():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return scanner.Err()
}

func TestForegroundStderrCleanup(t *testing.T) {
	t.Run("canceled blocked send", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		lines, done := make(chan string), make(chan error, 1)
		go func() { done <- scanForegroundStderr(ctx, reader, lines) }()
		// Write completes only once the scanner has read the line. With no
		// receiver on lines it cannot advance until cancellation releases it.
		if _, err := io.WriteString(writer, "synthetic line\n"); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-done; err != context.Canceled {
			t.Fatalf("scan = %v", err)
		}
		if _, ok := <-lines; ok {
			t.Fatal("scanner retained an undrained line")
		}
	})
	t.Run("canceled blocked read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		reader, writer := io.Pipe()
		defer writer.Close()
		lines, done := make(chan string), make(chan error, 1)
		go func() { done <- scanForegroundStderr(ctx, reader, lines) }()
		cancel()
		// Cancellation of sends does not interrupt Read; process cleanup also
		// closes the pipe before joining the scanner.
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != io.ErrClosedPipe {
			t.Fatalf("scan = %v", err)
		}
		if _, ok := <-lines; ok {
			t.Fatal("scanner emitted after pipe closure")
		}
	})
	t.Run("drain beyond channel capacity", func(t *testing.T) {
		lines, done := make(chan string, 1), make(chan error, 1)
		go func() {
			done <- scanForegroundStderr(t.Context(), strings.NewReader(strings.Repeat("synthetic line\n", 1024)), lines)
		}()
		count := 0
		for line := range lines {
			if line != "synthetic line" {
				t.Fatal(line)
			}
			count++
		}
		if err := <-done; err != nil || count != 1024 {
			t.Fatalf("scan = %v, lines = %d", err, count)
		}
	})
	t.Run("scanner error is preserved", func(t *testing.T) {
		lines := make(chan string, 1)
		if err := scanForegroundStderr(t.Context(), strings.NewReader(strings.Repeat("x", 128*1024)), lines); err != bufio.ErrTooLong {
			t.Fatalf("scan = %v", err)
		}
	})
}

// Separate process tests the Collector's existing signal owner and actual default
// stderr logger without installing handlers or replacing global SDKs in the test.
func TestForegroundCollectorHelper(t *testing.T) {
	config := os.Getenv("BARGEBOARD_SYNTHETIC_CONFIG")
	if config == "" {
		t.Skip("subprocess helper")
	}
	if err := featuregate.GlobalRegistry().Set("telemetry.newPipelineTelemetry", true); err != nil {
		t.Fatal(err)
	}
	col, err := otelcol.NewCollector(otelcol.CollectorSettings{
		Factories: syntheticCollectorComponents,
		ConfigProviderSettings: otelcol.ConfigProviderSettings{ResolverSettings: confmap.ResolverSettings{
			URIs: []string{config}, DefaultScheme: "file", ProviderFactories: []confmap.ProviderFactory{fileprovider.NewFactory()},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if os.Getenv("BARGEBOARD_SYNTHETIC_STOP") == "stdin" {
		stopped := make(chan struct{})
		go func() { defer close(stopped); _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
		defer func() { _ = os.Stdin.Close(); <-stopped }()
	}
	if err := col.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestForegroundCollectorOperationalTelemetry(t *testing.T) {
	for _, test := range []struct{ name, level, stop string }{
		{"basic", "basic", "stdin"}, {"none", "none", "stdin"}, {"posix_sigint", "basic", "sigint"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.stop == "sigint" && runtime.GOOS == "windows" {
				t.Skip("os.Process.Signal(os.Interrupt) is unsupported on Windows; Basic/None use stdin EOF")
			}
			level := test.level
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			var connections atomic.Int32
			firstData, drop, terminal := make(chan struct{}), make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.Method {
				case http.MethodOptions:
					http.SetCookie(w, &http.Cookie{Name: "AWSALBCORS", Value: "synthetic-affinity"})
					w.WriteHeader(http.StatusMethodNotAllowed)
				case http.MethodPost:
					_, _ = io.WriteString(w, `{"connectionId":"synthetic-id","connectionToken":"synthetic-token","negotiateVersion":1,"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]}`)
				case http.MethodGet:
					socket, err := websocket.Accept(w, req, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer socket.CloseNow()
					if _, _, err = socket.Read(ctx); err != nil {
						return
					}
					if err = socket.Write(ctx, websocket.MessageText, []byte("{}\x1e")); err != nil {
						return
					}
					if _, _, err = socket.Read(ctx); err != nil {
						return
					}
					n := connections.Add(1)
					if n > 2 {
						t.Error("unexpected third connection")
						return
					}
					if n == 1 {
						select {
						case <-firstData:
						case <-ctx.Done():
							return
						}
					}
					// Synthetic unused keys/text exercise input quality without changing
					// the envelope count: multiple malformed strings affect one update.
					if err = socket.Write(ctx, websocket.MessageText, []byte(`{"type":3,"invocationId":"0","result":{"SessionStatus":{"Status":"Started","unused":{"synthetic-private-key":"synthetic-private-text\uD800","\uDC00":"\uDFFF"}}}}`+"\x1e")); err != nil {
						return
					}
					wait, closeRecord := drop, `{"type":7,"allowReconnect":true}`
					if n == 2 {
						wait, closeRecord = terminal, `{"type":7}`
					}
					select {
					case <-wait:
					case <-ctx.Done():
						return
					}
					_ = socket.Write(ctx, websocket.MessageText, []byte(closeRecord+"\x1e"))
				}
			}))
			defer func() { cancel(); server.Close() }()
			// Reserve an ephemeral port, then release it for the pinned Prometheus
			// reader (whose public config accepts a port, not an existing listener).
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			token := filepath.Join(dir, "synthetic-token")
			if err := os.WriteFile(token, []byte("synthetic-only"), 0o600); err != nil {
				t.Fatal(err)
			}
			shipped, err := os.ReadFile("config.yaml")
			if err != nil {
				t.Fatal(err)
			}
			config := strings.NewReplacer(
				"${env:HOME}/.config/bargeboard/f1tv-token", token,
				"  f1livetiming:\n", fmt.Sprintf("  f1livetiming:\n    endpoint: %s\n    negotiate_endpoint: %s/negotiate\n", "ws"+strings.TrimPrefix(server.URL, "http"), server.URL),
				"localhost:4317", "127.0.0.1:0", "localhost:4318", "127.0.0.1:0",
				"level: basic", "level: "+level, "port: 8888", fmt.Sprintf("port: %d", port),
				"service:\n", "extensions:\n  syntheticstatus: {}\n\nservice:\n  extensions: [syntheticstatus]\n",
			).Replace(string(shipped))
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestForegroundCollectorHelper$")
			cmd.Env = append(os.Environ(), "BARGEBOARD_SYNTHETIC_CONFIG="+path, "BARGEBOARD_SYNTHETIC_STOP="+test.stop)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stderr, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			// No reader goroutine exists if Start fails. Both pipe ends owned by
			// this test are still closed by the defers above.
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			lines := make(chan string, 256)
			readDone := make(chan error, 1)
			go func() {
				readDone <- scanForegroundStderr(ctx, stderr, lines)
				close(readDone)
			}()
			reap := sync.OnceValue(cmd.Wait)
			defer func() {
				cancel()
				_ = stdin.Close()
				_ = stderr.Close()
				for range readDone {
				}
				_ = reap()
			}()
			var output []string
			await := func(message string) {
				t.Helper()
				for _, line := range output {
					if strings.Contains(line, message) {
						return
					}
				}
				for {
					select {
					case line, ok := <-lines:
						if !ok {
							t.Fatalf("stderr ended before %q: %s", message, strings.Join(output, "\n"))
						}
						output = append(output, line)
						if strings.Contains(line, message) {
							return
						}
					case <-ctx.Done():
						t.Fatalf("waiting for %q: %s", message, strings.Join(output, "\n"))
					}
				}
			}
			assertInputStatuses := func(want []string) {
				t.Helper()
				for _, signal := range []string{"traces", "metrics", "logs"} {
					var got []string
					for _, line := range output {
						_, rest, ok := strings.Cut(line, "synthetic input status "+signal+" ")
						if ok {
							got = append(got, strings.Fields(rest)[0])
						}
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("%s input status sequence = %v, want %v", signal, got, want)
					}
				}
			}
			await("Everything is ready")
			assertInputStatuses([]string{"StatusStarting", "StatusRecoverableError"})
			close(firstData)
			await("First Live Timing updates observed")
			for _, signal := range []string{"traces", "metrics", "logs"} {
				await("synthetic input status " + signal + " StatusOK")
			}
			assertInputStatuses([]string{"StatusStarting", "StatusRecoverableError", "StatusOK"})
			// The default-view Basic path is exercised by the real service, rather
			// than a private SDK that could accidentally bypass Collector filtering.
			endpoint := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
			client := &http.Client{Timeout: time.Second}
			checkExposition := func(updates, invalidUnicodeUpdates, outages, attempts, recoveries int) {
				t.Helper()
				response, err := client.Get(endpoint)
				if level == "none" {
					if err == nil {
						response.Body.Close()
						t.Fatal("None enabled a metrics listener")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != http.StatusOK {
					t.Fatalf("metrics status = %d", response.StatusCode)
				}
				for _, metadata := range []string{
					"# HELP otelcol_f1livetiming_normalized_updates Envelopes in fully normalized input batches, not cars, datapoints, or exported racing signals.",
					"# HELP otelcol_f1livetiming_last_update_age Process seconds since local acceptance of a nonempty normalized batch; omitted before first input, not source freshness.",
					"# HELP otelcol_f1livetiming_invalid_unicode_updates Envelopes with malformed Unicode scalar escapes in fully normalized payloads; not rejected input or dropped racing signals.",
					"# TYPE otelcol_f1livetiming_invalid_unicode_updates counter",
				} {
					if !strings.Contains(string(body), metadata) {
						t.Fatalf("missing metric metadata: %s", metadata)
					}
				}
				want := map[string]float64{
					"connection_active": 1, "subscription_active": 1, "outage_active": 0, "outage_duration": 0,
					"outages": float64(outages), "reconnect_attempts": float64(attempts), "recoveries": float64(recoveries),
					"normalized_updates": float64(updates), "consumer_failures": 0, "last_update_age": -1,
					"invalid_unicode_updates": float64(invalidUnicodeUpdates),
				}
				for _, line := range strings.Split(string(body), "\n") {
					if !strings.HasPrefix(line, "otelcol_f1livetiming_") {
						continue
					}
					fields := strings.Fields(line)
					if len(fields) != 2 {
						t.Fatalf("unexpected F1 exposition: %s", line)
					}
					name, ok := strings.CutSuffix(strings.TrimPrefix(fields[0], "otelcol_f1livetiming_"), `{receiver="f1livetiming"}`)
					if !ok {
						t.Fatalf("unexpected labels or scope suffix: %s", line)
					}
					value, err := strconv.ParseFloat(fields[1], 64)
					expected, exists := want[name]
					if err != nil || !exists || (expected != -1 && value != expected) || (expected == -1 && !(value >= 0)) {
						t.Fatalf("unexpected F1 metric: %s", line)
					}
					delete(want, name)
				}
				if len(want) != 0 {
					t.Fatalf("missing F1 metrics: %v", want)
				}
			}
			checkExposition(1, 1, 0, 0, 0)
			close(drop)
			await("Live Timing input interrupted; updates may be missing")
			await("Live Timing updates resumed; missed updates may be unrecoverable")
			checkExposition(2, 2, 1, 1, 1)
			close(terminal)
			await("Collector can still run")
			await("interruption summary")
			assertInputStatuses([]string{"StatusStarting", "StatusRecoverableError", "StatusOK", "StatusRecoverableError", "StatusOK", "StatusPermanentError"})
			if test.stop == "stdin" {
				if err := stdin.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			await("Shutdown complete")
			for line := range lines {
				output = append(output, line)
			}
			if err := <-readDone; err != nil {
				t.Fatalf("read stderr: %v", err)
			}
			if err := reap(); err != nil {
				t.Fatalf("Collector exit = %v: %s", err, strings.Join(output, "\n"))
			}
			joined := strings.Join(output, "\n")
			for _, required := range []string{"no F1 race export is implemented", "Press Ctrl-C to stop the Collector.", `"outages": 2`, `"recoveries": 1`, `"normalized_updates": 2`, `"unresolved_outage": true`} {
				if !strings.Contains(joined, required) {
					t.Errorf("stderr missing %q: %s", required, joined)
				}
			}
			qualityWarnings, summaries := 0, 0
			for _, line := range output {
				var want map[string]any
				switch {
				case strings.Contains(line, "Live Timing payload contains malformed Unicode scalar escapes; normalized payload bytes preserved; no F1 race export is implemented"):
					qualityWarnings++
					want = map[string]any{"invalid_unicode_updates": float64(1), "new_invalid_unicode_updates": float64(1)}
				case strings.Contains(line, "interruption summary"):
					summaries++
					want = map[string]any{
						"attempt": float64(1), "outages": float64(2), "recoveries": float64(1), "normalized_updates": float64(2),
						"consumer_failures": float64(0), "invalid_unicode_updates": float64(2), "next_delay_seconds": float64(0),
						"connection_active": false, "subscription_active": false, "unresolved_outage": true,
					}
				default:
					continue
				}
				// The real console logger appends JSON fields after its message.
				// Collector-injected context is independent of receiver-owned fields.
				start := strings.IndexByte(line, '{')
				var fields map[string]any
				if start < 0 || json.Unmarshal([]byte(line[start:]), &fields) != nil {
					t.Fatalf("missing structured notice fields: %s", line)
				}
				for key, value := range want {
					if fields[key] != value {
						t.Errorf("%s = %v, want %v: %s", key, fields[key], value, line)
					}
				}
				if strings.Contains(line, "interruption summary") {
					for _, key := range []string{"run_elapsed_seconds", "outage_duration_seconds", "total_outage_duration_seconds"} {
						if seconds, ok := fields[key].(float64); !ok || seconds < 0 {
							t.Errorf("invalid summary duration %s: %s", key, line)
						}
					}
				}
			}
			// Both snapshots arrive before the 30-second tick. The second finding
			// remains coalesced but must survive in the summary, also at level None.
			if qualityWarnings != 1 || summaries != 1 {
				t.Errorf("quality warnings=%d summaries=%d; want one each", qualityWarnings, summaries)
			}
			for _, private := range []string{"synthetic-only", "synthetic-token", "synthetic-private-key", "synthetic-private-text", `\uD800`, `\uDC00`, `\uDFFF`} {
				if strings.Contains(joined, private) {
					t.Error("synthetic confidential value exposed")
				}
			}
		})
	}
}
