package f1livetimingreceiver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const operationalScope = "github.com/CtrlSpice/bargeboard/receiver/f1livetimingreceiver"
const metricPrefix = "otelcol_f1livetiming_"
const outageMessage = "Live Timing input interrupted; updates may be missing. Press Ctrl-C to stop the Collector."
const recoveryMessage = "Live Timing updates resumed; missed updates may be unrecoverable"

var errInputOutage = errors.New("Live Timing input interrupted; updates may be missing")
var errAwaitingInput = errors.New("Live Timing input not receiving: awaiting validated subscription and first normalized updates")
var errSourceStopped = errors.New("Live Timing server closed the connection without reconnect; Collector can still run")

// One serialized writer owns state and terminal/status output. SDK callbacks only
// load an immutable snapshot, including when invoked reentrantly by a reporter.
type operationalReporter struct {
	mu           sync.Mutex
	state        atomic.Pointer[operationalState]
	logger       *zap.Logger
	host         component.Host
	attrs        metric.MeasurementOption
	counters     [5]metric.Int64Counter
	registration metric.Registration
	// Separate from reporting state: disabling drains admitted callbacks before
	// recreation, even when the supplied provider cannot unregister its callback.
	callbackMu      sync.RWMutex
	callbackEnabled bool
	stopOnce        sync.Once
	cleanupDone     chan struct{}
	stopDone        chan struct{}
	cleanupErr      error  // Published by closing cleanupDone.
	onStop          func() // Nonblocking shared-receiver stopping guard.
	afterCleanup    func() // Join the input run and release its factory reservation.
	cancel          context.CancelFunc
	done            chan struct{}
}

func newOperationalReporter(ctx context.Context, settings receiver.Settings) (*operationalReporter, error) {
	// Match builtin OTLP's structural interface, without importing Collector internals.
	provider := settings.MeterProvider
	if injected, ok := provider.(interface {
		DropInjectedAttributes(...string) metric.MeterProvider
	}); ok {
		provider = injected.DropInjectedAttributes("otelcol.signal")
	}
	logger := settings.Logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		if injected, ok := core.(interface{ DropInjectedAttributes(...string) zapcore.Core }); ok {
			return injected.DropInjectedAttributes("otelcol.signal")
		}
		return core
	}))
	r := &operationalReporter{
		logger: logger, attrs: metric.WithAttributes(attribute.String("receiver", settings.ID.String())),
		cleanupDone: make(chan struct{}), stopDone: make(chan struct{}),
	}
	r.state.Store(&operationalState{})
	meter := provider.Meter(operationalScope)
	for i, spec := range []struct{ name, unit, description string }{
		{"outages", "{outage}", "Detected Live Timing input interruption episodes, including terminal failures."},
		{"reconnect_attempts", "{attempt}", "Actual Live Timing reconnect calls, excluding initial startup and canceled waits."},
		{"recoveries", "{recovery}", "Input outages closed by validated subscription and normalized updates; not racing export success."},
		{"normalized_updates", "{update}", "Envelopes in fully normalized input batches, not cars, datapoints, or exported racing signals."},
		{"consumer_failures", "{failure}", "Failed normalized-batch consumer calls, counted independently of input outages."},
	} {
		var err error
		r.counters[i], err = meter.Int64Counter(metricPrefix+spec.name, metric.WithUnit(spec.unit), metric.WithDescription(spec.description))
		if err != nil {
			return nil, err
		}
	}
	var gauges [3]metric.Int64ObservableGauge
	for i, spec := range []struct{ name, description string }{
		{"connection_active", "Established Live Timing transport with Subscribe written (1 or 0); not confirmed input or export."},
		{"subscription_active", "Current connection has a fully normalized Subscribe completion (1 or 0); updates may still be absent."},
		{"outage_active", "Unresolved detected input interruption (1 or 0), including a terminally stopped input."},
	} {
		var err error
		gauges[i], err = meter.Int64ObservableGauge(metricPrefix+spec.name, metric.WithUnit("1"), metric.WithDescription(spec.description))
		if err != nil {
			return nil, err
		}
	}
	duration, err := meter.Float64ObservableGauge(metricPrefix+"outage_duration", metric.WithUnit("s"), metric.WithDescription("Process seconds in the current unresolved input outage; zero after recovery."))
	if err != nil {
		return nil, err
	}
	age, err := meter.Float64ObservableGauge(metricPrefix+"last_update_age", metric.WithUnit("s"), metric.WithDescription("Process seconds since local acceptance of a nonempty normalized batch; omitted before first input, not source freshness."))
	if err != nil {
		return nil, err
	}
	r.registration, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		r.callbackMu.RLock()
		defer r.callbackMu.RUnlock()
		if !r.callbackEnabled {
			return nil
		}
		s := r.state.Load()
		at := time.Now()
		for i, active := range []bool{s.connection, s.subscription, s.outage} {
			var value int64
			if active {
				value = 1
			}
			o.ObserveInt64(gauges[i], value, r.attrs)
		}
		var seconds float64
		if s.outage {
			seconds = s.currentOutageDuration(at).Seconds()
		}
		o.ObserveFloat64(duration, seconds, r.attrs)
		if !s.lastUpdate.IsZero() {
			o.ObserveFloat64(age, max(0, at.Sub(s.lastUpdate)).Seconds(), r.attrs)
		}
		return nil
	}, gauges[0], gauges[1], gauges[2], duration, age)
	if err != nil {
		// RegisterCallback may return both a live registration and an error.
		// It has never been enabled, so even failed cleanup cannot emit gauges.
		waitErr := r.stop(ctx)
		select {
		case <-r.cleanupDone:
			err = errors.Join(err, r.cleanupErr)
		default:
			// The once-owned worker still owns eventual cleanup and reporting.
		}
		return nil, errors.Join(err, waitErr)
	}
	r.callbackMu.Lock()
	r.callbackEnabled = true
	r.callbackMu.Unlock()
	// Synchronous counters retain provider history across receiver recreation;
	// never replace that history with a newly zeroed observable run total.
	for _, counter := range r.counters {
		counter.Add(context.Background(), 0, r.attrs)
	}
	return r, nil
}

func (r *operationalReporter) start(host component.Host) {
	r.host = host
	r.apply(operationalInput{event: opStart})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.apply(operationalInput{event: opTick})
			}
		}
	}()
}

// beginStop never waits on SDK code, observation locks, or the periodic reporter.
// The single worker retains ownership through cleanup and the input-run join.
func (r *operationalReporter) beginStop() <-chan struct{} {
	r.stopOnce.Do(func() {
		if r.onStop != nil {
			r.onStop()
		}
		if r.cancel != nil {
			r.cancel()
		}
		go func() {
			defer close(r.stopDone)
			r.callbackMu.Lock()
			r.callbackEnabled = false
			r.callbackMu.Unlock()
			if r.done != nil {
				<-r.done
			}
			if r.registration != nil {
				r.cleanupErr = r.registration.Unregister()
				if r.cleanupErr != nil {
					r.logger.Warn("Live Timing internal metric callback cleanup failed")
				}
			}
			close(r.cleanupDone)
			if r.afterCleanup != nil {
				r.afterCleanup()
			}
		}()
	})
	return r.stopDone
}

func (r *operationalReporter) stop(ctx context.Context) error {
	return waitForCompletion(ctx, r.beginStop())
}

func waitForCompletion(ctx context.Context, done <-chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if done == nil {
		// An input run or reporter that never existed.
		return nil
	}
	select {
	case <-done:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Return the published value after reporting, preserving its original process
// deadline even when synchronous output takes time.
func (r *operationalReporter) apply(in operationalInput) operationalState {
	r.mu.Lock()
	defer r.mu.Unlock()
	in.at = time.Now()
	old := *r.state.Load()
	s, notice := reduceOperational(old, in)
	r.state.Store(&s)
	for i, delta := range [...]int64{s.outages - old.outages, s.attempts - old.attempts, s.recoveries - old.recoveries, s.updates - old.updates, s.consumerFailures - old.consumerFailures} {
		if delta != 0 {
			r.counters[i].Add(context.Background(), delta, r.attrs)
		}
	}
	if notice == noticeNone {
		return s
	}
	durations := operationalTiming(old, s, in.at)
	fields := []zap.Field{
		zap.Int64("attempt", s.attempts), zap.Float64("run_elapsed_seconds", durations.run.Seconds()),
		zap.Float64("outage_duration_seconds", durations.outage.Seconds()),
		zap.Float64("total_outage_duration_seconds", durations.totalOutage.Seconds()),
		zap.Float64("next_delay_seconds", s.nextDelay(in.at).Seconds()),
		zap.Bool("connection_active", s.connection), zap.Bool("subscription_active", s.subscription),
		zap.Int64("normalized_updates", s.updates),
	}
	switch notice {
	case noticeStarting, noticeWaiting:
		r.logger.Info("Waiting for validated Live Timing subscription and first updates; no F1 race export is implemented. Press Ctrl-C to stop the Collector.", fields...)
		if notice == noticeStarting {
			componentstatus.ReportStatus(r.host, componentstatus.NewRecoverableErrorEvent(errAwaitingInput))
		}
	case noticeFirstData:
		r.logger.Info("First Live Timing updates observed with validated subscription; no F1 race export is implemented", fields...)
		componentstatus.ReportStatus(r.host, componentstatus.NewEvent(componentstatus.StatusOK))
	case noticeOutage:
		r.logger.Warn(outageMessage, fields...)
		componentstatus.ReportStatus(r.host, componentstatus.NewRecoverableErrorEvent(errInputOutage))
	case noticeProgress:
		r.logger.Info("Live Timing input outage; reconnect progress. Press Ctrl-C to stop the Collector.", fields...)
	case noticeRecovered:
		r.logger.Info(recoveryMessage, fields...)
		componentstatus.ReportStatus(r.host, componentstatus.NewEvent(componentstatus.StatusOK))
	case noticeConsumerFailure:
		r.logger.Warn("F1 live timing batch consumer failed; input observation does not imply export")
	case noticeInvalid:
		r.logger.Error(errPermanentLiveTimingFailure.Error())
		componentstatus.ReportStatus(r.host, componentstatus.NewPermanentErrorEvent(errPermanentLiveTimingFailure))
	case noticeSourceStopped:
		r.logger.Error(errSourceStopped.Error())
		componentstatus.ReportStatus(r.host, componentstatus.NewPermanentErrorEvent(errSourceStopped))
	case noticeSummary:
		fields = append(fields, zap.Int64("outages", s.outages), zap.Int64("recoveries", s.recoveries),
			zap.Int64("consumer_failures", s.consumerFailures), zap.Bool("unresolved_outage", s.outage))
		r.logger.Info("Live Timing input run ended; interruption summary (not an export summary)", fields...)
	case noticeStopped:
		r.logger.Warn("Live Timing input stopped; Collector can still run. Press Ctrl-C to stop the Collector.", fields...)
	}
	return s
}
