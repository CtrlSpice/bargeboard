package f1livetimingreceiver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func operationalTestRun(t *testing.T, host component.Host) (*liveTimingReceiver, *livenessSocket, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.InfoLevel)
	settings := receivertest.NewNopSettings(Type)
	settings.Logger = zap.New(core)
	r := newLiveTimingReceiver(nil, settings)
	var err error
	r.operational, err = newOperationalReporter(t.Context(), settings)
	if err != nil {
		t.Fatal(err)
	}
	r.operational.start(host)
	ctx, cancel := context.WithCancel(t.Context())
	r.cancel, r.done = cancel, make(chan struct{})
	socket := newLivenessSocket()
	connection := &signalRConnection{conn: socket}
	if err := connection.subscribe(ctx); err != nil {
		t.Fatal(err)
	}
	go r.run(ctx, connection, r.done)
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })
	synctest.Wait()
	return r, socket, logs
}

func TestOperationalPeriodicWaitingAndOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := &statusHost{events: make(chan *componentstatus.Event, 8)}
		r, socket, logs := operationalTestRun(t, host)
		requireAwaitingInput(t, host)
		// The racing observation clock cannot affect operational elapsed time.
		r.now = func() time.Time { return time.Unix(1, 0) }
		r.retryDelay = func(int) time.Duration { return time.Hour }
		send := func(record string) { socket.reads <- livenessRead{contents: record}; synctest.Wait() }
		send(`{"type":3,"invocationId":"0","result":{}}` + "\x1e")
		for range 6 {
			time.Sleep(10 * time.Second)
			send(hubPingRecord)
		}
		waiting := logs.FilterMessageSnippet("Waiting for validated").All()
		if len(waiting) != 3 {
			t.Fatalf("waiting notices = %d", len(waiting))
		}
		for i, log := range waiting {
			fields := log.ContextMap()
			if fields["run_elapsed_seconds"] != float64(i*30) || fields["outage_duration_seconds"] != float64(0) || fields["total_outage_duration_seconds"] != float64(0) || fields["normalized_updates"] != int64(0) {
				t.Fatalf("waiting fields = %v", fields)
			}
		}
		if r.operational.state.Load().outages != 0 || len(host.events) != 0 {
			t.Fatal("ping-only waiting manufactured an outage or OK")
		}
		send(incrementalFeedA)
		if logs.FilterMessageSnippet("First Live Timing").Len() != 1 || (<-host.events).Status() != componentstatus.StatusOK {
			t.Fatal("missing first data notice")
		}
		for range 3 {
			time.Sleep(10 * time.Second)
			send(hubPingRecord)
		}
		if logs.FilterMessageSnippet("Waiting for validated").Len() != 3 {
			t.Fatal("stale waiting after first data")
		}
		socket.reads <- livenessRead{err: errors.New("synthetic-confidential-transport")}
		synctest.Wait()
		outage := logs.FilterMessage(outageMessage).All()
		if len(outage) != 1 || (<-host.events).Status() != componentstatus.StatusRecoverableError {
			t.Fatal("outage was not visible before backoff")
		}
		progress := logs.FilterMessageSnippet("reconnect progress").All()
		wantFields := map[string]any{"attempt": int64(0), "run_elapsed_seconds": float64(90), "outage_duration_seconds": float64(0), "total_outage_duration_seconds": float64(0), "next_delay_seconds": float64(3600), "connection_active": false, "subscription_active": false, "normalized_updates": int64(1)}
		if len(progress) != 1 || !reflect.DeepEqual(progress[0].ContextMap(), wantFields) {
			t.Fatalf("scheduled progress = %#v", progress)
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		progress = logs.FilterMessageSnippet("reconnect progress").All()
		wantFields["run_elapsed_seconds"], wantFields["outage_duration_seconds"], wantFields["total_outage_duration_seconds"], wantFields["next_delay_seconds"] = float64(120), float64(30), float64(30), float64(3570)
		if len(progress) != 2 || !reflect.DeepEqual(progress[1].ContextMap(), wantFields) {
			t.Fatalf("periodic progress = %#v", progress)
		}
		if err := r.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		summary := logs.FilterMessageSnippet("interruption summary").All()
		wantFields["next_delay_seconds"] = float64(0)
		wantFields["outages"], wantFields["recoveries"], wantFields["consumer_failures"], wantFields["unresolved_outage"] = int64(1), int64(0), int64(0), true
		wantFields["invalid_unicode_updates"] = int64(0)
		if len(summary) != 1 || !reflect.DeepEqual(summary[0].ContextMap(), wantFields) {
			t.Fatalf("summary = %#v", summary)
		}
		count := logs.Len()
		time.Sleep(time.Minute)
		synctest.Wait()
		if logs.Len() != count {
			t.Fatal("reporter survived shutdown")
		}
		for _, log := range logs.All() {
			if strings.Contains(log.Message, "confidential") {
				t.Fatal("raw error escaped")
			}
		}
	})
}

func TestOperationalShutdownTimeoutWaitsForCallbackSummary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, socket, logs := operationalTestRun(t, nil)
		entered, release := make(chan struct{}), make(chan struct{})
		r.consume = func(context.Context, normalizedLiveTimingBatch) error {
			close(entered)
			<-release
			return errors.New("private downstream error")
		}
		socket.reads <- livenessRead{contents: incrementalFeedA}
		<-entered
		done := r.done
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := r.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown = %v", err)
		}
		if logs.FilterMessageSnippet("interruption summary").Len() != 0 {
			t.Fatal("summary claimed callback had finished")
		}
		if s := r.operational.state.Load(); s.updates != 1 || s.consumerFailures != 0 || s.summarized {
			t.Fatalf("callback state = %+v", s)
		}
		close(release)
		<-done
		if s := r.operational.state.Load(); s.updates != 1 || s.consumerFailures != 1 || !s.summarized || s.outages != 0 {
			t.Fatalf("finished state = %+v", s)
		}
		if logs.FilterMessageSnippet("interruption summary").Len() != 1 {
			t.Fatal("missing completed summary")
		}
		count := logs.Len()
		time.Sleep(time.Minute)
		synctest.Wait()
		if logs.Len() != count {
			t.Fatal("periodic reporter survived timed-out shutdown")
		}
	})
}

func TestOperationalSharedShutdownTimeoutKeepsOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader := sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		defer func() { _ = provider.Shutdown(t.Context()) }()
		settings := receivertest.NewNopSettings(Type)
		settings.MeterProvider = provider
		m := newReceiverMap()
		cfg := createDefaultConfig().(*Config)
		cfg.Auth.TokenFile = "unused-synthetic-path"
		shared, err := m.receiver(t.Context(), cfg, settings)
		if err != nil {
			t.Fatal(err)
		}
		r := shared.receiver
		entered, release := make(chan struct{}), make(chan struct{})
		r.consume = func(context.Context, normalizedLiveTimingBatch) error { close(entered); <-release; return nil }
		ctx, cancel := context.WithCancel(t.Context())
		r.cancel, r.done = cancel, make(chan struct{})
		r.operational.start(nil)
		socket := newLivenessSocket()
		go r.run(ctx, &signalRConnection{conn: socket}, r.done)
		socket.reads <- livenessRead{contents: incrementalFeedA}
		<-entered
		shutdown, stop := context.WithTimeout(t.Context(), time.Second)
		defer stop()
		if err := shared.Shutdown(shutdown); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown = %v", err)
		}
		retained, err := m.receiver(t.Context(), cfg, settings)
		if !errors.Is(err, errReceiverStopping) || retained != nil {
			t.Fatal("factory returned a receiver that is still stopping")
		}
		if err := shared.Start(t.Context(), nil); !errors.Is(err, errReceiverStopping) {
			t.Fatal("a stopped shared receiver reported a successful restart")
		}
		m.mu.Lock()
		ownsConfig := m.receivers[cfg] == shared
		m.mu.Unlock()
		if !ownsConfig {
			t.Fatal("timed-out run lost ownership before its callback finished")
		}
		for _, value := range collectOperational(t, reader) {
			if value.Kind != "Int64Counter" {
				t.Fatal("callback still registered after timeout")
			}
		}
		close(release)
		<-r.done
		synctest.Wait()
		recreated, err := m.receiver(t.Context(), cfg, settings)
		if err != nil || recreated == shared {
			t.Fatal("completed run retained factory cache")
		}
		if err := recreated.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOperationalTerminalRemainsVisible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, socket, logs := operationalTestRun(t, nil)
		socket.reads <- livenessRead{contents: incrementalClose}
		<-r.done
		if logs.FilterMessage(errSourceStopped.Error()).Len() != 1 {
			t.Fatal("terminal error absent")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if logs.FilterMessageSnippet("Live Timing input stopped;").Len() != 2 {
			t.Fatal("terminal input silently stopped while Collector remains running")
		}
		if logs.FilterMessageSnippet("interruption summary").Len() != 1 {
			t.Fatal("summary not singular")
		}
	})
}

type collectingStatusHost struct {
	reader *sdkmetric.ManualReader
	t      *testing.T
}

func (*collectingStatusHost) GetExtensions() map[component.ID]component.Component { return nil }
func (h collectingStatusHost) Report(*componentstatus.Event) {
	var data metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &data); err != nil {
		h.t.Error(err)
	}
}

func TestOperationalConcurrentReportingAndCollection(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = provider.Shutdown(t.Context()) }()
	core, logs := observer.New(zap.InfoLevel)
	settings := receivertest.NewNopSettings(Type)
	settings.Logger, settings.MeterProvider = zap.New(core), provider
	r, err := newOperationalReporter(t.Context(), settings)
	if err != nil {
		t.Fatal(err)
	}
	defer r.stop(t.Context())
	r.host = &collectingStatusHost{reader, t}
	r.apply(operationalInput{event: opStart})
	r.apply(operationalInput{event: opConnected})
	r.apply(operationalInput{event: opBatch, updates: 1, snapshot: true})
	r.apply(operationalInput{event: opOutage})
	r.apply(operationalInput{event: opConnected})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 30 {
				r.apply(operationalInput{event: opTick})
			}
		})
		wg.Go(func() {
			for range 30 {
				var data metricdata.ResourceMetrics
				if err := reader.Collect(t.Context(), &data); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Go(func() { r.apply(operationalInput{event: opBatch, updates: 1, snapshot: true}) })
	wg.Wait()
	recovered := false
	for _, log := range logs.All() {
		if log.Message == recoveryMessage {
			recovered = true
			continue
		}
		if recovered && strings.Contains(log.Message, "reconnect progress") {
			t.Fatal("stale periodic outage after recovery")
		}
	}
	if !recovered || logs.FilterMessage(recoveryMessage).Len() != 1 {
		t.Fatal("recovery not singular")
	}
}

func TestOperationalInputDoesNotImplyRacingEmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader := sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		defer func() { _ = provider.Shutdown(t.Context()) }()
		settings := receivertest.NewNopSettings(Type)
		settings.ID, settings.MeterProvider = component.NewID(Type), provider
		factory := NewFactory()
		cfg := connectionTestConfig(t, "http://127.0.0.1")
		traces, metrics, logs := &consumertest.TracesSink{}, &consumertest.MetricsSink{}, &consumertest.LogsSink{}
		sharedTraces, err := factory.CreateTraces(t.Context(), settings, cfg, traces)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := factory.CreateMetrics(t.Context(), settings, cfg, metrics); err != nil {
			t.Fatal(err)
		}
		if _, err := factory.CreateLogs(t.Context(), settings, cfg, logs); err != nil {
			t.Fatal(err)
		}
		shared := sharedTraces.(*sharedReceiver)
		shared.receiver.client = livenessWebSocketClient(t, func(socket *websocket.Conn) {
			_, _, _ = socket.Read(t.Context())
			_ = socket.Write(t.Context(), websocket.MessageText, []byte("{}\x1e"))
			_, _, _ = socket.Read(t.Context())
			_ = socket.Write(t.Context(), websocket.MessageText, []byte(
				`{"type":3,"invocationId":"0","result":{"SessionInfo":`+identityGateDescriptorA+`}}`+"\x1e"+incrementalClose,
			))
		})
		if err := shared.Start(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		<-shared.receiver.done
		want := zeroOperationalMetrics("f1livetiming")
		for _, name := range []string{"normalized_updates", "outages", "outage_active"} {
			want[metricPrefix+name].Points[0].Value = 1
		}
		want[metricPrefix+"last_update_age"] = metricResult{"s", "Float64Gauge", []metricPoint{{"f1livetiming", 0}}}
		if got := collectOperational(t, reader); !reflect.DeepEqual(got, want) {
			t.Fatalf("input metrics = %#v, want %#v", got, want)
		}
		if shared.receiver.state != identityGateTestState() {
			t.Fatalf("receiver state = %#v, want synchronized SessionInfo", shared.receiver.state)
		}
		if len(traces.AllTraces()) != 0 || len(metrics.AllMetrics()) != 0 || len(logs.AllLogs()) != 0 {
			t.Fatal("unwired racing projection emitted data")
		}
		if err := shared.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOperationalCanceledReconnectSetup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		core, logs := observer.New(zap.InfoLevel)
		settings := receivertest.NewNopSettings(Type)
		settings.Logger = zap.New(core)
		r := newLiveTimingReceiver(connectionTestConfig(t, "http://synthetic.test"), settings)
		client := livenessWebSocketClient(t, func(socket *websocket.Conn) {
			_, _, _ = socket.Read(t.Context())
			_ = socket.Write(t.Context(), websocket.MessageText, []byte("{}\x1e"))
			_, _, _ = socket.Read(t.Context())
			_ = socket.Write(t.Context(), websocket.MessageText, []byte(`{"type":7,"allowReconnect":true}`+"\x1e"))
		})
		transport := client.Transport
		entered := make(chan struct{})
		preflights := 0
		client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodOptions {
				preflights++
				if preflights == 2 {
					close(entered)
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
			}
			return transport.RoundTrip(req)
		})
		r.client = client
		r.retryDelay = func(int) time.Duration { return 0 }
		if err := r.Start(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		<-entered
		if state := r.operational.state.Load(); state.attempts != 1 || state.outages != 1 || state.summarized {
			t.Fatalf("setup state = %+v", state)
		}
		progress := logs.FilterMessageSnippet("reconnect progress").All()
		if len(progress) != 2 || progress[0].ContextMap()["attempt"] != int64(0) || progress[1].ContextMap()["attempt"] != int64(0) {
			t.Fatalf("attempt notices = %v", progress)
		}
		if err := r.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		if preflights != 2 || logs.FilterMessageSnippet("interruption summary").Len() != 1 || logs.FilterMessage(outageMessage).Len() != 1 {
			t.Fatal("cancellation retried or lost summary")
		}
	})
}

func TestOperationalRecoveryAndSummaryDurationFields(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		core, logs := observer.New(zap.InfoLevel)
		settings := receivertest.NewNopSettings(Type)
		settings.Logger = zap.New(core)
		r, err := newOperationalReporter(t.Context(), settings)
		if err != nil {
			t.Fatal(err)
		}
		defer r.stop(t.Context())
		r.apply(operationalInput{event: opStart})
		r.apply(operationalInput{event: opConnected})
		r.apply(operationalInput{event: opBatch, snapshot: true, updates: 1})
		time.Sleep(time.Hour)
		r.apply(operationalInput{event: opOutage})
		time.Sleep(10 * time.Second)
		r.apply(operationalInput{event: opConnected})
		r.apply(operationalInput{event: opBatch, snapshot: true, updates: 1})
		want := map[string]any{"attempt": int64(0), "run_elapsed_seconds": float64(3610), "outage_duration_seconds": float64(10), "total_outage_duration_seconds": float64(10), "next_delay_seconds": float64(0), "connection_active": true, "subscription_active": true, "normalized_updates": int64(2)}
		recovered := logs.FilterMessage(recoveryMessage).All()
		if len(recovered) != 1 || !reflect.DeepEqual(recovered[0].ContextMap(), want) {
			t.Fatalf("recovery fields = %+v; want %+v", recovered, want)
		}
		time.Sleep(3590 * time.Second)
		r.apply(operationalInput{event: opOutage})
		time.Sleep(25 * time.Second)
		r.apply(operationalInput{event: opFinish})
		want["run_elapsed_seconds"], want["outage_duration_seconds"], want["total_outage_duration_seconds"] = float64(7225), float64(25), float64(35)
		want["connection_active"], want["subscription_active"] = false, false
		want["outages"], want["recoveries"], want["consumer_failures"], want["unresolved_outage"] = int64(2), int64(1), int64(0), true
		want["invalid_unicode_updates"] = int64(0)
		summary := logs.FilterMessageSnippet("interruption summary").All()
		if len(summary) != 1 || !reflect.DeepEqual(summary[0].ContextMap(), want) {
			t.Fatalf("summary fields = %+v; want %+v", summary, want)
		}
	})
}

func TestOperationalRetryDeadlineIncludesScheduleLogging(t *testing.T) {
	for _, test := range []struct {
		name    string
		blocked time.Duration
		cancel  bool
	}{
		{"log takes 20 seconds", 20 * time.Second, false},
		{"log exceeds deadline", 40 * time.Second, false},
		{"cancel before deadline", 20 * time.Second, true},
		{"cancel after deadline", 40 * time.Second, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const delay = 30 * time.Second
				entered, release := make(chan struct{}), make(chan struct{})
				releaseLog := sync.OnceFunc(func() { close(release) })
				var scheduleLog sync.Once
				core, logs := observer.New(zap.InfoLevel)
				settings := receivertest.NewNopSettings(Type)
				settings.Logger = zap.New(core, zap.Hooks(func(entry zapcore.Entry) error {
					if strings.Contains(entry.Message, "reconnect progress") {
						scheduleLog.Do(func() { close(entered); <-release })
					}
					return nil
				}))
				r := newLiveTimingReceiver(connectionTestConfig(t, "http://127.0.0.1"), settings)
				var err error
				r.operational, err = newOperationalReporter(t.Context(), settings)
				if err != nil {
					t.Fatal(err)
				}
				attempts := make(chan time.Time, 1)
				r.client = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					if request.Method != http.MethodOptions {
						t.Errorf("unexpected reconnect request: %s", request.Method)
					}
					attempts <- time.Now()
					// Missing synthetic affinity stops this actual reconnect attempt
					// through the existing permanent-data policy, with no network I/O.
					return httptest.NewRecorder().Result(), nil
				})}
				r.retryDelay = func(int) time.Duration { return delay }
				ctx, cancel := context.WithCancel(t.Context())
				r.cancel, r.done = cancel, make(chan struct{})
				defer func() {
					cancel()
					releaseLog()
					if err := r.Shutdown(context.Background()); err != nil {
						t.Error(err)
					}
				}()
				origin := time.Now()
				// No periodic goroutine: the production run still schedules, logs,
				// waits, and reconnects. Explicit ticks below inspect its countdown
				// without a ticker contending on the deliberately blocked log mutex.
				r.operational.apply(operationalInput{event: opStart})
				socket := newLivenessSocket()
				go r.run(ctx, &signalRConnection{conn: socket}, r.done)
				socket.reads <- livenessRead{err: errors.New("synthetic transport failure")}
				<-entered
				wantState := operationalState{started: origin, outageStarted: origin, retryAt: origin.Add(delay), outage: true, outages: 1}
				if got := *r.operational.state.Load(); got != wantState {
					t.Fatalf("scheduled state = %+v, want %+v", got, wantState)
				}
				wantProgress := map[string]any{"attempt": int64(0), "run_elapsed_seconds": float64(0), "outage_duration_seconds": float64(0), "total_outage_duration_seconds": float64(0), "next_delay_seconds": float64(30), "connection_active": false, "subscription_active": false, "normalized_updates": int64(0)}
				checkProgress := func(index int) {
					t.Helper()
					entries := logs.FilterMessageSnippet("reconnect progress").All()
					if len(entries) != index+1 || !reflect.DeepEqual(entries[index].ContextMap(), wantProgress) {
						t.Fatalf("progress = %+v; want entry %d with %+v", entries, index, wantProgress)
					}
				}
				checkProgress(0)
				time.Sleep(test.blocked)
				if got := *r.operational.state.Load(); got != wantState || got.nextDelay(time.Now()) != max(0, delay-test.blocked) {
					t.Fatalf("blocked-log state = %+v", got)
				}
				if len(attempts) != 0 {
					t.Fatal("reconnect occurred while the schedule log was blocked")
				}
				if test.cancel {
					cancel()
				}
				releaseLog()
				synctest.Wait()
				if test.cancel {
					select {
					case <-r.done:
					default:
						t.Fatal("canceled run did not finish")
					}
					if len(attempts) != 0 || r.operational.state.Load().attempts != 0 {
						t.Fatal("cancellation while logging manufactured an attempt")
					}
					checkProgress(0)
					return
				}
				progressIndex := 1
				if test.blocked < delay {
					for _, elapsed := range []time.Duration{20 * time.Second, 29 * time.Second} {
						time.Sleep(elapsed - time.Since(origin))
						synctest.Wait()
						if len(attempts) != 0 || r.operational.state.Load().attempts != 0 {
							t.Fatal("reconnect occurred before the scheduled deadline")
						}
						r.operational.apply(operationalInput{event: opTick})
						wantProgress["run_elapsed_seconds"], wantProgress["outage_duration_seconds"], wantProgress["total_outage_duration_seconds"] = elapsed.Seconds(), elapsed.Seconds(), elapsed.Seconds()
						wantProgress["next_delay_seconds"] = (delay - elapsed).Seconds()
						checkProgress(progressIndex)
						progressIndex++
					}
					time.Sleep(time.Second)
					synctest.Wait()
				}
				wantAttemptAt := origin.Add(max(delay, test.blocked))
				select {
				case at := <-attempts:
					if at != wantAttemptAt {
						t.Fatalf("attempt at %s, want %s", at, wantAttemptAt)
					}
				default:
					t.Fatalf("no reconnect at %s; schedule logging must not start a fresh full backoff", wantAttemptAt)
				}
				if state := r.operational.state.Load(); state.attempts != 1 || !state.retryAt.IsZero() {
					t.Fatalf("attempt state = %+v", state)
				}
				elapsed := max(delay, test.blocked).Seconds()
				wantProgress["attempt"], wantProgress["next_delay_seconds"] = int64(0), float64(0)
				wantProgress["run_elapsed_seconds"], wantProgress["outage_duration_seconds"], wantProgress["total_outage_duration_seconds"] = elapsed, elapsed, elapsed
				checkProgress(progressIndex)
			})
		})
	}
}
