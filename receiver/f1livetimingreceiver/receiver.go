package f1livetimingreceiver

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

type liveTimingReceiver struct {
	config   *Config
	settings receiver.Settings
	client   *http.Client

	connection *signalRConnection

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

func (r *liveTimingReceiver) Start(ctx context.Context, _ component.Host) error {
	connection, err := connectSignalR(ctx, r.client, r.config)
	if err != nil {
		return fmt.Errorf("connect to F1 live timing: %w", err)
	}
	r.connection = connection
	return nil
}

func (r *liveTimingReceiver) Shutdown(ctx context.Context) error {
	if r.connection == nil {
		return nil
	}
	connection := r.connection
	r.connection = nil
	return connection.close(ctx)
}

type sharedReceiver struct {
	receiver *liveTimingReceiver
	remove   func()

	startOnce sync.Once
	startErr  error
	stopOnce  sync.Once
	stopErr   error
}

func (r *sharedReceiver) Start(ctx context.Context, host component.Host) error {
	r.startOnce.Do(func() {
		r.startErr = r.receiver.Start(ctx, host)
	})
	return r.startErr
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
