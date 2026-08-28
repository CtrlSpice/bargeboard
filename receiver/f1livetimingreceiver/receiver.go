package f1livetimingreceiver

import (
	"context"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

type liveTimingReceiver struct {
	config   *Config
	settings receiver.Settings

	consumersMu sync.Mutex
	traces      consumer.Traces
	metrics     consumer.Metrics
	logs        consumer.Logs
}

func newLiveTimingReceiver(config *Config, settings receiver.Settings) *liveTimingReceiver {
	return &liveTimingReceiver{config: config, settings: settings}
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

func (*liveTimingReceiver) Start(context.Context, component.Host) error {
	return nil
}

func (*liveTimingReceiver) Shutdown(context.Context) error {
	return nil
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
