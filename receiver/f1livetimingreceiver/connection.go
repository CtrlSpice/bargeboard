package f1livetimingreceiver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"github.com/coder/websocket"
)

const (
	maxNegotiateResponseSize = 64 * 1024
	maxHandshakeResponseSize = 16 * 1024
	recordSeparator          = byte(0x1e)
	handshakeRequest         = "{\"protocol\":\"json\",\"version\":1}\x1e"
	handshakeTimeout         = 15 * time.Second
)

type negotiation struct {
	connectionToken string
}

func (negotiation) String() string {
	return "SignalR negotiation [REDACTED]"
}

func (negotiation) GoString() string {
	return "SignalR negotiation [REDACTED]"
}

type negotiateResponse struct {
	ConnectionID        string `json:"connectionId"`
	ConnectionToken     string `json:"connectionToken"`
	NegotiateVersion    int    `json:"negotiateVersion"`
	URL                 string `json:"url"`
	AccessToken         string `json:"accessToken"`
	Error               string `json:"error"`
	AvailableTransports []struct {
		Transport       string   `json:"transport"`
		TransferFormats []string `json:"transferFormats"`
	} `json:"availableTransports"`
}

type signalRConnection struct {
	conn    *websocket.Conn
	pending []byte
}

func connectSignalR(ctx context.Context, client *http.Client, cfg *Config) (*signalRConnection, error) {
	credentials, err := bootstrapConnection(ctx, client, cfg)
	if err != nil {
		return nil, err
	}

	negotiateCookies, err := cookiesForEndpoint(
		cfg.NegotiateEndpoint,
		cfg.NegotiateEndpoint,
		credentials.affinityCookie,
		nil,
	)
	if err != nil {
		return nil, err
	}
	if len(negotiateCookies) == 0 {
		return nil, fmt.Errorf("SignalR affinity cookie does not apply to the negotiation endpoint")
	}

	negotiation, responseCookies, err := negotiate(
		ctx,
		client,
		cfg.NegotiateEndpoint,
		credentials,
		negotiateCookies,
	)
	if err != nil {
		return nil, err
	}
	endpoint, err := websocketEndpoint(cfg.Endpoint, negotiation.connectionToken)
	if err != nil {
		return nil, err
	}
	upgradeCookies, err := cookiesForEndpoint(
		cfg.NegotiateEndpoint,
		cfg.Endpoint,
		credentials.affinityCookie,
		responseCookies,
	)
	if err != nil {
		return nil, err
	}

	connection, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: credentials.headersWithCookies(upgradeCookies),
	})
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("SignalR WebSocket upgrade returned %s", response.Status)
		}
		return nil, fmt.Errorf("SignalR WebSocket upgrade failed")
	}

	handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	pending, err := exchangeHandshake(handshakeCtx, connection)
	if err != nil {
		_ = connection.CloseNow()
		return nil, err
	}
	return &signalRConnection{conn: connection, pending: pending}, nil
}

func negotiate(
	ctx context.Context,
	client *http.Client,
	rawEndpoint string,
	credentials connectionCredentials,
	cookies []*http.Cookie,
) (negotiation, []*http.Cookie, error) {
	endpoint, err := negotiateEndpoint(rawEndpoint)
	if err != nil {
		return negotiation{}, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return negotiation{}, nil, fmt.Errorf("create SignalR negotiation request: %w", err)
	}
	request.Header = credentials.headersWithCookies(cookies)
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return negotiation{}, nil, fmt.Errorf("perform SignalR negotiation: %w", err)
	}
	defer response.Body.Close()

	contents, err := io.ReadAll(io.LimitReader(response.Body, maxNegotiateResponseSize+1))
	if err != nil {
		return negotiation{}, nil, fmt.Errorf("read SignalR negotiation response: %w", err)
	}
	if len(contents) > maxNegotiateResponseSize {
		return negotiation{}, nil, fmt.Errorf("SignalR negotiation response exceeds %d bytes", maxNegotiateResponseSize)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return negotiation{}, nil, fmt.Errorf("SignalR negotiation returned %s", response.Status)
	}

	result, err := parseNegotiateResponse(contents)
	if err != nil {
		return negotiation{}, nil, err
	}
	return result, response.Cookies(), nil
}

func negotiateEndpoint(raw string) (string, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse SignalR negotiation endpoint: %w", err)
	}
	query := endpoint.Query()
	query.Set("negotiateVersion", "1")
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func parseNegotiateResponse(contents []byte) (negotiation, error) {
	var response negotiateResponse
	if err := json.Unmarshal(contents, &response); err != nil {
		return negotiation{}, fmt.Errorf("decode SignalR negotiation response: %w", err)
	}
	if response.Error != "" {
		return negotiation{}, fmt.Errorf("SignalR negotiation rejected the connection")
	}
	if response.URL != "" || response.AccessToken != "" {
		return negotiation{}, fmt.Errorf("SignalR negotiation redirects are not supported")
	}

	if response.NegotiateVersion < 0 || response.NegotiateVersion > 1 {
		return negotiation{}, fmt.Errorf("SignalR negotiation returned unsupported version %d", response.NegotiateVersion)
	}
	connectionToken := response.ConnectionID
	if response.NegotiateVersion >= 1 {
		if response.ConnectionID == "" {
			return negotiation{}, fmt.Errorf("SignalR negotiation did not return a connection ID")
		}
		connectionToken = response.ConnectionToken
	}
	if connectionToken == "" {
		return negotiation{}, fmt.Errorf("SignalR negotiation did not return a connection token")
	}

	for _, transport := range response.AvailableTransports {
		if transport.Transport != "WebSockets" {
			continue
		}
		for _, format := range transport.TransferFormats {
			if format == "Text" {
				return negotiation{connectionToken: connectionToken}, nil
			}
		}
	}
	return negotiation{}, fmt.Errorf("SignalR negotiation does not support WebSockets with text frames")
}

func cookiesForEndpoint(
	rawSource string,
	rawTarget string,
	initial *http.Cookie,
	response []*http.Cookie,
) ([]*http.Cookie, error) {
	source, err := httpEndpoint(rawSource)
	if err != nil {
		return nil, fmt.Errorf("parse SignalR cookie source: %w", err)
	}
	target, err := httpEndpoint(rawTarget)
	if err != nil {
		return nil, fmt.Errorf("parse SignalR cookie target: %w", err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create SignalR cookie jar: %w", err)
	}
	jar.SetCookies(source, cookiesAllowedByScheme(target, []*http.Cookie{initial}))
	jar.SetCookies(source, cookiesAllowedByScheme(target, response))
	return jar.Cookies(target), nil
}

func cookiesAllowedByScheme(target *url.URL, cookies []*http.Cookie) []*http.Cookie {
	if target.Scheme == "https" {
		return cookies
	}
	allowed := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie != nil && !cookie.Secure {
			allowed = append(allowed, cookie)
		}
	}
	return allowed
}

func httpEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	switch endpoint.Scheme {
	case "ws":
		endpoint.Scheme = "http"
	case "wss":
		endpoint.Scheme = "https"
	case "http", "https":
	default:
		return nil, fmt.Errorf("unsupported scheme %q", endpoint.Scheme)
	}
	return endpoint, nil
}

func websocketEndpoint(raw, connectionToken string) (string, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse SignalR WebSocket endpoint: %w", err)
	}
	if endpoint.Scheme != "ws" && endpoint.Scheme != "wss" {
		return "", fmt.Errorf("SignalR WebSocket endpoint uses unsupported scheme %q", endpoint.Scheme)
	}
	query := endpoint.Query()
	query.Set("id", connectionToken)
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func exchangeHandshake(ctx context.Context, connection *websocket.Conn) ([]byte, error) {
	if err := connection.Write(ctx, websocket.MessageText, encodeHandshakeRequest()); err != nil {
		return nil, fmt.Errorf("write SignalR handshake: %w", err)
	}

	var buffered []byte
	for {
		messageType, contents, err := connection.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("read SignalR handshake: %w", err)
		}
		if messageType != websocket.MessageText {
			return nil, fmt.Errorf("SignalR handshake used a non-text WebSocket message")
		}
		buffered = append(buffered, contents...)

		record, remaining, complete := splitFirstRecord(buffered)
		if !complete {
			if len(buffered) > maxHandshakeResponseSize {
				return nil, fmt.Errorf("SignalR handshake response exceeds %d bytes", maxHandshakeResponseSize)
			}
			continue
		}
		if len(record) > maxHandshakeResponseSize {
			return nil, fmt.Errorf("SignalR handshake response exceeds %d bytes", maxHandshakeResponseSize)
		}
		if err := parseHandshakeResponse(record); err != nil {
			return nil, err
		}
		return remaining, nil
	}
}

func encodeHandshakeRequest() []byte {
	return []byte(handshakeRequest)
}

func splitFirstRecord(contents []byte) (record, remaining []byte, complete bool) {
	separator := bytes.IndexByte(contents, recordSeparator)
	if separator == -1 {
		return nil, contents, false
	}
	return contents[:separator], contents[separator+1:], true
}

func parseHandshakeResponse(record []byte) error {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(record, &response); err != nil || response == nil {
		return fmt.Errorf("decode SignalR handshake response")
	}
	if _, ok := response["type"]; ok {
		return fmt.Errorf("expected a SignalR handshake response")
	}
	encodedError, ok := response["error"]
	if !ok {
		return nil
	}
	var message string
	if err := json.Unmarshal(encodedError, &message); err != nil || message == "" {
		return fmt.Errorf("decode SignalR handshake error")
	}
	return fmt.Errorf("SignalR handshake rejected the connection")
}

func (c *signalRConnection) close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		_ = c.conn.CloseNow()
		return err
	}
	return c.conn.CloseNow()
}
