package f1livetimingreceiver

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type sessionInfoFixture struct {
	Name    string          `json:"name"`
	Source  string          `json:"source"`
	Payload json.RawMessage `json:"payload"`
}

type sessionInfoFixtureExpectation struct {
	season      int64
	meetingKey  int64
	routeKey    int64
	sessionType canonicalSessionType
	sessionName canonicalSessionName
	startUTC    string
	endUTC      string
	utcOffset   time.Duration
}

func TestParseSessionInfoClassifiesPublicFixtures(t *testing.T) {
	expectations := map[string]sessionInfoFixtureExpectation{
		"2021_preseason_practice_1": {
			2021, 1087, 6394, canonicalSessionTypeTesting, canonicalSessionNameTestingDay1,
			"2021-03-12T07:00:00Z", "2021-03-12T16:00:00Z", 3 * time.Hour,
		},
		"2022_preseason_practice_2": {
			2022, 1132, 7175, canonicalSessionTypeTesting, canonicalSessionNameTestingDay2,
			"2022-03-11T07:00:00Z", "2022-03-11T16:00:00Z", 3 * time.Hour,
		},
		"2022_preseason_practice_3": {
			2022, 1132, 7176, canonicalSessionTypeTesting, canonicalSessionNameTestingDay3,
			"2022-03-12T07:00:00Z", "2022-03-12T16:00:00Z", 3 * time.Hour,
		},
		"2023_preseason_practice_1": {
			2023, 1140, 9222, canonicalSessionTypeTesting, canonicalSessionNameTestingDay1,
			"2023-02-23T07:00:00Z", "2023-02-23T16:30:00Z", 3 * time.Hour,
		},
		"2024_preseason_practice_2": {
			2024, 1228, 9463, canonicalSessionTypeTesting, canonicalSessionNameTestingDay2,
			"2024-02-22T07:00:00Z", "2024-02-22T16:00:00Z", 3 * time.Hour,
		},
		"2024_preseason_practice_3": {
			2024, 1228, 9464, canonicalSessionTypeTesting, canonicalSessionNameTestingDay3,
			"2024-02-23T07:00:00Z", "2024-02-23T16:00:00Z", 3 * time.Hour,
		},
		"2025_preseason_day_1": {
			2025, 1253, 9683, canonicalSessionTypeTesting, canonicalSessionNameTestingDay1,
			"2025-02-26T07:00:00Z", "2025-02-26T16:00:00Z", 3 * time.Hour,
		},
		"2026_preseason_day_2": {
			2026, 1304, 11466, canonicalSessionTypeTesting, canonicalSessionNameTestingDay2,
			"2026-02-12T07:00:00Z", "2026-02-12T16:00:00Z", 3 * time.Hour,
		},
		"2026_preseason_day_3": {
			2026, 1304, 11467, canonicalSessionTypeTesting, canonicalSessionNameTestingDay3,
			"2026-02-13T07:00:00Z", "2026-02-13T16:00:00Z", 3 * time.Hour,
		},
		"2021_abu_dhabi_practice_1_stream": {
			2021, 1107, 6594, canonicalSessionTypePractice, canonicalSessionNamePractice1,
			"2021-12-10T09:30:00Z", "2021-12-10T10:30:00Z", 4 * time.Hour,
		},
		"2021_abu_dhabi_practice_1_snapshot": {
			2021, 1107, 7165, canonicalSessionTypePractice, canonicalSessionNamePractice1,
			"2021-12-10T09:30:00Z", "2021-12-10T10:30:00Z", 4 * time.Hour,
		},
		"2021_abu_dhabi_practice_2": {
			2021, 1107, 6595, canonicalSessionTypePractice, canonicalSessionNamePractice2,
			"2021-12-10T13:00:00Z", "2021-12-10T14:00:00Z", 4 * time.Hour,
		},
		"2021_abu_dhabi_practice_3": {
			2021, 1107, 6596, canonicalSessionTypePractice, canonicalSessionNamePractice3,
			"2021-12-11T10:00:00Z", "2021-12-11T11:00:00Z", 4 * time.Hour,
		},
		"2020_imola_practice": {
			2020, 1057, 5905, canonicalSessionTypePractice, canonicalSessionNamePractice1,
			"2020-10-31T09:00:00Z", "2020-10-31T10:30:00Z", time.Hour,
		},
		"2021_abu_dhabi_qualifying": {
			2021, 1107, 6597, canonicalSessionTypeQualifying, canonicalSessionNameQualifying,
			"2021-12-11T13:00:00Z", "2021-12-11T14:00:00Z", 4 * time.Hour,
		},
		"2023_azerbaijan_sprint_shootout": {
			2023, 1207, 9278, canonicalSessionTypeSprintQualifying, canonicalSessionNameSprintQualifying,
			"2023-04-29T08:30:00Z", "2023-04-29T09:14:00Z", 4 * time.Hour,
		},
		"2024_austria_sprint_qualifying": {
			2024, 1239, 9545, canonicalSessionTypeSprintQualifying, canonicalSessionNameSprintQualifying,
			"2024-06-28T14:30:00Z", "2024-06-28T15:14:00Z", 2 * time.Hour,
		},
		"2021_britain_sprint_qualifying": {
			2021, 1072, 6425, canonicalSessionTypeSprint, canonicalSessionNameSprint,
			"2021-07-17T15:30:00Z", "2021-07-17T16:00:00Z", time.Hour,
		},
		"2023_azerbaijan_sprint": {
			2023, 1207, 9069, canonicalSessionTypeSprint, canonicalSessionNameSprint,
			"2023-04-29T13:30:00Z", "2023-04-29T14:00:00Z", 4 * time.Hour,
		},
		"2021_abu_dhabi_race": {
			2021, 1107, 6601, canonicalSessionTypeRace, canonicalSessionNameRace,
			"2021-12-12T13:00:00Z", "2021-12-12T15:00:00Z", 4 * time.Hour,
		},
	}

	contents, err := os.ReadFile("testdata/session_info/classification_cases.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures []sessionInfoFixture
	if err := json.Unmarshal(contents, &fixtures); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	if len(fixtures) != len(expectations) {
		t.Fatalf("fixture count = %d, want %d", len(fixtures), len(expectations))
	}

	parsed := make(map[string]sessionInfoParseResult, len(fixtures))
	seenNames := make(map[string]bool, len(fixtures))
	seenSources := make(map[string]bool, len(fixtures))
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			if seenNames[fixture.Name] {
				t.Fatalf("duplicate fixture name %q", fixture.Name)
			}
			seenNames[fixture.Name] = true
			expectation, ok := expectations[fixture.Name]
			if !ok {
				t.Fatalf("fixture has no independent expectation")
			}
			if !strings.HasPrefix(fixture.Source, "https://livetiming.formula1.com/static/") {
				t.Fatalf("fixture source = %q, want public static archive", fixture.Source)
			}
			if seenSources[fixture.Source] {
				t.Fatalf("duplicate fixture source %q", fixture.Source)
			}
			seenSources[fixture.Source] = true
			var sourceFields struct {
				Path string `json:"Path"`
			}
			if err := json.Unmarshal(fixture.Payload, &sourceFields); err != nil {
				t.Fatalf("decode fixture source fields: %v", err)
			}
			baseSource := "https://livetiming.formula1.com/static/" + sourceFields.Path + "SessionInfo."
			if fixture.Source != baseSource+"json" && fixture.Source != baseSource+"jsonStream" {
				t.Fatalf("fixture source %q does not match payload path %q", fixture.Source, sourceFields.Path)
			}

			got, err := parseSessionInfo(fixture.Payload)
			if err != nil {
				t.Fatalf("parseSessionInfo() error = %v", err)
			}
			want := sessionInfoParseResult{
				identity: sessionInfoIdentity{
					season:      expectation.season,
					meetingKey:  expectation.meetingKey,
					sessionType: expectation.sessionType,
					sessionName: expectation.sessionName,
				},
				identityAvailable: true,
				routeKey:          expectation.routeKey,
				routeAvailable:    true,
				schedule: sessionInfoSchedule{
					startUTC:  mustSessionInfoTime(t, expectation.startUTC),
					endUTC:    mustSessionInfoTime(t, expectation.endUTC),
					utcOffset: expectation.utcOffset,
				},
				scheduleAvailable: true,
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("parseSessionInfo() = %#v, want %#v", got, want)
			}
			parsed[fixture.Name] = got
		})
	}
	for name := range expectations {
		if !seenNames[name] {
			t.Errorf("missing fixture %q", name)
		}
	}

	stream := parsed["2021_abu_dhabi_practice_1_stream"]
	snapshot := parsed["2021_abu_dhabi_practice_1_snapshot"]
	if stream.identity != snapshot.identity || stream.schedule != snapshot.schedule {
		t.Fatal("Abu Dhabi correction fixture changed logical identity or schedule")
	}
	if stream.routeKey != 6594 || snapshot.routeKey != 7165 || stream.routeKey == snapshot.routeKey {
		t.Fatalf("Abu Dhabi route correction = %d to %d, want 6594 to 7165", stream.routeKey, snapshot.routeKey)
	}
}

func TestClassifySessionInfoRejectsNearMisses(t *testing.T) {
	tests := []struct {
		name        string
		season      int64
		meetingKey  int64
		meetingName string
		sourceType  string
		sourceName  string
	}{
		{name: "testing year below range", season: 2020, meetingName: "Pre-Season Test", sourceType: "Practice", sourceName: "Practice 1"},
		{name: "new testing name in first era", season: 2022, meetingName: "Pre-Season Testing", sourceType: "Practice", sourceName: "Practice 1"},
		{name: "old testing name in middle era", season: 2023, meetingName: "Pre-Season Test", sourceType: "Practice", sourceName: "Practice 1"},
		{name: "practice name in day era", season: 2025, meetingName: "Pre-Season Testing", sourceType: "Practice", sourceName: "Practice 1"},
		{name: "day name before day era", season: 2024, meetingName: "Pre-Season Testing", sourceType: "Practice", sourceName: "Day 1"},
		{name: "testing year above range", season: 2027, meetingName: "Pre-Season Testing", sourceType: "Practice", sourceName: "Day 1"},
		{name: "testing day zero", season: 2025, meetingName: "Pre-Season Testing", sourceType: "Practice", sourceName: "Day 0"},
		{name: "testing day four", season: 2025, meetingName: "Pre-Season Testing", sourceType: "Practice", sourceName: "Day 4"},
		{name: "testing day leading zero", season: 2025, meetingName: "Pre-Season Testing", sourceType: "Practice", sourceName: "Day 01"},
		{name: "testing meeting wrong case", season: 2025, meetingName: "Pre-season Testing", sourceType: "Practice", sourceName: "Day 1"},
		{name: "testing meeting trailing space", season: 2025, meetingName: "Pre-Season Testing ", sourceType: "Practice", sourceName: "Day 1"},
		{name: "testing source type wrong case", season: 2025, meetingName: "Pre-Season Testing", sourceType: "practice", sourceName: "Day 1"},
		{name: "testing source name wrong case", season: 2025, meetingName: "Pre-Season Testing", sourceType: "Practice", sourceName: "day 1"},
		{name: "grand prix wrong case", season: 2025, meetingName: "Example grand prix", sourceType: "Race", sourceName: "Race"},
		{name: "grand prix trailing space", season: 2025, meetingName: "Example Grand Prix ", sourceType: "Race", sourceName: "Race"},
		{name: "source type wrong case", season: 2025, meetingName: "Example Grand Prix", sourceType: "race", sourceName: "Race"},
		{name: "source name wrong case", season: 2025, meetingName: "Example Grand Prix", sourceType: "Race", sourceName: "race"},
		{name: "source name trailing space", season: 2025, meetingName: "Example Grand Prix", sourceType: "Race", sourceName: "Race "},
		{name: "imola wrong year", season: 2021, meetingKey: 1057, meetingName: "Example", sourceType: "Practice", sourceName: "Practice"},
		{name: "imola wrong meeting key", season: 2020, meetingKey: 1058, meetingName: "Example", sourceType: "Practice", sourceName: "Practice"},
		{name: "race-like sprint qualifying before 2021", season: 2020, meetingName: "Example Grand Prix", sourceType: "Race", sourceName: "Sprint Qualifying"},
		{name: "race-like sprint qualifying after 2021", season: 2022, meetingName: "Example Grand Prix", sourceType: "Race", sourceName: "Sprint Qualifying"},
		{name: "unknown broad type", season: 2025, meetingName: "Example Grand Prix", sourceType: "Sprint", sourceName: "Sprint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := classifySessionInfo(
				test.season,
				test.meetingKey,
				test.meetingName,
				test.sourceType,
				test.sourceName,
			)
			if ok || got != (sessionClassification{}) {
				t.Errorf("classifySessionInfo() = %#v, %t, want unresolved", got, ok)
			}
		})
	}
}

func TestClassifySessionInfoUsesOnlyExactGrandPrixSuffix(t *testing.T) {
	got, ok := classifySessionInfo(9999, 1, " Grand Prix", "Race", "Race")
	want := sessionClassification{canonicalSessionTypeRace, canonicalSessionNameRace}
	if !ok || got != want {
		t.Fatalf("classifySessionInfo() = %#v, %t, want %#v, true", got, ok, want)
	}
}

func TestParsePositiveCanonicalInt64(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  int64
		valid bool
	}{
		{name: "minimum", raw: `1`, want: 1, valid: true},
		{name: "ordinary", raw: `1057`, want: 1057, valid: true},
		{name: "structural whitespace", raw: " \t1057\n", want: 1057, valid: true},
		{name: "maximum", raw: `9223372036854775807`, want: 9223372036854775807, valid: true},
		{name: "empty", raw: ``},
		{name: "zero", raw: `0`},
		{name: "negative", raw: `-1`},
		{name: "leading plus", raw: `+1`},
		{name: "leading zero", raw: `01`},
		{name: "fraction", raw: `1.0`},
		{name: "exponent", raw: `1e3`},
		{name: "quoted", raw: `"1057"`},
		{name: "overflow", raw: `9223372036854775808`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := parsePositiveCanonicalInt64(json.RawMessage(test.raw))
			if got != test.want || valid != test.valid {
				t.Errorf("parsePositiveCanonicalInt64(%q) = %d, %t, want %d, %t", test.raw, got, valid, test.want, test.valid)
			}
		})
	}
}

func TestParseSessionInfoLocalTime(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  string
		valid bool
	}{
		{name: "minimum", raw: `"1000-01-01T00:00:00"`, want: "1000-01-01T00:00:00", valid: true},
		{name: "maximum", raw: `"9999-12-31T23:59:59"`, want: "9999-12-31T23:59:59", valid: true},
		{name: "leap year divisible by four", raw: `"2024-02-29T12:34:56"`, want: "2024-02-29T12:34:56", valid: true},
		{name: "leap year divisible by four hundred", raw: `"2000-02-29T12:34:56"`, want: "2000-02-29T12:34:56", valid: true},
		{name: "year below range", raw: `"0999-12-31T23:59:59"`},
		{name: "year zero", raw: `"0000-01-01T00:00:00"`},
		{name: "five digit year", raw: `"10000-01-01T00:00:00"`},
		{name: "century non-leap year", raw: `"1900-02-29T12:34:56"`},
		{name: "invalid month", raw: `"2025-13-01T00:00:00"`},
		{name: "invalid day", raw: `"2025-04-31T00:00:00"`},
		{name: "invalid hour", raw: `"2025-01-01T24:00:00"`},
		{name: "invalid minute", raw: `"2025-01-01T00:60:00"`},
		{name: "leap second", raw: `"2025-01-01T00:00:60"`},
		{name: "fraction", raw: `"2025-01-01T00:00:00.1"`},
		{name: "Zulu suffix", raw: `"2025-01-01T00:00:00Z"`},
		{name: "numeric offset", raw: `"2025-01-01T00:00:00+01:00"`},
		{name: "leading whitespace", raw: `" 2025-01-01T00:00:00"`},
		{name: "lowercase separator", raw: `"2025-01-01t00:00:00"`},
		{name: "non-string", raw: `20250101`},
		{name: "null", raw: `null`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := parseSessionInfoLocalTime(json.RawMessage(test.raw))
			if valid != test.valid {
				t.Fatalf("parseSessionInfoLocalTime(%q) valid = %t, want %t", test.raw, valid, test.valid)
			}
			if test.valid && got.Format("2006-01-02T15:04:05") != test.want {
				t.Errorf("parseSessionInfoLocalTime(%q) = %s, want %s", test.raw, got, test.want)
			}
			if !test.valid && !got.IsZero() {
				t.Errorf("parseSessionInfoLocalTime(%q) = %s, want zero", test.raw, got)
			}
		})
	}
}

func TestParseSessionInfoGMTOffset(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  time.Duration
		valid bool
	}{
		{name: "zero", raw: `"00:00:00"`, valid: true},
		{name: "positive fractional hour", raw: `"05:45:00"`, want: 5*time.Hour + 45*time.Minute, valid: true},
		{name: "positive maximum", raw: `"14:00:00"`, want: 14 * time.Hour, valid: true},
		{name: "negative fractional hour", raw: `"-00:30:00"`, want: -30 * time.Minute, valid: true},
		{name: "negative maximum", raw: `"-14:00:00"`, want: -14 * time.Hour, valid: true},
		{name: "leading plus", raw: `"+01:00:00"`},
		{name: "negative zero", raw: `"-00:00:00"`},
		{name: "above maximum", raw: `"14:01:00"`},
		{name: "hour overflow", raw: `"15:00:00"`},
		{name: "minute overflow", raw: `"01:60:00"`},
		{name: "nonzero seconds", raw: `"01:00:01"`},
		{name: "one digit hour", raw: `"1:00:00"`},
		{name: "whitespace", raw: `" 01:00:00"`},
		{name: "Zulu", raw: `"Z"`},
		{name: "non-string", raw: `3600`},
		{name: "null", raw: `null`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := parseSessionInfoGMTOffset(json.RawMessage(test.raw))
			if got != test.want || valid != test.valid {
				t.Errorf("parseSessionInfoGMTOffset(%q) = %s, %t, want %s, %t", test.raw, got, valid, test.want, test.valid)
			}
		})
	}
}

func TestParseSessionInfoValidatesBundlesIndependently(t *testing.T) {
	baseline := `{"Meeting":{"Key":1107,"Name":"Abu Dhabi Grand Prix"},"Key":6594,"Type":"Practice","Name":"Practice 1","StartDate":"2021-12-10T13:30:00","EndDate":"2021-12-10T14:30:00","GmtOffset":"04:00:00"}`
	replace := func(old, replacement string) json.RawMessage {
		t.Helper()
		result := strings.Replace(baseline, old, replacement, 1)
		if result == baseline {
			t.Fatalf("test prerequisite: %q was not present", old)
		}
		return json.RawMessage(result)
	}
	remove := func(value string) json.RawMessage { return replace(value, "") }
	withFields := func(payload json.RawMessage, fields string) json.RawMessage {
		return json.RawMessage(strings.TrimSuffix(string(payload), "}") + "," + fields + "}")
	}
	add := func(fields string) json.RawMessage {
		return withFields(json.RawMessage(baseline), fields)
	}

	baselineResult, err := parseSessionInfo(json.RawMessage(baseline))
	if err != nil {
		t.Fatalf("parse baseline: %v", err)
	}
	if !baselineResult.identityAvailable || !baselineResult.routeAvailable || !baselineResult.scheduleAvailable || baselineResult.issues != 0 {
		t.Fatalf("invalid test baseline: %#v", baselineResult)
	}

	tests := []struct {
		name          string
		payload       json.RawMessage
		identity      bool
		route         bool
		schedule      bool
		issues        sessionInfoIssueSet
		wantRouteKey  int64
		wantSeason    int64
		wantStartUTC  string
		wantEndUTC    string
		wantUTCOffset time.Duration
	}{
		{name: "complete descriptor", payload: json.RawMessage(baseline), identity: true, route: true, schedule: true},
		{name: "valid keyframe metadata", payload: add(`"_kf":true`), identity: true, route: true, schedule: true},
		{name: "invalid keyframe metadata", payload: add(`"_kf":false`), identity: true, route: true, schedule: true, issues: sessionInfoIssueKeyframe},
		{name: "duplicate keyframe metadata", payload: add(`"_kf":true,"_kf":true`), identity: true, route: true, schedule: true, issues: sessionInfoIssueKeyframe},
		{name: "missing route", payload: remove(`,"Key":6594`), identity: true, schedule: true, issues: sessionInfoIssueRoute},
		{name: "invalid route", payload: replace(`"Key":6594`, `"Key":0`), identity: true, schedule: true, issues: sessionInfoIssueRoute},
		{name: "duplicate route", payload: add(`"Key":7165`), identity: true, schedule: true, issues: sessionInfoIssueRoute},
		{name: "missing logical type", payload: remove(`,"Type":"Practice"`), route: true, schedule: true, issues: sessionInfoIssueIdentity},
		{name: "duplicate logical type", payload: add(`"Type":"Practice"`), route: true, schedule: true, issues: sessionInfoIssueIdentity},
		{name: "invalid meeting key", payload: replace(`"Key":1107`, `"Key":0`), route: true, schedule: true, issues: sessionInfoIssueIdentity},
		{name: "duplicate meeting key", payload: replace(`"Key":1107,"Name"`, `"Key":1107,"Key":1107,"Name"`), route: true, schedule: true, issues: sessionInfoIssueIdentity},
		{name: "unknown classification", payload: replace(`"Name":"Practice 1"`, `"Name":"Practice 4"`), route: true, schedule: true, issues: sessionInfoIssueClassification},
		{name: "missing end", payload: remove(`,"EndDate":"2021-12-10T14:30:00"`), identity: true, route: true, issues: sessionInfoIssueSchedule},
		{name: "missing offset", payload: remove(`,"GmtOffset":"04:00:00"`), identity: true, route: true, issues: sessionInfoIssueSchedule},
		{name: "invalid offset", payload: replace(`"GmtOffset":"04:00:00"`, `"GmtOffset":"+04:00:00"`), identity: true, route: true, issues: sessionInfoIssueSchedule},
		{name: "duplicate end", payload: add(`"EndDate":"2021-12-10T14:30:00"`), identity: true, route: true, issues: sessionInfoIssueSchedule},
		{name: "equal schedule", payload: replace(`"EndDate":"2021-12-10T14:30:00"`, `"EndDate":"2021-12-10T13:30:00"`), identity: true, route: true, issues: sessionInfoIssueSchedule},
		{name: "reversed schedule", payload: replace(`"EndDate":"2021-12-10T14:30:00"`, `"EndDate":"2021-12-10T12:30:00"`), identity: true, route: true, issues: sessionInfoIssueSchedule},
		{name: "invalid start affects identity and schedule", payload: replace(`"StartDate":"2021-12-10T13:30:00"`, `"StartDate":"2021-02-29T13:30:00"`), route: true, issues: sessionInfoIssueIdentity | sessionInfoIssueSchedule},
		{name: "path cannot repair invalid start", payload: withFields(replace(`"StartDate":"2021-12-10T13:30:00"`, `"StartDate":"2021-02-29T13:30:00"`), `"Path":"2021/2021-12-12_Abu_Dhabi_Grand_Prix/2021-12-10_Practice_1/"`), route: true, issues: sessionInfoIssueIdentity | sessionInfoIssueSchedule},
		{name: "number cannot repair unknown classification", payload: withFields(replace(`"Name":"Practice 1"`, `"Name":"Practice 4"`), `"Number":1`), route: true, schedule: true, issues: sessionInfoIssueClassification},
		{name: "meeting number cannot repair invalid meeting key", payload: replace(`"Key":1107,"Name"`, `"Key":0,"Number":1107,"Name"`), route: true, schedule: true, issues: sessionInfoIssueIdentity},
		{name: "duplicate unknown metadata", payload: add(`"Future":1,"Future":2`), identity: true, route: true, schedule: true},
		{name: "embedded metadata cannot repair identity", payload: json.RawMessage(`{"SessionStatus":"Started","ArchiveStatus":{"Status":"Complete"},"Number":1,"Path":"2021/example/"}`), issues: sessionInfoIssueIdentity | sessionInfoIssueRoute | sessionInfoIssueSchedule},
		{name: "empty object", payload: json.RawMessage(`{}`), issues: sessionInfoIssueIdentity | sessionInfoIssueRoute | sessionInfoIssueSchedule},
		{name: "null root", payload: json.RawMessage(`null`), issues: sessionInfoIssueShape},
		{name: "array root", payload: json.RawMessage(`[]`), issues: sessionInfoIssueShape},
		{name: "large numeric root", payload: json.RawMessage(`1e1000`), issues: sessionInfoIssueShape},
		{
			name:          "season stays local when UTC crosses year",
			payload:       json.RawMessage(`{"Meeting":{"Key":1,"Name":"Example Grand Prix"},"Key":2,"Type":"Race","Name":"Race","StartDate":"2021-01-01T00:30:00","EndDate":"2021-01-01T01:30:00","GmtOffset":"01:00:00"}`),
			identity:      true,
			route:         true,
			schedule:      true,
			wantRouteKey:  2,
			wantSeason:    2021,
			wantStartUTC:  "2020-12-31T23:30:00Z",
			wantEndUTC:    "2021-01-01T00:30:00Z",
			wantUTCOffset: time.Hour,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSessionInfo(test.payload)
			if err != nil {
				t.Fatalf("parseSessionInfo() error = %v", err)
			}
			if got.identityAvailable != test.identity || got.routeAvailable != test.route ||
				got.scheduleAvailable != test.schedule || got.issues != test.issues {
				t.Fatalf("parseSessionInfo() = %#v", got)
			}
			want := baselineResult
			want.issues = test.issues
			if !test.identity {
				want.identity = sessionInfoIdentity{}
				want.identityAvailable = false
			}
			if !test.route {
				want.routeKey = 0
				want.routeAvailable = false
			}
			if !test.schedule {
				want.schedule = sessionInfoSchedule{}
				want.scheduleAvailable = false
			}
			if test.wantStartUTC != "" {
				want = sessionInfoParseResult{
					identity: sessionInfoIdentity{
						season:      test.wantSeason,
						meetingKey:  1,
						sessionType: canonicalSessionTypeRace,
						sessionName: canonicalSessionNameRace,
					},
					identityAvailable: true,
					routeKey:          test.wantRouteKey,
					routeAvailable:    true,
					schedule: sessionInfoSchedule{
						startUTC:  mustSessionInfoTime(t, test.wantStartUTC),
						endUTC:    mustSessionInfoTime(t, test.wantEndUTC),
						utcOffset: test.wantUTCOffset,
					},
					scheduleAvailable: true,
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("parseSessionInfo() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestParseSessionInfoIsMemberOrderIndependent(t *testing.T) {
	first := json.RawMessage(`{"Meeting":{"Key":1107,"Name":"Abu Dhabi Grand Prix"},"Key":6594,"Type":"Practice","Name":"Practice 1","StartDate":"2021-12-10T13:30:00","EndDate":"2021-12-10T14:30:00","GmtOffset":"04:00:00"}`)
	second := json.RawMessage(`{"GmtOffset":"04:00:00","StartDate":"2021-12-10T13:30:00","Name":"Practice 1","Meeting":{"Name":"Abu Dhabi Grand Prix","Key":1107},"EndDate":"2021-12-10T14:30:00","Type":"Practice","Key":6594}`)
	firstResult, firstErr := parseSessionInfo(first)
	secondResult, secondErr := parseSessionInfo(second)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("parse errors = %v, %v", firstErr, secondErr)
	}
	if !reflect.DeepEqual(firstResult, secondResult) {
		t.Errorf("member order changed result: %#v != %#v", firstResult, secondResult)
	}
}

func TestParseSessionInfoRejectsInvalidNormalizedPayload(t *testing.T) {
	for _, payload := range []json.RawMessage{
		json.RawMessage(`{`),
		json.RawMessage{'{', '"', 0xff, '"', ':', '1', '}'},
	} {
		got, err := parseSessionInfo(payload)
		if !errors.Is(err, errInvalidNormalizedSessionInfo) {
			t.Errorf("parseSessionInfo(%q) error = %v", payload, err)
		}
		if got != (sessionInfoParseResult{}) {
			t.Errorf("parseSessionInfo(%q) result = %#v, want zero", payload, got)
		}
	}
}

func mustSessionInfoTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("invalid expected time %q: %v", value, err)
	}
	return parsed
}
