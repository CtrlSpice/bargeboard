package f1staticreceiver

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCursorLineEndingsAndOffsets(t *testing.T) {
	const zero = `00:00:00.000{}`
	const two = `00:00:02.000[]`
	for _, ending := range []string{"", "\n", "\r\n"} {
		c := cursor{reader: bufio.NewReader(strings.NewReader(zero + "\r\n" + two + "\n" + zero + ending))}
		for i, want := range []struct {
			record    StreamRecord
			effective int64
		}{
			{StreamRecord{0, "{}"}, 0},
			{StreamRecord{int64(2 * time.Second), "[]"}, int64(2 * time.Second)},
			{StreamRecord{0, "{}"}, int64(2 * time.Second)},
		} {
			if err := c.advance(); err != nil || c.record != want.record || c.effective != want.effective || c.seq != int64(i+1) {
				t.Fatalf("line %d, ending %q: got %+v at %d, sequence %d, error %v", i, ending, c.record, c.effective, c.seq, err)
			}
		}
		if err := c.advance(); err != io.EOF {
			t.Fatal("expected EOF", err)
		}
	}
	for _, invalid := range []string{"-0:00:01.000{}", zero + "\r", "\xef\xbb\xbf" + zero} {
		c := cursor{reader: bufio.NewReader(strings.NewReader(invalid))}
		err := c.advance()
		if !errors.Is(err, errRecord) && !errors.Is(c.invalid, errRecord) {
			t.Fatalf("accepted invalid record %q", invalid)
		}
	}
}

func TestHeapReplayMatchesReferenceAndRejectsLateCorruption(t *testing.T) {
	const count = 1200
	streams := map[string][]StreamRecord{"Heartbeat": {{0, `{"Utc":"2025-03-10T10:00:00Z"}`}}}
	files := map[string]string{"SessionInfo.json": info, "DriverList.json": drivers, "Heartbeat.jsonStream": "\xef\xbb\xbf00:00:00.000" + streams["Heartbeat"][0].Payload}
	for _, topic := range []string{"TimingData", "TeamRadio"} {
		var body strings.Builder
		for i := range count {
			ms := i % 1000
			payload := fmt.Sprintf(`{"n":%d}`, i)
			fmt.Fprintf(&body, "00:00:00.%03d%s\n", ms, payload)
			streams[topic] = append(streams[topic], StreamRecord{int64(ms) * int64(time.Millisecond), payload})
		}
		files[topic+".jsonStream"] = body.String()
	}
	want, err := freeze([]byte(info), []byte(drivers), streams)
	if err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := path.Base(r.URL.Path)
			data, ok := files[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, data)
			if corrupt && name == "TimingData.jsonStream" {
				fmt.Fprint(w, "00:00:01.200{")
			}
		}))
		c := DefaultConfig()
		c.Enabled = true
		c.Endpoint = srv.URL
		c.SessionPath = "archive"
		c.CircuitEnrichment.Enabled = false
		p, err := openPlan(t.Context(), c)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		err = p.walk(t.Context(), func(b FrozenBatch, _, _ bool) error {
			if len(p.heads) > len(streams) || len(p.batches) != 0 {
				t.Fatal("iterator retained more than the active stream heads")
			}
			if !reflect.DeepEqual(b, want.batches[n]) {
				t.Fatalf("batch %d differs from reference", n)
			}
			n++
			return nil
		})
		p.close()
		if _, statErr := os.Stat(p.dir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal("temporary replay directory remains", statErr)
		}
		if corrupt {
			if !errors.Is(err, errRecord) || n != len(want.batches) {
				t.Fatal(n, err)
			}
		} else if err != nil || n != len(want.batches) {
			t.Fatal(n, err)
		}
	}
}
