package f1staticreceiver

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
)

var errReceiverStopped = errors.New("static replay receiver stopped")

type statusBroadcaster struct {
	delivery sync.Mutex
	mu       sync.Mutex
	hosts    []component.Host
	current  *componentstatus.Event
	stopped  bool
}

func (b *statusBroadcaster) setInitial(event *componentstatus.Event) {
	b.mu.Lock()
	b.current = event
	b.mu.Unlock()
}

func (b *statusBroadcaster) register(host component.Host) error {
	b.delivery.Lock()
	defer b.delivery.Unlock()
	if b.stopped {
		return errReceiverStopped
	}
	if host == nil {
		return nil
	}
	b.mu.Lock()
	b.hosts = append(b.hosts, host)
	current := b.current
	b.mu.Unlock()
	if current != nil {
		componentstatus.ReportStatus(host, current)
	}
	return nil
}

func (b *statusBroadcaster) Report(event *componentstatus.Event) {
	b.delivery.Lock()
	defer b.delivery.Unlock()
	if b.stopped {
		return
	}
	b.mu.Lock()
	b.current = event
	hosts := append([]component.Host(nil), b.hosts...)
	b.mu.Unlock()
	for _, host := range hosts {
		componentstatus.ReportStatus(host, event)
	}
}

func (b *statusBroadcaster) stop() {
	b.delivery.Lock()
	b.stopped = true
	b.delivery.Unlock()
}

type staticReceiver struct {
	cfg      Config
	settings receiver.Settings
	metrics  consumer.Metrics
	logs     consumer.Logs
	traces   consumer.Traces
	clock    Clock
	status   statusBroadcaster
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	stopped  bool
	claim    func(f1id.SessionIdentity) error
	remove   func()
}

func newReceiver(cfg Config, settings receiver.Settings) *staticReceiver {
	return &staticReceiver{cfg: cfg, settings: settings, clock: ProcessClock{}}
}
func (r *staticReceiver) Start(ctx context.Context, host component.Host) error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return errReceiverStopped
	}
	if !r.cfg.Enabled {
		r.mu.Unlock()
		return nil
	}
	first := r.done == nil
	var runCtx context.Context
	var done chan struct{}
	if first {
		runCtx, r.cancel = context.WithCancel(context.WithoutCancel(ctx))
		done = make(chan struct{})
		r.done = done
		r.status.setInitial(componentstatus.NewRecoverableErrorEvent(errors.New("static archive replay in progress")))
	}
	r.mu.Unlock()

	if err := r.status.register(host); err != nil {
		return err
	}
	if first {
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
	}
	return nil
}
func (r *staticReceiver) Shutdown(ctx context.Context) error {
	r.status.stop()
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
	if r.claim != nil {
		if err := r.claim(p.Identity); err != nil {
			return err
		}
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
