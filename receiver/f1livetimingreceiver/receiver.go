package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

var errPermanentLiveTimingFailure = errors.New("F1 live timing receiver stopped after receiving invalid server data")

type liveTimingReceiver struct {
	config   *Config
	settings receiver.Settings
	client   *http.Client

	cancel     context.CancelFunc
	done       chan struct{}
	consume    func(context.Context, normalizedLiveTimingBatch) error
	now        func() time.Time
	retryDelay func(int) time.Duration

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
	connection, err := r.connect(ctx)
	if err != nil {
		return fmt.Errorf("connect to F1 live timing: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	r.cancel = cancel
	r.done = done
	go r.run(runCtx, connection, host, done)
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
	host component.Host,
	done chan<- struct{},
) {
	defer close(done)
	attempt := 0
	consumeFailureReported := false

	for {
		receivedBatch := false
		err := connection.read(ctx, func(ctx context.Context, batch liveTimingBatch) error {
			normalized, err := normalizeLiveTimingBatch(batch, r.now())
			if err != nil {
				return err
			}
			receivedBatch = true
			if err := r.consume(ctx, normalized); err != nil && !consumeFailureReported {
				r.settings.Logger.Error("F1 live timing batch consumer failed")
				consumeFailureReported = true
			}
			return nil
		})
		_ = connection.close(context.Background())
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errInvalidLiveTimingData) {
			r.reportPermanentFailure(host)
			return
		}
		if receivedBatch {
			attempt = 0
		}
		if errors.Is(err, errSignalRClosed) {
			r.settings.Logger.Warn("F1 live timing server closed the connection without reconnect")
			return
		}
		r.settings.Logger.Warn("F1 live timing connection lost; reconnecting")

		for {
			if !waitForReconnect(ctx, r.retryDelay(attempt)) {
				return
			}
			attempt++
			next, err := r.connect(ctx)
			if err == nil {
				connection = next
				break
			}
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, errInvalidLiveTimingData) {
				r.reportPermanentFailure(host)
				return
			}
			r.settings.Logger.Warn("F1 live timing reconnect failed: " + err.Error())
		}
	}
}

func (r *liveTimingReceiver) reportPermanentFailure(host component.Host) {
	r.settings.Logger.Error(errPermanentLiveTimingFailure.Error())
	componentstatus.ReportStatus(host, componentstatus.NewPermanentErrorEvent(errPermanentLiveTimingFailure))
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
	if r.cancel == nil {
		return nil
	}
	cancel := r.cancel
	done := r.done
	r.cancel = nil
	r.done = nil
	cancel()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type sharedReceiver struct {
	receiver *liveTimingReceiver
	remove   func()
	status   statusBroadcaster

	startOnce sync.Once
	startErr  error
	stopOnce  sync.Once
	stopErr   error
}

func (r *sharedReceiver) Start(ctx context.Context, host component.Host) error {
	r.status.register(host)
	r.startOnce.Do(func() {
		r.startErr = r.receiver.Start(ctx, &r.status)
	})
	return r.startErr
}

type statusBroadcaster struct {
	mu      sync.Mutex
	hosts   []component.Host
	current *componentstatus.Event
}

func (b *statusBroadcaster) register(host component.Host) {
	b.mu.Lock()
	b.hosts = append(b.hosts, host)
	current := b.current
	b.mu.Unlock()

	if current != nil {
		componentstatus.ReportStatus(host, current)
	}
}

func (b *statusBroadcaster) Report(event *componentstatus.Event) {
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
		r.stopErr = r.receiver.Shutdown(ctx)
		r.remove()
	})
	return r.stopErr
}

type receiverMap struct {
	mu        sync.Mutex
	receivers map[*Config]*sharedReceiver
}

func newReceiverMap() *receiverMap {
	return &receiverMap{receivers: make(map[*Config]*sharedReceiver)}
}

func (m *receiverMap) loadOrStore(config *Config, settings receiver.Settings) *sharedReceiver {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.receivers[config]; ok {
		return existing
	}

	shared := &sharedReceiver{
		receiver: newLiveTimingReceiver(config, settings),
	}
	shared.remove = func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.receivers[config] == shared {
			delete(m.receivers, config)
		}
	}
	m.receivers[config] = shared
	return shared
}
