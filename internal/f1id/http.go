package f1id

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewRestrictedHTTPTransport returns a single-flight HTTP/1.1 transport that
// disables environment proxies, keep-alives, and automatic decompression so
// loopback URLs never leave the host, rejected bodies are not drained on close,
// and unsolicited idle bytes cannot reach Go's default logger.
func NewRestrictedHTTPTransport() *http.Transport {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	return &http.Transport{
		Proxy:              nil,
		DisableKeepAlives:  true,
		DisableCompression: true,
		Protocols:          protocols,
	}
}

// NewRestrictedHTTPClient returns a redirect-disabled client over NewRestrictedHTTPTransport.
func NewRestrictedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport:     NewRestrictedHTTPTransport(),
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

var (
	ErrInvalidMediaType       = errors.New("expected application/json")
	ErrInvalidContentEncoding = errors.New("unsupported content encoding")
	ErrResponseTooLarge       = errors.New("response exceeds byte limit")
	ErrBodyRead               = errors.New("response body read failed")
)

func ValidateHTTPSOrLoopbackEndpoint(fieldName, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(raw, "?#") || strings.HasSuffix(raw, "/") {
		return fmt.Errorf("%s: invalid endpoint", fieldName)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return fmt.Errorf("%s: expected HTTPS or loopback HTTP", fieldName)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

// SanitizedHTTPError uses only a caller-defined operation label in diagnostics.
// The label must be static, never taken from a request or response.
func SanitizedHTTPError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	if cause := ctx.Err(); cause != nil {
		return cause
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, cause) {
			return cause
		}
	}
	return fmt.Errorf("%s failed", operation)
}

// ReadBoundedJSONBody checks response metadata and size. The caller owns Body.Close.
func ReadBoundedJSONBody(resp *http.Response, maxBytes int64) ([]byte, error) {
	return ReadBoundedJSONBodyContext(context.Background(), resp, maxBytes)
}

// ReadBoundedJSONBodyContext checks response metadata and size while preserving
// canonical context cancellation and deadline errors during body reads.
func ReadBoundedJSONBodyContext(ctx context.Context, resp *http.Response, maxBytes int64) ([]byte, error) {
	enc := resp.Header.Values("Content-Encoding")
	if len(enc) > 1 || len(enc) == 1 && enc[0] != "" && enc[0] != "identity" {
		return nil, ErrInvalidContentEncoding
	}
	types := resp.Header.Values("Content-Type")
	if len(types) != 1 {
		return nil, ErrInvalidMediaType
	}
	mediaType, _, err := mime.ParseMediaType(types[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return nil, ErrInvalidMediaType
	}
	if maxBytes < 0 {
		return nil, ErrResponseTooLarge
	}
	if resp.Body == nil {
		return nil, ErrBodyRead
	}
	limit := maxBytes
	if limit < math.MaxInt64 {
		limit++
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, ErrBodyRead
	}
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrResponseTooLarge
	}
	return body, nil
}
