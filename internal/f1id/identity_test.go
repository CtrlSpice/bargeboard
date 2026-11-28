package f1id

import (
	"fmt"
	"testing"
)

// Synthetic inputs for the Session Coverage rows in docs/architecture.md.
func TestClassifySessionInfo(t *testing.T) {
	const gp = "Example Grand Prix"
	check := func(year, key int64, meeting, typ, name string, wantType SessionType, wantName SessionName) {
		t.Helper()
		st, sn, ok := ClassifySessionInfo(year, key, meeting, typ, name)
		if st != wantType || sn != wantName || ok != (wantType != "") {
			t.Errorf("%d/%d %q %q %q: got (%q, %q, %t)", year, key, meeting, typ, name, st, sn, ok)
		}
	}
	for year := int64(2021); year <= 2026; year++ {
		meeting, prefix := "Pre-Season Testing", "Practice"
		if year <= 2022 {
			meeting = "Pre-Season Test"
		}
		if year >= 2025 {
			prefix = "Day"
		}
		for day := 1; day <= 3; day++ {
			check(year, 123, meeting, "Practice", fmt.Sprintf("%s %d", prefix, day), "testing", SessionName(fmt.Sprintf("testing_day_%d", day)))
		}
	}
	for n := 1; n <= 3; n++ {
		check(2026, 123, gp, "Practice", fmt.Sprintf("Practice %d", n), "practice", SessionName(fmt.Sprintf("practice_%d", n)))
	}
	check(2020, 1057, "Imola", "Practice", "Practice", "practice", "practice_1")
	check(2020, 1058, gp, "Practice", "Practice", "", "")
	check(2021, 1057, gp, "Practice", "Practice", "", "")
	check(2021, 123, gp, "Race", "Sprint Qualifying", "sprint", "sprint")
	check(2022, 123, gp, "Race", "Sprint Qualifying", "", "")
	check(2022, 123, gp, "Race", "Sprint", "sprint", "sprint")
	check(2023, 123, gp, "Qualifying", "Sprint Shootout", "sprint_qualifying", "sprint_qualifying")
	check(2024, 123, gp, "Qualifying", "Sprint Qualifying", "sprint_qualifying", "sprint_qualifying")
	check(2026, 123, gp, "Qualifying", "Qualifying", "qualifying", "qualifying")
	check(2026, 123, gp, "Race", "Race", "race", "race")
	check(2026, 123, gp+" ", "Race", "Race", "", "")
	check(2026, 123, gp, "race", "Race", "", "")
	check(2027, 123, "Pre-Season Testing", "Practice", "Day 1", "", "")
	check(2024, 123, "Pre-Season Testing", "Practice", "Day 1", "", "")
	check(2025, 123, "Pre-Season Testing", "Practice", "Practice 1", "", "")
	check(2022, 123, "Pre-Season Testing", "Practice", "Practice 1", "", "")
	check(2025, 123, "Pre-Season Testing", "Practice", "Day 01", "", "")
	check(2025, 123, "Pre-Season Testing", "Practice", "Day 4", "", "")
	check(2025, 123, "Pre-Season Testing", "Race", "Day 1", "", "")
}

func TestIdentityDomain(t *testing.T) {
	names := map[SessionType][]SessionName{
		"testing":    {"testing_day_1", "testing_day_2", "testing_day_3"},
		"practice":   {"practice_1", "practice_2", "practice_3"},
		"qualifying": {"qualifying"}, "sprint_qualifying": {"sprint_qualifying"},
		"sprint": {"sprint"}, "race": {"race"},
	}
	for st, allowed := range names {
		if !st.Valid() {
			t.Errorf("invalid type %q", st)
		}
		for _, name := range allowed {
			for other := range names {
				if got := name.ValidForType(other); got != (other == st) {
					t.Errorf("%q.ValidForType(%q) = %v", name, other, got)
				}
			}
		}
	}
	if SessionType("unknown").Valid() || SessionName("unknown").ValidForType("unknown") {
		t.Fatal("accepted unknown type")
	}
	for _, tc := range []struct {
		e  SessionIdentity
		ok bool
	}{
		{SessionIdentity{1000, 1, "race", "race"}, true}, {SessionIdentity{9999, 1, "race", "race"}, true},
		{SessionIdentity{999, 1, "race", "race"}, false}, {SessionIdentity{10000, 1, "race", "race"}, false},
		{SessionIdentity{2026, 0, "race", "race"}, false}, {SessionIdentity{2026, -1, "race", "race"}, false},
		{SessionIdentity{2026, 1, "race", "sprint"}, false},
	} {
		if tc.e.Valid() != tc.ok {
			t.Errorf("%+v.Valid() != %t", tc.e, tc.ok)
		}
	}
	for s, ok := range map[string]bool{"A": true, "AB": true, "ABC": true, "ABCD": true, "": false, "ABCDE": false, "a": false, "É": false, "A1": false, " A": false} {
		if IsValidAcronym(s) != ok {
			t.Errorf("acronym %q != %v", s, ok)
		}
	}
}

func TestResolveQualifyingPhase(t *testing.T) {
	for st, phases := range map[SessionType][4]QualifyingPhase{
		"qualifying": {"", "q1", "q2", "q3"}, "sprint_qualifying": {"", "sq1", "sq2", "sq3"},
		"practice": {"none"}, "testing": {"none"}, "race": {"none"}, "sprint": {"none"}, "unknown": {},
	} {
		for part := int64(-1); part <= 4; part++ {
			var want QualifyingPhase
			if part >= 0 && part < 4 {
				want = phases[part]
			}
			p, ok := ResolveQualifyingPhase(st, part)
			if p != want || ok != (want != "") {
				t.Errorf("%s/%d: got %q,%t; want %q", st, part, p, ok, want)
			}
		}
	}
}
