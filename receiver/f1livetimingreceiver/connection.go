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
	"strings"
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
	ConnectionID        jsonControlString
	ConnectionToken     jsonControlString
	NegotiateVersion    int
	URL                 jsonControlString
	AccessToken         jsonControlString
	ErrorNonempty       bool
	AvailableTransports []negotiateTransport
}

type negotiateTransport struct {
	Transport       jsonControlString
	TransferFormats []jsonControlString
}

// Keep the prior struct decoder's case-insensitive matching, ordered repeated
// assignments, scalar-null no-ops, and slice-element reuse. Each assignment sees
// the raw string first, even if a later duplicate overwrites it. Unknown values
// are skipped rather than decoded; all keys at these control levels are strict.
func (transport *negotiateTransport) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return visitRawJSONObject(raw, func(key, value json.RawMessage) error {
		field, err := decodeLosslessJSONString(key)
		if err != nil {
			return err
		}
		switch {
		case strings.EqualFold(field, "transport"):
			return json.Unmarshal(value, &transport.Transport)
		case strings.EqualFold(field, "transferFormats"):
			return json.Unmarshal(value, &transport.TransferFormats)
		default:
			return nil
		}
	})
}

func decodeNegotiateResponse(raw []byte) (negotiateResponse, error) {
	var response negotiateResponse
	// A top-level null previously decoded as the zero response, then failed the
	// missing-token check. Preserve that policy as well as member-null behavior.
	if bytes.Equal(bytes.Trim(raw, " \t\r\n"), []byte("null")) {
		return response, nil
	}
	err := visitRawJSONObject(raw, func(key, value json.RawMessage) error {
		field, err := decodeLosslessJSONString(key)
		if err != nil {
			return err
		}
		var destination any
		switch {
		case strings.EqualFold(field, "connectionId"):
			destination = &response.ConnectionID
		case strings.EqualFold(field, "connectionToken"):
			destination = &response.ConnectionToken
		case strings.EqualFold(field, "negotiateVersion"):
			destination = &response.NegotiateVersion
		case strings.EqualFold(field, "url"):
			destination = &response.URL
		case strings.EqualFold(field, "accessToken"):
			destination = &response.AccessToken
		case strings.EqualFold(field, "availableTransports"):
			destination = &response.AvailableTransports
		case strings.EqualFold(field, "error"):
			if bytes.Equal(value, []byte("null")) {
				return nil
			}
			isString, empty := jsonStringShape(value)
			if !isString {
				return errJSONString
			}
			response.ErrorNonempty = !empty
			return nil
		default:
			return nil
		}
		return json.Unmarshal(value, destination)
	})
	if err != nil {
		return negotiateResponse{}, err
	}
	return response, nil
}

// signalRSocket is the message-level I/O boundary. Tests use channel-backed I/O
// with synctest; production uses coder/websocket's single reader and writer.
type signalRSocket interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	CloseNow() error
	SetReadLimit(int64)
}

type signalRConnection struct {
	conn            signalRSocket
	pending         []byte
	requestedTopics []string
	subscribedAt    time.Time
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
		HTTPClient: setupHTTPClient(client, stageUpgrade),
		HTTPHeader: credentials.headersWithCookies(upgradeCookies),
	})
	if err != nil {
		if callerErr := setupContextError(ctx, time.Now()); callerErr != nil {
			return nil, sanitizedTransportError(ctx, "SignalR WebSocket upgrade", callerErr)
		}
		if failure := asSetupHTTPError(err); failure != nil {
			return nil, failure // Discard Dial's URL-bearing wrappers.
		}
		if response != nil {
			if response.StatusCode == http.StatusSwitchingProtocols {
				return nil, invalidLiveTimingData("SignalR WebSocket upgrade response is invalid")
			}
			return nil, setupHTTPFailure(stageUpgrade, response.StatusCode, "", time.Time{})
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
		return sanitizedTransportError(ctx, "write F1 topic subscription", err)
	}
	c.subscribedAt = time.Now()
	c.requestedTopics = append([]string(nil), topics...)
	return nil
}

func (c *signalRConnection) read(
	ctx context.Context,
	consume func(context.Context, liveTimingBatch) error,
) (result error) {
	// Subscription success is the initial clock boundary. Charge the handoff
	// to this read owner once, then pause while processing handshake pending data.
	started := time.Now()
	budget := newServerWaitBudget(len(c.requestedTopics) != 0)
	if !c.subscribedAt.IsZero() {
		budget = budget.wait(started.Sub(c.subscribedAt))
	}
	runCtx, stop := context.WithCancel(ctx)
	writerDone := make(chan struct{})
	var pingErr error // Published only by closing writerDone.
	if !c.subscribedAt.IsZero() {
		go func() {
			defer close(writerDone)
			pingErr = writeHubPings(runCtx, c.conn, c.subscribedAt)
			if pingErr != nil {
				stop() // Interrupt the active read or a cooperative consumer.
			}
		}()
	} else {
		// Direct internal connections have a receive budget, and a subscription
		// budget only if they explicitly carry a manifest; no unsolicited writer.
		close(writerDone)
	}
	defer func() {
		stop()
		<-writerDone
		callerErr := ctx.Err()
		if deadline, ok := ctx.Deadline(); callerErr == nil && ok && !time.Now().Before(deadline) {
			// Equal-deadline timer callbacks may run in either order. The caller's
			// elapsed deadline wins even before its cancellation callback runs.
			callerErr = context.DeadlineExceeded
		}
		if callerErr != nil {
			result = callerErr
		} else if pingErr != nil && !errors.Is(result, errInvalidLiveTimingData) &&
			!errors.Is(result, errSignalRClosed) && !errors.Is(result, errSignalRReconnectAllowed) {
			result = pingErr
		}
	}()

	buffered := hubRecordBuffer{contents: c.pending, needsCompaction: true}
	c.pending = nil

	for {
		if err := runCtx.Err(); err != nil {
			return err
		}
		if err := budget.expired(); err != nil {
			return err
		}
		record, complete, err := buffered.next()
		if err != nil {
			return err
		}
		if complete {
			batch, err := decodeHubRecord(record, c.requestedTopics)
			if err != nil {
				return err
			}
			if batch != nil {
				if batch.source == liveTimingUpdateSourceSnapshot {
					c.requestedTopics = nil
				}
				if err := consume(runCtx, *batch); err != nil {
					return fmt.Errorf("consume F1 live timing batch: %w", err)
				}
			}
			budget = budget.accepted(batch != nil && batch.source == liveTimingUpdateSourceSnapshot)
			continue
		}

		waitStarted := time.Now()
		readCtx, cancelRead := context.WithDeadline(runCtx, waitStarted.Add(budget.remaining()))
		messageType, contents, err := c.conn.Read(readCtx)
		budget = budget.wait(time.Since(waitStarted))
		// coder/websocket v1.8.15 clears its cancellation hook in finishRead
		// before a successful Read returns, so canceling here leaves it usable.
		cancelRead()
		if err != nil {
			if invalidWebSocketRead(err) {
				return invalidLiveTimingData("F1 live timing WebSocket data is invalid")
			}
			if expired := budget.expired(); expired != nil {
				return expired
			}
			return sanitizedTransportError(runCtx, "read F1 live timing message", err)
		}
		if messageType != websocket.MessageText {
			return invalidLiveTimingData("F1 live timing used a non-text WebSocket message")
		}
		buffered.contents = append(buffered.contents, contents...)
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

	response, err := setupHTTPClient(client, stageNegotiate).Do(request)
	if err != nil {
		if callerErr := setupContextError(ctx, time.Now()); callerErr != nil {
			return negotiation{}, nil, sanitizedTransportError(ctx, "perform SignalR negotiation", callerErr)
		}
		if failure := asSetupHTTPError(err); failure != nil {
			return negotiation{}, nil, failure // Discard http.Client's URL-bearing wrapper.
		}
		return negotiation{}, nil, sanitizedTransportError(ctx, "perform SignalR negotiation", err)
	}
	defer response.Body.Close()
	now := time.Now()
	if err := setupContextError(ctx, now); err != nil {
		return negotiation{}, nil, sanitizedTransportError(ctx, "perform SignalR negotiation", err)
	}

	contents, err := io.ReadAll(io.LimitReader(response.Body, maxNegotiateResponseSize+1))
	if err != nil {
		return negotiation{}, nil, sanitizedTransportError(ctx, "read SignalR negotiation response", err)
	}
	if len(contents) > maxNegotiateResponseSize {
		return negotiation{}, nil, invalidLiveTimingData(
			fmt.Sprintf("SignalR negotiation response exceeds %d bytes", maxNegotiateResponseSize),
		)
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
	response, err := decodeNegotiateResponse(contents)
	if err != nil {
		return negotiation{}, invalidLiveTimingData("decode SignalR negotiation response")
	}
	if response.ErrorNonempty {
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
				return negotiation{connectionToken: string(connectionToken)}, nil
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
		return nil, sanitizedTransportError(ctx, "write SignalR handshake", err)
	}
	return readHandshakeResponse(ctx, connection)
}

func readHandshakeResponse(ctx context.Context, connection signalRSocket) ([]byte, error) {
	var buffered []byte
	scanned := 0
	for {
		messageType, contents, err := connection.Read(ctx)
		if err != nil {
			if invalidWebSocketRead(err) {
				return nil, invalidLiveTimingData("SignalR handshake WebSocket data is invalid")
			}
			return nil, sanitizedTransportError(ctx, "read SignalR handshake", err)
		}
		if messageType != websocket.MessageText {
			return nil, invalidLiveTimingData("SignalR handshake used a non-text WebSocket message")
		}
		buffered = append(buffered, contents...)

		record, remaining, complete := splitFirstRecord(buffered, scanned)
		if !complete {
			if len(buffered) > maxHandshakeResponseSize {
				return nil, invalidLiveTimingData(
					fmt.Sprintf("SignalR handshake response exceeds %d bytes", maxHandshakeResponseSize),
				)
			}
			scanned = len(buffered)
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

// splitFirstRecord returns views of the first record and its uninspected tail.
// scanned is a byte offset in contents: contents[:scanned] has already been
// checked and contains no separator. Only appended bytes need another search.
func splitFirstRecord(contents []byte, scanned int) (record, remaining []byte, complete bool) {
	separator := bytes.IndexByte(contents[scanned:], recordSeparator)
	if separator == -1 {
		return nil, contents, false
	}
	separator += scanned
	return contents[:separator], contents[separator+1:], true
}

func parseHandshakeResponse(record []byte) error {
	if !utf8.Valid(record) {
		return invalidLiveTimingData("SignalR handshake response is not UTF-8")
	}
	var hasType bool
	var encodedError json.RawMessage
	err := visitRawJSONObject(record, func(key, value json.RawMessage) error {
		field, err := decodeLosslessJSONString(key)
		if err != nil {
			return err
		}
		switch field {
		case "type":
			hasType = true
		case "error":
			encodedError = value // Preserve the map decoder's last-value policy.
		}
		return nil
	})
	if err != nil {
		return invalidLiveTimingData("decode SignalR handshake response")
	}
	if hasType {
		return invalidLiveTimingData("expected a SignalR handshake response")
	}
	if encodedError == nil {
		return nil
	}
	isString, empty := jsonStringShape(encodedError)
	if !isString || empty {
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
