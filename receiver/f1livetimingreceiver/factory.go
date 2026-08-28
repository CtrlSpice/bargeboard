package f1livetimingreceiver

import (
	"context"
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

var Type = component.MustNewType("f1livetiming")

func NewFactory() receiver.Factory {
	receivers := newReceiverMap()
	return receiver.NewFactory(
		Type,
		createDefaultConfig,
		receiver.WithTraces(receivers.createTraces, component.StabilityLevelDevelopment),
		receiver.WithMetrics(receivers.createMetrics, component.StabilityLevelDevelopment),
		receiver.WithLogs(receivers.createLogs, component.StabilityLevelDevelopment),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		Endpoint:          defaultEndpoint,
		NegotiateEndpoint: defaultNegotiateEndpoint,
	}
}

func (m *receiverMap) createTraces(
	_ context.Context,
	settings receiver.Settings,
	config component.Config,
	next consumer.Traces,
) (receiver.Traces, error) {
	shared, err := m.receiver(config, settings)
	if err != nil {
		return nil, err
	}
	shared.receiver.registerTraces(next)
	return shared, nil
}

func (m *receiverMap) createMetrics(
	_ context.Context,
	settings receiver.Settings,
	config component.Config,
	next consumer.Metrics,
) (receiver.Metrics, error) {
	shared, err := m.receiver(config, settings)
	if err != nil {
		return nil, err
	}
	shared.receiver.registerMetrics(next)
	return shared, nil
}

func (m *receiverMap) createLogs(
	_ context.Context,
	settings receiver.Settings,
	config component.Config,
	next consumer.Logs,
) (receiver.Logs, error) {
	shared, err := m.receiver(config, settings)
	if err != nil {
		return nil, err
	}
	shared.receiver.registerLogs(next)
	return shared, nil
}

func (m *receiverMap) receiver(config component.Config, settings receiver.Settings) (*sharedReceiver, error) {
	cfg, ok := config.(*Config)
	if !ok {
		return nil, fmt.Errorf("invalid config type %T", config)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return m.loadOrStore(cfg, settings), nil
}
