package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

var errPermanentLiveTimingFailure = errors.New("F1 live timing receiver stopped after receiving invalid server data; Collector can still run")
var errReceiverStopping = errors.New("F1 live timing receiver is stopping")

type liveTimingReceiver struct {
	config   *Config
	settings receiver.Settings
	client   *http.Client

	cancel          context.CancelFunc
	done            chan struct{}
	state           liveTimingState
	reduce          func(liveTimingState, normalizedLiveTimingBatch) (liveTimingReduction, error)
	consume         func(context.Context, normalizedLiveTimingBatch) error
	now             func() time.Time
	retryDelay      func(int) time.Duration
	operational     *operationalReporter
	newRacingTicker func() (<-chan time.Time, func())
	reported        [3]bool

	consumersMu sync.Mutex
	traces      consumer.Traces
	metrics     consumer.Metrics
	logs        consumer.Logs
}

func newLiveTimingReceiver(config *Config, settings receiver.Settings) *liveTimingReceiver {
	return &liveTimingReceiver{
		config:   config,
		settings: settings,
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		reduce:     reduceLiveTimingBatch,
		now:        time.Now,
		retryDelay: reconnectDelay,
		newRacingTicker: func() (<-chan time.Time, func()) {
			t := time.NewTicker(time.Second)
			return t.C, t.Stop
		},
	}
}

func (r *liveTimingReceiver) registerTraces(next consumer.Traces) {
	r.consumersMu.Lock()
	defer r.consumersMu.Unlock()
	r.traces = next
}

func (r *liveTimingReceiver) registerMetrics(next consumer.Metrics) {
	r.consumersMu.Lock()
	defer r.consumersMu.Unlock()
	r.metrics = next
}

func (r *liveTimingReceiver) registerLogs(next consumer.Logs) {
	r.consumersMu.Lock()
	defer r.consumersMu.Unlock()
	r.logs = next
}

func (r *liveTimingReceiver) Start(ctx context.Context, host component.Host) error {
	if r.operational == nil {
		var err error
		r.operational, err = newOperationalReporter(ctx, r.settings)
		if err != nil {
			return fmt.Errorf("create F1 internal telemetry: %w", err)
		}
	}
	connection, err := r.connect(ctx)
	if err != nil {
		return errors.Join(fmt.Errorf("connect to F1 live timing: %w", err), r.operational.stop(ctx))
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	r.cancel = cancel
	r.done = done
	r.operational.start(host)
	go r.run(runCtx, connection, done)
	return nil
}

func (r *liveTimingReceiver) connect(ctx context.Context) (*signalRConnection, error) {
	connection, err := connectSignalR(ctx, r.client, r.config)
	if err != nil {
		return nil, err
	}
	if err := connection.subscribe(ctx); err != nil {
		_ = connection.close(context.Background())
		return nil, err
	}
	return connection, nil
}

func (r *liveTimingReceiver) consumers() (consumer.Traces, consumer.Metrics, consumer.Logs) {
	r.consumersMu.Lock()
	defer r.consumersMu.Unlock()
	return r.traces, r.metrics, r.logs
}

type racingDiagnostics [3]bool

func (s liveTimingState) diagnostics() racingDiagnostics {
	return racingDiagnostics{s.idCollision, s.timing.cars.lateSample || s.timing.lateObservation || s.timing.control.blueAfterEnd,
		s.timing.control.overflow || s.radio.overflow || len(s.timing.control.repairOverflow) > 0}
}

func (r *liveTimingReceiver) reportDiagnostics(active racingDiagnostics) {
	logger := r.settings.Logger
	if r.operational != nil && r.operational.logger != nil {
		logger = r.operational.logger
	}
	for i, message := range [...]string{
		"F1 span ID collision; affected span subtree suppressed",
		"F1 late metric observation excluded after window finalization",
		"F1 session record bound reached; excess records suppressed",
	} {
		if active[i] && !r.reported[i] && logger != nil {
			logger.Warn(message)
			r.reported[i] = true
		}
	}
}
func (r *liveTimingReceiver) deliver(ctx context.Context, e liveTimingEffects) bool {
	r.reportDiagnostics(r.state.diagnostics())
	traces, metrics, logs := r.consumers()
	version := r.settings.BuildInfo.Version
	failed := false
	if traces != nil && len(e.spans) > 0 {
		if p := projectTraces(version, e); p.SpanCount() > 0 && traces.ConsumeTraces(ctx, p) != nil {
			failed = true
		}
	}
	if metrics != nil && len(e.metrics) > 0 && metrics.ConsumeMetrics(ctx, projectMetrics(version, e)) != nil {
		failed = true
	}
	if logs != nil && len(e.logs) > 0 && logs.ConsumeLogs(ctx, projectLogs(version, e)) != nil {
		failed = true
	}
	return failed
}

// Keep ticks and batches on one state owner. The reader waits for each
// callback to finish before accepting the next record.
func (r *liveTimingReceiver) read(ctx context.Context, c *signalRConnection, consume func(context.Context, liveTimingBatch) error) error {
	t, m, l := r.consumers()
	if t == nil && m == nil && l == nil {
		return c.read(ctx, consume)
	}
	type input struct {
		ctx   context.Context
		batch liveTimingBatch
	}
	inputs := make(chan input)
	ack := make(chan error)
	done := make(chan error, 1)
	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()
	go func() {
		defer cancelConn()
		done <- c.read(connCtx, func(cbCtx context.Context, b liveTimingBatch) error {
			select {
			case <-cbCtx.Done():
				return cbCtx.Err()
			case inputs <- input{cbCtx, b}:
				return <-ack
			}
		})
	}()
	ticks, stop := r.newRacingTicker()
	defer stop()
	for {
		select {
		case err := <-done:
			return err
		case in := <-inputs:
			ack <- consume(in.ctx, in.batch)
		case <-ticks:
			r.tick(connCtx)
		}
	}
}

func (r *liveTimingReceiver) tick(ctx context.Context) {
	var e liveTimingEffects
	r.state, e = reduceLiveTimingTick(r.state, r.now())
	if r.deliver(ctx, e) {
		r.operational.apply(operationalInput{event: opConsumerFailure})
	}
}

func (r *liveTimingReceiver) flushTerminal(ctx context.Context) {
	t, m, l := r.consumers()
	if t == nil && m == nil && l == nil {
		return
	}
	var e liveTimingEffects
	r.state, e = reduceLiveTimingShutdown(r.state)
	if r.deliver(context.WithoutCancel(ctx), e) && r.operational != nil {
		r.operational.apply(operationalInput{event: opConsumerFailure})
	}
}

func (r *liveTimingReceiver) run(
	ctx context.Context,
	connection *signalRConnection,
	done chan<- struct{},
) {
	defer close(done)
	defer r.operational.apply(operationalInput{event: opFinish})
	defer r.flushTerminal(ctx)
	attempt := 0

	for {
		r.operational.apply(operationalInput{event: opConnected})
		receivedBatch := false
		err := r.read(ctx, connection, func(ctx context.Context, batch liveTimingBatch) error {
			normalized, err := normalizeLiveTimingBatch(batch, r.now())
			if err != nil {
				return err
			}
			receivedBatch = true
			r.operational.apply(operationalInput{event: opBatch, snapshot: normalized.source == liveTimingUpdateSourceSnapshot,
				updates: len(normalized.updates), invalidUnicodeUpdates: normalized.invalidUnicodeUpdates})
			reduction, err := r.reduce(r.state, normalized)
			if err != nil {
				r.operational.apply(operationalInput{event: opConsumerFailure})
				return nil
			}
			r.state = reduction.state
			if reduction.sessionInfoDisposition == sessionInfoDispositionReplaced {
				r.reportDiagnostics(reduction.retiredDiagnostics)
				r.reported = [3]bool{}
			}
			failed := r.deliver(ctx, reduction.effects)
			if r.consume != nil && r.consume(ctx, normalized) != nil {
				failed = true
			}
			if failed {
				r.operational.apply(operationalInput{event: opConsumerFailure})
			}
			return nil
		})
		_ = connection.close(context.Background())
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errInvalidLiveTimingData) {
			r.operational.apply(operationalInput{event: opInvalid})
			return
		}
		if receivedBatch {
			attempt = 0
		}
		if errors.Is(err, errSignalRClosed) {
			r.operational.apply(operationalInput{event: opSourceStopped})
			return
		}
		r.state = reduceLiveTimingDisconnect(r.state)
		r.operational.apply(operationalInput{event: opOutage})

		var ticks <-chan time.Time
		stop := func() {}
		if t, m, l := r.consumers(); t != nil || m != nil || l != nil {
			ticks, stop = r.newRacingTicker()
		}
		next, ok := r.reconnect(ctx, ticks, &attempt)
		stop()
		if !ok {
			return
		}
		connection = next
	}
}

func (r *liveTimingReceiver) reconnect(ctx context.Context, ticks <-chan time.Time, attempt *int) (*signalRConnection, bool) {
	var notBefore time.Time
	for {
		delay := r.retryDelay(*attempt)
		scheduled := r.operational.apply(operationalInput{event: opSchedule, delay: delay, notBefore: notBefore})
		if !r.waitForReconnect(ctx, time.Until(scheduled.retryAt), ticks) || ctx.Err() != nil {
			return nil, false
		}
		r.operational.apply(operationalInput{event: opTick})
		if !r.operational.beginAttempt(ctx) {
			return nil, false
		}
		(*attempt)++
		next, err := r.connectWithTicks(ctx, ticks)
		if err == nil {
			return next, true
		}
		if ctx.Err() != nil {
			return nil, false
		}
		if errors.Is(err, errInvalidLiveTimingData) {
			r.operational.apply(operationalInput{event: opInvalid})
			return nil, false
		}
		notBefore = time.Time{}
		if failure := asSetupHTTPError(err); failure != nil {
			if failure.permanent() {
				r.operational.apply(operationalInput{event: opSetupStopped, setupStage: failure.stage, httpStatus: failure.status})
				return nil, false
			}
			notBefore = failure.retryAt
		}
		r.operational.apply(operationalInput{event: opOutage})
	}
}

func (r *liveTimingReceiver) connectWithTicks(ctx context.Context, ticks <-chan time.Time) (*signalRConnection, error) {
	if ticks == nil {
		return r.connect(ctx)
	}
	type result struct {
		connection *signalRConnection
		err        error
	}
	done := make(chan result, 1)
	go func() { c, err := r.connect(ctx); done <- result{c, err} }()
	for {
		select {
		case v := <-done:
			return v.connection, v.err
		case <-ticks:
			r.tick(ctx)
		}
	}
}
func reconnectDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Second << min(attempt, 5)
	return min(delay, 30*time.Second)
}

func (r *liveTimingReceiver) waitForReconnect(ctx context.Context, delay time.Duration, ticks <-chan time.Time) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return true
		case <-ctx.Done():
			return false
		case <-ticks:
			r.tick(ctx)
		}
	}
}

func (r *liveTimingReceiver) Shutdown(ctx context.Context) error {
	r.beginShutdown()
	return r.waitShutdown(ctx)
}

func (r *liveTimingReceiver) beginShutdown() {
	if r.cancel != nil {
		r.cancel()
	}
	if r.operational != nil {
		r.operational.beginStop()
	}
}

func (r *liveTimingReceiver) waitShutdown(ctx context.Context) error {
	if err := waitForCompletion(ctx, r.done); err != nil {
		return err
	}
	if r.operational != nil {
		return waitForCompletion(ctx, r.operational.stopDone)
	}
	return ctx.Err()
}

type sharedReceiver struct {
	receiver *liveTimingReceiver
	remove   func()
	status   statusBroadcaster

	startOnce sync.Once
	startErr  error
	stopOnce  sync.Once
	stopping  atomic.Bool
}

func (r *sharedReceiver) Start(ctx context.Context, host component.Host) error {
	if r.stopping.Load() {
		return errReceiverStopping
	}
	r.status.register(host)
	r.startOnce.Do(func() {
		r.startErr = r.receiver.Start(ctx, &r.status)
	})
	return r.startErr
}

type statusBroadcaster struct {
	// Order replay and broadcasts separately from state access: external Report
	// may call GetExtensions, which must never need the delivery lock.
	delivery sync.Mutex
	mu       sync.Mutex
	hosts    []component.Host
	current  *componentstatus.Event
}

func (b *statusBroadcaster) register(host component.Host) {
	b.delivery.Lock()
	defer b.delivery.Unlock()
	b.mu.Lock()
	b.hosts = append(b.hosts, host)
	current := b.current
	b.mu.Unlock()

	if current != nil {
		componentstatus.ReportStatus(host, current)
	}
}

func (b *statusBroadcaster) Report(event *componentstatus.Event) {
	b.delivery.Lock()
	defer b.delivery.Unlock()
	b.mu.Lock()
	b.current = event
	hosts := append([]component.Host(nil), b.hosts...)
	b.mu.Unlock()

	for _, host := range hosts {
		componentstatus.ReportStatus(host, event)
	}
}

func (b *statusBroadcaster) GetExtensions() map[component.ID]component.Component {
	b.mu.Lock()
	var host component.Host
	if len(b.hosts) > 0 {
		host = b.hosts[0]
	}
	b.mu.Unlock()

	if host == nil {
		return nil
	}
	return host.GetExtensions()
}

func (r *sharedReceiver) Shutdown(ctx context.Context) error {
	r.stopOnce.Do(func() {
		r.stopping.Store(true)
		r.receiver.beginShutdown()
	})
	return r.receiver.waitShutdown(ctx)
}

type receiverMap struct {
	mu        sync.Mutex
	receivers map[*Config]*sharedReceiver
}

func newReceiverMap() *receiverMap {
	return &receiverMap{receivers: make(map[*Config]*sharedReceiver)}
}

func (m *receiverMap) loadOrStore(ctx context.Context, config *Config, settings receiver.Settings) (*sharedReceiver, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.receivers[config]; ok {
		if existing.stopping.Load() {
			return nil, errReceiverStopping
		}
		return existing, nil
	}

	operational, err := newOperationalReporter(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("create F1 internal telemetry: %w", err)
	}
	shared := &sharedReceiver{
		receiver: newLiveTimingReceiver(config, settings),
	}
	shared.receiver.operational = operational
	shared.remove = func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.receivers[config] == shared {
			delete(m.receivers, config)
		}
	}
	operational.onStop = func() { shared.stopping.Store(true) }
	operational.afterCleanup = func() {
		if shared.receiver.done != nil {
			<-shared.receiver.done
		}
		shared.remove()
	}
	m.receivers[config] = shared
	return shared, nil
}
