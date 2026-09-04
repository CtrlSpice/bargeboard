package f1livetimingreceiver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
	"unicode/utf8"

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
	conn            *websocket.Conn
	pending         []byte
	requestedTopics []string
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
	if !hasAffinityCookie(negotiateCookies) {
		return nil, invalidLiveTimingData("SignalR affinity cookie does not apply to the negotiation endpoint")
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
	if !hasAffinityCookie(upgradeCookies) {
		return nil, invalidLiveTimingData("SignalR affinity cookie does not apply to the WebSocket endpoint")
	}

	connection, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: credentials.headersWithCookies(upgradeCookies),
	})
	if err != nil {
		if response != nil {
			if response.StatusCode == http.StatusSwitchingProtocols {
				return nil, invalidLiveTimingData("SignalR WebSocket upgrade response is invalid")
			}
			return nil, fmt.Errorf("SignalR WebSocket upgrade returned HTTP %d", response.StatusCode)
		}
		return nil, sanitizedTransportError(ctx, "SignalR WebSocket upgrade", err)
	}
	connection.SetReadLimit(maxWebSocketMessage)

	handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	pending, err := exchangeHandshake(handshakeCtx, connection)
	if err != nil {
		_ = connection.CloseNow()
		return nil, err
	}
	return &signalRConnection{conn: connection, pending: pending}, nil
}

func hasAffinityCookie(cookies []*http.Cookie) bool {
	for _, cookie := range cookies {
		if cookie != nil && cookie.Name == affinityCookieName && cookie.Value != "" {
			return true
		}
	}
	return false
}

func (c *signalRConnection) subscribe(ctx context.Context) error {
	topics := subscriptionTopics()
	message, err := encodeSubscribeInvocation(topics)
	if err != nil {
		return err
	}
	if err := c.conn.Write(ctx, websocket.MessageText, message); err != nil {
		return fmt.Errorf("write F1 topic subscription: %w", err)
	}
	c.requestedTopics = append([]string(nil), topics...)
	return nil
}

func (c *signalRConnection) read(
	ctx context.Context,
	consume func(context.Context, liveTimingBatch) error,
) error {
	buffered := c.pending
	c.pending = nil

	for {
		records, remaining, err := splitHubRecords(buffered)
		if err != nil {
			return err
		}
		buffered = remaining
		for _, record := range records {
			batch, err := decodeHubRecord(record, c.requestedTopics)
			if err != nil {
				return err
			}
			if batch != nil {
				if batch.source == liveTimingUpdateSourceSnapshot {
					c.requestedTopics = nil
				}
				if err := consume(ctx, *batch); err != nil {
					return fmt.Errorf("consume F1 live timing batch: %w", err)
				}
			}
		}

		messageType, contents, err := c.conn.Read(ctx)
		if err != nil {
			if invalidWebSocketRead(err) {
				return invalidLiveTimingData("F1 live timing WebSocket data is invalid")
			}
			return fmt.Errorf("read F1 live timing message: %w", err)
		}
		if messageType != websocket.MessageText {
			return invalidLiveTimingData("F1 live timing used a non-text WebSocket message")
		}
		buffered = append(buffered, contents...)
	}
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
		return negotiation{}, nil, sanitizedTransportError(ctx, "perform SignalR negotiation", err)
	}
	defer response.Body.Close()

	contents, err := io.ReadAll(io.LimitReader(response.Body, maxNegotiateResponseSize+1))
	if err != nil {
		return negotiation{}, nil, sanitizedTransportError(ctx, "read SignalR negotiation response", err)
	}
	if len(contents) > maxNegotiateResponseSize {
		return negotiation{}, nil, invalidLiveTimingData(
			fmt.Sprintf("SignalR negotiation response exceeds %d bytes", maxNegotiateResponseSize),
		)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return negotiation{}, nil, fmt.Errorf("SignalR negotiation returned HTTP %d", response.StatusCode)
	}

	result, err := parseNegotiateResponse(contents)
	if err != nil {
		return negotiation{}, nil, err
	}
	return result, response.Cookies(), nil
}

func sanitizedTransportError(ctx context.Context, operation string, transportErr error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if errors.Is(transportErr, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(transportErr, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s failed", operation)
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
	if !utf8.Valid(contents) {
		return negotiation{}, invalidLiveTimingData("SignalR negotiation response is not UTF-8")
	}
	var response negotiateResponse
	if err := json.Unmarshal(contents, &response); err != nil {
		return negotiation{}, invalidLiveTimingData("decode SignalR negotiation response")
	}
	if response.Error != "" {
		return negotiation{}, invalidLiveTimingData("SignalR negotiation rejected the connection")
	}
	if response.URL != "" || response.AccessToken != "" {
		return negotiation{}, invalidLiveTimingData("SignalR negotiation redirects are not supported")
	}

	if response.NegotiateVersion < 0 || response.NegotiateVersion > 1 {
		return negotiation{}, invalidLiveTimingData(
			fmt.Sprintf("SignalR negotiation returned unsupported version %d", response.NegotiateVersion),
		)
	}
	connectionToken := response.ConnectionID
	if response.NegotiateVersion >= 1 {
		if response.ConnectionID == "" {
			return negotiation{}, invalidLiveTimingData("SignalR negotiation did not return a connection ID")
		}
		connectionToken = response.ConnectionToken
	}
	if connectionToken == "" {
		return negotiation{}, invalidLiveTimingData("SignalR negotiation did not return a connection token")
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
	return negotiation{}, invalidLiveTimingData("SignalR negotiation does not support WebSockets with text frames")
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
			if invalidWebSocketRead(err) {
				return nil, invalidLiveTimingData("SignalR handshake WebSocket data is invalid")
			}
			return nil, fmt.Errorf("read SignalR handshake: %w", err)
		}
		if messageType != websocket.MessageText {
			return nil, invalidLiveTimingData("SignalR handshake used a non-text WebSocket message")
		}
		buffered = append(buffered, contents...)

		record, remaining, complete := splitFirstRecord(buffered)
		if !complete {
			if len(buffered) > maxHandshakeResponseSize {
				return nil, invalidLiveTimingData(
					fmt.Sprintf("SignalR handshake response exceeds %d bytes", maxHandshakeResponseSize),
				)
			}
			continue
		}
		if len(record) > maxHandshakeResponseSize {
			return nil, invalidLiveTimingData(
				fmt.Sprintf("SignalR handshake response exceeds %d bytes", maxHandshakeResponseSize),
			)
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
	if !utf8.Valid(record) {
		return invalidLiveTimingData("SignalR handshake response is not UTF-8")
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(record, &response); err != nil || response == nil {
		return invalidLiveTimingData("decode SignalR handshake response")
	}
	if _, ok := response["type"]; ok {
		return invalidLiveTimingData("expected a SignalR handshake response")
	}
	encodedError, ok := response["error"]
	if !ok {
		return nil
	}
	var message string
	if err := json.Unmarshal(encodedError, &message); err != nil || message == "" {
		return invalidLiveTimingData("decode SignalR handshake error")
	}
	return invalidLiveTimingData("SignalR handshake rejected the connection")
}

func invalidWebSocketRead(err error) bool {
	if errors.Is(err, websocket.ErrMessageTooBig) {
		return true
	}
	switch websocket.CloseStatus(err) {
	case websocket.StatusProtocolError,
		websocket.StatusUnsupportedData,
		websocket.StatusInvalidFramePayloadData,
		websocket.StatusMessageTooBig:
		return true
	default:
		return false
	}
}

func (c *signalRConnection) close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		_ = c.conn.CloseNow()
		return err
	}
	return c.conn.CloseNow()
}
