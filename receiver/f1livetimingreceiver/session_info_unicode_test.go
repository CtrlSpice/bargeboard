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
)

// Mutations are synthetic boundary probes of the attributed archive fixtures,
// not claims that malformed Unicode was observed in the source.
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
			if fixture.Source == "" {
				t.Fatal("fixture lacks attribution")
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
	raw := sessionInfoUnicodeFixture(t, "2021_abu_dhabi_practice_1_stream")
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
	fixture := sessionInfoUnicodeFixture(t, "2021_abu_dhabi_practice_1_stream")
	tests := []struct {
		name   string
		fields []string
		issues sessionInfoIssueSet
	}{
		{"meeting", []string{"Meeting", "Name", `"\uD800 Grand Prix"`}, sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"type", []string{"Type", `"Prac\udfff tice"`}, sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"name", []string{"Name", `"Practice 1\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"start", []string{"StartDate", `"2021-12-10T13:30:0\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueIdentity | sessionInfoIssueSchedule},
		{"end", []string{"EndDate", `"2021-12-10T14:30:0\udc00"`}, sessionInfoIssueUnicode | sessionInfoIssueSchedule},
		{"offset", []string{"GmtOffset", `"04:00:0\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueSchedule},
		{"route remains integer grammar", []string{"Key", `"\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueRoute},
		{"meeting key remains integer grammar", []string{"Meeting", "Key", `"\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"keyframe", []string{"_kf", `"\ud800"`}, sessionInfoIssueUnicode | sessionInfoIssueKeyframe},
		{"literal replacement", []string{"Meeting", "Name", `"� Grand Prix"`}, 0},
		{"escaped replacement", []string{"Meeting", "Name", `"\uFFFD Grand Prix"`}, 0},
		{"accented", []string{"Meeting", "Name", `"São Paulo Grand Prix"`}, 0},
		{"paired", []string{"Meeting", "Name", `"\uD83D\uDE80 Grand Prix"`}, 0},
		{"literal escape", []string{"Meeting", "Name", `"\\uD800 Grand Prix"`}, 0},
		{"escaped type", []string{"Type", `"\u0050ractice"`}, 0},
		{"escaped name", []string{"Name", `"Practice \u0031"`}, 0},
		{"replacement type is scalar valid", []string{"Type", `"�Practice"`}, sessionInfoIssueClassification},
		{"replacement name is scalar valid", []string{"Name", `"\ufffdPractice 1"`}, sessionInfoIssueClassification},
		{"paired start is not ASCII date", []string{"StartDate", `"2021-12-10T13:30:\ud83d\ude80"`}, sessionInfoIssueIdentity | sessionInfoIssueSchedule},
		{"replacement end is not ASCII date", []string{"EndDate", `"2021-12-10T14:30:�"`}, sessionInfoIssueSchedule},
		{"replacement offset is not ASCII offset", []string{"GmtOffset", `"04:00:\ufffd"`}, sessionInfoIssueSchedule},
		{"escaped route digits remain quoted", []string{"Key", `"\u0036\u0035\u0039\u0034"`}, sessionInfoIssueRoute},
		{"route exponent", []string{"Key", `6594e0`}, sessionInfoIssueRoute},
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

func TestSessionInfoUnicodeTestingAndImolaCannotBypassValidation(t *testing.T) {
	for _, fixtureName := range []string{"2021_preseason_practice_1", "2025_preseason_day_1", "2020_imola_practice"} {
		fixture := sessionInfoUnicodeFixture(t, fixtureName)
		baseline, err := parseSessionInfo(fixture)
		if err != nil || !baseline.identityAvailable || baseline.issues != 0 {
			t.Fatalf("invalid attributed baseline: %#v, %v", baseline, err)
		}
		for _, fields := range [][]string{
			{"Meeting", "Name", `"\ud800 Grand Prix"`},
			{"Type", `"Practice\ud800"`},
			{"Name", `"Practice\ud800"`},
		} {
			t.Run(fixtureName+"/"+strings.Join(fields[:len(fields)-1], "."), func(t *testing.T) {
				got, err := parseSessionInfo(mutateSessionInfoUnicode(t, fixture, fields...))
				if err != nil {
					t.Fatal(err)
				}
				want := baseline
				want.identity, want.identityAvailable = sessionInfoIdentity{}, false
				want.issues = sessionInfoIssueUnicode | sessionInfoIssueIdentity
				assertSessionInfoUnicodeParse(t, got, want)
			})
		}
	}
}

func TestSessionInfoUnicodeRawKeysAndOccurrenceUnion(t *testing.T) {
	// This compact descriptor is the existing Abu Dhabi stream fixture's consumed
	// fields. Raw edits preserve duplicate/key token spellings that maps would lose.
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
		{"escaped nested", replace(`"Key":1107`, `"\u004bey":1107`), 0},
		{"duplicate type", add(`"\u0054ype":"Practice"`), sessionInfoIssueIdentity},
		{"duplicate route", add(`"\u004bey":6594`), sessionInfoIssueRoute},
		{"duplicate end", add(`"\u0045ndDate":"2021-12-10T14:30:00"`), sessionInfoIssueSchedule},
		{"duplicate meeting name", replace(`"Name":"Abu Dhabi Grand Prix"`, `"Name":"Abu Dhabi Grand Prix","\u004eame":"Abu Dhabi Grand Prix"`), sessionInfoIssueIdentity},
		{"duplicate bad later scalar", add(`"\u0054ype":"\ud800"`), sessionInfoIssueIdentity | sessionInfoIssueUnicode},
		{"duplicate bad earlier scalar", replace(`"Type":"Practice"`, `"Type":"\ud800","\u0054ype":"Practice"`), sessionInfoIssueIdentity | sessionInfoIssueUnicode},
		{"duplicate meeting still reports", add(`"Meeting":{"Name":"\ud800"}`), sessionInfoIssueIdentity | sessionInfoIssueUnicode},
		{"duplicate earlier meeting still reports", `{"Meeting":{"Name":"\ud800"},` + base[1:], sessionInfoIssueIdentity | sessionInfoIssueUnicode},
		{"unknown scalar keys", add(`"\ud800":1,"\udfff":2,"�":3,"\ufffd":4`), sessionInfoIssueUnicode},
		{"nested unknown scalar key", replace(`"Key":1107`, `"\ud800":{"Name":"\udfff"},"Key":1107`), sessionInfoIssueUnicode},
		{"required root key absent", replace(`"Type"`, `"\ud800Type"`), sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"required nested key absent", replace(`"Name":"Abu Dhabi Grand Prix"`, `"\ud800Name":"Abu Dhabi Grand Prix"`), sessionInfoIssueUnicode | sessionInfoIssueIdentity},
		{"bad unknown key plus bad recognized value", strings.Replace(add(`"\ud800":1`), `"GmtOffset":"04:00:00"`, `"GmtOffset":"\udfff"`, 1), sessionInfoIssueUnicode | sessionInfoIssueSchedule},
		{"unknown duplicate values opaque", add(`"Future":"\ud800","Future":{"\udfff":"\ud800"}`), 0},
		{"delete metadata remains ignored", add(`"_deleted":["\ud800"],"_kf":true`), 0},
		{"keyframe escaped duplicate", add(`"_kf":true,"\u005fkf":true`), sessionInfoIssueKeyframe},
		{"all applicable object issues", `{"\ud800":0,"Key":0,"Meeting":{"Key":1107,"Name":"\ud800 Grand Prix"},"_kf":false}`, sessionInfoIssueUnicode | sessionInfoIssueIdentity | sessionInfoIssueRoute | sessionInfoIssueSchedule | sessionInfoIssueKeyframe},
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
		{`"\uD83D\uDE80"`, "🚀", true}, {`"São Paulo"`, "São Paulo", true},
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
					raw = strings.Replace(base, `"Key":1107`, `"Future":`+value+`,"Key":1107`, 1)
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
