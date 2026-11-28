package f1id

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestEndpointValidation(t *testing.T) {
	for _, raw := range []string{"https://example.test", "https://example.test:8443/v1", "http://localhost", "http://LOCALHOST:8080/path", "http://127.0.0.1", "http://127.255.255.254:9443", "http://[::1]:1234"} {
		if err := ValidateHTTPSOrLoopbackEndpoint("endpoint", raw); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "localhost", "//example.test", "https:///private", "http://example.test", "http://localhost.example.test", "http://10.0.0.1", "http://[::2]", "ftp://127.0.0.1", "https://example.test/", "https://example.test/path/", "https://private@example.test", "https://example.test?private", "https://example.test?", "https://example.test#private", "https://example.test#", "https://example.test/%private"} {
		err := ValidateHTTPSOrLoopbackEndpoint("endpoint", raw)
		if err == nil || !strings.Contains(err.Error(), "endpoint") || strings.Contains(err.Error(), "private") {
			t.Errorf("unexpected endpoint error: %v", err)
		}
	}
}

func TestSanitizedHTTPError(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	expired, cancelDeadline := context.WithDeadline(ctx, time.Unix(1, 0))
	defer cancelDeadline()
	private := errors.New("private header and body")
	wrap := func(err error) error {
		return &url.Error{Op: "private", URL: "https://example.test/private?private", Err: err}
	}
	for _, tc := range []struct {
		ctx       context.Context
		err, want error
	}{
		{ctx, nil, nil}, {ctx, wrap(context.Canceled), context.Canceled}, {ctx, wrap(context.DeadlineExceeded), context.DeadlineExceeded},
		{cancelled, wrap(private), context.Canceled}, {expired, wrap(private), context.DeadlineExceeded},
	} {
		if got := SanitizedHTTPError(tc.ctx, "fetch", tc.err); got != tc.want {
			t.Errorf("got %v, want %v", got, tc.want)
		}
	}
	err := SanitizedHTTPError(ctx, "fetch", wrap(private))
	if err == nil || err.Error() != "fetch failed" || errors.Unwrap(err) != nil {
		t.Errorf("unexpected transport error: %v", err)
	}
}

func TestBoundedJSONBody(t *testing.T) {
	for _, tc := range []struct {
		name       string
		types, enc []string
		body       string
		limit      int64
		want       error
	}{
		{"exact", []string{"Application/JSON; charset=utf-8"}, []string{"identity"}, "{}", 2, nil},
		{"max", []string{"application/json"}, nil, "[]", math.MaxInt64, nil},
		{"zero", []string{"application/json"}, []string{""}, "", 0, nil},
		{"over", []string{"application/json"}, nil, "private", 2, ErrResponseTooLarge},
		{"zero over", []string{"application/json"}, nil, "x", 0, ErrResponseTooLarge},
		{"missing type", nil, nil, "private", 100, ErrInvalidMediaType},
		{"other type", []string{"text/json"}, nil, "private", 100, ErrInvalidMediaType},
		{"broken type", []string{"application/json; private="}, nil, "private", 100, ErrInvalidMediaType},
		{"two types", []string{"application/json", "text/private"}, nil, "private", 100, ErrInvalidMediaType},
		{"encoding", []string{"application/json"}, []string{"gzip"}, "private", 100, ErrInvalidContentEncoding},
		{"encoding case", []string{"application/json"}, []string{"Identity"}, "private", 100, ErrInvalidContentEncoding},
		{"two encodings", []string{"application/json"}, []string{"identity", "private"}, "private", 100, ErrInvalidContentEncoding},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := strings.NewReader(tc.body)
			resp := &http.Response{Header: http.Header{"Content-Type": tc.types, "Content-Encoding": tc.enc}, Body: io.NopCloser(reader)}
			body, err := ReadBoundedJSONBody(resp, tc.limit)
			if err != tc.want || err != nil && body != nil || err == nil && string(body) != tc.body {
				t.Fatalf("body %q, error %v", body, err)
			}
			consumed := int64(len(tc.body) - reader.Len())
			if err == ErrResponseTooLarge && consumed != tc.limit+1 || (err == ErrInvalidContentEncoding || err == ErrInvalidMediaType) && consumed != 0 {
				t.Errorf("read %d bytes", consumed)
			}
		})
	}
	resp := &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader("private"), iotest.ErrReader(errors.New("private read failure"))))}
	if body, err := ReadBoundedJSONBody(resp, 100); body != nil || err != ErrBodyRead {
		t.Errorf("read failure: %q, %v", body, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	resp = &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(iotest.ErrReader(errors.New("interrupted")))}
	if body, err := ReadBoundedJSONBodyContext(cancelled, resp, 100); body != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("canceled read: %q, %v", body, err)
	}
	resp = &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(iotest.ErrReader(context.DeadlineExceeded))}
	if body, err := ReadBoundedJSONBodyContext(context.Background(), resp, 100); body != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline read: %q, %v", body, err)
	}
	client := NewRestrictedHTTPClient(5 * time.Second)
	tr, ok := client.Transport.(*http.Transport)
	if !ok || !tr.DisableKeepAlives || !tr.DisableCompression || tr.Proxy != nil {
		t.Fatalf("unexpected restricted transport: %#v", client.Transport)
	}
}
