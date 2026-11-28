package jolpicareceiver

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

var Type = component.MustNewType("jolpica")

func NewFactory() receiver.Factory {
	var mu sync.Mutex
	instances := map[*Config]*roundReceiver{}
	// Signal pipelines are assembled before the Collector starts components.
	register := func(settings receiver.Settings, config component.Config, attach func(*roundReceiver)) (component.Component, error) {
		cfg, ok := config.(*Config)
		if !ok || cfg == nil {
			return nil, errors.New("invalid Jolpica config")
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		r := instances[cfg]
		if r == nil {
			r = newReceiver(*cfg, settings)
			instances[cfg] = r
			r.remove = func() {
				mu.Lock()
				defer mu.Unlock()
				if instances[cfg] == r {
					delete(instances, cfg)
				}
			}
		}
		attach(r)
		return r, nil
	}
	return receiver.NewFactory(Type, createDefaultConfig,
		receiver.WithMetrics(func(_ context.Context, s receiver.Settings, c component.Config, next consumer.Metrics) (receiver.Metrics, error) {
			return register(s, c, func(r *roundReceiver) { r.metrics = next })
		}, component.StabilityLevelDevelopment),
		receiver.WithLogs(func(_ context.Context, s receiver.Settings, c component.Config, next consumer.Logs) (receiver.Logs, error) {
			return register(s, c, func(r *roundReceiver) { r.logs = next })
		}, component.StabilityLevelDevelopment))
}
