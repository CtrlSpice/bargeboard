package jolpicareceiver

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func config() Config {
	c := *createDefaultConfig().(*Config)
	c.Enabled = true
	c.Season = 2025
	c.Round = 4
	c.IncludeSprint = true
	return c
}
func fixture(t *testing.T, k table) string {
	t.Helper()
	v, err := os.ReadFile("testdata/" + string(k) + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return string(v)
}
func TestParse(t *testing.T) {
	for k, want := range map[table][]row{
		race:   {{driverID: "elm", acronym: "ELM", team: "Copper Works", number: 17, position: 1, positionText: "1", points: 7.5, hasGrid: true, hasLaps: true, laps: 12, status: "Finished"}, {driverID: "ash", team: "Silver Works", number: 93, positionText: "R", grid: 2, laps: 3, hasGrid: true, hasLaps: true, status: "Engine"}},
		sprint: {{driverID: "elm", acronym: "ELM", team: "Copper Works", number: 17, position: 1, positionText: "1", points: 8, grid: 3, laps: 6, hasGrid: true, hasLaps: true, status: "Finished"}},
		wdc:    {{driverID: "elm", acronym: "ELM", number: 17, position: 1, positionText: "1", points: 35.5, wins: 2}, {driverID: "ash", positionText: "-"}},
		wcc:    {{teamID: "copper", team: "Copper Works", position: 1, positionText: "1", points: 42.5, wins: 3}},
	} {
		got, err := parse(config(), k, []byte(fixture(t, k)))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, %v", k, got, err)
		}
	}
}
func TestRejectMalformed(t *testing.T) {
	for _, tc := range []struct {
		k        table
		from, to string
	}{
		{race, `"f1"`, `"F1"`}, {race, `"offset":"0"`, `"offset":"1"`}, {race, `"100"`, `"1"`}, {race, `"total":"2"`, `"total":"3"`}, {race, `"limit":"100"`, `"limit":"0100"`}, {race, `"round":"4"`, `"round":"5"`}, {race, `"Races":[`, `"Races":null,"x":[`},
		{race, `"93"`, `"099"`}, {race, `"positionText":"1"`, `"positionText":"3"`}, {race, `"position":"1","positionText":"1"`, `"position":"2","positionText":"2"`}, {race, `"grid":"0"`, `"grid":"-1"`}, {race, `"laps":"3"`, `"laps":"3.0"`}, {race, `"ELM"`, `"eLm"`}, {race, `"Copper Works"`, `"\ud800"`}, {race, `"Engine"`, `"Bearer invented"`}, {race, `"Engine"`, `"https://example.test"`}, {race, `"Engine"`, `"\u0001"`}, {race, `"elm"`, `"É"`}, {race, `"elm"`, `"elm","driverId":"ash"`},
		{wdc, `"wins":"2"`, `"wins":"-1"`}, {wdc, `"17"`, `null`}, {wcc, `"42.5"`, `"4e2"`}, {wcc, `"copper"`, `""`},
	} {
		raw := strings.Replace(fixture(t, tc.k), tc.from, tc.to, 1)
		if rows, err := parse(config(), tc.k, []byte(raw)); err != errData || rows != nil {
			t.Errorf("%+v: %+v, %v", tc, rows, err)
		}
	}
	for _, s := range []string{"-1", "1e2", "NaN", " 1", "00", strings.Repeat("9", 310)} {
		if _, ok := parsePoints(s); ok {
			t.Errorf("accepted invalid points %q", s)
		}
	}
	for _, k := range []table{race, sprint, wdc, wcc} {
		if _, err := parse(config(), k, []byte(empty(k))); !errors.Is(err, errUnpublished) {
			t.Errorf("%s: %v", k, err)
		}
	}
}
func TestStatus(t *testing.T) {
	for s, want := range map[string]string{"Lapped": "lapped", "Engine fire": "mechanical", "Fatal accident": "collision_or_incident"} {
		if got := statusCategory(s); got != want {
			t.Errorf("%s: %s", s, got)
		}
	}
	for s, want := range map[string]string{"Finished": "finished", "+1 Lap": "lapped", "+12 Laps": "lapped", "+0 Laps": "other", "Engine": "mechanical", "Wheel nut": "mechanical", "Collision": "collision_or_incident", "Disqualified": "disqualified", "DNS": "did_not_start", "Did not qualify": "did_not_start", "Withdrew": "withdrawn", "Not classified": "not_classified", "Unspecified retirement": "other"} {
		if got := statusCategory(s); got != want {
			t.Errorf("%s: %s", s, got)
		}
	}
	if got, ok := display("  Collision\t damage "); !ok || got != "Collision damage" {
		t.Errorf("normalized status: %q %v", got, ok)
	}
}

func TestDisplayMarkers(t *testing.T) {
	for _, s := range []string{"http://x", "https://x", "x://y", "Authorization", "Access Token", "access_token", "access-token", "Cookie", "secret", "signature", "password", "credential", "Bearer x", "Basic x", "API key", "api_key", "api-key", "apikey"} {
		if _, ok := display(s); ok {
			t.Errorf("accepted %q", s)
		}
	}
	for _, s := range []string{"Copper/Silver Works", "Oil/water leak", "Token"} {
		if got, ok := display(s); !ok || got != s {
			t.Errorf("display(%q) = %q, %v", s, got, ok)
		}
	}
}

func TestSharedDrives(t *testing.T) {
	for _, k := range []table{race, sprint} {
		_, _, entries, _ := k.schema()
		base := strings.ReplaceAll(fixture(t, race), `"Results"`, `"`+entries+`"`)
		base = strings.NewReplacer(`"positionText":"R"`, `"position":"1","positionText":"1"`, `"7.5"`, `"0.14"`).Replace(base)
		for _, shared := range []string{strings.Replace(base, `"93"`, `"17"`, 1), strings.Replace(base, `"ash"`, `"elm"`, 1)} {
			rows, err := parse(config(), k, []byte(shared))
			if err != nil || len(rows) != 2 || rows[0].points != 0.14 || rows[0].position != 1 || rows[1].position != 1 {
				t.Fatalf("%s: %+v, %v", k, rows, err)
			}
		}
		dup := strings.NewReplacer(`"ash"`, `"elm"`, `"93"`, `"17"`).Replace(base)
		if _, err := parse(config(), k, []byte(dup)); err != errData {
			t.Errorf("%s duplicate: %v", k, err)
		}
	}
}

func TestStandingsIdentity(t *testing.T) {
	drivers := strings.Replace(fixture(t, wdc), `"positionText":"-"`, `"position":"1","positionText":"1"`, 1)
	teams := strings.NewReplacer(`"total":"1"`, `"total":"2"`, `"ConstructorStandings":[`, `"ConstructorStandings":[{"position":"1","positionText":"1","points":"42.5","wins":"3","Constructor":{"constructorId":"silver","name":"Copper Works"}},`).Replace(fixture(t, wcc))
	for _, tc := range []struct {
		k               table
		tied, duplicate string
	}{
		{wdc, drivers, strings.Replace(drivers, `"ash"`, `"elm"`, 1)},
		{wcc, teams, strings.NewReplacer(`"silver"`, `"copper"`, `"name":"Copper Works"}},`, `"name":"Silver Works"}},`).Replace(teams)},
	} {
		if rows, err := parse(config(), tc.k, []byte(tc.tied)); err != nil || len(rows) != 2 {
			t.Fatalf("%s tie: %v", tc.k, err)
		}
		if _, err := parse(config(), tc.k, []byte(tc.duplicate)); err != errData {
			t.Errorf("%s duplicate: %v", tc.k, err)
		}
	}
}
