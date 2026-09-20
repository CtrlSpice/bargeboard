package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Fixtures and mutations are independently authored synthetic boundary probes.
// Their contract and research references are in testdata/session_info/SOURCES.md.
func sessionInfoUnicodeFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/session_info/classification_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []sessionInfoFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		if fixture.Name == name {
			if !fixture.Synthetic {
				t.Fatal("fixture must be marked synthetic")
			}
			return fixture.Payload
		}
	}
	t.Fatalf("missing fixture %s", name)
	return nil
}

func mutateSessionInfoUnicode(t *testing.T, raw json.RawMessage, fields ...string) json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if len(fields) == 3 {
		object[fields[0]] = mutateSessionInfoUnicode(t, object[fields[0]], fields[1:]...)
	} else {
		object[fields[0]] = json.RawMessage(fields[1])
	}
	result, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSessionInfoUnicodeGrandPrixRegression(t *testing.T) {
	raw := sessionInfoUnicodeFixture(t, "2021_example_practice_1_initial")
	raw = mutateSessionInfoUnicode(t, raw, "Meeting", "Name", `"\uD800 Grand Prix"`)
	before := bytes.Clone(raw)
	got, err := parseSessionInfo(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := sessionInfoUnicodeExpected(t, sessionInfoIssueUnicode|sessionInfoIssueIdentity)
	assertSessionInfoUnicodeParse(t, got, want)
	if !bytes.Equal(raw, before) {
		t.Fatal("parser mutated source bytes")
	}
}

func sessionInfoUnicodeExpected(t *testing.T, issues sessionInfoIssueSet) sessionInfoParseResult {
	t.Helper()
	state := expectedSessionInfoBatchReductionA(t).state
	want := sessionInfoParseResult{
		identity: state.identity, identityAvailable: true,
		routeKey: state.routeKey, routeAvailable: true,
		schedule: state.schedule, scheduleAvailable: true,
		issues: issues,
	}
	if issues&(sessionInfoIssueIdentity|sessionInfoIssueClassification) != 0 {
		want.identity, want.identityAvailable = sessionInfoIdentity{}, false
	}
	if issues&sessionInfoIssueRoute != 0 {
		want.routeKey, want.routeAvailable = 0, false
	}
	if issues&sessionInfoIssueSchedule != 0 {
		want.schedule, want.scheduleAvailable = sessionInfoSchedule{}, false
	}
	return want
}

func assertSessionInfoUnicodeParse(t *testing.T, got, want sessionInfoParseResult) {
	t.Helper()
	if got != want {
		t.Fatalf("parse result = %#v, want %#v", got, want)
	}
}

func TestSessionInfoUnicodeStringsAndIndependentBundles(t *testing.T) {
	fixture := sessionInfoUnicodeFixture(t, "2021_example_practice_1_initial")
	tests := []struct {
		name   string
		fields []string
		issues sessionInfoIssueSet
	}{
		{"meeting", []string{"Meeting", "Name", `"\uD800 Grand Prix"`}, sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"type", []string{"Type", `"Prac\udfff tice"`}, sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"name", []string{"Name", `"Practice 1\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"start", []string{"StartDate", `"2021-05-04T10:15:0\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueIdentity | sessionInfoIssueSchedule},
		{"end", []string{"EndDate", `"2021-05-04T11:45:0\udc00"`}, sessionInfoIssueUnicode | sessionInfoIssueSchedule},
		{"offset", []string{"GmtOffset", `"02:00:0\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueSchedule},
		{"route remains integer grammar", []string{"Key", `"\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueRoute},
		{"meeting key remains integer grammar", []string{"Meeting", "Key", `"\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"keyframe", []string{"_kf", `"\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueKeyframe},
		{"literal replacement", []string{"Meeting", "Name", `"� Grand Prix"`}, 0},
		{"escaped replacement", []string{"Meeting", "Name", `"\uFFFD Grand Prix"`}, 0},
		{"accented", []string{"Meeting", "Name", `"Exémple Grand Prix"`}, 0},
		{"paired", []string{"Meeting", "Name", `"\uD83D\uDE80 Grand Prix"`}, 0},
		{"literal escape", []string{"Meeting", "Name", `"\\uD800 Grand Prix"`}, 0},
		{"escaped type", []string{"Type", `"\u0050ractice"`}, 0},
		{"escaped name", []string{"Name", `"Practice \u0031"`}, 0},
		{"replacement type is scalar valid", []string{"Type", `"�Practice"`}, sessionInfoIssueClassification},
		{"replacement name is scalar valid", []string{"Name", `"\ufffdPractice 1"`}, sessionInfoIssueClassification},
		{"paired start is not ASCII date", []string{"StartDate", `"2021-05-04T10:15:\ud83d\ude80"`}, sessionInfoIssueIdentity | sessionInfoIssueSchedule},
		{"replacement end is not ASCII date", []string{"EndDate", `"2021-05-04T11:45:�"`}, sessionInfoIssueSchedule},
		{"replacement offset is not ASCII offset", []string{"GmtOffset", `"02:00:\ufffd"`}, sessionInfoIssueSchedule},
		{"escaped route digits remain quoted", []string{"Key", `"\u0031\u0030\u0031"`}, sessionInfoIssueRoute},
		{"route exponent", []string{"Key", `101e0`}, sessionInfoIssueRoute},
		{"unknown path value", []string{"Path", `"\ud800"`}, 0},
		{"unknown nested value", []string{"Meeting", "Location", `{"\ud800":"\udfff"}`}, 0},
		{"unsupported type object stays opaque", []string{"Type", `{"\ud800":"\udfff"}`}, sessionInfoIssueIdentity},
		{"null type", []string{"Type", `null`}, sessionInfoIssueIdentity},
		{"null meeting", []string{"Meeting", `null`}, sessionInfoIssueIdentity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := mutateSessionInfoUnicode(t, fixture, test.fields...)
			before := bytes.Clone(raw)
			got, err := parseSessionInfo(raw)
			if err != nil {
				t.Fatal(err)
			}
			assertSessionInfoUnicodeParse(t, got, sessionInfoUnicodeExpected(t, test.issues))
			if !bytes.Equal(raw, before) {
				t.Fatal("source bytes changed")
			}
		})
	}
}

func TestSessionInfoUnicodeTestingAndSpecialPracticeCannotBypassValidation(t *testing.T) {
	for _, test := range []struct {
		fixtureName string
		routeKey    int64
		startUTC    string
		endUTC      string
		utcOffset   time.Duration
	}{
		{"2021_preseason_practice_1", 201, "2021-01-11T08:15:00Z", "2021-01-11T11:45:00Z", time.Hour},
		{"2025_preseason_day_1", 207, "2025-01-11T08:45:00Z", "2025-01-11T12:15:00Z", 30 * time.Minute},
		{"2020_special_practice", 301, "2020-08-17T10:20:00Z", "2020-08-17T11:40:00Z", -time.Hour},
	} {
		fixture := sessionInfoUnicodeFixture(t, test.fixtureName)
		for _, fields := range [][]string{
			{"Meeting", "Name", `"\ud800 Grand Prix"`},
			{"Type", `"Practice\ud800"`},
			{"Name", `"Practice\ud800"`},
		} {
			t.Run(test.fixtureName+"/"+strings.Join(fields[:len(fields)-1], "."), func(t *testing.T) {
				got, err := parseSessionInfo(mutateSessionInfoUnicode(t, fixture, fields...))
				if err != nil {
					t.Fatal(err)
				}
				want := sessionInfoParseResult{
					routeKey:       test.routeKey,
					routeAvailable: true,
					schedule: sessionInfoSchedule{
						startUTC:  mustSessionInfoTime(t, test.startUTC),
						endUTC:    mustSessionInfoTime(t, test.endUTC),
						utcOffset: test.utcOffset,
					},
					scheduleAvailable: true,
					issues:            sessionInfoIssueUnicode | sessionInfoIssueIdentity,
				}
				assertSessionInfoUnicodeParse(t, got, want)
			})
		}
	}
}

func TestSessionInfoUnicodeRawKeysAndOccurrenceUnion(t *testing.T) {
	// Raw edits to the synthetic descriptor preserve duplicate/key token spellings
	// that maps would lose.
	base := sessionInfoBatchDescriptorA
	add := func(fields string) string { return base[:len(base)-1] + "," + fields + "}" }
	replace := func(old, next string) string {
		t.Helper()
		if !strings.Contains(base, old) {
			t.Fatalf("missing mutation target %s", old)
		}
		return strings.Replace(base, old, next, 1)
	}
	tests := []struct {
		name, raw string
		issues    sessionInfoIssueSet
	}{
		{"escaped root", replace(`"Type"`, `"\u0054ype"`), 0},
		{"escaped meeting", replace(`"Meeting"`, `"\u004deeting"`), 0},
		{"escaped nested", replace(`"Key":21`, `"\u004bey":21`), 0},
		{"duplicate type", add(`"\u0054ype":"Practice"`), sessionInfoIssueIdentity},
		{"duplicate route", add(`"\u004bey":101`), sessionInfoIssueRoute},
		{"duplicate end", add(`"\u0045ndDate":"2021-05-04T11:45:00"`), sessionInfoIssueSchedule},
		{"duplicate meeting name", replace(`"Name":"Example Grand Prix"`, `"Name":"Example Grand Prix","\u004eame":"Example Grand Prix"`), sessionInfoIssueIdentity},
		{"duplicate bad later scalar", add(`"\u0054ype":"\ud800"`), sessionInfoIssueIdentity | sessionInfoIssueUnicode},
		{"duplicate bad earlier scalar", replace(`"Type":"Practice"`, `"Type":"\ud800","\u0054ype":"Practice"`), sessionInfoIssueIdentity | sessionInfoIssueUnicode},
		{"duplicate meeting still reports", add(`"Meeting":{"Name":"\ud800"}`), sessionInfoIssueIdentity | sessionInfoIssueUnicode},
		{"duplicate earlier meeting still reports", `{"Meeting":{"Name":"\ud800"},` + base[1:], sessionInfoIssueIdentity | sessionInfoIssueUnicode},
		{"unknown scalar keys", add(`"\ud800":1,"\udfff":2,"�":3,"\ufffd":4`), sessionInfoIssueUnicode},
		{"nested unknown scalar key", replace(`"Key":21`, `"\ud800":{"Name":"\udfff"},"Key":21`), sessionInfoIssueUnicode},
		{"required root key absent", replace(`"Type"`, `"\ud800Type"`), sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"required nested key absent", replace(`"Name":"Example Grand Prix"`, `"\ud800Name":"Example Grand Prix"`), sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"bad unknown key plus bad recognized value", strings.Replace(add(`"\ud800":1`), `"GmtOffset":"02:00:00"`, `"GmtOffset":"\udfff"`, 1), sessionInfoIssueUnicode | sessionInfoIssueSchedule},
		{"unknown duplicate values opaque", add(`"Future":"\ud800","Future":{"\udfff":"\ud800"}`), 0},
		{"delete metadata remains ignored", add(`"_deleted":["\ud800"],"_kf":true`), 0},
		{"keyframe escaped duplicate", add(`"_kf":true,"\u005fkf":true`), sessionInfoIssueKeyframe},
		{"all applicable object issues", `{"\ud800":0,"Key":0,"Meeting":{"Key":21,"Name":"\ud800 Grand Prix"},"_kf":false}`, sessionInfoIssueUnicode | sessionInfoIssueIdentity | sessionInfoIssueRoute | sessionInfoIssueSchedule | sessionInfoIssueKeyframe},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSessionInfo(json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			assertSessionInfoUnicodeParse(t, got, sessionInfoUnicodeExpected(t, test.issues))
		})
	}
}

func TestSessionInfoUnicodeLosslessStringAndWholePayloadDepth(t *testing.T) {
	for _, test := range []struct {
		raw, want string
		valid     bool
	}{
		{`"\ud800"`, "", false}, {`"\udfff"`, "", false},
		{`"�"`, "�", true}, {`"\ufffd"`, "�", true},
		{`"\uD83D\uDE80"`, "🚀", true}, {`"Exémple"`, "Exémple", true},
		{`"\\uD800"`, `\uD800`, true}, {`null`, "", false},
	} {
		got, valid, issues := parseJSONString(json.RawMessage(test.raw))
		var wantIssues sessionInfoIssueSet
		if !test.valid && test.raw != "null" {
			wantIssues = sessionInfoIssueUnicode
		}
		if got != test.want || valid != test.valid || issues != wantIssues {
			t.Fatalf("lossless string result = %q/%t, want %q/%t", got, valid, test.want, test.valid)
		}
	}
	for _, nested := range []bool{false, true} {
		for _, excess := range []int{0, 1} {
			t.Run(fmt.Sprintf("nested=%t/excess=%d", nested, excess), func(t *testing.T) {
				depth := 9999 + excess
				if nested {
					depth--
				}
				value := strings.Repeat("[", depth) + `"\ud800"` + strings.Repeat("]", depth)
				base := sessionInfoBatchDescriptorA
				raw := base[:len(base)-1] + `,"Future":` + value + `}`
				if nested {
					raw = strings.Replace(base, `"Key":21`, `"Future":`+value+`,"Key":21`, 1)
				}
				before := []byte(raw)
				got, err := parseSessionInfo(before)
				if excess == 1 {
					if !errors.Is(err, errInvalidNormalizedSessionInfo) || got != (sessionInfoParseResult{}) {
						t.Fatalf("over-depth result must be zero with invariant error")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					assertSessionInfoUnicodeParse(t, got, sessionInfoUnicodeExpected(t, 0))
				}
				if string(before) != raw {
					t.Fatal("depth probe mutated input")
				}
			})
		}
	}
}

func TestSessionInfoUnicodeResultShapesStayBounded(t *testing.T) {
	// A new result/state field requires an explicit oracle decision; do not let a
	// new queue, diagnostic history, or output field silently evade these tests.
	for _, test := range []struct {
		value  any
		fields string
	}{
		{sessionInfoParseResult{}, "identity identityAvailable routeKey routeAvailable schedule scheduleAvailable issues"},
		{sessionInfoState{}, "identity identityAvailable synchronized routeKey routeAvailable schedule scheduleAvailable generation routeEpoch retired"},
		{sessionInfoIdentity{}, "season meetingKey sessionType sessionName"},
		{sessionInfoSchedule{}, "startUTC endUTC utcOffset"},
		{sessionInfoRetiredTuples{}, "tuples start count"},
		{sessionInfoLogicalTuple{}, "season meetingKey sessionName"},
		{sessionInfoReduction{}, "state disposition routeTransition issues"},
		{liveTimingState{}, "sessionInfo"},
		{liveTimingReduction{}, "state sessionInfoDisposition sessionInfoAuthoritative sessionInfoRouteTransition sessionInfoIssues sessionScopedUpdatesAllowed"},
	} {
		typeOf := reflect.TypeOf(test.value)
		var names []string
		for i := 0; i < typeOf.NumField(); i++ {
			names = append(names, typeOf.Field(i).Name)
		}
		if strings.Join(names, " ") != test.fields {
			t.Fatalf("review full oracle for %s: %v", typeOf, names)
		}
	}
}
