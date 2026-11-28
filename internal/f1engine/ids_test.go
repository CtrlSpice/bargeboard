package f1engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"strings"
	"testing"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

func oracleParts(t *testing.T, parts ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, p := range parts {
		if err := binary.Write(&b, binary.BigEndian, uint32(len(p))); err != nil {
			t.Fatal(err)
		}
		b.WriteString(p)
	}
	return b.Bytes()
}

func TestDeterministicIDPaths(t *testing.T) {
	e := SessionInfoIdentity{2026, 42, "qualifying", "qualifying"}
	race := SessionInfoIdentity{2026, 42, "race", "race"}
	oracle := func(id SessionInfoIdentity, marker string, path ...string) [32]byte {
		base := []string{"season", decimal(id.Season), "meeting", decimal(id.meetingKey), "session.type", string(id.sessionType), "session.name", string(id.sessionName), "driver", "12"}
		return sha256.Sum256(oracleParts(t, slices.Concat([]string{"bargeboard.otlp.id/v1", marker}, base, path)...))
	}
	sum := oracle(e, "trace")
	trace := DeterministicTraceID(e, 12)
	sharedTrace, err := f1id.DriverTraceID(e.shared(), 12)
	if err != nil || trace != pcommon.TraceID(sum[:16]) || trace != sharedTrace {
		t.Fatal(trace, sharedTrace, err)
	}
	seen := map[pcommon.SpanID]bool{}
	for _, c := range []struct {
		session SessionInfoIdentity
		id      pcommon.SpanID
		path    string
	}{
		{e, DeterministicRootSpanID(e, 12), "driver.session root"},
		{race, DeterministicStintSpanID(race, 12, "", 2), "phase none stint 2"},
		{race, DeterministicLapSpanID(race, 12, "", 2), "phase none lap 2"},
		{race, DeterministicSectorSpanID(race, 12, "", 2, 1), "phase none lap 2 sector 1"},
		{e, deterministicPhaseSpanID(e, 12, "q1"), "qualifying.phase q1"},
		{e, deterministicPhaseSpanID(e, 12, "q2"), "qualifying.phase q2"},
		{e, DeterministicStintSpanID(e, 12, "q1", 2), "phase q1 stint 2"},
		{e, DeterministicStintSpanID(e, 12, "q1", 3), "phase q1 stint 3"},
		{e, DeterministicLapSpanID(e, 12, "q1", 2), "phase q1 lap 2"},
		{e, DeterministicLapSpanID(e, 12, "q1", 3), "phase q1 lap 3"},
		{e, DeterministicSectorSpanID(e, 12, "q1", 2, 1), "phase q1 lap 2 sector 1"},
		{e, DeterministicSectorSpanID(e, 12, "q1", 2, 2), "phase q1 lap 2 sector 2"},
	} {
		sum := oracle(c.session, "span", strings.Fields(c.path)...)
		if c.id.IsEmpty() || seen[c.id] || c.id != pcommon.SpanID(sum[:8]) {
			t.Fatalf("%s: %v", c.path, c.id)
		}
		seen[c.id] = true
	}
	practice1 := DeterministicTraceID(SessionInfoIdentity{2026, 42, "practice", "practice_1"}, 12)
	practice2 := DeterministicTraceID(SessionInfoIdentity{2026, 42, "practice", "practice_2"}, 12)
	if practice1.IsEmpty() || practice2.IsEmpty() || practice1 == practice2 {
		t.Fatal("session.name omitted")
	}
	for _, other := range []SessionInfoIdentity{
		{2025, 42, "qualifying", "qualifying"},
		{2026, 41, "qualifying", "qualifying"},
		{2026, 42, "sprint_qualifying", "sprint_qualifying"},
		{2026, 42, "practice", "practice_1"},
	} {
		got := DeterministicTraceID(other, 12)
		if got.IsEmpty() || got == trace {
			t.Fatalf("identity omitted: %+v", other)
		}
	}
	if got := DeterministicTraceID(e, 2); got.IsEmpty() || got == trace {
		t.Fatal("driver omitted")
	}
	payload := `{"Key":ROUTE,"Meeting":{"Key":42,"Name":"Synthetic Grand Prix"},"StartDate":"2026-01-02T03:04:05","Type":"Qualifying","Name":"Qualifying"}`
	for _, route := range []string{"1", "999"} {
		info, err := parseSessionInfo([]byte(strings.Replace(payload, "ROUTE", route, 1)))
		if err != nil || !info.routeAvailable || !info.identityAvailable || DeterministicTraceID(info.identity, 12) != trace {
			t.Fatal(info, err)
		}
	}
}

func TestDeterministicIDPhase(t *testing.T) {
	for _, c := range []struct {
		typ    canonicalSessionType
		phases [3]string
	}{
		{"qualifying", [3]string{"q1", "q2", "q3"}}, {"sprint_qualifying", [3]string{"sq1", "sq2", "sq3"}}, {"race", [3]string{}},
	} {
		for n := -1; n <= 4; n++ {
			var want string
			if n >= 1 && n <= 3 {
				want = c.phases[n-1]
			}
			got, ok := canonicalQualifyingPhase(c.typ, int64(n))
			if got != want || ok != (want != "") {
				t.Fatal(c.typ, n, got, ok)
			}
			if want == "" {
				want = "none"
			}
			if phaseOrNone(got) != want {
				t.Fatal("phaseOrNone", got)
			}
		}
	}
}
