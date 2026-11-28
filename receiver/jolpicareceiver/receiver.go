package jolpicareceiver

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
)

type roundReceiver struct {
	cfg      Config
	settings receiver.Settings
	metrics  consumer.Metrics
	logs     consumer.Logs
	client   *http.Client
	now      func() time.Time
	wait     func(context.Context, time.Duration) error
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	stopped  bool
	remove   func()
}

func newReceiver(cfg Config, settings receiver.Settings) *roundReceiver {
	return &roundReceiver{
		cfg: cfg, settings: settings, now: time.Now, wait: wait,
		client: f1id.NewRestrictedHTTPClient(10 * time.Second),
	}
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
func (r *roundReceiver) Start(ctx context.Context, _ component.Host) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return errors.New("Jolpica receiver stopped")
	}
	if !r.cfg.Enabled || r.done != nil {
		return nil
	}
	ctx, r.cancel = context.WithCancel(context.WithoutCancel(ctx))
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		defer r.client.CloseIdleConnections()
		if err := r.run(ctx); err != nil && ctx.Err() == nil && r.settings.Logger != nil {
			r.settings.Logger.Error("Jolpica collection stopped", zap.Error(err))
		}
	}()
	return nil
}
func (r *roundReceiver) Shutdown(ctx context.Context) error {
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
func (r *roundReceiver) run(ctx context.Context) error {
	tables := []table{race}
	if r.cfg.IncludeSprint {
		tables = append(tables, sprint)
	}
	if r.cfg.IncludeStandings {
		tables = append(tables, wdc)
		if r.cfg.Season >= 1958 {
			tables = append(tables, wcc)
		}
	}
	interval, err := pollInterval(r.cfg.PollInterval)
	if err != nil {
		return err
	}
	for attempt := 1; attempt <= r.cfg.MaxAttempts; attempt++ {
		s := snapshot{}
		delay := interval
		for i, t := range tables {
			if i > 0 {
				if err := r.wait(ctx, time.Second); err != nil {
					return err
				}
			}
			s[t], err = r.fetch(ctx, t)
			if err == nil {
				continue
			}
			if errors.Is(err, errUnpublished) {
				if t == sprint {
					err = nil
					continue
				}
				break
			}
			if limited, ok := err.(rateLimit); ok {
				delay = limited.delay
				break
			}
			return err
		}
		if err == nil {
			return r.deliver(ctx, s, r.now().UTC())
		}
		if attempt == r.cfg.MaxAttempts {
			return err
		}
		if err = r.wait(ctx, delay); err != nil {
			return err
		}
	}
	return nil
}
func (r *roundReceiver) deliver(ctx context.Context, s snapshot, at time.Time) error {
	var metricErr, logErr error
	if r.metrics != nil {
		m := projectMetrics(r.cfg, s, r.settings.BuildInfo.Version, at)
		if m.DataPointCount() > 0 && r.metrics.ConsumeMetrics(ctx, m) != nil {
			metricErr = errors.New("Jolpica metrics delivery failed")
		}
	}
	if r.logs != nil {
		l := projectLogs(r.cfg, s, r.settings.BuildInfo.Version, at)
		if l.LogRecordCount() > 0 && r.logs.ConsumeLogs(ctx, l) != nil {
			logErr = errors.New("Jolpica logs delivery failed")
		}
	}
	return errors.Join(metricErr, logErr)
}
