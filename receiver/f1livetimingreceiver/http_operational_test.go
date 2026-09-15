package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/receiver/receivertest"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestHTTPOperationalRetryAndQualifiedRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader := sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		defer func() { _ = provider.Shutdown(t.Context()) }()
		core, logs := observer.New(zap.InfoLevel)
		settings := receivertest.NewNopSettings(Type)
		settings.ID, settings.Logger, settings.MeterProvider = component.NewID(Type), zap.New(core), provider
		r := newLiveTimingReceiver(connectionTestConfig(t, "http://synthetic.test"), settings)
		var err error
		r.operational, err = newOperationalReporter(t.Context(), settings)
		if err != nil {
			t.Fatal(err)
		}
		host := &statusHost{events: make(chan *componentstatus.Event, 8)}
		r.operational.start(host)
		ctx, cancel := context.WithCancel(t.Context())
		r.cancel, r.done = cancel, make(chan struct{})
		defer func() { _ = r.Shutdown(context.Background()) }()
		origin := time.Now()
		initial := newLivenessSocket()
		connection := &signalRConnection{conn: initial}
		if err := connection.subscribe(ctx); err != nil {
			t.Fatal(err)
		}
		go r.run(ctx, connection, r.done)
		initial.reads <- livenessRead{contents: `{"type":3,"invocationId":"0","result":{}}` + "\x1e" + incrementalFeedA}
		synctest.Wait()
		requireAwaitingInput(t, host)
		requireHTTPStatus(t, host, componentstatus.StatusOK, nil)
		peer := make(chan *websocket.Conn, 1)
		client := livenessWebSocketClient(t, func(socket *websocket.Conn) {
			_, _, _ = socket.Read(t.Context())
			_ = socket.Write(t.Context(), websocket.MessageText, []byte("{}\x1e"))
			_, _, _ = socket.Read(t.Context())
			peer <- socket
			for {
				if _, _, err := socket.Read(t.Context()); err != nil {
					return
				}
			}
		})
		base := client.Transport
		var methods []string
		var calls []time.Time
		var callCount atomic.Int64
		var bodies []*preflightTestBody
		client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			methods = append(methods, request.Method)
			if request.Method == http.MethodOptions {
				calls = append(calls, time.Now())
				callCount.Add(1)
			}
			status, hint := 0, ""
			switch {
			case len(calls) == 1:
				status, hint = 503, "60"
			case len(calls) == 2:
				status, hint = 429, "2" // Must not shorten the four-second backoff.
			case len(calls) == 3 && request.Method == http.MethodGet:
				status, hint = 404, origin.Add(75*time.Second).UTC().Format(http.TimeFormat)
			}
			if status != 0 {
				body := &preflightTestBody{}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {hint}}, Body: body}, nil
			}
			response, err := base.RoundTrip(request)
			if response != nil {
				response.Header.Set("Retry-After", "999999")
			} // Success has no hint authority.
			return response, err
		})
		r.client = client
		var indexes []int
		r.retryDelay = func(index int) time.Duration { indexes = append(indexes, index); return reconnectDelay(index) }
		initial.reads <- livenessRead{err: errors.New("private-transport")}
		synctest.Wait()
		requireHTTPStatus(t, host, componentstatus.StatusRecoverableError, errInputOutage)
		want := operationalState{started: origin, lastUpdate: origin, ready: true, outage: true, outageStarted: origin, outages: 1, updates: 1, retryAt: origin.Add(time.Second)}
		checkState := func() {
			t.Helper()
			if got := *r.operational.state.Load(); got != want {
				t.Fatalf("state=%+v, want %+v", got, want)
			}
		}
		checkState()
		time.Sleep(time.Second)
		synctest.Wait()
		want.attempts, want.retryAt = 1, origin.Add(61*time.Second)
		checkState()
		checkProgress := func(elapsed, delay float64, attempt int64) {
			t.Helper()
			entries := logs.FilterMessageSnippet("reconnect progress").All()
			want := httpProgressFields(attempt, elapsed, elapsed, delay, 1)
			if len(entries) == 0 || !reflect.DeepEqual(entries[len(entries)-1].ContextMap(), want) {
				t.Fatalf("progress=%+v, want %+v", entries, want)
			}
		}
		checkProgress(1, 60, 1)
		for _, seconds := range []int{30, 60} {
			time.Sleep(time.Duration(seconds)*time.Second - time.Since(origin))
			synctest.Wait()
			checkState()
			checkProgress(float64(seconds), float64(61-seconds), 1)
			if callCount.Load() != 1 || len(host.events) != 0 {
				t.Fatal("Retry-After wait retried or changed status")
			}
		}
		time.Sleep(time.Second)
		synctest.Wait()
		want.attempts, want.retryAt = 2, origin.Add(65*time.Second)
		checkState()
		checkProgress(61, 4, 2)
		time.Sleep(4 * time.Second)
		synctest.Wait()
		want.attempts, want.retryAt = 3, origin.Add(75*time.Second)
		checkState()
		checkProgress(65, 10, 3)
		time.Sleep(10 * time.Second)
		synctest.Wait()
		server := <-peer
		want.attempts, want.retryAt, want.connection = 4, time.Time{}, true
		checkState()
		if err := server.Write(t.Context(), websocket.MessageText, []byte(`{"type":3,"invocationId":"0","result":{}}`+"\x1e")); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		want.subscription = true
		checkState()
		if len(host.events) != 0 || logs.FilterMessage(recoveryMessage).Len() != 0 {
			t.Fatal("empty completion claimed recovery")
		}
		if err := server.Write(t.Context(), websocket.MessageText, []byte(incrementalFeedC)); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		want.outage, want.outageStarted, want.connectionData = false, time.Time{}, true
		want.recoveries, want.updates, want.lastUpdate, want.closedOutageDuration = 1, 2, origin.Add(75*time.Second), 75*time.Second
		checkState()
		requireHTTPStatus(t, host, componentstatus.StatusOK, nil)
		wantCalls := []time.Time{origin.Add(time.Second), origin.Add(61 * time.Second), origin.Add(65 * time.Second), origin.Add(75 * time.Second)}
		wantMethods := []string{"OPTIONS", "OPTIONS", "OPTIONS", "POST", "GET", "OPTIONS", "POST", "GET"}
		if !reflect.DeepEqual(calls, wantCalls) || !reflect.DeepEqual(methods, wantMethods) || !reflect.DeepEqual(indexes, []int{0, 1, 2, 3}) {
			t.Fatalf("calls=%v methods=%v indexes=%v", calls, methods, indexes)
		}
		for _, body := range bodies {
			if *body != (preflightTestBody{closes: 1}) {
				t.Fatalf("body=%+v", body)
			}
		}
		wantMetrics := zeroOperationalMetrics("f1livetiming")
		for name, value := range map[string]float64{"connection_active": 1, "subscription_active": 1, "outages": 1, "reconnect_attempts": 4, "recoveries": 1, "normalized_updates": 2} {
			wantMetrics[metricPrefix+name].Points[0].Value = value
		}
		wantMetrics[metricPrefix+"last_update_age"] = metricResult{"s", "Float64Gauge", []metricPoint{{"f1livetiming", 0}}}
		if got := collectOperational(t, reader); !reflect.DeepEqual(got, wantMetrics) {
			t.Fatalf("metrics=%#v, want %#v", got, wantMetrics)
		}
		fields := httpProgressFields(4, 75, 75, 0, 2)
		fields["connection_active"], fields["subscription_active"] = true, true
		recovered := logs.FilterMessage(recoveryMessage).All()
		if len(recovered) != 1 || !reflect.DeepEqual(recovered[0].ContextMap(), fields) || logs.FilterMessage(outageMessage).Len() != 1 || logs.FilterMessageSnippet("First Live Timing").Len() != 1 {
			t.Fatalf("recovery notices=%+v", recovered)
		}
		if err := r.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		want.connection, want.subscription, want.connectionData, want.stopped, want.summarized = false, false, false, true, true
		checkState()
		fields["connection_active"], fields["subscription_active"], fields["outage_duration_seconds"] = false, false, float64(0)
		fields["outages"], fields["recoveries"], fields["consumer_failures"], fields["unresolved_outage"] = int64(1), int64(1), int64(0), false
		summary := logs.FilterMessageSnippet("interruption summary").All()
		if len(summary) != 1 || !reflect.DeepEqual(summary[0].ContextMap(), fields) || len(host.events) != 0 {
			t.Fatalf("summary=%+v", summary)
		}
	})
}

func httpProgressFields(attempt int64, elapsed, outage, delay float64, updates int64) map[string]any {
	return map[string]any{"attempt": attempt, "run_elapsed_seconds": elapsed, "outage_duration_seconds": outage, "total_outage_duration_seconds": outage, "next_delay_seconds": delay, "connection_active": false, "subscription_active": false, "normalized_updates": updates}
}

func requireHTTPStatus(t *testing.T, host *statusHost, status componentstatus.Status, want error) {
	t.Helper()
	select {
	case event := <-host.events:
		if event.Status() != status || !errors.Is(event.Err(), want) {
			t.Fatalf("status=%v error=%v, want %v / %v", event.Status(), event.Err(), status, want)
		}
	default:
		t.Fatalf("missing status %v", status)
	}
}

func TestHTTPOperationalTerminalSetup(t *testing.T) {
	for _, stage := range []setupStage{stagePreflight, stageNegotiate, stageUpgrade} {
		for _, status := range []int{401, 403, 404, 302} {
			if stage == stageUpgrade && status == 404 {
				continue
			}
			t.Run(fmt.Sprintf("%s/%d", stage, status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					host := &statusHost{events: make(chan *componentstatus.Event, 8)}
					r, socket, logs := operationalTestRun(t, host)
					origin := time.Now()
					r.config = connectionTestConfig(t, "http://synthetic.test/private-route")
					client := livenessWebSocketClient(t, func(*websocket.Conn) { t.Error("unexpected upgrade") })
					base := client.Transport
					calls := 0
					body := &preflightTestBody{}
					client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
						calls++
						if calls != int(stage) {
							return base.RoundTrip(request)
						}
						return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"999999"}}, Body: body}, nil
					})
					r.client = client
					socket.reads <- livenessRead{contents: `{"type":3,"invocationId":"0","result":{}}` + "\x1e" + incrementalFeedA}
					synctest.Wait()
					requireAwaitingInput(t, host)
					requireHTTPStatus(t, host, componentstatus.StatusOK, nil)
					socket.reads <- livenessRead{err: errors.New("private-transport")}
					<-r.done
					requireHTTPStatus(t, host, componentstatus.StatusRecoverableError, errInputOutage)
					event := <-host.events
					guidance := ""
					if status == 401 || status == 403 {
						guidance = " Check the F1 TV token and access."
					}
					message := fmt.Sprintf("Live Timing input stopped: %s returned HTTP %d; Collector can still run.%s Press Ctrl-C to stop the Collector.", stage, status, guidance)
					failure := asSetupHTTPError(event.Err())
					if event.Status() != componentstatus.StatusPermanentError || failure == nil || *failure != (setupHTTPError{stage: stage, status: status}) || event.Err().Error() != message || errors.Is(event.Err(), errInvalidLiveTimingData) {
						t.Fatalf("terminal=%v / %v", event.Status(), event.Err())
					}
					want := operationalState{started: origin, lastUpdate: origin, ready: true, outage: true, outageStarted: origin, outages: 1, attempts: 1, updates: 1, stopped: true, summarized: true}
					if got := *r.operational.state.Load(); got != want {
						t.Fatalf("terminal state=%+v, want %+v", got, want)
					}
					fields := httpProgressFields(1, 1, 1, 0, 1)
					fields["setup_stage"], fields["http_status"] = stage.String(), int64(status)
					entries := logs.FilterMessage(message).All()
					if len(entries) != 1 || entries[0].Level != zap.ErrorLevel || !reflect.DeepEqual(entries[0].ContextMap(), fields) {
						t.Fatalf("terminal log=%+v, want %+v", entries, fields)
					}
					delete(fields, "setup_stage")
					delete(fields, "http_status")
					fields["outages"], fields["recoveries"], fields["consumer_failures"], fields["unresolved_outage"] = int64(1), int64(0), int64(0), true
					summary := logs.FilterMessageSnippet("interruption summary").All()
					if len(summary) != 1 || !reflect.DeepEqual(summary[0].ContextMap(), fields) {
						t.Fatalf("summary=%+v", summary)
					}
					time.Sleep(59 * time.Second)
					synctest.Wait()
					if calls != int(stage) || *body != (preflightTestBody{closes: 1}) || *r.operational.state.Load() != want || len(host.events) != 0 {
						t.Fatal("terminal setup retried or changed status/state")
					}
					stopped := logs.FilterMessageSnippet("Live Timing input stopped;").All()
					if len(stopped) != 2 {
						t.Fatalf("stopped notices=%+v", stopped)
					}
					for i, entry := range stopped {
						if want := httpProgressFields(1, float64((i+1)*30), float64((i+1)*30), 0, 1); !reflect.DeepEqual(entry.ContextMap(), want) {
							t.Fatalf("stopped fields=%+v, want %+v", entry.ContextMap(), want)
						}
					}
				})
			})
		}
	}
}

func TestHTTPOperationalRetryAfterLoggingAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		blocked time.Duration
		cancel  bool
	}{
		{"cleanup and logging spend hint", 20 * time.Second, false},
		{"logging past deadline", 80 * time.Second, false},
		{"cancel blocked notice", 20 * time.Second, true},
		{"cancel notice past deadline", 80 * time.Second, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				core, logs := observer.New(zap.InfoLevel)
				entered, release := make(chan struct{}), make(chan struct{})
				releaseLog := sync.OnceFunc(func() { close(release) })
				progress := 0
				settings := receivertest.NewNopSettings(Type)
				settings.Logger = zap.New(core, zap.Hooks(func(entry zapcore.Entry) error {
					if strings.Contains(entry.Message, "reconnect progress") {
						progress++
						if progress == 3 {
							close(entered)
							<-release
						}
					}
					return nil
				}))
				r := newLiveTimingReceiver(connectionTestConfig(t, "http://synthetic.test"), settings)
				var err error
				r.operational, err = newOperationalReporter(t.Context(), settings)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				r.cancel, r.done = cancel, make(chan struct{})
				defer func() { cancel(); releaseLog(); _ = r.Shutdown(context.Background()) }()
				origin := time.Now()
				var calls []time.Time
				body := &slowHTTPCloseBody{delay: 5 * time.Second}
				r.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
					calls = append(calls, time.Now())
					if len(calls) == 1 {
						return &http.Response{StatusCode: 503, Header: http.Header{"Retry-After": {"60"}}, Body: body}, nil
					}
					return &http.Response{StatusCode: 401, Header: make(http.Header), Body: http.NoBody}, nil
				})
				// Explicit ticks avoid a periodic mutex waiter while logging blocks.
				r.operational.apply(operationalInput{event: opStart})
				socket := newLivenessSocket()
				go r.run(ctx, &signalRConnection{conn: socket}, r.done)
				socket.reads <- livenessRead{err: errors.New("private-transport")}
				<-entered // first failure observed at +1, body close ends at +6
				want := operationalState{started: origin, outage: true, outageStarted: origin, outages: 1, attempts: 1, retryAt: origin.Add(61 * time.Second)}
				if got := *r.operational.state.Load(); got != want || *body != (slowHTTPCloseBody{delay: 5 * time.Second, closes: 1}) {
					t.Fatalf("schedule=%+v body=%+v", got, body)
				}
				entries := logs.FilterMessageSnippet("reconnect progress").All()
				if len(entries) != 3 || !reflect.DeepEqual(entries[2].ContextMap(), httpProgressFields(1, 6, 6, 55, 0)) {
					t.Fatalf("schedule log=%+v", entries)
				}
				time.Sleep(test.blocked)
				if test.cancel {
					cancel()
				}
				releaseLog()
				synctest.Wait()
				if !test.cancel && time.Since(origin) < 61*time.Second {
					r.operational.apply(operationalInput{event: opTick})
					entries := logs.FilterMessageSnippet("reconnect progress").All()
					if !reflect.DeepEqual(entries[len(entries)-1].ContextMap(), httpProgressFields(1, 26, 26, 35, 0)) {
						t.Fatal("logging restarted server delay")
					}
					time.Sleep(35 * time.Second)
				}
				<-r.done
				want.retryAt, want.stopped, want.summarized = time.Time{}, true, true
				wantCalls := []time.Time{origin.Add(time.Second)}
				if !test.cancel {
					want.attempts = 2
					wantCalls = append(wantCalls, origin.Add(max(61*time.Second, 6*time.Second+test.blocked)))
				}
				if got := *r.operational.state.Load(); got != want || !reflect.DeepEqual(calls, wantCalls) {
					t.Fatalf("finished=%+v calls=%v, want %+v calls=%v", got, calls, want, wantCalls)
				}
				fields := httpProgressFields(want.attempts, time.Since(origin).Seconds(), time.Since(origin).Seconds(), 0, 0)
				fields["outages"], fields["recoveries"], fields["consumer_failures"], fields["unresolved_outage"] = int64(1), int64(0), int64(0), true
				summary := logs.FilterMessageSnippet("interruption summary").All()
				if len(summary) != 1 || !reflect.DeepEqual(summary[0].ContextMap(), fields) {
					t.Fatalf("summary=%+v", summary)
				}
			})
		})
	}
}

type slowHTTPCloseBody struct {
	delay         time.Duration
	reads, closes int
}

func (b *slowHTTPCloseBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("private-body")
}
func (b *slowHTTPCloseBody) Close() error { b.closes++; time.Sleep(b.delay); return nil }

func TestHTTPOperationalCancelLargeRetryAfterWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := &statusHost{events: make(chan *componentstatus.Event, 8)}
		r, socket, logs := operationalTestRun(t, host)
		r.config = connectionTestConfig(t, "http://synthetic.test")
		origin := time.Now()
		calls := 0
		r.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"9223372036"}}, Body: http.NoBody}, nil
		})
		socket.reads <- livenessRead{err: errors.New("private-transport")}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		want := operationalState{started: origin, outage: true, outageStarted: origin, outages: 1, attempts: 1, retryAt: origin.Add(time.Second).Add(9223372036 * time.Second)}
		if got := *r.operational.state.Load(); got != want {
			t.Fatalf("large hint=%+v, want %+v", got, want)
		}
		if err := r.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		want.retryAt, want.stopped, want.summarized = time.Time{}, true, true
		if got := *r.operational.state.Load(); got != want || calls != 1 || time.Since(origin) != 30*time.Second {
			t.Fatalf("canceled state=%+v calls=%d", got, calls)
		}
		requireAwaitingInput(t, host)
		requireHTTPStatus(t, host, componentstatus.StatusRecoverableError, errInputOutage)
		if len(host.events) != 0 || logs.FilterMessageSnippet("interruption summary").Len() != 1 || logs.FilterMessage(recoveryMessage).Len() != 0 {
			t.Fatal("canceled wait manufactured status/recovery")
		}
	})
}
