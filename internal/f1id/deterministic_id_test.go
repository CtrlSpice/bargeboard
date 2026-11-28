package f1id

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

func TestIDVectors(t *testing.T) {
	// Independently computed SHA-256 vectors for the architecture's LP encoding.
	e := SessionIdentity{2025, 123, "qualifying", "qualifying"}
	trace, err := DriverTraceID(e, 12)
	if err != nil || hex.EncodeToString(trace[:]) != "b7f75068a01ab70cf6c8ff5f0855574f" {
		t.Fatalf("trace %x, %v", trace[:], err)
	}
	check := func(want string) func(pcommon.SpanID, error) {
		return func(id pcommon.SpanID, err error) {
			t.Helper()
			if err != nil || hex.EncodeToString(id[:]) != want {
				t.Errorf("span %x, %v; want %s", id[:], err, want)
			}
		}
	}
	check("0b62766be315c8b5")(DriverSessionRootSpanID(e, 12))
	check("066f3f235aeb03de")(QualifyingPhaseSpanID(e, 12, PhaseQ2))
	check("b8aa9fc7ff7baa68")(StintSpanID(e, 12, PhaseQ2, 2))
	check("ec75ade33a5eb598")(LapSpanID(e, 12, PhaseQ2, 11))
	check("18768d7c6a41b704")(SectorSpanID(e, 12, PhaseQ2, 11, 3))
}

func TestZeroIDRetries(t *testing.T) {
	for _, size := range []int{8, 16} {
		for _, retries := range []int{1, 2} {
			t.Run(fmt.Sprintf("%d/retry%d", size, retries), func(t *testing.T) {
				calls := 0
				sum := nonzeroDigest([]byte("base"), size, func(input []byte) [32]byte {
					want := "base"
					if calls > 0 {
						want += "\x00\x00\x00\x05retry\x00\x00\x00\x01" + fmt.Sprint(calls)
					}
					if string(input) != want || calls > retries {
						t.Fatalf("unexpected digest input %q", input)
					}
					var digest [32]byte
					digest[size] = 1 // Nonzero outside the ID must not stop retry.
					if calls == retries {
						digest[size-1] = 42
					}
					calls++
					return digest
				})
				want := make([]byte, size)
				want[size-1] = 42
				if calls != retries+1 || !bytes.Equal(sum, want) {
					t.Errorf("%d calls, result %x", calls, sum)
				}
			})
		}
	}
}

func TestIDInputDomains(t *testing.T) {
	e := SessionIdentity{2025, 123, "race", "race"}
	q := SessionIdentity{2025, 123, "qualifying", "qualifying"}
	sq := SessionIdentity{2025, 123, "sprint_qualifying", "sprint_qualifying"}
	for _, input := range []struct {
		e SessionIdentity
		d int64
	}{{SessionIdentity{}, 1}, {e, 0}, {e, -1}} {
		if id, err := DriverTraceID(input.e, input.d); err == nil || id != (pcommon.TraceID{}) {
			t.Errorf("accepted trace %+v", input)
		}
	}
	reject := func(id pcommon.SpanID, err error) {
		t.Helper()
		if err == nil || id != (pcommon.SpanID{}) {
			t.Errorf("expected no ID, got %v,%v", id, err)
		}
	}
	reject(DriverSessionRootSpanID(e, 0))
	reject(DriverSessionRootSpanID(SessionIdentity{}, 1))
	reject(QualifyingPhaseSpanID(e, 1, PhaseNone))
	reject(QualifyingPhaseSpanID(q, 1, PhaseSQ1))
	reject(StintSpanID(q, 1, PhaseNone, 1))
	reject(StintSpanID(e, 1, PhaseNone, 0))
	reject(LapSpanID(e, 1, PhaseNone, -1))
	reject(LapSpanID(e, 1, "unknown", 1))
	reject(SectorSpanID(e, 1, PhaseNone, 0, 1))
	reject(SectorSpanID(e, 1, PhaseNone, 1, 0))
	reject(SectorSpanID(e, 1, PhaseNone, 1, 4))
	for _, tc := range []struct {
		e     SessionIdentity
		phase QualifyingPhase
	}{
		{q, PhaseQ1}, {q, PhaseQ2}, {q, PhaseQ3}, {sq, PhaseSQ1}, {sq, PhaseSQ2}, {sq, PhaseSQ3}, {e, PhaseNone},
	} {
		if _, err := LapSpanID(tc.e, 1, tc.phase, 1); err != nil {
			t.Errorf("lap %s: %v", tc.phase, err)
		}
	}
}
