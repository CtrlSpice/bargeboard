package f1staticreceiver

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
)

type statusBroadcaster struct {
	delivery sync.Mutex
	mu       sync.Mutex
	hosts    []component.Host
	current  *componentstatus.Event
}

func (b *statusBroadcaster) register(host component.Host) {
	if host == nil {
		return
	}
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

type staticReceiver struct {
	cfg        Config
	settings   receiver.Settings
	metrics    consumer.Metrics
	logs       consumer.Logs
	traces     consumer.Traces
	clock      Clock
	status     statusBroadcaster
	statusInit sync.Once
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	stopped    bool
	remove     func()
}

func newReceiver(cfg Config, settings receiver.Settings) *staticReceiver {
	return &staticReceiver{cfg: cfg, settings: settings, clock: ProcessClock{}}
}
func (r *staticReceiver) Start(ctx context.Context, host component.Host) error {
	r.mu.Lock()
	stopped := r.stopped
	r.mu.Unlock()
	if stopped {
		return errors.New("static replay receiver stopped")
	}
	if !r.cfg.Enabled {
		return nil
	}
	r.statusInit.Do(func() {
		r.status.Report(componentstatus.NewRecoverableErrorEvent(errors.New("static archive replay in progress")))
	})
	r.status.register(host)

	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return errors.New("static replay receiver stopped")
	}
	if r.done != nil {
		r.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	r.cancel = cancel
	r.done = done
	r.mu.Unlock()

	go func() {
		defer close(done)
		if err := r.run(runCtx); err != nil {
			if runCtx.Err() == nil {
				if r.settings.Logger != nil {
					r.settings.Logger.Error("static replay collection stopped", zap.Error(err))
				}
				r.status.Report(componentstatus.NewPermanentErrorEvent(errors.New("static archive replay failed")))
			}
			return
		}
		if r.settings.Logger != nil {
			r.settings.Logger.Info("Static archive replay completed")
		}
		r.status.Report(componentstatus.NewEvent(componentstatus.StatusOK))
	}()
	return nil
}
func (r *staticReceiver) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if !r.stopped {
		r.stopped = true
		if r.cancel != nil {
			r.cancel()
		}
	}
	done := r.done
	r.mu.Unlock()
	if done != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}
	if r.remove != nil {
		r.remove()
	}
	return nil
}
func (r *staticReceiver) run(ctx context.Context) error {
	p, err := Materialize(ctx, r.cfg)
	if err != nil {
		return err
	}
	return r.play(ctx, p)
}
func (r *staticReceiver) play(ctx context.Context, p FrozenReplayPlan) error {
	s := newSession(p)
	var tracesFailed, metricsFailed, logsFailed bool
	reported := map[string]bool{}
	err := Replay(ctx, p, r.cfg.Pace, r.clock, func(ctx context.Context, b FrozenBatch, now time.Time) error {
		e := s.apply(b, b.Ordinal == int64(len(p.batches)-1))
		for _, reason := range slices.Sorted(maps.Keys(e.issues)) {
			if !reported[reason] && r.settings.Logger != nil {
				r.settings.Logger.Warn("Static replay input rejected", zap.String("reason", reason))
				reported[reason] = true
			}
		}
		v := r.settings.BuildInfo.Version
		if r.traces != nil && len(e.spans) > 0 && r.traces.ConsumeTraces(ctx, projectTraces(p, e.spans, v)) != nil {
			tracesFailed = true
		}
		if r.metrics != nil && len(e.points) > 0 && r.metrics.ConsumeMetrics(ctx, projectMetrics(p, e.points, v)) != nil {
			metricsFailed = true
		}
		if r.logs != nil && len(e.logs) > 0 && r.logs.ConsumeLogs(ctx, projectLogs(p, e.logs, v, now)) != nil {
			logsFailed = true
		}
		return nil
	})
	if tracesFailed {
		err = errors.Join(err, errors.New("trace delivery failed"))
	}
	if metricsFailed {
		err = errors.Join(err, errors.New("metric delivery failed"))
	}
	if logsFailed {
		err = errors.Join(err, errors.New("log delivery failed"))
	}
	return err
}
