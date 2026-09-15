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

	cancel      context.CancelFunc
	done        chan struct{}
	consume     func(context.Context, normalizedLiveTimingBatch) error
	now         func() time.Time
	retryDelay  func(int) time.Duration
	operational *operationalReporter

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
		consume: func(context.Context, normalizedLiveTimingBatch) error {
			return nil
		},
		now:        time.Now,
		retryDelay: reconnectDelay,
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

func (r *liveTimingReceiver) run(
	ctx context.Context,
	connection *signalRConnection,
	done chan<- struct{},
) {
	defer close(done)
	defer r.operational.apply(operationalInput{event: opFinish})
	attempt := 0

	for {
		r.operational.apply(operationalInput{event: opConnected})
		receivedBatch := false
		err := connection.read(ctx, func(ctx context.Context, batch liveTimingBatch) error {
			normalized, err := normalizeLiveTimingBatch(batch, r.now())
			if err != nil {
				return err
			}
			receivedBatch = true
			r.operational.apply(operationalInput{event: opBatch, snapshot: normalized.source == liveTimingUpdateSourceSnapshot, updates: len(normalized.updates)})
			if err := r.consume(ctx, normalized); err != nil {
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
		r.operational.apply(operationalInput{event: opOutage})

		for {
			delay := r.retryDelay(attempt)
			scheduled := r.operational.apply(operationalInput{event: opSchedule, delay: delay})
			if !waitForReconnect(ctx, time.Until(scheduled.retryAt)) || ctx.Err() != nil {
				return
			}
			attempt++
			r.operational.apply(operationalInput{event: opAttempt})
			next, err := r.connect(ctx)
			if err == nil {
				connection = next
				break
			}
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, errInvalidLiveTimingData) {
				r.operational.apply(operationalInput{event: opInvalid})
				return
			}
			r.operational.apply(operationalInput{event: opOutage})
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

func waitForReconnect(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
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
