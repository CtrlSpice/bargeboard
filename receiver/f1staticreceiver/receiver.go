package f1staticreceiver

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/circuitmap"
	"github.com/CtrlSpice/bargeboard/internal/f1engine"
	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
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
	var state f1engine.State
	var tracesFailed, metricsFailed, logsFailed bool
	var reported f1engine.RacingDiagnostics
	var nextTick time.Time
	deliver := func(e f1engine.Effects, now time.Time) {
		if r.settings.Logger != nil {
			var messages []string
			reported, messages = state.Diagnostics().NewMessages(reported)
			for _, message := range messages {
				r.settings.Logger.Warn(message)
			}
		}
		version := r.settings.BuildInfo.Version
		if r.traces != nil && e.HasSpans() {
			traces := f1engine.ProjectTracesWithCircuit(version, e, func(f1engine.SessionInfoIdentity) circuitmap.Map { return p.Circuit })
			if traces.SpanCount() > 0 && r.traces.ConsumeTraces(ctx, traces) != nil {
				tracesFailed = true
			}
		}
		if r.metrics != nil && e.HasMetrics() && r.metrics.ConsumeMetrics(ctx, f1engine.ProjectMetrics(version, e)) != nil {
			metricsFailed = true
		}
		if r.logs != nil && e.HasLogs() {
			logs := f1engine.ProjectLogs(version, e)
			for _, resource := range logs.ResourceLogs().All() {
				for _, scope := range resource.ScopeLogs().All() {
					for _, l := range scope.LogRecords().All() {
						l.SetObservedTimestamp(pcommon.NewTimestampFromTime(now))
					}
				}
			}
			if r.logs.ConsumeLogs(ctx, logs) != nil {
				logsFailed = true
			}
		}
	}
	tick := func(now time.Time) {
		var e f1engine.Effects
		state, e = f1engine.ReduceTick(state, nextTick)
		deliver(e, now)
		nextTick = nextTick.Add(time.Second)
	}
	err := Replay(ctx, p, r.cfg.Pace, r.clock, func(ctx context.Context, b FrozenBatch, now time.Time) error {
		if b.Snapshot {
			nextTick = b.ObservationUTC
		}
		for nextTick.Before(b.ObservationUTC) {
			if err := ctx.Err(); err != nil {
				return err
			}
			tick(now)
		}
		red, err := f1engine.ReduceBatch(state, b.engineBatch())
		if err != nil {
			return err
		}
		state = red.State
		deliver(red.Effects, now)
		last := b.Ordinal == int64(len(p.batches)-1)
		if nextTick.Equal(b.ObservationUTC) && (last || p.batches[b.Ordinal+1].CoordNs != b.CoordNs) {
			tick(now)
		}
		if last {
			var e f1engine.Effects
			state, e = f1engine.ReduceShutdown(state)
			deliver(e, now)
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
