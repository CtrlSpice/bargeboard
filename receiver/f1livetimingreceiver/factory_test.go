package f1livetimingreceiver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

func TestFactorySharesReceiverAcrossSignals(t *testing.T) {
	var preflights atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodOptions {
			t.Errorf("request method = %s, want OPTIONS", request.Method)
		}
		preflights.Add(1)
		http.SetCookie(writer, &http.Cookie{Name: affinityCookieName, Value: "affinity-token"})
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	factory := NewFactory()
	config := factory.CreateDefaultConfig().(*Config)
	config.Auth.TokenFile = filepath.Join(t.TempDir(), "f1tv-token")
	config.NegotiateEndpoint = server.URL
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
	if err := os.WriteFile(config.Auth.TokenFile, []byte("subscription-token"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := traces.Start(ctx, host); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := metrics.Start(ctx, host); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if got := preflights.Load(); got != 1 {
		t.Fatalf("preflight count = %d, want 1", got)
	}
	if err := logs.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if shared.receiver.credentials != nil {
		t.Fatal("credentials retained after shutdown")
	}

	recreated, err := factory.CreateLogs(ctx, settings, config, next)
	if err != nil {
		t.Fatalf("CreateLogs() after shutdown error = %v", err)
	}
	if recreated.(*sharedReceiver) == shared {
		t.Fatal("factory reused a shutdown receiver")
	}
	if err := recreated.Start(ctx, host); err != nil {
		t.Fatalf("recreated Start() error = %v", err)
	}
	if got := preflights.Load(); got != 2 {
		t.Fatalf("preflight count after recreated Start() = %d, want 2", got)
	}
}
