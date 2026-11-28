package jolpicareceiver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/receiver"
)

func empty(k table) string {
	tab, list, _, _ := k.schema()
	return fmt.Sprintf(`{"MRData":{"series":"f1","offset":"0","total":"0","limit":"100","%s":{"season":"2025","round":"4","%s":[]}}}`, tab, list)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestStandingsSeasonBoundary(t *testing.T) {
	for _, season := range []int64{1957, 1958} {
		c := config()
		c.Season = season
		c.IncludeSprint = false
		r := newReceiver(c, receiver.Settings{})
		r.wait = func(context.Context, time.Duration) error { return nil }
		want := []table{race, wdc}
		if season == 1958 {
			want = append(want, wcc)
		}
		var got []table
		r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			k := table(strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, fmt.Sprintf("/ergast/f1/%d/4/", season)), ".json"))
			got = append(got, k)
			body := strings.ReplaceAll(fixture(t, k), `"2025"`, fmt.Sprintf(`"%d"`, season))
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		if err := r.run(t.Context()); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%d: %v, %v", season, got, err)
		}
	}
}

func TestRestrictedClient(t *testing.T) {
	client := newReceiver(config(), receiver.Settings{}).client
	tr := client.Transport.(*http.Transport)
	if client.Timeout != 10*time.Second || tr.Proxy != nil || !tr.DisableKeepAlives || !tr.DisableCompression || tr.Protocols == nil || !tr.Protocols.HTTP1() || tr.Protocols.HTTP2() || tr.Protocols.UnencryptedHTTP2() {
		t.Fatal("unrestricted Jolpica HTTP client")
	}
}

func TestFetchBodyContextErrors(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		ctx, cancel := context.WithCancel(t.Context())
		r := newReceiver(config(), receiver.Settings{})
		r.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			bodyErr := fmt.Errorf("private: %w", cause)
			if cause == context.Canceled {
				cancel()
				bodyErr = errors.New("private read failure")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(iotest.ErrReader(bodyErr))}, nil
		})
		rows, err := r.fetch(ctx, race)
		cancel()
		if rows != nil || err != cause {
			t.Errorf("expected %v, got %v, %v", cause, rows, err)
		}
	}
}

func TestNoEmptyLogDelivery(t *testing.T) {
	r := newReceiver(config(), receiver.Settings{})
	r.logs, _ = consumer.NewLogs(func(context.Context, plog.Logs) error { t.Error("consumed empty logs"); return nil })
	if err := r.deliver(t.Context(), snapshot{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
}
func TestPollUntilCoherent(t *testing.T) {
	// First the race is unpublished, then WDC is unpublished. The third
	// attempt has all requested data, without a sprint on this weekend.
	type reply struct {
		k    table
		body string
	}
	raceBody := fixture(t, race)
	wdcBody := fixture(t, wdc)
	wccBody := fixture(t, wcc)
	script := []reply{{race, empty(race)}, {race, raceBody}, {sprint, empty(sprint)}, {wdc, empty(wdc)}, {race, raceBody}, {sprint, empty(sprint)}, {wdc, wdcBody}, {wcc, wccBody}}
	var n atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		i := int(n.Add(1)) - 1
		if i >= len(script) {
			t.Error("polled after completion")
			w.WriteHeader(500)
			return
		}
		step := script[i]
		if req.Method != "GET" || req.URL.RequestURI() != "/2025/4/"+string(step.k)+".json?limit=100" || req.Header.Get("Accept-Encoding") != "identity" || req.Header.Get("Accept") != "application/json" || req.UserAgent() != "bargeboard/1.0" || req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" || req.Header.Get("Proxy-Authorization") != "" {
			t.Errorf("unexpected request at step %d", i)
		}
		http.SetCookie(w, &http.Cookie{Name: "fake", Value: "invented"})
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, step.body)
	}))
	defer server.Close()
	c := config()
	c.Endpoint = server.URL
	r := newReceiver(c, receiver.Settings{})
	var waits []time.Duration
	r.wait = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	logCalls, metricCalls := 0, 0
	r.logs, _ = consumer.NewLogs(func(_ context.Context, l plog.Logs) error {
		logCalls++
		if l.LogRecordCount() != 2 || n.Load() != 8 {
			t.Error("partial log delivery")
		}
		return nil
	})
	r.metrics, _ = consumer.NewMetrics(func(_ context.Context, m pmetric.Metrics) error {
		metricCalls++
		if m.DataPointCount() != 11 || n.Load() != 8 {
			t.Error("partial metric delivery")
		}
		return nil
	})
	if err := r.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	<-r.done
	if err := r.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if logCalls != 1 || metricCalls != 1 || n.Load() != 8 || !reflect.DeepEqual(waits, []time.Duration{5 * time.Minute, time.Second, time.Second, 5 * time.Minute, time.Second, time.Second, time.Second}) {
		t.Fatalf("calls %d %d, requests %d, waits %v", logCalls, metricCalls, n.Load(), waits)
	}
}
func TestRateLimitBudget(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	for raw, delay := range map[string]time.Duration{"2": 30 * time.Second, "120": 2 * time.Minute, now.Add(time.Minute).Format(http.TimeFormat): time.Minute, "invented": 30 * time.Second} {
		t.Run(raw, func(t *testing.T) {
			var n atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n.Add(1)
				w.Header().Set("Retry-After", raw)
				w.WriteHeader(429)
			}))
			defer server.Close()
			c := config()
			c.MaxAttempts = 2
			c.Endpoint = server.URL
			r := newReceiver(c, receiver.Settings{})
			r.now = func() time.Time { return now }
			var waits []time.Duration
			r.wait = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
			if err := r.run(t.Context()); err == nil || err.Error() != "Jolpica request rate limited" || n.Load() != 2 || !reflect.DeepEqual(waits, []time.Duration{delay}) {
				t.Fatalf("error %v, requests %d, waits %v", err, n.Load(), waits)
			}
		})
	}
}
func TestHTTPRejection(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed redirect") }))
	defer target.Close()
	for name, serve := range map[string]func(http.ResponseWriter){
		"redirect": func(w http.ResponseWriter) {
			w.Header().Set("Location", target.URL+"/invented-secret")
			w.WriteHeader(302)
		},
		"status":        func(w http.ResponseWriter) { w.WriteHeader(401); io.WriteString(w, "invented-secret") },
		"encoding":      func(w http.ResponseWriter) { w.Header().Set("Content-Encoding", "invented-secret") },
		"type":          func(w http.ResponseWriter) { w.Header().Set("Content-Type", "text/invented-secret") },
		"size":          func(w http.ResponseWriter) { io.WriteString(w, strings.Repeat("x", 512<<10)+"x") },
		"invalid UTF-8": func(w http.ResponseWriter) { io.WriteString(w, "{\"invented-secret\":\"\xff\"}") },
		"invalid JSON":  func(w http.ResponseWriter) { io.WriteString(w, "invented-secret") },
		"transport":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				serve(w)
			}))
			defer server.Close()
			if serve == nil {
				server.Close()
			}
			c := config()
			c.Endpoint = server.URL + "/invented-secret"
			r := newReceiver(c, receiver.Settings{})
			if got, err := r.fetch(t.Context(), race); got != nil || err == nil || strings.Contains(err.Error(), "invented") || strings.Contains(err.Error(), server.Listener.Addr().String()) {
				t.Fatalf("unsafe fetch result: %v, %v", got, err)
			}
		})
	}
}
func TestIndependentConsumers(t *testing.T) {
	s := parsed(t)
	for _, fail := range []string{"logs", "metrics", "both"} {
		t.Run(fail, func(t *testing.T) {
			r := newReceiver(config(), receiver.Settings{})
			var calls []string
			call := func(which string) error {
				calls = append(calls, which)
				if which == fail || fail == "both" {
					return errors.New("invented private consumer detail")
				}
				return nil
			}
			r.logs, _ = consumer.NewLogs(func(context.Context, plog.Logs) error { return call("logs") })
			r.metrics, _ = consumer.NewMetrics(func(context.Context, pmetric.Metrics) error { return call("metrics") })
			err := r.deliver(t.Context(), s, time.Now())
			if err == nil || strings.Contains(err.Error(), "private") || !reflect.DeepEqual(calls, []string{"metrics", "logs"}) {
				t.Fatalf("errors %v, calls %v", err, calls)
			}
		})
	}
}
func TestFactoryDisabled(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig().(*Config)
	if cfg.Enabled || cfg.Endpoint != DefaultEndpoint || !cfg.IncludeStandings || cfg.IncludeSprint || cfg.CorrelateTraces || cfg.MaxAttempts != 16 || cfg.PollInterval != "5m" {
		t.Fatal("incorrect defaults")
	}
	m, _ := consumer.NewMetrics(func(context.Context, pmetric.Metrics) error { return nil })
	l, _ := consumer.NewLogs(func(context.Context, plog.Logs) error { return nil })
	ctx := t.Context()
	s := receiver.Settings{ID: component.NewID(Type)}
	metrics, err := f.CreateMetrics(ctx, s, cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := f.CreateLogs(ctx, s, cfg, l)
	if err != nil || logs != metrics {
		t.Fatal("expected shared receiver")
	}
	if _, err := f.CreateTraces(ctx, s, cfg, nil); err == nil {
		t.Fatal("unexpected trace support")
	}
	if err := logs.Start(ctx, nil); err != nil || logs.(*roundReceiver).done != nil {
		t.Fatal("disabled receiver started worker")
	}
	if err := metrics.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestConfigBounds(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.Endpoint = "https://invented@example.test" }, func(c *Config) { c.Season = 1949 }, func(c *Config) { c.Round = 100 }, func(c *Config) { c.MeetingKey = -1 }, func(c *Config) { c.CorrelateTraces = true }, func(c *Config) { c.MaxAttempts = 65 }, func(c *Config) { c.PollInterval = "29s" }, func(c *Config) { c.PollInterval = "31m" }, func(c *Config) { c.PollInterval = "05m" }} {
		c := config()
		change(&c)
		if err := c.Validate(); err == nil || strings.Contains(err.Error(), "invented") {
			t.Error("expected sanitized validation error")
		}
	}
}
