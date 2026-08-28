package f1livetimingreceiver

import (
	"context"
	"testing"

	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

func TestFactorySharesReceiverAcrossSignals(t *testing.T) {
	factory := NewFactory()
	config := factory.CreateDefaultConfig().(*Config)
	config.Auth.TokenFile = "/run/secrets/f1tv-token"
	settings := receivertest.NewNopSettings(Type)
	next := consumertest.NewNop()

	traces, err := factory.CreateTraces(context.Background(), settings, config, next)
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	metrics, err := factory.CreateMetrics(context.Background(), settings, config, next)
	if err != nil {
		t.Fatalf("CreateMetrics() error = %v", err)
	}
	logs, err := factory.CreateLogs(context.Background(), settings, config, next)
	if err != nil {
		t.Fatalf("CreateLogs() error = %v", err)
	}

	shared := traces.(*sharedReceiver)
	if metrics.(*sharedReceiver) != shared || logs.(*sharedReceiver) != shared {
		t.Fatal("factory created more than one receiver for the same config")
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	if err := traces.Start(ctx, host); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := metrics.Start(ctx, host); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if err := logs.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	recreated, err := factory.CreateLogs(ctx, settings, config, next)
	if err != nil {
		t.Fatalf("CreateLogs() after shutdown error = %v", err)
	}
	if recreated.(*sharedReceiver) == shared {
		t.Fatal("factory reused a shutdown receiver")
	}
}
