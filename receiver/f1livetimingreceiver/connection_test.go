package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"io"
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
				if !errors.Is(err, errInvalidLiveTimingData) {
					t.Errorf("parseNegotiateResponse() error does not wrap errInvalidLiveTimingData")
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
			if !errors.Is(err, errInvalidLiveTimingData) {
				t.Errorf("parseHandshakeResponse() error does not wrap errInvalidLiveTimingData")
			}
		})
	}
}

func TestInvalidWebSocketRead(t *testing.T) {
	if !invalidWebSocketRead(fmt.Errorf("read limit: %w", websocket.ErrMessageTooBig)) {
		t.Error("invalidWebSocketRead() did not recognize wrapped ErrMessageTooBig")
	}

	tests := []struct {
		status websocket.StatusCode
		want   bool
	}{
		{status: websocket.StatusProtocolError, want: true},
		{status: websocket.StatusUnsupportedData, want: true},
		{status: websocket.StatusInvalidFramePayloadData, want: true},
		{status: websocket.StatusMessageTooBig, want: true},
		{status: websocket.StatusGoingAway, want: false},
		{status: websocket.StatusServiceRestart, want: false},
	}

	for _, test := range tests {
		err := websocket.CloseError{Code: test.status, Reason: "sensitive-reason"}
		if got := invalidWebSocketRead(err); got != test.want {
			t.Errorf("invalidWebSocketRead(%s) = %t, want %t", test.status, got, test.want)
		}
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

func TestHTTPTransportErrorsAreSanitized(t *testing.T) {
	const sensitive = "secret-route-or-transport"
	endpoint := "https://example.test/" + sensitive
	credentials := connectionCredentials{token: "subscription-token"}

	tests := []struct {
		name    string
		client  *http.Client
		request func(*http.Client) error
		want    string
	}{
		{
			name: "preflight request",
			client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New(sensitive)
			})},
			request: func(client *http.Client) error {
				cfg := createDefaultConfig().(*Config)
				cfg.Auth.TokenFile = writeTokenFile(t, "subscription-token")
				cfg.NegotiateEndpoint = endpoint
				_, err := bootstrapConnection(context.Background(), client, cfg)
				return err
			},
			want: "perform negotiation preflight failed",
		},
		{
			name: "negotiation request",
			client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New(sensitive)
			})},
			request: func(client *http.Client) error {
				_, _, err := negotiate(context.Background(), client, endpoint, credentials, nil)
				return err
			},
			want: "perform SignalR negotiation failed",
		},
		{
			name: "negotiation response body",
			client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       failingReadCloser{err: errors.New(sensitive)},
					Header:     make(http.Header),
				}, nil
			})},
			request: func(client *http.Client) error {
				_, _, err := negotiate(context.Background(), client, endpoint, credentials, nil)
				return err
			},
			want: "read SignalR negotiation response failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.request(test.client)
			if err == nil || err.Error() != test.want {
				t.Fatalf("request error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), sensitive) {
				t.Errorf("request error exposed sensitive endpoint or transport data: %q", err)
			}
		})
	}
}

func TestSanitizedTransportErrorPreservesContextFailure(t *testing.T) {
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name         string
		ctx          context.Context
		transportErr error
		want         error
	}{
		{
			name:         "caller cancellation",
			ctx:          cancelledCtx,
			transportErr: errors.New("sensitive transport error"),
			want:         context.Canceled,
		},
		{
			name:         "client timeout",
			ctx:          context.Background(),
			transportErr: fmt.Errorf("sensitive transport error: %w", context.DeadlineExceeded),
			want:         context.DeadlineExceeded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := sanitizedTransportError(test.ctx, "perform request", test.transportErr)
			if !errors.Is(err, test.want) {
				t.Fatalf("sanitizedTransportError() = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Errorf("sanitizedTransportError() exposed transport data: %q", err)
			}
		})
	}
}

func TestConnectSignalRHonorsWebSocketDialCancellation(t *testing.T) {
	dialStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodOptions:
			http.SetCookie(writer, &http.Cookie{Name: affinityCookieName, Value: "affinity-token"})
			writer.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodPost:
			_, _ = writer.Write([]byte(`{
				"connectionId":"public-id",
				"connectionToken":"connection-token",
				"negotiateVersion":1,
				"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]
			}`))
		case http.MethodGet:
			close(dialStarted)
			<-request.Context().Done()
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	cfg := connectionTestConfig(t, server.URL+"/sensitive-path")
	go func() {
		_, err := connectSignalR(ctx, server.Client(), cfg)
		result <- err
	}()

	select {
	case <-dialStarted:
		cancel()
	case err := <-result:
		cancel()
		t.Fatalf("connectSignalR() returned before WebSocket dial: %v", err)
	case <-time.After(time.Second):
		cancel()
		t.Fatal("connectSignalR() did not reach WebSocket dial")
	}

	var err error
	select {
	case err = <-result:
	case <-time.After(time.Second):
		t.Fatal("connectSignalR() did not return after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("connectSignalR() error = %v, want context canceled", err)
	}
	if strings.Contains(err.Error(), "sensitive-path") {
		t.Errorf("connectSignalR() error exposed endpoint path: %q", err)
	}
}

func TestBootstrapConnectionPreservesHTTPClientTimeout(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)

	client := server.Client()
	client.Timeout = 25 * time.Millisecond
	cfg := createDefaultConfig().(*Config)
	cfg.Auth.TokenFile = writeTokenFile(t, "subscription-token")
	cfg.NegotiateEndpoint = server.URL + "/sensitive-path"

	_, err := bootstrapConnection(context.Background(), client, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bootstrapConnection() error = %v, want deadline exceeded", err)
	}
	select {
	case <-requestStarted:
	default:
		t.Fatal("bootstrapConnection() did not start HTTP request")
	}
	if strings.Contains(err.Error(), "sensitive-path") {
		t.Errorf("bootstrapConnection() error exposed endpoint path: %q", err)
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

func TestSignalRConnectionRejectsDuplicateSubscriptionCompletion(t *testing.T) {
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte(
			`{"type":3,"invocationId":"0","result":{}}`+"\x1e"+
				`{"type":3,"invocationId":"0","result":{}}`+"\x1e",
		))
	})
	connection, err := connectSignalR(context.Background(), server.Client(), connectionTestConfig(t, server.URL))
	if err != nil {
		t.Fatalf("connectSignalR() error = %v", err)
	}
	defer connection.close(context.Background())
	if err := connection.subscribe(context.Background()); err != nil {
		t.Fatalf("subscribe() error = %v", err)
	}

	consumed := 0
	err = connection.read(context.Background(), func(_ context.Context, batch liveTimingBatch) error {
		consumed++
		if batch.source != liveTimingUpdateSourceSnapshot {
			t.Errorf("batch source = %d, want snapshot", batch.source)
		}
		return nil
	})
	if !errors.Is(err, errInvalidLiveTimingData) {
		t.Fatalf("read() error = %v, want invalid live timing data", err)
	}
	if consumed != 1 {
		t.Errorf("consumed batches = %d, want 1", consumed)
	}
}

func TestSignalRConnectionRejectsMessageOverReadLimit(t *testing.T) {
	server := newConnectionTestServer(t, func(connection *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte("{}\x1e"))
		_, _, _ = connection.Read(ctx)
		_ = connection.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", 17)))
	})
	connection, err := connectSignalR(context.Background(), server.Client(), connectionTestConfig(t, server.URL))
	if err != nil {
		t.Fatalf("connectSignalR() error = %v", err)
	}
	defer connection.close(context.Background())
	connection.conn.SetReadLimit(16)
	if err := connection.subscribe(context.Background()); err != nil {
		t.Fatalf("subscribe() error = %v", err)
	}

	err = connection.read(context.Background(), func(context.Context, liveTimingBatch) error { return nil })
	if !errors.Is(err, errInvalidLiveTimingData) {
		t.Fatalf("read() error = %v, want invalid live timing data", err)
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
	if !errors.Is(err, errInvalidLiveTimingData) {
		t.Errorf("connectSignalR() error does not wrap errInvalidLiveTimingData")
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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type failingReadCloser struct {
	err error
}

func (reader failingReadCloser) Read([]byte) (int, error) {
	return 0, reader.err
}

func (failingReadCloser) Close() error {
	return nil
}

var _ io.ReadCloser = failingReadCloser{}
