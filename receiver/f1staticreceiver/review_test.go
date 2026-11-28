package f1staticreceiver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"

	"strings"
	"testing"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

func TestArchiveDownloadPolicy(t *testing.T) {
	for _, tc := range []struct {
		file   string
		status int
		size   int
	}{
		{"TeamRadio.jsonStream", 403, 0}, {"TimingData.jsonStream", 403, 0},
		{"DriverList.json", 403, 0}, {"SessionInfo.json", 403, 0},
		{"SessionInfo.json", 200, 4<<20 + 1}, {"Position.z.jsonStream", 200, 64<<20 + 1},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.file, tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				file := path.Base(r.URL.Path)
				if file == tc.file {
					if tc.size > 4<<20+1 {
						w.Header().Set("Content-Length", fmt.Sprint(tc.size))
					}
					w.WriteHeader(tc.status)
					if tc.size == 4<<20+1 {
						w.(http.Flusher).Flush()
						fmt.Fprint(w, strings.Repeat(" ", tc.size))
					}
					return
				}
				switch file {
				case "Heartbeat.jsonStream":
					fmt.Fprint(w, `00:00:00.000{"Utc":"2025-01-01T00:00:00Z"}`)
				case "SessionInfo.json":
					fmt.Fprint(w, info)
				case "DriverList.json":
					fmt.Fprint(w, drivers)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c := DefaultConfig()
			c.Enabled, c.Endpoint, c.SessionPath = true, srv.URL, "s"
			p, err := Materialize(t.Context(), c)
			switch {
			case tc.size > 0:
				if !errors.Is(err, f1id.ErrResponseTooLarge) {
					t.Fatal(err)
				}
			case tc.file == "TeamRadio.jsonStream":
				if err != nil || len(p.batches) != 2 {
					t.Fatal(err, p.batches)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
					t.Fatal(err)
				}
			}
			if err != nil && len(p.batches) != 0 {
				t.Fatal("partial replay")
			}
		})
	}
}

func TestArchiveInterruptedBody(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := DefaultConfig()
	c.Enabled, c.Endpoint, c.SessionPath = true, srv.URL, "s"
	if _, err := Materialize(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestFreezeTimelineRange(t *testing.T) {
	for _, tc := range []struct {
		utc            string
		anchor, record int64
		valid          bool
	}{
		{"1969-12-31T23:59:59Z", 0, 0, false}, {"1970-01-01T00:00:00Z", 0, 0, false},
		{"1970-01-01T00:00:01Z", 1e9, 2e9, false}, {"1970-01-01T00:00:01Z", 0, 0, true},
		{"2261-12-31T23:59:59Z", 0, 1e9, false}, {"2261-12-31T23:59:59Z", 0, 0, true},
		{"2500-01-01T00:00:00Z", 0, 0, false},
	} {
		p, err := freeze([]byte(info), []byte(drivers), map[string][]StreamRecord{
			"Heartbeat": {{tc.anchor, fmt.Sprintf(`{"Utc":%q}`, tc.utc)}}, "TimingData": {{tc.record, `{}`}},
		})
		if (err == nil) != tc.valid || (len(p.batches) > 0) != tc.valid {
			t.Fatal(tc, err)
		}
	}
}
