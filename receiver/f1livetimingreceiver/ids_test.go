package f1livetimingreceiver

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"strings"
	"testing"

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
	e := sessionInfoIdentity{2026, 42, "qualifying", "qualifying"}
	base := []string{"season", "2026", "meeting", "42", "session.type", "qualifying", "session.name", "qualifying", "driver", "12"}
	oracle := func(marker string, path ...string) [32]byte {
		return sha256.Sum256(oracleParts(t, slices.Concat([]string{"bargeboard.otlp.id/v1", marker}, base, path)...))
	}
	sum := oracle("trace")
	trace := deterministicTraceID(e, 12)
	if trace != pcommon.TraceID(sum[:16]) {
		t.Fatal(trace)
	}
	seen := map[pcommon.SpanID]bool{}
	for _, c := range []struct {
		id   pcommon.SpanID
		path string
	}{
		{deterministicRootSpanID(e, 12), "driver.session root"},
		{deterministicStintSpanID(e, 12, "", 2), "phase none stint 2"},
		{deterministicLapSpanID(e, 12, "", 2), "phase none lap 2"},
		{deterministicSectorSpanID(e, 12, "", 2, 1), "phase none lap 2 sector 1"},
		{deterministicPhaseSpanID(e, 12, "q1"), "qualifying.phase q1"},
		{deterministicPhaseSpanID(e, 12, "q2"), "qualifying.phase q2"},
		{deterministicStintSpanID(e, 12, "q1", 2), "phase q1 stint 2"},
		{deterministicStintSpanID(e, 12, "q1", 3), "phase q1 stint 3"},
		{deterministicLapSpanID(e, 12, "q1", 2), "phase q1 lap 2"},
		{deterministicLapSpanID(e, 12, "q1", 3), "phase q1 lap 3"},
		{deterministicSectorSpanID(e, 12, "q1", 2, 1), "phase q1 lap 2 sector 1"},
		{deterministicSectorSpanID(e, 12, "q1", 2, 2), "phase q1 lap 2 sector 2"},
	} {
		sum := oracle("span", strings.Fields(c.path)...)
		if seen[c.id] || c.id != pcommon.SpanID(sum[:8]) {
			t.Fatalf("%s: %v", c.path, c.id)
		}
		seen[c.id] = true
	}
	for _, other := range []sessionInfoIdentity{{2025, 42, "qualifying", "qualifying"}, {2026, 41, "qualifying", "qualifying"}, {2026, 42, "sprint_qualifying", "qualifying"}, {2026, 42, "qualifying", "sprint_qualifying"}} {
		if deterministicTraceID(other, 12) == trace {
			t.Fatalf("identity omitted: %+v", other)
		}
	}
	if deterministicTraceID(e, 2) == trace {
		t.Fatal("driver omitted")
	}
	payload := `{"Key":ROUTE,"Meeting":{"Key":42,"Name":"Synthetic Grand Prix"},"StartDate":"2026-01-02T03:04:05","Type":"Qualifying","Name":"Qualifying"}`
	for _, route := range []string{"1", "999"} {
		info, err := parseSessionInfo([]byte(strings.Replace(payload, "ROUTE", route, 1)))
		if err != nil || !info.routeAvailable || !info.identityAvailable || deterministicTraceID(info.identity, 12) != trace {
			t.Fatal(info, err)
		}
	}
}

func TestDeterministicIDRetry(t *testing.T) {
	for _, width := range []int{8, 16} {
		calls := 0
		input := oracleParts(t, "é")
		sum := nonzeroDigest(input, width, func(b []byte) [32]byte {
			want := input
			switch calls {
			case 1:
				want = slices.Concat(input, oracleParts(t, "retry", "1"))
			case 2:
				want = slices.Concat(input, oracleParts(t, "retry", "2"))
			case 0:
			default:
				t.Fatal("unexpected retry")
			}
			if !bytes.Equal(b, want) {
				t.Fatalf("input %x, want %x", b, want)
			}
			calls++
			var digest [32]byte
			digest[width] = 1
			if calls == 3 {
				digest[width-1] = 7
			}
			return digest
		})
		if calls != 3 || sum[width-1] != 7 {
			t.Fatal(width, calls, sum)
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
