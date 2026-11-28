package openf1receiver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
)

// HTTPAdapter uses a fresh HTTP/1 connection for each attempt. This prevents
// net/http from replaying GET on a previously used connection. Direct
// RoundTrip avoids client redirects, cookies, and other implicit requests.
type HTTPAdapter struct {
	endpoint  string
	transport http.RoundTripper
}

func NewHTTPAdapter(endpoint string) (*HTTPAdapter, error) {
	if err := f1id.ValidateHTTPSOrLoopbackEndpoint("endpoint", endpoint); err != nil {
		return nil, err
	}
	return &HTTPAdapter{endpoint, f1id.NewRestrictedHTTPTransport()}, nil
}
func (h *HTTPAdapter) Fetch(ctx context.Context, q Request) Response {
	if (q.Stage != sessions && q.Stage != drivers && q.Stage != results) || q.RouteKey <= 0 {
		return Response{Failure: DiagIdentity}
	}
	timeout := 10 * time.Second
	if !q.Deadline.IsZero() {
		timeout = min(timeout, time.Until(q.Deadline))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", h.endpoint+"/"+q.Stage+"?session_key="+strconv.FormatInt(q.RouteKey, 10), nil)
	if err != nil {
		return Response{Failure: DiagTransport, Err: f1id.SanitizedHTTPError(ctx, "openf1 request", err)}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	r := Response{StartedAt: time.Now()}
	resp, err := h.transport.RoundTrip(req)
	if err != nil {
		r.Failure = DiagTransport
		r.Err = f1id.SanitizedHTTPError(ctx, "openf1 request", err)
		return r
	}
	defer resp.Body.Close()
	r.Status = resp.StatusCode
	if r.Status == 429 {
		if vs := resp.Header.Values("Retry-After"); len(vs) == 1 {
			r.RetryAfter = vs[0]
		}
	}
	if r.Status == 200 {
		if vs := resp.Header.Values("Content-Encoding"); len(vs) > 0 && vs[0] == "" {
			r.Failure = DiagDecode
			r.Err = f1id.SanitizedHTTPError(ctx, "openf1 request", f1id.ErrInvalidContentEncoding)
			return r
		}
		r.Body, err = f1id.ReadBoundedJSONBodyContext(ctx, resp, maxResponseBytes)
		r.Err = f1id.SanitizedHTTPError(ctx, "openf1 request", err)
		switch err {
		case nil:
		case f1id.ErrResponseTooLarge:
			r.Failure = DiagResponseSize
		case f1id.ErrBodyRead, context.Canceled, context.DeadlineExceeded:
			r.Failure = DiagTransport
		default:
			r.Failure = DiagDecode
		}
	}
	return r
}

var Type = component.MustNewType("openf1")

func NewFactory() receiver.Factory {
	return receiver.NewFactory(Type, func() component.Config {
		return &Config{Endpoint: DefaultEndpoint, FinalisedGrace: DefaultFinalisedGrace}
	}, receiver.WithLogs(createLogs, component.StabilityLevelDevelopment))
}
func createLogs(_ context.Context, set receiver.Settings, config component.Config, next consumer.Logs) (receiver.Logs, error) {
	cfg, ok := config.(*Config)
	if !ok || cfg == nil {
		return nil, errors.New("invalid openf1 config")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	h, err := NewHTTPAdapter(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	return &resultReceiver{cfg: *cfg, http: h, next: next, set: set, queue: make(chan action, 4), done: make(chan struct{})}, nil
}

type action struct {
	kind  string
	token Epoch
	q     Request
	r     Response
	at    time.Time
}
type resultReceiver struct {
	cfg               Config
	http              *HTTPAdapter
	next              consumer.Logs
	set               receiver.Settings
	queue             chan action
	done              chan struct{}
	mu                sync.Mutex
	cancelDelivery    context.CancelFunc
	started, stopping bool
}

func (r *resultReceiver) Start(ctx context.Context, _ component.Host) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping {
		return errors.New("openf1 receiver is stopping")
	}
	if !r.started && r.cfg.Enabled {
		r.started = true
		runCtx := context.WithoutCancel(ctx)
		var delivery context.Context
		delivery, r.cancelDelivery = context.WithCancel(runCtx)
		go r.run(runCtx, delivery)
	}
	return nil
}
func (r *resultReceiver) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if !r.stopping {
		r.stopping = true
		if r.started {
			r.cancelDelivery()
			r.queue <- action{kind: "stop"}
		} else {
			close(r.done)
		}
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *resultReceiver) run(ctx, delivery context.Context) {
	defer close(r.done)
	var s AdapterState
	c := r.cfg
	s, _ = s.Update(Session{Identity: c.identity(), RouteKey: c.SessionKey, Enabled: true, Terminal: true, Drivers: c.Drivers, DiscoverRoster: len(c.Drivers) == 0}, time.Now())
	var workers sync.WaitGroup
	var cancel context.CancelFunc
	var timer *time.Timer
	schedule := func() {
		if at, ok := s.Next(); ok {
			token := s.Epoch
			timer = time.AfterFunc(time.Until(at), func() { r.queue <- action{kind: "timer", token: token} })
		}
	}
	schedule()
	for a := range r.queue {
		switch a.kind {
		case "timer":
			var q Request
			var ok bool
			s, q, ok = s.Dispatch(a.token, time.Now())
			if !ok {
				continue
			}
			reqCtx, release := context.WithCancel(ctx)
			cancel = release
			workers.Go(func() {
				defer release()
				resp := r.http.Fetch(reqCtx, q)
				r.queue <- action{kind: "response", q: q, r: resp, at: time.Now()}
			})
		case "response":
			cancel = nil
			var effects Effects
			s, effects = s.Complete(a.q, a.r, a.at, a.at)
			for _, d := range effects.Diagnostics {
				if r.set.Logger != nil {
					r.set.Logger.Warn("OpenF1 observation", zap.String("category", string(d)))
				}
			}
			if len(effects.Observation.Changes) > 0 {
				logs := effects.Observation.Logs(r.set.BuildInfo.Version, c.hasRoot)
				_ = r.next.ConsumeLogs(delivery, logs)
			}
			schedule()
		case "stop":
			s, _ = s.BeginShutdown()
			if timer != nil {
				timer.Stop()
			}
			if cancel != nil {
				cancel()
			}
			go func() { workers.Wait(); r.queue <- action{kind: "barrier"} }()
		case "barrier":
			s, _ = s.Barrier()
			return
		}
	}
}
