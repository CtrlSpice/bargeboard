package f1staticreceiver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestInitialRadioArray(t *testing.T) {
	const capture = `{"Path":"TeamRadio/FIG_17_20250310_100000.mp3","RacingNumber":"17"}`
	s := session{e: &effects{}}
	send := func(raw string) {
		s.publications("TeamRadio", object([]byte(`{"Captures":`+raw+`}`)), time.Unix(100, 0))
	}
	send(`[` + capture + `,{},` + capture + `]`)
	send(`[{}, {}, {}, ` + capture + `]`)
	send(`{"1":` + capture + `,"4":` + capture + `}`)
	var indexes []any
	for _, l := range s.e.logs {
		indexes = append(indexes, l.a["f1.team_radio.index"])
	}
	if !reflect.DeepEqual(indexes, []any{int64(0), int64(2), int64(4)}) {
		t.Fatal(indexes)
	}
	s = session{e: &effects{}}
	send(`[` + strings.Repeat(`{},`, 4096) + capture + `]`)
	if !s.records.radio.overflow || len(s.records.radio.rows) != 0 || len(s.e.logs) != 0 {
		t.Fatal("overflow was not atomic")
	}
}

func TestInvalidCountIsolatesLapFields(t *testing.T) {
	now := time.Unix(100, 0)
	for _, n := range []string{`"3"`, `0`, `1`, `5`} {
		s := session{}
		d := driver{lap: &lap{span: &span{fact: fact{at: now}}, number: 3}, pending: &lap{span: &span{fact: fact{at: now}}, number: 2, boundary: now, reported: time.Second}}
		s.lapTiming(&d, object([]byte(`{"NumberOfLaps":`+n+`,"LastLapTime":{"Value":"9.000","PersonalFastest":true},"Sectors":[{"Value":"1.000"}]}`)), now, false)
		if d.pending.reported != time.Second || d.pending.personal || d.lap.sectors[0] != 0 || d.pending.sectors[0] != 0 {
			t.Fatal(n)
		}
	}
}

func TestConflictingLapKeepsSectorMeasurements(t *testing.T) {
	for _, boundary := range []int64{2e9, 6e9} {
		e := reduceFixture(t, map[string][]StreamRecord{
			"SessionData": {{0, `{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`}},
			"TimingData":  {{0, `{"Lines":{"17":{"NumberOfLaps":1}}}`}, {boundary, `{"Lines":{"17":{"NumberOfLaps":2,"LastLapTime":{"Value":"5.999"},"Sectors":[{"Value":"1.000"},{"Value":"2.000"},{"Value":"3.000"}]}}}`}},
		})
		var gauges, hist []float64
		for _, p := range e.points {
			switch p.name {
			case "f1.driver.last_sector_time":
				gauges = append(gauges, p.value.(float64))
			case "f1.driver.sector_time":
				if p.histogram.count != 1 {
					t.Fatal(p)
				}
				hist = append(hist, p.histogram.sum)
			case "f1.driver.last_lap_time", "f1.driver.lap_time":
				t.Fatal("accepted conflicting lap")
			}
		}
		for _, values := range [][]float64{gauges, hist} {
			if !reflect.DeepEqual(values, []float64{1, 2, 3}) {
				t.Fatal(boundary, values)
			}
		}
	}
}

func TestSparseBestLapAuthority(t *testing.T) {
	s := session{part: 1, e: &effects{}}
	s.p.Identity.SessionType = f1id.SessionTypeQualifying
	d := driver{phase: f1id.PhaseQ1, root: &span{}, values: map[string]any{}}
	for _, raw := range []string{`{"BestLapTime":{"Value":"4.000"}}`, `{"BestLapTimes":[{"Value":"2.000"},{"Value":"3.000"}]}`, `{"BestLapTime":{"Value":"4.000"}}`} {
		s.bestLapTiming(&d, object([]byte(raw)), time.Unix(100, 0))
	}
	if len(d.root.events) != 0 || len(s.e.points) != 2 || s.e.points[0].value != 4. || s.e.points[1].value != 3. || s.e.points[1].a["f1.session.phase"] != "q2" {
		t.Fatal(s.e.points, d.root.events)
	}
}

func TestStaticLateEndsOwnsRaceAndSprintClosure(t *testing.T) {
	for _, typ := range []f1id.SessionType{f1id.SessionTypeRace, f1id.SessionTypeSprint} {
		p, err := freeze([]byte(info), []byte(drivers), map[string][]StreamRecord{
			"SessionData": {
				{0, `{"StatusSeries":[{"Utc":"2025-03-10T10:00:00Z","SessionStatus":"Started"}]}`},
				{1e9, `{"StatusSeries":{"1":{"Utc":"2025-03-10T10:00:01Z","SessionStatus":"Finalised"}}}`},
				{400e9, `{"StatusSeries":{"2":{"Utc":"2025-03-10T10:06:00Z","SessionStatus":"Ends"}}}`},
			},
			"WeatherData": {{310e9, `{"WindDirection":"2"}`}},
		})
		if err != nil {
			t.Fatal(err)
		}
		p.Identity.SessionType = typ
		out := reduce(p)
		for _, e := range out[:len(out)-1] {
			if len(e.spans) != 0 {
				t.Fatalf("%s exported before Ends", typ)
			}
		}
		spans := out[len(out)-1].spans
		if len(spans) != 1 || spans[0].end.Sub(spans[0].at) != 6*time.Minute {
			t.Fatalf("%s: wrong root end: %v", typ, spans)
		}
	}
}

func TestPitOccupiedSlotsAndMalformedLap(t *testing.T) {
	for _, kind := range []int{0, 1} {
		s := session{start: time.Unix(100, 0), e: &effects{}}
		d := driver{visits: []pitVisit{{out: 3}, {out: 3}}}
		send := func(value, lap string) {
			s.acceptPit(&d, kind, object([]byte(fmt.Sprintf(`{"PitStopTime":%q,"Duration":%q,"Lap":%q}`, value, value, lap))), s.start)
		}
		send("2.0", "03")
		if !s.e.issues["invalid pit report lap"] || len(s.pitSamples[kind]) != kind {
			t.Fatal(kind, s.e)
		}
		send("2.0", "")
		send("2.1", "3")
		send("2.2", "03")
		if len(s.pitSamples[kind]) != 1 || s.pitSamples[kind][0].value != 2*time.Second || d.visits[1].lane != 0 || d.visits[1].stop != 0 || !s.e.issues["pit report correction"] {
			t.Fatal(kind, d.visits, s.pitSamples)
		}
	}
}

func TestGapFreshnessAfterPublicationRegression(t *testing.T) {
	at := time.Unix(100, 0)
	leader := &driver{position: 1}
	d := &driver{position: 2}
	s := session{drivers: map[string]*driver{"1": leader, "2": d}, keys: []string{"1", "2"}, coord: 30e9, e: &effects{}}
	s.p.Identity.SessionType = f1id.SessionTypeRace
	s.elapsed(object([]byte(`{"2":{"GapToLeader":"+2.000"}}`)), at)
	l := lap{span: &span{fact: fact{at: at.Add(30 * time.Second), a: attrs{}}}, startCoord: 30e9, boundaryCoord: 45e9}
	d.boundaryGaps(&l, at.Add(45*time.Second))
	if l.a["f1.lap.gap_to_leader.start_seconds"] != 2. || l.a["f1.lap.gap_to_leader.end_seconds"] != nil {
		t.Fatal(l.a)
	}
}

func TestRepairReasonTarget(t *testing.T) {
	for reason, want := range map[string]int64{"TRACK LIMITS AT TURN 9 LAP 3 2:03:04": 3, "TRACK LIMITS AT TURN 9 LAP 4 (NEXT LAP)": 4, "TRACK LIMITS AT PIT ENTRY LAP 5 2:03:04": 5, "DOUBLE YELLOW AT TURN 2 LAP 6 2:03:04": 6, "TRACK LIMITS AT TURN 3 (NEXT LAP)": 0, "DOUBLE YELLOW AT TURN 4": 0, "TRACK LIMITS AT TURN 9 (NEXT LAP) LAP 2": -1} {
		r, ok := parseRepair("CAR 17 (FIG) TIME 1:02.034 DELETED - " + reason)
		if ok != (want >= 0) || ok && r.label != want {
			t.Fatal(reason, r, ok)
		}
	}
}
