package f1staticreceiver

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

var Type = component.MustNewType("f1static")

func NewFactory() receiver.Factory {
	var mu sync.Mutex
	type instanceKey struct {
		cfg *Config
		id  component.ID
	}
	instances := map[instanceKey]*staticReceiver{}
	activeSessions := map[string]*staticReceiver{}
	// Signal pipelines are assembled before the Collector starts components.
	register := func(settings receiver.Settings, config component.Config, attach func(*staticReceiver)) (component.Component, error) {
		cfg, ok := config.(*Config)
		if !ok || cfg == nil {
			return nil, errors.New("invalid static replay config")
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		key := instanceKey{cfg, settings.ID}
		r := instances[key]
		if r == nil {
			if cfg.Enabled {
				if owner := activeSessions[cfg.SessionPath]; owner != nil {
					return nil, errors.New("duplicate static replay session_path")
				}
			}
			r = newReceiver(*cfg, settings)
			instances[key] = r
			if cfg.Enabled {
				activeSessions[cfg.SessionPath] = r
			}
			r.remove = func() {
				mu.Lock()
				defer mu.Unlock()
				if instances[key] == r {
					delete(instances, key)
				}
				if cfg.Enabled && activeSessions[cfg.SessionPath] == r {
					delete(activeSessions, cfg.SessionPath)
				}
			}
		}
		attach(r)
		return r, nil
	}
	return receiver.NewFactory(Type, func() component.Config { c := DefaultConfig(); return &c },
		receiver.WithTraces(func(_ context.Context, s receiver.Settings, c component.Config, next consumer.Traces) (receiver.Traces, error) {
			return register(s, c, func(r *staticReceiver) { r.traces = next })
		}, component.StabilityLevelDevelopment),
		receiver.WithMetrics(func(_ context.Context, s receiver.Settings, c component.Config, next consumer.Metrics) (receiver.Metrics, error) {
			return register(s, c, func(r *staticReceiver) { r.metrics = next })
		}, component.StabilityLevelDevelopment),
		receiver.WithLogs(func(_ context.Context, s receiver.Settings, c component.Config, next consumer.Logs) (receiver.Logs, error) {
			return register(s, c, func(r *staticReceiver) { r.logs = next })
		}, component.StabilityLevelDevelopment))
}
