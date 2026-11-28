package openf1receiver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRestrictedTransport(t *testing.T) {
	h, err := NewHTTPAdapter(DefaultEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	tr := h.transport.(*http.Transport)
	if tr.Proxy != nil || !tr.DisableCompression || !tr.DisableKeepAlives || tr.Protocols == nil || !tr.Protocols.HTTP1() || tr.Protocols.HTTP2() || tr.Protocols.UnencryptedHTTP2() {
		t.Fatal("expected direct, uncompressed, single-use HTTP/1 connections")
	}
}

func TestCancelledResponseBody(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h, _ := NewHTTPAdapter(DefaultEndpoint)
	h.transport = roundTrip(func(*http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("[]"))}, nil
	})
	r := h.Fetch(ctx, Request{Stage: sessions, RouteKey: 23})
	if !errors.Is(r.Err, context.Canceled) || r.Failure != DiagTransport || r.Body != nil {
		t.Fatal(r)
	}
}
func TestHTTPBoundary(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := requests.Add(1)
		path := []string{sessions, drivers, results, results, results, results, results, results}
		if int(i) > len(path) {
			t.Error("unexpected redirect/replay")
			return
		}
		if r.Method != "GET" || r.RequestURI != "/opaque/v1/"+path[i-1]+"?session_key=23" || r.Header.Get("Accept") != "application/json" || r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected request")
		}
		w.Header().Set("Content-Type", "Application/JSON; charset=utf-8")
		switch i {
		case 1:
			w.Write(array(session))
		case 2:
			w.Header().Set("Content-Encoding", "identity")
			w.Write(array(driver(1)))
		case 3:
			w.Write(array(result(1, 1, 9, "")))
		case 4:
			w.Header().Set("Location", "/forbidden")
			w.WriteHeader(302)
		case 5:
			w.Header().Set("Content-Encoding", "gzip")
			io.WriteString(w, "private")
		case 6:
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "private")
		case 7:
			io.WriteString(w, strings.Repeat(" ", maxResponseBytes+1))
		case 8:
			w.Header().Set("Retry-After", "56")
			w.WriteHeader(429)
		}
	}))
	defer server.Close()
	h, err := NewHTTPAdapter(server.URL + "/opaque/v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		stage string
		want  Response
	}{
		{sessions, success(array(session))}, {drivers, success(array(driver(1)))}, {results, success(array(result(1, 1, 9, "")))}, {results, Response{Status: 302}}, {results, Response{Status: 200, Failure: DiagDecode}}, {results, Response{Status: 200, Failure: DiagDecode}}, {results, Response{Status: 200, Failure: DiagResponseSize}}, {results, Response{Status: 429, RetryAfter: "56"}},
	} {
		got := h.Fetch(t.Context(), Request{Stage: tc.stage, RouteKey: 23})
		if got.StartedAt.IsZero() || (got.Err != nil) != (tc.want.Failure != "") {
			t.Fatal(got)
		}
		got.StartedAt, got.Err = time.Time{}, nil
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatal(tc.stage, got.Status, got.Failure)
		}
	}
	if requests.Load() != 8 {
		t.Fatal(requests.Load())
	}
}
func TestFactoryDisabled(t *testing.T) {
	f := NewFactory()
	if f.Type() != Type {
		t.Fatal(f.Type())
	}
	if _, err := f.CreateTraces(t.Context(), receiver.Settings{}, f.CreateDefaultConfig(), nil); err == nil {
		t.Fatal("created traces")
	}
	if _, err := f.CreateMetrics(t.Context(), receiver.Settings{}, f.CreateDefaultConfig(), nil); err == nil {
		t.Fatal("created metrics")
	}
	c, err := createLogs(t.Context(), receiver.Settings{}, f.CreateDefaultConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := c.(*resultReceiver)
	r.http.transport = roundTrip(func(*http.Request) (*http.Response, error) { panic("disabled egress") })
	if err = r.Start(t.Context(), nil); err != nil || r.started {
		t.Fatal(err)
	}
	if err = r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func enabled(t *testing.T, next consumer.Logs) *resultReceiver {
	t.Helper()
	cfg := &Config{Enabled: true, SessionName: "race", MeetingKey: 17, SessionKey: 23, Season: 2042, Endpoint: "https://example.invalid/v1"}
	r, err := createLogs(t.Context(), receiver.Settings{}, cfg, next)
	if err != nil {
		t.Fatal(err)
	}
	return r.(*resultReceiver)
}
func TestTimeoutAndSanitizedTransport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := NewHTTPAdapter(DefaultEndpoint)
		calls := 0
		h.transport = roundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			<-r.Context().Done()
			return nil, errors.New("private credential and URL")
		})
		start := time.Now()
		got := h.Fetch(t.Context(), Request{Stage: sessions, RouteKey: 23})
		if time.Since(start) != 10*time.Second || calls != 1 || !got.StartedAt.Equal(start) || got.Failure != DiagTransport || !errors.Is(got.Err, context.DeadlineExceeded) || strings.Contains(got.Err.Error(), "private") {
			t.Fatal(time.Since(start), calls, got)
		}
	})
}
func TestConsumerFailureDoesNotReplayOrSuppress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		delivered := make(chan plog.Logs, 2)
		next, _ := consumer.NewLogs(func(_ context.Context, l plog.Logs) error { delivered <- l; return errors.New("downstream failed") })
		r := enabled(t, next)
		r.cfg.RootDrivers = []int64{1}
		var starts []time.Time
		blocked := make(chan struct{})
		r.http.transport = roundTrip(func(q *http.Request) (*http.Response, error) {
			starts = append(starts, time.Now())
			endpoints := []string{sessions, drivers, results, results, results, results}
			i := len(starts) - 1
			if i >= len(endpoints) || q.URL.Path != "/v1/"+endpoints[i] {
				t.Error("unexpected request")
				return nil, errors.New("unexpected request")
			}
			var b []byte
			switch i {
			case 0:
				b = array(session)
			case 1:
				b = array(driver(1), driver(2))
			case 2, 3:
				b = array(result(1, 1, 4, ""), result(2, 2, 4, ""))
			case 4:
				b = array(result(1, 1, 4, ""), result(2, -1, -1, "dsq"))
			case 5:
				close(blocked)
				<-q.Context().Done()
				return nil, q.Context().Err()
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
		})
		if err := r.Start(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		first, second := <-delivered, <-delivered
		records := first.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
		for i := 0; i < records.Len(); i++ {
			rec := records.At(i)
			if rec.TraceID().IsEmpty() != (i == 1) || rec.SpanID().IsEmpty() != (i == 1) {
				t.Fatal("wrong driver root correlation", i)
			}
		}
		rec := second.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
		if first.LogRecordCount() != 2 || second.LogRecordCount() != 1 || rec.Body().Str() != "f1.driver.result.revised" {
			t.Fatal("duplicate or lost logs")
		}
		<-blocked
		if err := r.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		for i, d := range []time.Duration{time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second} {
			if starts[i+1].Sub(starts[i]) != d {
				t.Fatal(starts)
			}
		}
		if len(starts) != 6 || len(delivered) != 0 {
			t.Fatal("shutdown dispatched work")
		}
	})
}
