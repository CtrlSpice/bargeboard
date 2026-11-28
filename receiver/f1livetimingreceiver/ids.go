package f1livetimingreceiver

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"strconv"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

func decimal(n int64) string { return strconv.FormatInt(n, 10) }

func canonicalQualifyingPhase(sessionType canonicalSessionType, sessionPart int64) (string, bool) {
	if sessionPart >= 1 && sessionPart <= 3 {
		switch sessionType {
		case canonicalSessionTypeQualifying:
			return "q" + decimal(sessionPart), true
		case canonicalSessionTypeSprintQualifying:
			return "sq" + decimal(sessionPart), true
		}
	}
	return "", false
}

func phaseOrNone(phase string) string {
	if phase == "" {
		return "none"
	}
	return phase
}

func appendIDParts(b []byte, parts ...string) []byte {
	for _, part := range parts {
		b = binary.BigEndian.AppendUint32(b, uint32(len(part)))
		b = append(b, part...)
	}
	return b
}

func nonzeroDigest(input []byte, width int, hash func([]byte) [32]byte) [32]byte {
	sum := hash(input)
	for n := int64(1); bytes.Equal(sum[:width], make([]byte, width)); n++ {
		sum = hash(appendIDParts(input[:len(input):len(input)], "retry", decimal(n)))
	}
	return sum
}

func entityDigest(e sessionInfoIdentity, marker string, width int, driver int64, path ...string) [32]byte {
	b := appendIDParts(nil, "bargeboard.otlp.id/v1", marker, "season", decimal(e.season),
		"meeting", decimal(e.meetingKey), "session.type", string(e.sessionType),
		"session.name", string(e.sessionName), "driver", decimal(driver))
	return nonzeroDigest(appendIDParts(b, path...), width, sha256.Sum256)
}

func deterministicTraceID(e sessionInfoIdentity, driverNumber int64) pcommon.TraceID {
	sum := entityDigest(e, "trace", 16, driverNumber)
	return pcommon.TraceID(sum[:16])
}

func entitySpanID(e sessionInfoIdentity, driverNumber int64, parts ...string) pcommon.SpanID {
	sum := entityDigest(e, "span", 8, driverNumber, parts...)
	return pcommon.SpanID(sum[:8])
}

func deterministicRootSpanID(e sessionInfoIdentity, driverNumber int64) pcommon.SpanID {
	return entitySpanID(e, driverNumber, "driver.session", "root")
}

func deterministicPhaseSpanID(e sessionInfoIdentity, driverNumber int64, phase string) pcommon.SpanID {
	return entitySpanID(e, driverNumber, "qualifying.phase", phase)
}

func deterministicStintSpanID(e sessionInfoIdentity, driverNumber int64, phase string, stintOrdinal int64) pcommon.SpanID {
	return entitySpanID(e, driverNumber, "phase", phaseOrNone(phase), "stint", decimal(stintOrdinal))
}

func deterministicLapSpanID(e sessionInfoIdentity, driverNumber int64, phase string, lapNumber int64) pcommon.SpanID {
	return entitySpanID(e, driverNumber, "phase", phaseOrNone(phase), "lap", decimal(lapNumber))
}

func deterministicSectorSpanID(e sessionInfoIdentity, driverNumber int64, phase string, lapNumber, sectorNumber int64) pcommon.SpanID {
	return entitySpanID(e, driverNumber, "phase", phaseOrNone(phase), "lap", decimal(lapNumber), "sector", decimal(sectorNumber))
}
