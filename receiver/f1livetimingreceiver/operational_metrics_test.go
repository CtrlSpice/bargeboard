package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type metricPoint struct {
	Receiver string
	Value    float64
}
type metricResult struct {
	Unit, Kind string
	Points     []metricPoint
}

// Independent metadata oracle: do not read descriptions from instrument specs.
var expectedOperationalDescriptions = map[string]string{
	"otelcol_f1livetiming_connection_active":   "Established Live Timing transport with Subscribe written (1 or 0); not confirmed input or export.",
	"otelcol_f1livetiming_subscription_active": "Current connection has a fully normalized Subscribe completion (1 or 0); updates may still be absent.",
	"otelcol_f1livetiming_outage_active":       "Unresolved detected input interruption (1 or 0), including a terminally stopped input.",
	"otelcol_f1livetiming_outages":             "Detected Live Timing input interruption episodes, including terminal failures.",
	"otelcol_f1livetiming_reconnect_attempts":  "Actual Live Timing reconnect calls, excluding initial startup and canceled waits.",
	"otelcol_f1livetiming_recoveries":          "Input outages closed by validated subscription and normalized updates; not racing export success.",
	"otelcol_f1livetiming_outage_duration":     "Process seconds in the current unresolved input outage; zero after recovery.",
	"otelcol_f1livetiming_normalized_updates":  "Envelopes in fully normalized input batches, not cars, datapoints, or exported racing signals.",
	"otelcol_f1livetiming_last_update_age":     "Process seconds since local acceptance of a nonempty normalized batch; omitted before first input, not source freshness.",
	"otelcol_f1livetiming_consumer_failures":   "Failed normalized-batch consumer calls, counted independently of input outages.",
}

func collectOperational(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricResult {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	result := map[string]metricResult{}
	for _, scope := range data.ScopeMetrics {
		if scope.Scope.Name != "github.com/CtrlSpice/bargeboard/receiver/f1livetimingreceiver" || scope.Scope.Version != "" || scope.Scope.SchemaURL != "" || scope.Scope.Attributes.Len() != 0 {
			t.Fatalf("unexpected scope: %+v", scope.Scope)
		}
		for _, m := range scope.Metrics {
			if want, ok := expectedOperationalDescriptions[m.Name]; !ok || m.Description != want {
				t.Fatalf("%s description = %q, want %q", m.Name, m.Description, want)
			}
			r := metricResult{Unit: m.Unit}
			add := func(attrs attribute.Set, value float64, start, end time.Time, exemplars int) {
				t.Helper()
				id, ok := attrs.Value("receiver")
				if !ok || attrs.Len() != 1 || id.Type() != attribute.STRING || end.IsZero() || (!start.IsZero() && start.After(end)) || exemplars != 0 {
					t.Fatalf("unexpected datapoint: %v %v %v %d", attrs, start, end, exemplars)
				}
				r.Points = append(r.Points, metricPoint{id.AsString(), value})
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				if !d.IsMonotonic || d.Temporality != metricdata.CumulativeTemporality {
					t.Fatalf("wrong sum contract: %+v", d)
				}
				r.Kind = "Int64Counter"
				for _, p := range d.DataPoints {
					add(p.Attributes, float64(p.Value), p.StartTime, p.Time, len(p.Exemplars))
				}
			case metricdata.Gauge[int64]:
				r.Kind = "Int64Gauge"
				for _, p := range d.DataPoints {
					add(p.Attributes, float64(p.Value), p.StartTime, p.Time, len(p.Exemplars))
				}
			case metricdata.Gauge[float64]:
				r.Kind = "Float64Gauge"
				for _, p := range d.DataPoints {
					add(p.Attributes, p.Value, p.StartTime, p.Time, len(p.Exemplars))
				}
			default:
				t.Fatalf("unexpected aggregation %T", d)
			}
			sort.Slice(r.Points, func(i, j int) bool { return r.Points[i].Receiver < r.Points[j].Receiver })
			if _, exists := result[m.Name]; exists {
				t.Fatalf("duplicate metric %s", m.Name)
			}
			result[m.Name] = r
		}
	}
	return result
}

func zeroOperationalMetrics(id string) map[string]metricResult {
	result := map[string]metricResult{}
	for _, spec := range []struct{ name, unit, kind string }{
		{"connection_active", "1", "Int64Gauge"}, {"subscription_active", "1", "Int64Gauge"}, {"outage_active", "1", "Int64Gauge"},
		{"outage_duration", "s", "Float64Gauge"},
		{"outages", "{outage}", "Int64Counter"}, {"reconnect_attempts", "{attempt}", "Int64Counter"},
		{"recoveries", "{recovery}", "Int64Counter"}, {"normalized_updates", "{update}", "Int64Counter"}, {"consumer_failures", "{failure}", "Int64Counter"},
	} {
		result[metricPrefix+spec.name] = metricResult{spec.unit, spec.kind, []metricPoint{{id, 0}}}
	}
	return result
}

func TestOperationalMetricsLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, other := sdkmetric.NewManualReader(), sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithReader(other))
		defer func() { _ = provider.Shutdown(t.Context()) }()
		settings := receivertest.NewNopSettings(Type)
		settings.ID = component.NewID(Type)
		settings.MeterProvider = provider
		r, err := newOperationalReporter(settings)
		if err != nil {
			t.Fatal(err)
		}
		defer r.stop()
		want := zeroOperationalMetrics("f1livetiming")
		check := func() {
			t.Helper()
			before := *r.state.Load()
			for _, rd := range []*sdkmetric.ManualReader{reader, other, reader} {
				if got := collectOperational(t, rd); !reflect.DeepEqual(got, want) {
					t.Fatalf("metrics = %#v; want %#v", got, want)
				}
			}
			if *r.state.Load() != before {
				t.Fatal("collection changed run state")
			}
		}
		set := func(name string, value float64) { want[metricPrefix+name].Points[0].Value = value }
		check()
		r.apply(operationalInput{event: opStart})
		r.apply(operationalInput{event: opConnected})
		set("connection_active", 1)
		check()
		r.apply(operationalInput{event: opBatch, snapshot: true})
		set("subscription_active", 1)
		check()
		r.apply(operationalInput{event: opBatch, updates: 2})
		set("normalized_updates", 2)
		want[metricPrefix+"last_update_age"] = metricResult{"s", "Float64Gauge", []metricPoint{{"f1livetiming", 0}}}
		check()
		r.apply(operationalInput{event: opOutage})
		r.apply(operationalInput{event: opSchedule, delay: 30 * time.Second})
		time.Sleep(5 * time.Second)
		set("connection_active", 0)
		set("subscription_active", 0)
		set("outage_active", 1)
		set("outages", 1)
		set("outage_duration", 5)
		set("last_update_age", 5)
		check()
		r.apply(operationalInput{event: opAttempt})
		r.apply(operationalInput{event: opConnected})
		r.apply(operationalInput{event: opBatch, snapshot: true})
		set("reconnect_attempts", 1)
		set("connection_active", 1)
		set("subscription_active", 1)
		check() // empty completion has not recovered
		r.apply(operationalInput{event: opBatch, updates: 1})
		r.apply(operationalInput{event: opConsumerFailure})
		set("recoveries", 1)
		set("normalized_updates", 3)
		set("consumer_failures", 1)
		set("outage_active", 0)
		set("outage_duration", 0)
		set("last_update_age", 0)
		check()
		r.apply(operationalInput{event: opSourceStopped})
		r.apply(operationalInput{event: opFinish})
		time.Sleep(7 * time.Second)
		set("connection_active", 0)
		set("subscription_active", 0)
		set("outage_active", 1)
		set("outages", 2)
		set("outage_duration", 7)
		set("last_update_age", 7)
		check() // terminal state remains observable and ages keep increasing
		r.stop()
		r.stop()
		for name, m := range want {
			if m.Kind != "Int64Counter" {
				delete(want, name)
			}
		}
		check()
		// Same provider/ID retains synchronous counter history but starts fresh gauges
		// and run totals, with no random run-ID dimension.
		recreated, err := newOperationalReporter(settings)
		if err != nil {
			t.Fatal(err)
		}
		defer recreated.stop()
		for name, m := range zeroOperationalMetrics("f1livetiming") {
			if m.Kind != "Int64Counter" {
				want[name] = m
			}
		}
		check()
		if *recreated.state.Load() != (operationalState{}) {
			t.Fatal("recreated run inherited old run totals")
		}
		recreated.apply(operationalInput{event: opOutage})
		set("outages", 3)
		set("outage_active", 1)
		check()
	})
}

// Structural injection double follows the pinned Collector's public method.
type injectedMeterProvider struct {
	metric.MeterProvider
	drops atomic.Int32
}

func (p *injectedMeterProvider) DropInjectedAttributes(keys ...string) metric.MeterProvider {
	if !reflect.DeepEqual(keys, []string{"otelcol.signal"}) {
		panic("unexpected dropped keys")
	}
	p.drops.Add(1)
	return p.MeterProvider
}
func (p *injectedMeterProvider) Meter(name string, opts ...metric.MeterOption) metric.Meter {
	return p.MeterProvider.Meter(name, append(opts, metric.WithInstrumentationAttributes(attribute.String("otelcol.signal", "traces")))...)
}

func TestOperationalMetricsSharedFactoriesAndIDs(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = provider.Shutdown(t.Context()) }()
	injected := &injectedMeterProvider{MeterProvider: provider}
	factory := NewFactory()
	want := zeroOperationalMetrics("f1livetiming/a")
	for name, m := range want {
		m.Points = append(m.Points, metricPoint{"f1livetiming/b", 0})
		want[name] = m
	}
	for _, id := range []string{"a", "b"} {
		settings := receivertest.NewNopSettings(Type)
		settings.ID = component.NewIDWithName(Type, id)
		settings.MeterProvider = injected
		cfg := factory.CreateDefaultConfig().(*Config)
		cfg.Auth.TokenFile = "synthetic-unread-token-file"
		traces, err := factory.CreateTraces(t.Context(), settings, cfg, consumertest.NewNop())
		if err != nil {
			t.Fatal(err)
		}
		metrics, err := factory.CreateMetrics(t.Context(), settings, cfg, consumertest.NewNop())
		if err != nil {
			t.Fatal(err)
		}
		logs, err := factory.CreateLogs(t.Context(), settings, cfg, consumertest.NewNop())
		if err != nil {
			t.Fatal(err)
		}
		if traces != metrics || traces != logs {
			t.Fatal("signal factories did not share receiver")
		}
		defer func() { _ = traces.Shutdown(t.Context()) }()
	}
	if injected.drops.Load() != 2 {
		t.Fatalf("injection drops = %d", injected.drops.Load())
	}
	if got := collectOperational(t, reader); !reflect.DeepEqual(got, want) {
		t.Fatalf("metrics = %#v; want %#v", got, want)
	}
}

type failingMeterProvider struct {
	noop.MeterProvider
	meter failingMeter
}

func (p *failingMeterProvider) Meter(string, ...metric.MeterOption) metric.Meter { return &p.meter }

type failingMeter struct {
	noop.Meter
	fail                           bool
	failInstrument                 string
	registrations, unregisters     int
	partial                        bool
	registrationErr, unregisterErr error
	callbacks                      []metric.Callback
}

func (m *failingMeter) Int64Counter(name string, opts ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	if name == metricPrefix+m.failInstrument {
		return nil, errors.New("synthetic instrument failure")
	}
	return m.Meter.Int64Counter(name, opts...)
}
func (m *failingMeter) Int64ObservableGauge(name string, opts ...metric.Int64ObservableGaugeOption) (metric.Int64ObservableGauge, error) {
	if name == metricPrefix+m.failInstrument {
		return nil, errors.New("synthetic instrument failure")
	}
	return m.Meter.Int64ObservableGauge(name, opts...)
}
func (m *failingMeter) Float64ObservableGauge(name string, opts ...metric.Float64ObservableGaugeOption) (metric.Float64ObservableGauge, error) {
	if name == metricPrefix+m.failInstrument {
		return nil, errors.New("synthetic instrument failure")
	}
	return m.Meter.Float64ObservableGauge(name, opts...)
}
func (m *failingMeter) RegisterCallback(callback metric.Callback, _ ...metric.Observable) (metric.Registration, error) {
	m.registrations++
	reg := countedRegistration{unregisters: &m.unregisters, err: m.unregisterErr}
	if m.partial {
		m.callbacks = append(m.callbacks, callback)
		return reg, m.registrationErr
	}
	if m.fail {
		return nil, errors.New("synthetic registration failure")
	}
	m.callbacks = append(m.callbacks, callback)
	return reg, nil
}

type countedRegistration struct {
	noop.Registration
	unregisters *int
	err         error
}

func (r countedRegistration) Unregister() error { *r.unregisters++; return r.err }

type callbackObservations struct {
	metric.Observer
	values []any
}

type blockedCallbackObserver struct {
	callbackObservations
	entered, release chan struct{}
	once             sync.Once
}

func (o *blockedCallbackObserver) ObserveInt64(instrument metric.Int64Observable, value int64, opts ...metric.ObserveOption) {
	o.once.Do(func() { close(o.entered); <-o.release })
	o.callbackObservations.ObserveInt64(instrument, value, opts...)
}

func TestOperationalCallbackShutdownDrainsObservations(t *testing.T) {
	provider := &failingMeterProvider{meter: failingMeter{unregisterErr: errors.New("synthetic retained callback")}}
	settings := receivertest.NewNopSettings(Type)
	settings.MeterProvider = provider
	r, err := newOperationalReporter(settings)
	if err != nil {
		t.Fatal(err)
	}
	o := &blockedCallbackObserver{entered: make(chan struct{}), release: make(chan struct{})}
	collected := make(chan error, 1)
	go func() { collected <- provider.meter.callbacks[0](t.Context(), o) }()
	<-o.entered
	// The lifetime barrier must still cover the external observation call.
	if r.callbackMu.TryLock() {
		r.callbackMu.Unlock()
		close(o.release)
		<-collected
		r.stop()
		t.Fatal("callback released its lifetime guard before observations finished")
	}
	stopped := make(chan struct{})
	go func() { r.stop(); close(stopped) }()
	close(o.release)
	if err := <-collected; err != nil {
		t.Fatal(err)
	}
	<-stopped
	if want := []any{int64(0), int64(0), int64(0), float64(0)}; !reflect.DeepEqual(o.values, want) {
		t.Fatalf("admitted observations = %v", o.values)
	}
	late := &callbackObservations{}
	if err := provider.meter.callbacks[0](t.Context(), late); err != nil || len(late.values) != 0 {
		t.Fatalf("late callback = %v, %v", err, late.values)
	}
}

func (o *callbackObservations) ObserveInt64(_ metric.Int64Observable, value int64, _ ...metric.ObserveOption) {
	o.values = append(o.values, value)
}
func (o *callbackObservations) ObserveFloat64(_ metric.Float64Observable, value float64, _ ...metric.ObserveOption) {
	o.values = append(o.values, value)
}

func TestOperationalPartialRegistrationUnwinds(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanupFails), func(t *testing.T) {
			registrationErr := errors.New("synthetic partial registration failure")
			var cleanupErr error
			if cleanupFails {
				cleanupErr = errors.New("synthetic unregister failure")
			}
			provider := &failingMeterProvider{meter: failingMeter{partial: true, registrationErr: registrationErr, unregisterErr: cleanupErr}}
			settings := receivertest.NewNopSettings(Type)
			settings.MeterProvider = provider
			m := newReceiverMap()
			cfg := createDefaultConfig().(*Config)
			cfg.Auth.TokenFile = "unused-synthetic-path"
			r, err := m.receiver(cfg, settings)
			if r != nil || !errors.Is(err, registrationErr) || (cleanupFails && !errors.Is(err, cleanupErr)) || len(m.receivers) != 0 || provider.meter.unregisters != 1 {
				t.Fatalf("partial construction = %v, %v; cache=%d unregisters=%d", r, err, len(m.receivers), provider.meter.unregisters)
			}
			provider.meter.partial, provider.meter.unregisterErr = false, nil
			recreated, err := m.receiver(cfg, settings)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = recreated.Shutdown(t.Context()) }()
			// A bad provider can retain the old callback despite Unregister. Only
			// the recreated receiver may observe gauges under this same ID.
			observed := &callbackObservations{}
			for _, callback := range provider.meter.callbacks {
				if err := callback(t.Context(), observed); err != nil {
					t.Fatal(err)
				}
			}
			if want := []any{int64(0), int64(0), int64(0), float64(0)}; !reflect.DeepEqual(observed.values, want) {
				t.Fatalf("observations = %v, want %v", observed.values, want)
			}
		})
	}
}

func TestOperationalFailedUnregisterDisablesRetainedCallback(t *testing.T) {
	provider := &failingMeterProvider{meter: failingMeter{unregisterErr: errors.New("private cleanup error")}}
	settings := receivertest.NewNopSettings(Type)
	core, logs := observer.New(zap.WarnLevel)
	settings.Logger, settings.MeterProvider = zap.New(core), provider
	r, err := newOperationalReporter(settings)
	if err != nil {
		t.Fatal(err)
	}
	r.apply(operationalInput{event: opConnected})
	r.apply(operationalInput{event: opBatch, snapshot: true, updates: 1})
	r.stop()
	r.stop()
	if provider.meter.unregisters != 1 || logs.FilterMessage("Live Timing internal metric callback cleanup failed").Len() != 1 {
		t.Fatal("cleanup not attempted and reported exactly once")
	}
	provider.meter.unregisterErr = nil
	recreated, err := newOperationalReporter(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer recreated.stop()
	// Late run completion/publication must not re-enable the old callback.
	r.apply(operationalInput{event: opFinish})
	observed := &callbackObservations{}
	for _, callback := range provider.meter.callbacks {
		if err := callback(t.Context(), observed); err != nil {
			t.Fatal(err)
		}
	}
	if want := []any{int64(0), int64(0), int64(0), float64(0)}; !reflect.DeepEqual(observed.values, want) {
		t.Fatalf("retained callbacks emitted %v, want %v", observed.values, want)
	}
}

type retainingSDKProvider struct {
	metric.MeterProvider
	unregisterErr error
}

func (p *retainingSDKProvider) Meter(name string, opts ...metric.MeterOption) metric.Meter {
	return retainingSDKMeter{Meter: p.MeterProvider.Meter(name, opts...), unregisterErr: p.unregisterErr}
}

type retainingSDKMeter struct {
	metric.Meter
	unregisterErr error
}

func (m retainingSDKMeter) RegisterCallback(callback metric.Callback, instruments ...metric.Observable) (metric.Registration, error) {
	reg, err := m.Meter.RegisterCallback(callback, instruments...)
	return retainedSDKRegistration{Registration: reg, err: m.unregisterErr}, err
}

type retainedSDKRegistration struct {
	metric.Registration
	err error
}

func (r retainedSDKRegistration) Unregister() error {
	if r.err != nil {
		return r.err
	}
	return r.Registration.Unregister()
}

func TestOperationalFailedUnregisterRecreationWithSDK(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader := sdkmetric.NewManualReader()
		sdk := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		defer func() { _ = sdk.Shutdown(t.Context()) }()
		provider := &retainingSDKProvider{MeterProvider: sdk, unregisterErr: errors.New("synthetic retained callback")}
		settings := receivertest.NewNopSettings(Type)
		settings.ID, settings.MeterProvider = component.NewID(Type), provider
		old, err := newOperationalReporter(settings)
		if err != nil {
			t.Fatal(err)
		}
		old.apply(operationalInput{event: opConnected})
		old.apply(operationalInput{event: opBatch, snapshot: true, updates: 3})
		_ = collectOperational(t, reader)
		old.stop()
		provider.unregisterErr = nil
		recreated, err := newOperationalReporter(settings)
		if err != nil {
			t.Fatal(err)
		}
		defer recreated.stop()
		want := zeroOperationalMetrics("f1livetiming")
		want[metricPrefix+"normalized_updates"].Points[0].Value = 3
		for range 3 {
			if got := collectOperational(t, reader); !reflect.DeepEqual(got, want) {
				t.Fatalf("recreated metrics = %#v, want %#v", got, want)
			}
		}
	})
}

func TestOperationalMetricsConstructionAndFailedStartCleanup(t *testing.T) {
	provider := &failingMeterProvider{meter: failingMeter{fail: true}}
	settings := receivertest.NewNopSettings(Type)
	settings.MeterProvider = provider
	m := newReceiverMap()
	cfg := createDefaultConfig().(*Config)
	cfg.Auth.TokenFile = "unused-synthetic-path"
	if r, err := m.receiver(cfg, settings); err == nil || r != nil || len(m.receivers) != 0 {
		t.Fatal("failed construction cached a receiver")
	}
	provider.meter.fail = false
	r, err := m.receiver(cfg, settings)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Start(ctx, nil); err == nil {
		t.Fatal("canceled start succeeded")
	}
	if len(m.receivers) != 0 || provider.meter.unregisters != 1 {
		t.Fatal("failed start retained cache or callback")
	}
	if err := r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if provider.meter.unregisters != 1 {
		t.Fatal("callback unregistered twice")
	}
	if provider.meter.registrations != 2 {
		t.Fatal("construction was not retried")
	}
}

func TestOperationalInstrumentErrorsDoNotCache(t *testing.T) {
	for _, name := range []string{"outages", "subscription_active", "last_update_age"} {
		t.Run(name, func(t *testing.T) {
			provider := &failingMeterProvider{meter: failingMeter{failInstrument: name}}
			settings := receivertest.NewNopSettings(Type)
			settings.MeterProvider = provider
			m := newReceiverMap()
			cfg := createDefaultConfig().(*Config)
			cfg.Auth.TokenFile = "unused-synthetic-path"
			if r, err := m.receiver(cfg, settings); r != nil || err == nil || len(m.receivers) != 0 || provider.meter.registrations != 0 {
				t.Fatal("instrument failure cached a receiver or callback")
			}
			provider.meter.failInstrument = ""
			r, err := m.receiver(cfg, settings)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if provider.meter.registrations != 1 || provider.meter.unregisters != 1 {
				t.Fatal("incorrect callback lifetime")
			}
		})
	}
}
