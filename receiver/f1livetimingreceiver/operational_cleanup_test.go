package f1livetimingreceiver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Wrap the public SDK API to expose entry to Unregister, without changing the
// SDK's locking. Channel blocking is also usable with a noop provider.
type cleanupTestProvider struct {
	metric.MeterProvider
	entered, release               chan struct{}
	registrationErr, unregisterErr error
	calls                          atomic.Int64
	callbacks                      []metric.Callback
}

func (p *cleanupTestProvider) Meter(name string, opts ...metric.MeterOption) metric.Meter {
	return cleanupTestMeter{Meter: p.MeterProvider.Meter(name, opts...), provider: p}
}

type cleanupTestMeter struct {
	metric.Meter
	provider *cleanupTestProvider
}

func (m cleanupTestMeter) RegisterCallback(callback metric.Callback, instruments ...metric.Observable) (metric.Registration, error) {
	reg, err := m.Meter.RegisterCallback(callback, instruments...)
	if err != nil {
		return reg, err
	}
	p := m.provider
	p.callbacks = append(p.callbacks, callback)
	return cleanupTestRegistration{Registration: reg, entered: p.entered, release: p.release, err: p.unregisterErr, calls: &p.calls}, p.registrationErr
}

type cleanupTestRegistration struct {
	metric.Registration
	entered, release chan struct{}
	err              error
	calls            *atomic.Int64
}

func (r cleanupTestRegistration) Unregister() error {
	r.calls.Add(1)
	if r.entered != nil {
		close(r.entered)
	}
	if r.release != nil {
		<-r.release
	}
	return errors.Join(r.Registration.Unregister(), r.err)
}

func cleanupTestShared(t *testing.T, settings receiver.Settings) (*receiverMap, *sharedReceiver, *Config) {
	t.Helper()
	m := newReceiverMap()
	cfg := createDefaultConfig().(*Config)
	cfg.Auth.TokenFile = "unused-synthetic-path"
	r, err := m.receiver(t.Context(), cfg, settings)
	if err != nil {
		t.Fatal(err)
	}
	return m, r, cfg
}

func requireCleanupReservation(t *testing.T, m *receiverMap, shared *sharedReceiver, cfg *Config, settings receiver.Settings) {
	t.Helper()
	if got, err := m.receiver(t.Context(), cfg, settings); got != nil || err != errReceiverStopping {
		t.Fatalf("stopping factory = %v, %v", got, err)
	}
	if err := shared.Start(t.Context(), nil); err != errReceiverStopping {
		t.Fatalf("stopping Start = %v", err)
	}
	m.mu.Lock()
	reserved := m.receivers[cfg] == shared
	m.mu.Unlock()
	if !reserved {
		t.Fatal("cleanup lost the factory reservation")
	}
	select {
	case <-shared.receiver.operational.stopDone:
		t.Fatal("cleanup completed while blocked")
	default:
	}
}

func TestOperationalCleanupDeadlineWithAdmittedObserver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &cleanupTestProvider{MeterProvider: noop.NewMeterProvider(), unregisterErr: errors.New("synthetic unregister failure")}
		settings := receivertest.NewNopSettings(Type)
		settings.MeterProvider = provider
		m, shared, cfg := cleanupTestShared(t, settings)
		r := shared.receiver
		// Input has already completed; only metric observations hold ownership.
		r.done = make(chan struct{})
		close(r.done)
		r.operational.apply(operationalInput{event: opConnected})
		o := &blockedCallbackObserver{entered: make(chan struct{}), release: make(chan struct{})}
		observed := make(chan error, 1)
		go func() { observed <- provider.callbacks[0](t.Context(), o) }()
		<-o.entered
		ctx, cancel := context.WithTimeoutCause(t.Context(), time.Millisecond, errors.New("private shutdown cause"))
		defer cancel()
		// Expire while the observer is admitted, before the cleanup worker tries
		// its RWMutex: mutex waits are not durably blocked in synctest.
		time.Sleep(time.Millisecond)
		if err := shared.Shutdown(ctx); err != context.DeadlineExceeded {
			t.Fatalf("Shutdown = %v", err)
		}
		requireCleanupReservation(t, m, shared, cfg, settings)
		// A longer-waiting caller must not hold shared stopOnce across its wait.
		joined := make(chan error, 1)
		go func() { joined <- shared.Shutdown(t.Context()) }()
		if err := shared.Shutdown(ctx); err != context.DeadlineExceeded {
			t.Fatalf("second Shutdown = %v", err)
		}
		close(o.release)
		if err := <-observed; err != nil {
			t.Fatal(err)
		}
		if err := <-joined; err != nil {
			t.Fatal(err)
		}
		if provider.calls.Load() != 1 {
			t.Fatal("cleanup ran more than once")
		}
		if want := []any{int64(1), int64(0), int64(0), float64(0)}; !reflect.DeepEqual(o.values, want) {
			t.Fatalf("admitted observations = %v", o.values)
		}
		provider.unregisterErr = nil
		recreated, err := m.receiver(t.Context(), cfg, settings)
		if err != nil || recreated == shared {
			t.Fatalf("recreation = %v, %v", recreated, err)
		}
		late := &callbackObservations{}
		if err := provider.callbacks[0](t.Context(), late); err != nil || len(late.values) != 0 {
			t.Fatalf("old observations after recreation = %v, %v", late.values, err)
		}
		if err := recreated.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOperationalCleanupDeadlineWithOtherSDKCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader := sdkmetric.NewManualReader()
		sdk := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		defer func() { _ = sdk.Shutdown(t.Context()) }()
		provider := &cleanupTestProvider{MeterProvider: sdk, entered: make(chan struct{})}
		settings := receivertest.NewNopSettings(Type)
		settings.ID, settings.MeterProvider = component.NewID(Type), provider
		m, shared, cfg := cleanupTestShared(t, settings)
		r := shared.receiver
		r.done = make(chan struct{})
		close(r.done)
		r.operational.apply(operationalInput{event: opConnected})
		r.operational.apply(operationalInput{event: opBatch, snapshot: true, updates: 3})
		otherMeter := sdk.Meter("synthetic-other-component")
		gauge, err := otherMeter.Int64ObservableGauge("synthetic_other")
		if err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		other, err := otherMeter.RegisterCallback(func(context.Context, metric.Observer) error { close(entered); <-release; return nil }, gauge)
		if err != nil {
			t.Fatal(err)
		}
		collected := make(chan error, 1)
		go func() { var data metricdata.ResourceMetrics; collected <- reader.Collect(t.Context(), &data) }()
		<-entered
		if !r.operational.callbackMu.TryLock() {
			t.Fatal("F1 callback lock unexpectedly held")
		}
		r.operational.callbackMu.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
		defer cancel()
		time.Sleep(time.Millisecond)
		if err := shared.Shutdown(ctx); err != context.DeadlineExceeded {
			t.Fatalf("Shutdown = %v", err)
		}
		<-provider.entered // The worker has reached the actual SDK Unregister.
		requireCleanupReservation(t, m, shared, cfg, settings)
		select {
		case <-r.operational.cleanupDone:
			t.Fatal("Unregister escaped the other SDK callback")
		default:
		}
		// Also cancel a live caller while Unregister is blocked on the SDK's
		// pipeline mutex, without attempting unsupported reentrant collection.
		waiting, cancelWaiting := context.WithCancelCause(t.Context())
		result := make(chan error, 1)
		go func() { result <- shared.Shutdown(waiting) }()
		cancelWaiting(errors.New("private cancellation cause"))
		if err := <-result; err != context.Canceled {
			t.Fatalf("canceled Shutdown = %v", err)
		}
		close(release)
		if err := <-collected; err != nil {
			t.Fatal(err)
		}
		<-r.operational.stopDone
		if err := other.Unregister(); err != nil {
			t.Fatal(err)
		}
		provider.entered = nil
		recreated, err := m.receiver(t.Context(), cfg, settings)
		if err != nil {
			t.Fatal(err)
		}
		want := zeroOperationalMetrics("f1livetiming")
		want[metricPrefix+"normalized_updates"].Points[0].Value = 3
		if got := collectOperational(t, reader); !reflect.DeepEqual(got, want) {
			t.Fatalf("recreated metrics = %#v, want %#v", got, want)
		}
		if err := recreated.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOperationalCleanupDeadlineWithPeriodicJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		core, logs := observer.New(zap.InfoLevel)
		settings := receivertest.NewNopSettings(Type)
		settings.Logger = zap.New(core, zap.Hooks(func(entry zapcore.Entry) error {
			if strings.HasPrefix(entry.Message, "Live Timing input stopped;") {
				close(entered)
				<-release
			}
			return nil
		}))
		m, shared, cfg := cleanupTestShared(t, settings)
		r := shared.receiver
		ctx, cancel := context.WithCancel(t.Context())
		r.cancel, r.done = cancel, make(chan struct{})
		r.operational.start(nil)
		socket := newLivenessSocket()
		go r.run(ctx, &signalRConnection{conn: socket}, r.done)
		socket.reads <- livenessRead{contents: incrementalClose}
		<-r.done
		time.Sleep(30 * time.Second)
		<-entered
		shutdown, stop := context.WithTimeout(t.Context(), time.Millisecond)
		defer stop()
		started := time.Now()
		if err := shared.Shutdown(shutdown); err != context.DeadlineExceeded || time.Since(started) != time.Millisecond {
			t.Fatalf("Shutdown = %v after %s", err, time.Since(started))
		}
		requireCleanupReservation(t, m, shared, cfg, settings)
		if logs.FilterMessageSnippet("interruption summary").Len() != 1 {
			t.Fatal("reporter cleanup manufactured a run summary")
		}
		close(release)
		<-r.operational.stopDone
		recreated, err := m.receiver(t.Context(), cfg, settings)
		if err != nil {
			t.Fatal(err)
		}
		if err := recreated.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOperationalCleanupConcurrentShutdownCallers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &cleanupTestProvider{MeterProvider: noop.NewMeterProvider(), entered: make(chan struct{}), release: make(chan struct{})}
		settings := receivertest.NewNopSettings(Type)
		settings.MeterProvider = provider
		m, shared, cfg := cleanupTestShared(t, settings)
		// This receiver was constructed but never started. Its first caller may
		// wait indefinitely without imposing that wait on a later caller.
		first := make(chan error, 1)
		go func() { first <- shared.Shutdown(t.Context()) }()
		<-provider.entered
		ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
		defer cancel()
		at := time.Now()
		if err := shared.Shutdown(ctx); err != context.DeadlineExceeded || time.Since(at) != time.Millisecond {
			t.Fatalf("second Shutdown = %v after %s", err, time.Since(at))
		}
		requireCleanupReservation(t, m, shared, cfg, settings)
		close(provider.release)
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if err := shared.Shutdown(t.Context()); err != nil {
			t.Fatalf("previous caller's timeout became sticky: %v", err)
		}
		if provider.calls.Load() != 1 {
			t.Fatal("concurrent callers duplicated cleanup")
		}
	})
}

func TestOperationalCleanupFailedStartPreservesCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &cleanupTestProvider{MeterProvider: noop.NewMeterProvider(), entered: make(chan struct{}), release: make(chan struct{}), unregisterErr: errors.New("private unregister failure")}
		settings := receivertest.NewNopSettings(Type)
		core, logs := observer.New(zap.WarnLevel)
		settings.Logger, settings.MeterProvider = zap.New(core), provider
		m := newReceiverMap()
		cfg := connectionTestConfig(t, "http://127.0.0.1")
		shared, err := m.receiver(t.Context(), cfg, settings)
		if err != nil {
			t.Fatal(err)
		}
		shared.receiver.client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return httptest.NewRecorder().Result(), nil })}
		ctx, cancel := context.WithTimeoutCause(t.Context(), time.Millisecond, errors.New("private start deadline"))
		defer cancel()
		err = shared.Start(ctx, nil)
		if !errors.Is(err, errInvalidLiveTimingData) || !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "private") {
			t.Fatalf("Start lost its sanitized cause or cleanup deadline: %v", err)
		}
		<-provider.entered
		requireCleanupReservation(t, m, shared, cfg, settings)
		close(provider.release)
		<-shared.receiver.operational.stopDone
		if logs.FilterMessage("Live Timing internal metric callback cleanup failed").Len() != 1 {
			t.Fatal("eventual cleanup failure was not reported")
		}
		if logs.FilterMessageSnippet("interruption summary").Len() != 0 {
			t.Fatal("failed Start manufactured a run summary")
		}
		provider.entered, provider.release, provider.unregisterErr = nil, nil, nil
		recreated, err := m.receiver(t.Context(), cfg, settings)
		if err != nil {
			t.Fatal(err)
		}
		if err := recreated.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOperationalCleanupPartialRegistrationDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cause := errors.New("synthetic partial registration")
		provider := &cleanupTestProvider{MeterProvider: noop.NewMeterProvider(), entered: make(chan struct{}), release: make(chan struct{}), registrationErr: cause}
		settings := receivertest.NewNopSettings(Type)
		settings.MeterProvider = provider
		ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
		defer cancel()
		if r, err := newOperationalReporter(ctx, settings); r != nil || !errors.Is(err, cause) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("partial construction = %v, %v", r, err)
		}
		<-provider.entered
		observed := &callbackObservations{}
		if err := provider.callbacks[0](t.Context(), observed); err != nil || len(observed.values) != 0 {
			t.Fatal("partial registration emitted observations")
		}
		close(provider.release)
		synctest.Wait()
		if provider.calls.Load() != 1 {
			t.Fatal("partial registration cleanup not singular")
		}
	})
}

func TestOperationalCleanupNeverStartedWithoutReporter(t *testing.T) {
	r := newLiveTimingReceiver(nil, receivertest.NewNopSettings(Type))
	if err := r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Shutdown(ctx); err != context.Canceled {
		t.Fatalf("canceled Shutdown = %v", err)
	}
}
