package openf1receiver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDiscoverCompletedRace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions" || r.URL.RawQuery != "country_name=Example+Islands&session_name=Race&session_type=Race&year=2042" {
			t.Errorf("discovery request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		row := strings.Replace(session, `"session_key"`, `"country_name":"Example Islands","session_key"`, 1)
		io.WriteString(w, "["+strings.ReplaceAll(row, "2042-03-04", "2042-04-09")+","+row+"]")
	}))
	defer srv.Close()
	h, err := NewHTTPAdapter(srv.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.discover(t.Context(), Config{Year: 2042, CountryName: "Example Islands", SessionName: "race"}, time.Date(2042, 3, 6, 0, 0, 0, 0, time.UTC))
	if err != nil || got.SessionKey != 23 || got.MeetingKey != 17 || got.Season != 2042 || got.SessionName != "race" || len(got.Drivers) != 0 {
		t.Fatal(got, err)
	}
}

func TestDiscoverWithoutFiltersSkipsUpcomingSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/mirror/v1/sessions?session_type=Race" {
			t.Errorf("discovery request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		future := strings.NewReplacer("2042-03-04", "2042-04-09", `"session_key":23`, `"session_key":24`).Replace(session)
		io.WriteString(w, "["+future+","+session+"]")
	}))
	defer srv.Close()
	h, err := NewHTTPAdapter(srv.URL + "/mirror/v1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.discover(t.Context(), Config{}, time.Date(2042, 3, 6, 0, 0, 0, 0, time.UTC))
	if err != nil || got.SessionKey != 23 || got.MeetingKey != 17 || got.Season != 2042 || got.SessionName != "race" {
		t.Fatal(got, err)
	}
}
