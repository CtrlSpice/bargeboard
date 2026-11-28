package f1staticreceiver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	info     = `{"Key":175,"Type":"Race","Name":"Race","Meeting":{"Key":78,"Name":"Invented Grand Prix","Circuit":{"Key":3}},"StartDate":"2025-03-10T12:00:00","EndDate":"2025-03-10T14:00:00","GmtOffset":"02:00:00"}`
	drivers  = `{"17":{"RacingNumber":"17","Tla":"FIG"}}`
	position = `"q1YKyC/OLMnMz1Oyio6tBQA="`
)

func TestStreamFraming(t *testing.T) {
	prefix := "00:00:00.000"
	for _, tc := range []struct {
		data       string
		compressed bool
		want       []StreamRecord
	}{
		{"\xef\xbb\xbf" + prefix + "{}\r\n99:59:59.9991\n", false, []StreamRecord{{0, "{}"}, {int64(100*time.Hour - time.Millisecond), "1"}}},
		{prefix + position, true, []StreamRecord{{0, `{"Position":[]}`}}},
	} {
		got, err := ParseStreamRecords([]byte(tc.data), tc.compressed)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v %v", got, err)
		}
	}
	for _, s := range []string{prefix + "{}\n\n", prefix + "\xff", prefix + "{}", "0:00:00.000{}", "00:00:60.000{}", "00:60:00.000{}", "00:00:00,000{}", prefix + "null\r"} {
		if s == prefix+"{}" {
			s = "\xef\xbb\xbf\xef\xbb\xbf" + s
		}
		if _, err := ParseStreamRecords([]byte(s), false); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	bytes, _ := base64.StdEncoding.DecodeString(strings.Trim(position, `"`))
	trailing, _ := json.Marshal(base64.StdEncoding.EncodeToString(append(bytes, 42)))
	for _, s := range []string{`"!"`, `"AB=="`, `"AA=="`, `"\ud800"`, string(trailing)} {
		got, err := ParseStreamRecords([]byte(prefix+position+"\n"+prefix+s), true)
		if err == nil || len(got) != 0 {
			t.Fatalf("released corrupt stream %q", s)
		}
	}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time                                 { return c.now }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error { c.now = c.now.Add(d); return nil }

func TestFrozenDelivery(t *testing.T) {
	files := map[string]string{
		"SessionInfo.json": "\xef\xbb\xbf" + info, "DriverList.json": drivers,
		"Heartbeat.jsonStream":  `00:00:02.000{"Utc":"2025-03-10T10:00:02Z"}`,
		"Position.z.jsonStream": "00:00:03.000" + position,
		"CarData.z.jsonStream":  `00:00:03.000"q1ZyzSspykwtVrKKjq0FAA=="`,
		"TimingData.jsonStream": "00:00:03.000{}\n00:00:01.000null\n00:00:03.000[]",
	}
	c := DefaultConfig()
	c.Enabled = true
	c.SessionPath = "2025/Test_Race"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer invented-token" {
			t.Error("missing synthetic credential")
		}
		f := strings.TrimPrefix(r.URL.Path, "/2025/Test_Race/")
		if s, ok := files[f]; ok {
			_, _ = w.Write([]byte(s))
			return
		}
		if !strings.HasSuffix(f, ".jsonStream") || strings.Contains(f, "CarData") {
			t.Errorf("unexpected file: %s", f)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	c.Auth.TokenFile = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(c.Auth.TokenFile, []byte(" invented-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c.Endpoint = srv.URL
	p, err := Materialize(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	start := time.Date(2025, 3, 10, 10, 0, 0, 0, time.UTC)
	if p.Identity.MeetingKey != 78 || p.RouteKey != 175 || p.CircuitKey != 3 || p.ScheduledStart != start || p.ScheduledEnd != start.Add(2*time.Hour) {
		t.Fatal("descriptor")
	}
	wantTopics := []string{"SessionInfo", "Heartbeat", "Position", "CarData", "TimingData", "TimingData", "TimingData"}
	coords := []int{0, 1, 2, 2, 2, 2, 2}
	pub := []int{1, 2, 3, 3, 3, 1, 3}
	for _, speed := range []string{"instant", "1x", "10x", "100x"} {
		t.Run(speed, func(t *testing.T) {
			host := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			clock := &fakeClock{host}
			i := 0
			err := Replay(t.Context(), p, speed, clock, func(_ context.Context, b FrozenBatch, now time.Time) error {
				if i >= len(coords) {
					t.Fatal("extra batch")
				}
				r := b.Records()
				d := time.Duration(coords[i]) * time.Second
				if b.Ordinal != int64(i) || b.CoordNs != int64(d) || b.Snapshot != (i == 0) || !b.ObservationUTC.Equal(start.Add(time.Second+d)) || r[0].Topic != wantTopics[i] || !r[0].PublicationUTC.Equal(start.Add(time.Duration(pub[i])*time.Second)) {
					t.Fatalf("ordinal %d: %+v", i, b)
				}
				if i == 0 && (len(r) != 2 || r[1].Topic != "DriverList") {
					t.Fatal("bootstrap not atomic")
				}
				if r[0].Topic == "CarData" && r[0].Payload != `{"Entries":[]}` {
					t.Fatalf("CarData payload: %s", r[0].Payload)
				}
				scale, _ := multiplier(speed)
				elapsed := time.Duration(0)
				if scale != 0 {
					elapsed = d / time.Duration(scale)
				}
				if now.Sub(host) != elapsed {
					t.Fatalf("host elapsed %s", now.Sub(host))
				}
				r[0].Topic = "mutation"
				i++
				return nil
			})
			if err != nil || i != len(coords) {
				t.Fatalf("%d %v", i, err)
			}
		})
	}
}

func TestAnchorFallback(t *testing.T) {
	r := map[string][]StreamRecord{"Heartbeat": {{0, `{"Utc":"unknown"}`}}}
	if _, err := freeze([]byte(info), []byte(drivers), r); !errors.Is(err, ErrMissingHistoricalOrigin) {
		t.Fatal(err)
	}
	r["SessionData"] = []StreamRecord{{int64(time.Second), `{"StatusSeries":{"10":{"Utc":"2026-01-01T00:00:00Z"},"2":{"Utc":"2025-01-01T00:00:01Z","SessionStatus":"Started"}}}`}}
	t0, err := anchor(r)
	if err != nil || t0.Format(time.RFC3339) != "2025-01-01T00:00:00Z" {
		t.Fatal(t0, err)
	}
}

func TestRedirectAndConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Enabled || c.Validate() != nil {
		t.Fatal("default configuration")
	}
	c.Enabled = true
	for _, s := range []string{"", ".", "..", "a//b", "/x", "a/", "foo/../bar", "a%2fb", "x?y", "a#b", "a\\b"} {
		c.SessionPath = s
		if c.Validate() == nil {
			t.Errorf("accepted %q", s)
		}
	}
	const secret = "invented-redirect-token"
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect followed") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/"+secret, http.StatusFound)
	}))
	defer server.Close()
	c.Endpoint = server.URL
	c.SessionPath = "session"
	c.Auth.TokenFile = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(c.Auth.TokenFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Materialize(t.Context(), c)
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "302") {
		t.Fatal("unsafe redirect handling")
	}
}
