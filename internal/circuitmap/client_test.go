package circuitmap

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoadBodyReadErrors(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("private body error")} {
		c, err := NewClient("http://LOCALHOST")
		if err != nil {
			t.Fatal(err)
		}
		c.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(iotest.ErrReader(cause))}, nil
		})
		m, err := c.Load(context.Background(), 91, 2026)
		if m.Valid() || err == nil {
			t.Fatalf("accepted failed read: %v", cause)
		}
		switch cause {
		case context.Canceled, context.DeadlineExceeded:
			if !errors.Is(err, cause) {
				t.Errorf("got %v, want %v", err, cause)
			}
		default:
			if err.Error() != "read circuit map failed" {
				t.Errorf("unexpected diagnostic: %v", err)
			}
		}
	}
}

func TestLoad(t *testing.T) {
	body := fixture(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.RequestURI != "/api/v1/circuits/91/2026" || !reflect.DeepEqual(r.Header, http.Header{
			"Accept": {"application/json"}, "Accept-Encoding": {"identity"}, "User-Agent": {"bargeboard/1.0"}, "Connection": {"close"},
		}) {
			t.Error("unexpected request path, method, or headers")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(body)
	}))
	defer s.Close()
	c, err := NewClient(s.URL + "/api/v1/circuits")
	if err != nil {
		t.Fatal(err)
	}
	if c.http.Timeout != 10*time.Second {
		t.Fatal("incorrect timeout")
	}
	m, err := c.Load(context.Background(), 91, 2026)
	if err != nil || !reflect.DeepEqual(m, testMap(t)) {
		t.Fatalf("unexpected map: %v", err)
	}
	for _, id := range [][2]int64{{0, 2026}, {91, 999}, {91, 10000}} {
		if m, err := c.Load(context.Background(), id[0], id[1]); err == nil || m.Valid() {
			t.Fatal("accepted invalid identity")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Load(ctx, 91, 2026); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestLoadFailures(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		status                int
		media, encoding, body string
		want                  error
	}{
		{"redirect", 302, "application/json", "", minimal, nil}, {"missing", 404, "application/json", "", minimal, nil}, {"server", 500, "application/json", "", minimal, nil},
		{"media", 200, "text/html", "", "private", f1id.ErrInvalidMediaType}, {"encoding", 200, "application/json", "gzip", minimal, f1id.ErrInvalidContentEncoding},
		{"size", 200, "application/json", "", strings.Repeat(" ", maxBodyBytes) + minimal, f1id.ErrResponseTooLarge}, {"syntax", 200, "application/json", "", "private", errMap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/private/91/2026" {
					t.Error("followed redirect")
				}
				w.Header().Set("Content-Type", tc.media)
				if tc.encoding != "" {
					w.Header().Set("Content-Encoding", tc.encoding)
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer s.Close()
			c, err := NewClient(s.URL + "/private")
			if err != nil {
				t.Fatal(err)
			}
			m, err := c.Load(context.Background(), 91, 2026)
			if err == nil || m.Valid() || strings.Contains(err.Error(), "private") || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
	for _, endpoint := range []string{"http://example.test", "https://private@example.test", "https://example.test/?private"} {
		if c, err := NewClient(endpoint); c != nil || err == nil || strings.Contains(err.Error(), "private") {
			t.Errorf("endpoint error: %v", err)
		}
	}
}
