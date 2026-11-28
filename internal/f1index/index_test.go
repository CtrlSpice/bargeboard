package f1index

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestResolveOffsetAndFallback(t *testing.T) {
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		year := 2026
		if r.URL.Path == "/2025/Index.json" {
			year = 2025
		}
		fmt.Fprintf(w, "\xef\xbb\xbf"+`{"Meetings":[{"Name":"Frozen Lake","Sessions":[{"Name":"Race","Type":"Race","EndDate":"%d-01-02T09:00:01","GmtOffset":"-01:00:00","Path":"race/"},{"Name":"Practice 2","Type":"Practice","EndDate":"%d-01-02T11:30:00","GmtOffset":"01:30:00","Path":"practice/"}]}]}`, year, year)
	}))
	defer server.Close()
	for _, test := range []struct {
		selector, path string
		any            bool
		years          []string
	}{
		{"latest", "race/", false, []string{"/2026/Index.json", "/2025/Index.json"}},
		{"latest", "practice/", true, []string{"/2026/Index.json"}},
		{"2026/LaKe/PrAcTiCe 2", "practice/", false, []string{"/2026/Index.json"}},
	} {
		paths = nil
		q, err := ParseSelector(test.selector)
		if err != nil {
			t.Fatal(err)
		}
		if test.any {
			q.Type = ""
		}
		got, err := Resolve(t.Context(), server.URL, q, now, "")
		if err != nil || got.Session.Path != test.path || got.Meeting != "Frozen Lake" || !slices.Equal(paths, test.years) {
			t.Fatal(got, paths, err)
		}
	}
}

func TestIndexErrorsHideSource(t *testing.T) {
	const secret = "invented-private-input"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/"+secret, http.StatusFound) }))
	defer srv.Close()
	for _, endpoint := range []string{srv.URL, srv.URL + "?" + secret} {
		_, err := Fetch(t.Context(), endpoint, 2025, "")
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("unsanitized index error")
		}
	}
}

func TestFetchAuthorization(t *testing.T) {
	for _, token := range []string{"", "invented-index"} {
		for _, status := range []int{401, 403} {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var want []string
				if token != "" {
					want = []string{"Bearer " + token}
				}
				if !slices.Equal(r.Header.Values("Authorization"), want) {
					t.Error("incorrect index authorization")
				}
				http.Error(w, "invented-index", status)
			}))
			_, err := Fetch(t.Context(), s.URL, 2025, token)
			s.Close()
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("season index: HTTP %d", status)) || !strings.Contains(err.Error(), "--token-file") || strings.Contains(err.Error(), "invented-index") {
				t.Fatal("expected a sanitized index authorization hint")
			}
		}
	}
}

func TestSelectSessionNormalizesSlugs(t *testing.T) {
	const end = "2025-01-01T12:00:00"
	now := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	race := Session{Type: "Race", Name: "Race", EndDate: end, GmtOffset: "00:00:00", Path: "race/"}
	sprint := Session{Type: "Qualifying", Name: "Sprint Qualifying", EndDate: end, GmtOffset: "00:00:00", Path: "sprint/"}
	practice := Session{Type: "Free-Practice", Name: "Practice 1", EndDate: end, GmtOffset: "00:00:00", Path: "practice/"}
	ms := []Meeting{
		{Name: "Las Vegas Grand Prix", Sessions: []Session{race}},
		{Name: "Miami Grand Prix", Sessions: []Session{sprint}},
		{Name: "North_East Grand Prix", Sessions: []Session{practice}},
	}
	for _, tt := range []struct {
		selector string
		want     Match
	}{
		{"2025/las-vegas/race", Match{ms[0].Name, race}},
		{"2025/LAS_VEGAS/RACE", Match{ms[0].Name, race}},
		{"2025/las   vegas/race", Match{ms[0].Name, race}},
		{"2025/miami/sprint-qualifying", Match{ms[1].Name, sprint}},
		{"2025/miami/sprint_qualifying", Match{ms[1].Name, sprint}},
		{"2025/miami/sprint qualifying", Match{ms[1].Name, sprint}},
		{"2025/north east/free practice", Match{ms[2].Name, practice}},
	} {
		t.Run(tt.selector, func(t *testing.T) {
			q, err := ParseSelector(tt.selector)
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := selectSession(ms, q, now); !ok || got != tt.want {
				t.Fatalf("selectSession() = %+v, %v, want %+v", got, ok, tt.want)
			}
		})
	}
}
