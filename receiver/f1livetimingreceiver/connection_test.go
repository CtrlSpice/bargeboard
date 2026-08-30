package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestParseNegotiateResponse(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
		wantErr  string
	}{
		{
			name: "version one",
			contents: `{
				"connectionId":"public-id",
				"connectionToken":"secret-token",
				"negotiateVersion":1,
				"availableTransports":[{"transport":"WebSockets","transferFormats":["Text","Binary"]}]
			}`,
			want: "secret-token",
		},
		{
			name: "version zero",
			contents: `{
				"connectionId":"connection-id",
				"negotiateVersion":0,
				"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]
			}`,
			want: "connection-id",
		},
		{
			name:     "server error",
			contents: `{"error":"not allowed"}`,
			wantErr:  "rejected",
		},
		{
			name:     "redirect",
			contents: `{"url":"https://example.test/hub","accessToken":"redirect-token"}`,
			wantErr:  "redirects",
		},
		{
			name: "missing connection token",
			contents: `{
				"connectionId":"public-id",
				"negotiateVersion":1,
				"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]
			}`,
			wantErr: "connection token",
		},
		{
			name: "invalid version",
			contents: `{
				"connectionId":"connection-id",
				"negotiateVersion":-1,
				"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]
			}`,
			wantErr: "unsupported version",
		},
		{
			name: "future version",
			contents: `{
				"connectionId":"public-id",
				"connectionToken":"secret-token",
				"negotiateVersion":2,
				"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]
			}`,
			wantErr: "unsupported version",
		},
		{
			name: "version one missing connection ID",
			contents: `{
				"connectionToken":"secret-token",
				"negotiateVersion":1,
				"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]
			}`,
			wantErr: "connection ID",
		},
		{
			name: "missing WebSocket transport",
			contents: `{
				"connectionId":"public-id",
				"connectionToken":"secret-token",
				"negotiateVersion":1,
				"availableTransports":[{"transport":"LongPolling","transferFormats":["Text"]}]
			}`,
			wantErr: "WebSockets",
		},
		{
			name: "binary only",
			contents: `{
				"connectionId":"public-id",
				"connectionToken":"secret-token",
				"negotiateVersion":1,
				"availableTransports":[{"transport":"WebSockets","transferFormats":["Binary"]}]
			}`,
			wantErr: "text frames",
		},
		{name: "malformed", contents: `{`, wantErr: "decode"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseNegotiateResponse([]byte(test.contents))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseNegotiateResponse() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNegotiateResponse() error = %v", err)
			}
			if got.connectionToken != test.want {
				t.Errorf("connection token = %q, want %q", got.connectionToken, test.want)
			}
		})
	}
}

func TestNegotiateEndpoint(t *testing.T) {
	got, err := negotiateEndpoint("https://example.test/negotiate?existing=value")
	if err != nil {
		t.Fatalf("negotiateEndpoint() error = %v", err)
	}
	if got != "https://example.test/negotiate?existing=value&negotiateVersion=1" {
		t.Errorf("negotiateEndpoint() = %q", got)
	}
}

func TestWebsocketEndpoint(t *testing.T) {
	got, err := websocketEndpoint("wss://example.test/hub?existing=value", "token /+=")
	if err != nil {
		t.Fatalf("websocketEndpoint() error = %v", err)
	}
	if got != "wss://example.test/hub?existing=value&id=token+%2F%2B%3D" {
		t.Errorf("websocketEndpoint() = %q", got)
	}
}

func TestCookiesForEndpoint(t *testing.T) {
	credentials := connectionCredentials{
		token: "subscription-token",
		affinityCookie: &http.Cookie{
			Name:   affinityCookieName,
			Value:  "first-affinity-token",
			Path:   "/signalrcore",
			Secure: true,
		},
	}
	cookies, err := cookiesForEndpoint(
		"https://example.test/signalrcore/negotiate",
		"wss://example.test/signalrcore",
		credentials.affinityCookie,
		[]*http.Cookie{
			{Name: "AWSALB", Value: "load-balancer-token", Path: "/signalrcore", Secure: true},
			{Name: affinityCookieName, Value: "refreshed-affinity-token", Path: "/signalrcore", Secure: true},
		},
	)
	if err != nil {
		t.Fatalf("cookiesForEndpoint() error = %v", err)
	}
	headers := credentials.headersWithCookies(cookies)

	if got := headers.Get("Authorization"); got != "Bearer subscription-token" {
		t.Errorf("Authorization header = %q", got)
	}
	if got := headers.Get("Cookie"); got != "AWSALBCORS=refreshed-affinity-token; AWSALB=load-balancer-token" {
		t.Errorf("Cookie header = %q", got)
	}
}

func TestCookiesForEndpointHonorsScopeAndDeletion(t *testing.T) {
	initial := &http.Cookie{Name: affinityCookieName, Value: "affinity-token", Path: "/signalrcore", Secure: true}
	tests := []struct {
		name     string
		target   string
		response []*http.Cookie
		want     int
	}{
		{name: "matching secure endpoint", target: "wss://example.test/signalrcore", want: 1},
		{name: "different host", target: "wss://other.test/signalrcore", want: 0},
		{name: "different path", target: "wss://example.test/other", want: 0},
		{name: "insecure endpoint", target: "ws://example.test/signalrcore", want: 0},
		{
			name:   "deleted cookie",
			target: "wss://example.test/signalrcore",
			response: []*http.Cookie{
				{Name: affinityCookieName, Path: "/signalrcore", MaxAge: -1},
			},
			want: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := cookiesForEndpoint(
				"https://example.test/signalrcore/negotiate",
				test.target,
				initial,
				test.response,
			)
			if err != nil {
				t.Fatalf("cookiesForEndpoint() error = %v", err)
			}
			if len(got) != test.want {
				t.Errorf("cookiesForEndpoint() returned %d cookies, want %d", len(got), test.want)
			}
		})
	}
}

func TestSplitFirstRecord(t *testing.T) {
	record, remaining, complete := splitFirstRecord([]byte("{}\x1e{\"type\":6}\x1e"))
	if !complete {
		t.Fatal("splitFirstRecord() did not find complete record")
	}
	if string(record) != "{}" || string(remaining) != "{\"type\":6}\x1e" {
		t.Errorf("splitFirstRecord() = %q, %q", record, remaining)
	}

	_, remaining, complete = splitFirstRecord([]byte("{"))
	if complete || string(remaining) != "{" {
		t.Errorf("splitFirstRecord(incomplete) remaining = %q, complete = %v", remaining, complete)
	}
}

func TestEncodeHandshakeRequest(t *testing.T) {
	first := encodeHandshakeRequest()
	if string(first) != handshakeRequest {
		t.Fatalf("encodeHandshakeRequest() = %q", first)
	}
	first[0] = 'x'
	if got := string(encodeHandshakeRequest()); got != handshakeRequest {
		t.Errorf("second encodeHandshakeRequest() = %q", got)
	}
}

func TestParseHandshakeResponse(t *testing.T) {
	tests := []struct {
		name    string
		record  string
		wantErr string
	}{
		{name: "success", record: `{}`},
		{name: "future success field", record: `{"extension":true}`},
		{name: "hub message", record: `{"type":6}`, wantErr: "expected"},
		{name: "server error", record: `{"error":"unsupported protocol"}`, wantErr: "rejected"},
		{name: "empty error", record: `{"error":""}`, wantErr: "decode"},
		{name: "malformed", record: `{`, wantErr: "decode"},
		{name: "null", record: `null`, wantErr: "decode"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := parseHandshakeResponse([]byte(test.record))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("parseHandshakeResponse() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("parseHandshakeResponse() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestSensitiveConnectionValuesAreRedacted(t *testing.T) {
	values := []any{
		negotiation{connectionToken: "secret-connection-token"},
		connectionCredentials{
			token:          "secret-subscription-token",
			affinityCookie: &http.Cookie{Name: affinityCookieName, Value: "secret-cookie"},
		},
	}
	for _, value := range values {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(format, value); strings.Contains(got, "secret") {
				t.Errorf("Sprintf(%q) exposed credentials: %q", format, got)
			}
		}
	}
}

func TestConnectSignalRHonorsHandshakeContext(t *testing.T) {
	server := newConnectionTestServer(t, func(*websocket.Conn) {
		time.Sleep(250 * time.Millisecond)
	})
	cfg := connectionTestConfig(t, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := connectSignalR(ctx, server.Client(), cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connectSignalR() error = %v, want deadline exceeded", err)
	}
}

func TestConnectSignalRRejectsInapplicableAffinityCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodOptions {
			t.Errorf("unexpected request method %s", request.Method)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		http.SetCookie(writer, &http.Cookie{
			Name:   affinityCookieName,
			Value:  "secure-affinity-token",
			Path:   "/signalrcore",
			Secure: true,
		})
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}))
	t.Cleanup(server.Close)

	_, err := connectSignalR(context.Background(), server.Client(), connectionTestConfig(t, server.URL))
	if err == nil || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("connectSignalR() error = %v, want inapplicable cookie error", err)
	}
}

func TestSignalRConnectionCloseHonorsContext(t *testing.T) {
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		_, _, _ = connection.Read(context.Background())
		_ = connection.Write(context.Background(), websocket.MessageText, []byte("{}\x1e"))
		time.Sleep(250 * time.Millisecond)
	})
	connection, err := connectSignalR(context.Background(), server.Client(), connectionTestConfig(t, server.URL))
	if err != nil {
		t.Fatalf("connectSignalR() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := connection.close(ctx); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("close() took %s, want less than context deadline", elapsed)
	}
}

func TestSignalRConnectionCloseHonorsCancelledContext(t *testing.T) {
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		_, _, _ = connection.Read(context.Background())
		_ = connection.Write(context.Background(), websocket.MessageText, []byte("{}\x1e"))
		time.Sleep(250 * time.Millisecond)
	})
	connection, err := connectSignalR(context.Background(), server.Client(), connectionTestConfig(t, server.URL))
	if err != nil {
		t.Fatalf("connectSignalR() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := connection.close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close() error = %v, want context canceled", err)
	}
}

func newConnectionTestServer(t *testing.T, connected func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodOptions:
			http.SetCookie(writer, &http.Cookie{Name: affinityCookieName, Value: "affinity-token"})
			writer.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodPost:
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{
				"connectionId":"public-id",
				"connectionToken":"connection-token",
				"negotiateVersion":1,
				"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]
			}`))
		case http.MethodGet:
			connection, err := websocket.Accept(writer, request, nil)
			if err != nil {
				t.Errorf("Accept() error = %v", err)
				return
			}
			defer connection.CloseNow()
			connected(connection)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func connectionTestConfig(t *testing.T, serverURL string) *Config {
	t.Helper()
	cfg := createDefaultConfig().(*Config)
	cfg.Endpoint = "ws" + strings.TrimPrefix(serverURL, "http") + "/signalrcore"
	cfg.NegotiateEndpoint = serverURL + "/signalrcore/negotiate"
	cfg.Auth.TokenFile = writeTokenFile(t, "subscription-token")
	return cfg
}
