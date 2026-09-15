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

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/receiver/receivertest"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestOperationalPreAttemptLoggingCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		blocked time.Duration
		cancel  bool
	}{
		{"cancel at eligibility", 0, true},
		{"cancel past eligibility", 20 * time.Second, true},
		{"commit after slow notice", 20 * time.Second, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reader := sdkmetric.NewManualReader()
				provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
				defer func() { _ = provider.Shutdown(t.Context()) }()
				entered, release := make(chan struct{}), make(chan struct{})
				releaseLog := sync.OnceFunc(func() { close(release) })
				progressCount := 0
				core, logs := observer.New(zap.InfoLevel)
				settings := receivertest.NewNopSettings(Type)
				settings.ID, settings.MeterProvider = component.NewID(Type), provider
				settings.Logger = zap.New(core, zap.Hooks(func(entry zapcore.Entry) error {
					if strings.Contains(entry.Message, "reconnect progress") {
						progressCount++
						if progressCount == 2 {
							close(entered)
							<-release
						}
					}
					return nil
				}))
				r := newLiveTimingReceiver(connectionTestConfig(t, "http://127.0.0.1"), settings)
				var err error
				r.operational, err = newOperationalReporter(t.Context(), settings)
				if err != nil {
					t.Fatal(err)
				}
				requests := make(chan struct{}, 1)
				r.client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					requests <- struct{}{}
					return httptest.NewRecorder().Result(), nil
				})}
				r.retryDelay = func(int) time.Duration { return 30 * time.Second }
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
				// Drive the actual run, including its wait and connect call site.
				// Only its second progress notice blocks; scheduling has completed.
				r.operational.apply(operationalInput{event: opStart})
				socket := newLivenessSocket()
				go r.run(ctx, &signalRConnection{conn: socket}, r.done)
				socket.reads <- livenessRead{err: errors.New("synthetic transport failure")}
				time.Sleep(30 * time.Second)
				<-entered
				time.Sleep(test.blocked)
				wantState := operationalState{started: origin, outageStarted: origin, retryAt: origin.Add(30 * time.Second), outage: true, outages: 1}
				if got := *r.operational.state.Load(); got != wantState {
					t.Fatalf("uncommitted attempt state = %+v, want %+v", got, wantState)
				}
				wantFields := map[string]any{"attempt": int64(0), "run_elapsed_seconds": float64(0), "outage_duration_seconds": float64(0), "total_outage_duration_seconds": float64(0), "next_delay_seconds": float64(30), "connection_active": false, "subscription_active": false, "normalized_updates": int64(0)}
				progress := logs.FilterMessageSnippet("reconnect progress").All()
				if len(progress) != 2 || !reflect.DeepEqual(progress[0].ContextMap(), wantFields) {
					t.Fatalf("scheduled progress = %+v", progress)
				}
				wantFields["run_elapsed_seconds"], wantFields["outage_duration_seconds"], wantFields["total_outage_duration_seconds"], wantFields["next_delay_seconds"] = float64(30), float64(30), float64(30), float64(0)
				if !reflect.DeepEqual(progress[1].ContextMap(), wantFields) {
					t.Fatalf("pre-attempt progress = %+v, want %+v", progress[1].ContextMap(), wantFields)
				}
				wantMetrics := zeroOperationalMetrics("f1livetiming")
				wantMetrics[metricPrefix+"outages"].Points[0].Value = 1
				wantMetrics[metricPrefix+"outage_active"].Points[0].Value = 1
				elapsed := (30*time.Second + test.blocked).Seconds()
				wantMetrics[metricPrefix+"outage_duration"].Points[0].Value = elapsed
				if got := collectOperational(t, reader); !reflect.DeepEqual(got, wantMetrics) {
					t.Fatalf("uncommitted metrics = %#v, want %#v", got, wantMetrics)
				}
				if test.cancel {
					cancel()
				}
				releaseLog()
				<-r.done
				wantState.retryAt, wantState.stopped, wantState.summarized = time.Time{}, true, true
				wantRequests, wantLogs := 0, 5
				if !test.cancel {
					wantState.attempts = 1
					wantMetrics[metricPrefix+"reconnect_attempts"].Points[0].Value = 1
					wantFields["attempt"] = int64(1)
					wantRequests, wantLogs = 1, 6 // The synthetic preflight then stops on missing affinity.
				}
				if got := *r.operational.state.Load(); got != wantState || len(requests) != wantRequests {
					t.Fatalf("run = %+v, requests=%d; want %+v, %d", got, len(requests), wantState, wantRequests)
				}
				if got := collectOperational(t, reader); !reflect.DeepEqual(got, wantMetrics) {
					t.Fatalf("final metrics = %#v, want %#v", got, wantMetrics)
				}
				wantFields["run_elapsed_seconds"], wantFields["outage_duration_seconds"], wantFields["total_outage_duration_seconds"] = elapsed, elapsed, elapsed
				wantFields["outages"], wantFields["recoveries"], wantFields["consumer_failures"], wantFields["unresolved_outage"] = int64(1), int64(0), int64(0), true
				summary := logs.FilterMessageSnippet("interruption summary").All()
				if len(summary) != 1 || !reflect.DeepEqual(summary[0].ContextMap(), wantFields) {
					t.Fatalf("summary = %+v, want %+v", summary, wantFields)
				}
				if logs.Len() != wantLogs || logs.FilterMessageSnippet("First Live Timing").Len() != 0 || logs.FilterMessage(recoveryMessage).Len() != 0 {
					t.Fatalf("unexpected outcome claims: %+v", logs.All())
				}
			})
		})
	}
}

// Err keeps the real context semantics and records whether admission checks it
// inside the reporting critical section. TryLock is nonblocking, including when
// a periodic notice owns the mutex while another goroutine requests admission.
type attemptLockContext struct {
	context.Context
	reporter *operationalReporter
	checks   chan bool
}

func (c attemptLockContext) Err() error {
	held := true
	if c.reporter.mu.TryLock() {
		held = false
		c.reporter.mu.Unlock()
	}
	c.checks <- held
	return c.Context.Err()
}

func TestOperationalAttemptCommitChecksCancellationUnderLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader := sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		defer func() { _ = provider.Shutdown(t.Context()) }()
		entered, release := make(chan struct{}), make(chan struct{})
		releaseLog := sync.OnceFunc(func() { close(release) })
		progressCount := 0
		core, logs := observer.New(zap.InfoLevel)
		settings := receivertest.NewNopSettings(Type)
		settings.ID, settings.MeterProvider = component.NewID(Type), provider
		settings.Logger = zap.New(core, zap.Hooks(func(entry zapcore.Entry) error {
			if strings.Contains(entry.Message, "reconnect progress") {
				progressCount++
				if progressCount == 2 {
					close(entered)
					<-release
				}
			}
			return nil
		}))
		r, err := newOperationalReporter(t.Context(), settings)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			releaseLog()
			if err := r.stop(context.Background()); err != nil {
				t.Error(err)
			}
		}()
		host := &statusHost{events: make(chan *componentstatus.Event, 8)}
		r.host = host
		r.apply(operationalInput{event: opStart})
		r.apply(operationalInput{event: opOutage})
		r.apply(operationalInput{event: opSchedule, delay: 30 * time.Second})
		time.Sleep(30 * time.Second)
		periodicDone := make(chan struct{})
		go func() { r.apply(operationalInput{event: opTick}); close(periodicDone) }()
		<-entered
		before, beforeLogs, beforeStatuses := *r.state.Load(), logs.Len(), len(host.events)
		base, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		checks := make(chan bool, 2)
		ctx := attemptLockContext{Context: base, reporter: r, checks: checks}
		requesting, result := make(chan struct{}), make(chan bool, 1)
		go func() { close(requesting); result <- r.beginAttempt(ctx) }()
		<-requesting
		// No synctest.Wait while a mutex waiter exists. Cancellation precedes
		// releasing the actual periodic notice's critical section.
		cancel(errors.New("private cancellation cause"))
		releaseLog()
		<-periodicDone
		if <-result {
			t.Fatal("canceled admission succeeded after periodic reporting")
		}
		if len(checks) != 1 || !<-checks {
			t.Fatal("cancellation was not checked inside the commit lock")
		}
		if got := *r.state.Load(); got != before || logs.Len() != beforeLogs || len(host.events) != beforeStatuses {
			t.Fatalf("rejected admission changed state or output: %+v", got)
		}
		wantMetrics := zeroOperationalMetrics("f1livetiming")
		wantMetrics[metricPrefix+"outages"].Points[0].Value = 1
		wantMetrics[metricPrefix+"outage_active"].Points[0].Value = 1
		wantMetrics[metricPrefix+"outage_duration"].Points[0].Value = 30
		if got := collectOperational(t, reader); !reflect.DeepEqual(got, wantMetrics) {
			t.Fatalf("rejected admission metrics = %#v, want %#v", got, wantMetrics)
		}
		// With no other mutex owner, this also deterministically rejects moving
		// Err outside the lock, independent of how the contended case was scheduled.
		ctx.Context = t.Context()
		if !r.beginAttempt(ctx) {
			t.Fatal("live admission failed")
		}
		if len(checks) != 1 || !<-checks {
			t.Fatal("live context was checked outside the commit lock")
		}
		before.attempts++
		before.retryAt = time.Time{}
		if got := *r.state.Load(); got != before || logs.Len() != beforeLogs || len(host.events) != beforeStatuses {
			t.Fatalf("commit state/output = %+v, want %+v and no output", got, before)
		}
		wantMetrics[metricPrefix+"reconnect_attempts"].Points[0].Value = 1
		if got := collectOperational(t, reader); !reflect.DeepEqual(got, wantMetrics) {
			t.Fatalf("commit metrics = %#v, want %#v", got, wantMetrics)
		}
	})
}
