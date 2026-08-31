package f1livetimingreceiver

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

func TestFactorySharesReceiverAcrossSignals(t *testing.T) {
	var preflights atomic.Int32
	var negotiations atomic.Int32
	var webSockets atomic.Int32
	subscribed := make(chan struct{}, 2)
	wantSubscription, err := encodeSubscribeInvocation(subscriptionTopics())
	if err != nil {
		t.Fatalf("encodeSubscribeInvocation() error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodOptions:
			preflights.Add(1)
			http.SetCookie(writer, &http.Cookie{Name: affinityCookieName, Value: "initial-affinity-token"})
			writer.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodPost:
			negotiations.Add(1)
			if got := request.URL.Query().Get("negotiateVersion"); got != "1" {
				t.Errorf("negotiateVersion = %q, want 1", got)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer subscription-token" {
				t.Errorf("negotiation Authorization header = %q", got)
			}
			if got := request.Header.Get("Cookie"); got != affinityCookieName+"=initial-affinity-token" {
				t.Errorf("negotiation Cookie header = %q", got)
			}
			http.SetCookie(writer, &http.Cookie{Name: affinityCookieName, Value: "refreshed-affinity-token"})
			http.SetCookie(writer, &http.Cookie{Name: "AWSALB", Value: "load-balancer-token"})
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{
				"connectionId":"public-id",
				"connectionToken":"connection-token",
				"negotiateVersion":1,
				"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]
			}`))
		case http.MethodGet:
			webSockets.Add(1)
			if got := request.URL.Query().Get("id"); got != "connection-token" {
				t.Errorf("WebSocket id = %q", got)
			}
			if got := request.URL.Query().Get("access_token"); got != "" {
				t.Errorf("WebSocket access_token query = %q, want empty", got)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer subscription-token" {
				t.Errorf("WebSocket Authorization header = %q", got)
			}
			cookie := request.Header.Get("Cookie")
			if !strings.Contains(cookie, affinityCookieName+"=refreshed-affinity-token") ||
				!strings.Contains(cookie, "AWSALB=load-balancer-token") {
				t.Errorf("WebSocket Cookie header = %q", cookie)
			}
			if strings.Contains(cookie, "initial-affinity-token") {
				t.Errorf("WebSocket Cookie header retained stale affinity: %q", cookie)
			}

			connection, err := websocket.Accept(writer, request, nil)
			if err != nil {
				t.Errorf("Accept() error = %v", err)
				return
			}
			defer connection.CloseNow()
			messageType, contents, err := connection.Read(context.Background())
			if err != nil {
				t.Errorf("Read() handshake error = %v", err)
				return
			}
			if messageType != websocket.MessageText || string(contents) != handshakeRequest {
				t.Errorf("handshake request = %q", contents)
				return
			}
			if err := connection.Write(context.Background(), websocket.MessageText, []byte("{")); err != nil {
				t.Errorf("Write() handshake error = %v", err)
				return
			}
			if err := connection.Write(
				context.Background(),
				websocket.MessageText,
				[]byte("}\x1e{\"type\":6}\x1e"),
			); err != nil {
				t.Errorf("Write() handshake completion error = %v", err)
				return
			}
			readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			messageType, contents, err = connection.Read(readCtx)
			if err != nil {
				t.Errorf("Read() subscription error = %v", err)
				return
			}
			if messageType != websocket.MessageText || !bytes.Equal(contents, wantSubscription) {
				t.Errorf("subscription request = %q", contents)
				return
			}
			subscribed <- struct{}{}
			_, _, _ = connection.Read(readCtx)
		default:
			t.Errorf("unexpected request method %s", request.Method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	factory := NewFactory()
	config := factory.CreateDefaultConfig().(*Config)
	config.Auth.TokenFile = filepath.Join(t.TempDir(), "f1tv-token")
	config.Endpoint = "ws" + strings.TrimPrefix(server.URL, "http") + "/signalrcore"
	config.NegotiateEndpoint = server.URL + "/signalrcore/negotiate"
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
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription")
	}
	if err := metrics.Start(ctx, host); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if got := preflights.Load(); got != 1 {
		t.Fatalf("preflight count = %d, want 1", got)
	}
	if got := negotiations.Load(); got != 1 {
		t.Fatalf("negotiation count = %d, want 1", got)
	}
	if got := webSockets.Load(); got != 1 {
		t.Fatalf("WebSocket count = %d, want 1", got)
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
	if err := recreated.Start(ctx, host); err != nil {
		t.Fatalf("recreated Start() error = %v", err)
	}
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recreated subscription")
	}
	if got := preflights.Load(); got != 2 {
		t.Fatalf("preflight count after recreated Start() = %d, want 2", got)
	}
	if got := negotiations.Load(); got != 2 {
		t.Fatalf("negotiation count after recreated Start() = %d, want 2", got)
	}
	if got := webSockets.Load(); got != 2 {
		t.Fatalf("WebSocket count after recreated Start() = %d, want 2", got)
	}
	if err := recreated.Shutdown(ctx); err != nil {
		t.Fatalf("recreated Shutdown() error = %v", err)
	}
}

func TestSharedReceiverReportsPermanentStatusToEverySignalHost(t *testing.T) {
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte(
			`{"type":3,"invocationId":"0","result":"invalid-snapshot"}`+"\x1e",
		))
	})

	factory := NewFactory()
	config := connectionTestConfig(t, server.URL)
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

	hosts := []*statusHost{
		{events: make(chan *componentstatus.Event, 1)},
		{events: make(chan *componentstatus.Event, 1)},
		{events: make(chan *componentstatus.Event, 1)},
	}
	if err := traces.Start(context.Background(), hosts[0]); err != nil {
		t.Fatalf("traces Start() error = %v", err)
	}
	shared := traces.(*sharedReceiver)
	select {
	case <-shared.receiver.done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shared receiver to stop")
	}
	if err := metrics.Start(context.Background(), hosts[1]); err != nil {
		t.Fatalf("metrics Start() error = %v", err)
	}
	if err := logs.Start(context.Background(), hosts[2]); err != nil {
		t.Fatalf("logs Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = logs.Shutdown(ctx)
	})

	for index, host := range hosts {
		select {
		case event := <-host.events:
			if event.Status() != componentstatus.StatusPermanentError {
				t.Errorf("host %d status = %s, want permanent error", index, event.Status())
			}
		case <-time.After(time.Second):
			t.Errorf("host %d did not receive permanent status", index)
		}
	}
}
