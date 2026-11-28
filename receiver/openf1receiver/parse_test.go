package openf1receiver

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

var race = f1id.SessionIdentity{Season: 2042, MeetingKey: 17, SessionType: "race", SessionName: "race"}

const session = `{"session_key":23,"meeting_key":17,"year":2042,"session_type":"Race","session_name":"Race","is_cancelled":false,"date_start":"2042-03-04T11:00:00+01:00","date_end":"2042-03-04T11:00:01Z"}`

func array(rows ...string) []byte { return []byte("[" + strings.Join(rows, ",") + "]") }
func driver(n int) string {
	return fmt.Sprintf(`{"session_key":23,"meeting_key":17,"driver_number":%d,"name_acronym":"SYN"}`, n)
}
func result(n, p, laps int, flag string) string {
	nullable := func(n int) string {
		if n < 0 {
			return "null"
		}
		return fmt.Sprint(n)
	}
	return fmt.Sprintf(`{"session_key":23,"meeting_key":17,"driver_number":%d,"position":%s,"number_of_laps":%s,"dnf":%t,"dns":%t,"dsq":%t,"points":"ignored","gap_to_leader":{},"duration":[]}`, n, nullable(p), nullable(laps), flag == "dnf", flag == "dns", flag == "dsq")
}
func change(row, key, raw string) string {
	var f map[string]json.RawMessage
	if err := json.Unmarshal([]byte(row), &f); err != nil {
		panic(err)
	}
	if raw == "" {
		delete(f, key)
	} else {
		f[key] = json.RawMessage(raw)
	}
	b, err := json.Marshal(f)
	if err != nil {
		panic(err)
	}
	return string(b)
}

var pair = map[int64]string{1: "ONE", 2: "TWO"}

func TestSchedulePrecision(t *testing.T) {
	start := `"2042-03-04T11:00:00.1234567891+01:00"`
	for end, want := range map[string]bool{
		`"2042-03-04T10:00:00.1234567892Z"`:      true,
		`"2042-03-04T10:00:00.123456789100Z"`:    false,
		`"2042-03-04T10:00:00.12345678909Z"`:     false,
		`"2042-03-04T10:00:00.123456789xZ"`:      false,
		`"2042-03-04T11:00:00.1234567892+01:60"`: false,
		`"2042-02-30T10:00:00.1234567892Z"`:      false,
	} {
		_, d := parse(array(change(change(session, "date_start", start), "date_end", end)), sessions, race, 23, nil)
		if (d == "") != want {
			t.Fatal(end, d)
		}
	}
}

func TestRootDriversValidation(t *testing.T) {
	numbers := make([]int64, 33)
	for i := range numbers {
		numbers[i] = int64(i + 1)
	}
	c := NewFactory().CreateDefaultConfig().(*Config)
	for _, bad := range [][]int64{{0}, {-1}, {2, 1, 2}, numbers} {
		c.RootDrivers = bad
		if c.Validate() == nil {
			t.Fatal(bad)
		}
	}
	c.RootDrivers = numbers[:32]
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionIdentity(t *testing.T) {
	for _, name := range []string{"Race", "Sprint"} {
		e := race
		e.SessionName = f1id.SessionName(strings.ToLower(name))
		e.SessionType = f1id.SessionType(e.SessionName)
		if _, d := parse(array(change(session, "session_name", fmt.Sprintf("%q", name))), sessions, e, 23, nil); d != "" {
			t.Fatal(name, d)
		}
	}
	checkBad := func(b []byte) {
		t.Helper()
		if _, d := parse(b, sessions, race, 23, nil); d == "" {
			t.Errorf("accepted %s", b)
		}
	}
	for key, values := range map[string][]string{
		"session_key": {"0", "24", "23.0", `"23"`}, "meeting_key": {"18", "17e0"}, "year": {"2041", "2042.0"},
		"session_name": {`"Sprint"`, `"race"`, `"\ud800"`}, "session_type": {`"race"`, `"Sprint"`}, "is_cancelled": {"true", "null", "0"},
		"date_start": {`"2042-03-04T10:00:00"`}, "date_end": {`"2042-03-04T10:00:00Z"`, `"2042-03-04T09:59:59Z"`},
	} {
		for _, v := range append(values, "") {
			checkBad(array(change(session, key, v)))
		}
	}
	for _, b := range [][]byte{array(session, session), []byte("{}"), []byte("[] []"), []byte("[null]"), []byte("["), array(strings.Replace(session, `"year":`, `"year":2042,"year":`, 1))} {
		checkBad(b)
	}
	for _, stage := range []string{sessions, drivers, results} {
		p, d := parse(array(), stage, race, 23, nil)
		if d != "" || !p.empty {
			t.Fatal(stage, p, d)
		}
	}
}
func TestDriverRegistry(t *testing.T) {
	for _, n := range []int{1, 32, 33} {
		rows := make([]string, n)
		expected := map[int64]string{}
		for i := range rows {
			rows[i] = driver(i + 1)
			expected[int64(i+1)] = "SYN"
		}
		for _, registry := range []map[int64]string{nil, expected} {
			p, d := parse(array(rows...), drivers, race, 23, registry)
			if n <= 32 && (d != "" || !reflect.DeepEqual(p.drivers, expected)) || n == 33 && d != DiagIdentity {
				t.Fatal(n, p, d)
			}
		}
	}
	for _, rows := range [][]string{{driver(1), driver(1)}, {driver(2)}, {driver(1), driver(2)}, {"null"}} {
		if _, d := parse(array(rows...), drivers, race, 23, map[int64]string{1: "SYN"}); d == "" {
			t.Fatal(rows)
		}
	}
	for key, values := range map[string][]string{"driver_number": {"0", "1.1", "9223372036854775808"}, "meeting_key": {"18"}, "session_key": {"22"}, "name_acronym": {`"B"`, `"lower"`, `"ABCDE"`, `"É"`, `"\udfff"`, "null"}} {
		for _, v := range append(values, "") {
			if _, d := parse(array(change(driver(1), key, v)), drivers, race, 23, map[int64]string{1: "SYN"}); d == "" {
				t.Fatal(key, v)
			}
		}
	}
}
func TestAtomicResults(t *testing.T) {
	winner := result(1, 1, 10, "")
	for _, tc := range []struct {
		p, l int
		flag string
		out  Outcome
	}{
		{2, 1, "", OutcomeFinished}, {2, 0, "dnf", OutcomeClassifiedDNF}, {-1, 0, "dnf", OutcomeUnclassifiedDNF}, {-1, 3, "dnf", OutcomeUnclassifiedDNF}, {-1, 0, "dns", OutcomeDNS}, {-1, -1, "dsq", OutcomeDSQ}, {2, 0, "dsq", OutcomeDSQ}, {-1, 1, "", OutcomeUnresolved},
		{2, 0, "", ""}, {-1, 0, "", ""}, {-1, -1, "dnf", ""}, {2, 0, "dns", ""}, {-1, 1, "dns", ""}, {-1, -1, "dns", ""}, {3, 1, "", ""}, {0, 1, "", ""}, {1, 3, "", ""},
	} {
		p, d := parse(array(result(2, tc.p, tc.l, tc.flag), winner), results, race, 23, pair)
		if tc.out == "" {
			if d != DiagValidation || p.rows != nil {
				t.Fatal(tc, p, d)
			}
			continue
		}
		want := []Row{{1, "ONE", 1, OutcomeFinished}, {2, "TWO", int64(max(0, tc.p)), tc.out}}
		if d != "" || !reflect.DeepEqual(p.rows, want) {
			t.Fatal(tc, p, d)
		}
	}
	second := result(2, 2, 1, "dnf")
	for key, vs := range map[string][]string{"session_key": {"24"}, "meeting_key": {"18"}, "driver_number": {"1", "3"}, "position": {"2.0", "-1"}, "number_of_laps": {"1e0", "-1", "9223372036854775808"}, "dsq": {"true", "null"}, "dns": {"true", `"false"`}, "dnf": {"0"}} {
		for _, v := range append(vs, "") {
			if p, d := parse(array(winner, change(second, key, v)), results, race, 23, pair); d == "" || p.rows != nil {
				t.Fatal(key, v, p, d)
			}
		}
	}
	for _, b := range [][]byte{array(winner), array(result(1, -1, 4, ""), second), array(result(1, 1, 1, "dnf"), second), array(result(1, 1, -1, "dsq"), second)} {
		if _, d := parse(b, results, race, 23, pair); d == "" {
			t.Fatal(string(b))
		}
	}
}

func TestConfiguration(t *testing.T) {
	for s, want := range map[string]time.Duration{"": 5 * time.Minute, "1s": time.Second, "30m": 30 * time.Minute, "1800s": 30 * time.Minute, "01m": 0, "0s": 0, "1m30s": 0, "+1s": 0, "-1s": 0, "1.0s": 0, "1M": 0, " 5m": 0, "5m ": 0, "31m": 0, "1801s": 0, "9223372036854775808s": 0} {
		got, err := ParseFinalisedGrace(s)
		if got != want || (err != nil) != (want == 0) {
			t.Fatal(s, got, err)
		}
	}
	c := NewFactory().CreateDefaultConfig().(*Config)
	if c.Enabled || len(c.RootDrivers) != 0 || c.Endpoint != DefaultEndpoint || c.FinalisedGrace != "5m" || c.Validate() != nil {
		t.Fatal(c)
	}
	c.Enabled = true
	c.Season = 2042
	c.SessionKey = 23
	c.MeetingKey = 17
	for _, n := range []f1id.SessionName{"race", "sprint", "practice_1", "qualifying", "Race"} {
		c.SessionName = n
		if (c.Validate() == nil) != (n == "race" || n == "sprint") {
			t.Fatal(n)
		}
	}
	c.SessionName = "race"
	for _, modify := range []func(*Config){func(c *Config) { c.SessionKey = -1 }, func(c *Config) { c.MeetingKey = 0 }, func(c *Config) { c.Season = 999 }, func(c *Config) { c.Season = 10000 }, func(c *Config) { c.Drivers = []DriverConfig{{1, "A"}, {1, "B"}} }, func(c *Config) { c.Endpoint = "https://user@private.test/v1" }} {
		v := *c
		modify(&v)
		if v.Validate() == nil {
			t.Fatal(v)
		}
	}
}
