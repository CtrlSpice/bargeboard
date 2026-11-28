package f1staticreceiver

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
)

type staticReceiver struct {
	cfg      Config
	settings receiver.Settings
	metrics  consumer.Metrics
	logs     consumer.Logs
	traces   consumer.Traces
	clock    Clock
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	stopped  bool
	remove   func()
}

func newReceiver(cfg Config, settings receiver.Settings) *staticReceiver {
	return &staticReceiver{cfg: cfg, settings: settings, clock: ProcessClock{}}
}
func (r *staticReceiver) Start(ctx context.Context, _ component.Host) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return errors.New("static replay receiver stopped")
	}
	if !r.cfg.Enabled || r.done != nil {
		return nil
	}
	ctx, r.cancel = context.WithCancel(context.WithoutCancel(ctx))
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		if err := r.run(ctx); err != nil && ctx.Err() == nil && r.settings.Logger != nil {
			r.settings.Logger.Error("static replay collection stopped", zap.Error(err))
		}
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
