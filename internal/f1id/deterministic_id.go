package f1id

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strconv"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

var errIdentity = errors.New("invalid F1 identity")

func decimal(n int64) string { return strconv.FormatInt(n, 10) }

func appendParts(buf []byte, parts ...string) []byte {
	for _, part := range parts {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(part)))
		buf = append(buf, part...)
	}
	return buf
}

func idPrefix(e SessionIdentity, marker string, driver int64) []byte {
	return appendParts(nil, "bargeboard.otlp.id/v1", marker, "season", decimal(e.Season), "meeting", decimal(e.MeetingKey),
		"session.type", string(e.SessionType), "session.name", string(e.SessionName), "driver", decimal(driver))
}

func nonzeroDigest(base []byte, size int, digest func([]byte) [32]byte) []byte {
	sum := digest(base)
	for retry := int64(1); bytes.Equal(sum[:size], make([]byte, size)); retry++ {
		sum = digest(appendParts(base, "retry", decimal(retry)))
	}
	return sum[:size]
}

func DriverTraceID(e SessionIdentity, driverNumber int64) (pcommon.TraceID, error) {
	if !e.Valid() || driverNumber <= 0 {
		return pcommon.TraceID{}, errIdentity
	}
	return pcommon.TraceID(nonzeroDigest(idPrefix(e, "trace", driverNumber), 16, sha256.Sum256)), nil
}

func spanID(e SessionIdentity, driver int64, parts ...string) (pcommon.SpanID, error) {
	if !e.Valid() || driver <= 0 {
		return pcommon.SpanID{}, errIdentity
	}
	base := appendParts(idPrefix(e, "span", driver), parts...)
	return pcommon.SpanID(nonzeroDigest(base, 8, sha256.Sum256)), nil
}

func DriverSessionRootSpanID(e SessionIdentity, driverNumber int64) (pcommon.SpanID, error) {
	return spanID(e, driverNumber, "driver.session", "root")
}

func (phase QualifyingPhase) validFor(st SessionType) bool {
	switch phase {
	case PhaseQ1, PhaseQ2, PhaseQ3:
		return st == SessionTypeQualifying
	case PhaseSQ1, PhaseSQ2, PhaseSQ3:
		return st == SessionTypeSprintQualifying
	case PhaseNone:
		return st.Valid() && st != SessionTypeQualifying && st != SessionTypeSprintQualifying
	}
	return false
}

func QualifyingPhaseSpanID(e SessionIdentity, driverNumber int64, phase QualifyingPhase) (pcommon.SpanID, error) {
	if phase == PhaseNone || !phase.validFor(e.SessionType) {
		return pcommon.SpanID{}, errIdentity
	}
	return spanID(e, driverNumber, "qualifying.phase", string(phase))
}

func StintSpanID(e SessionIdentity, driverNumber int64, phase QualifyingPhase, stintOrdinal int64) (pcommon.SpanID, error) {
	if !phase.validFor(e.SessionType) || stintOrdinal <= 0 {
		return pcommon.SpanID{}, errIdentity
	}
	return spanID(e, driverNumber, "phase", string(phase), "stint", decimal(stintOrdinal))
}

func LapSpanID(e SessionIdentity, driverNumber int64, phase QualifyingPhase, lapNumber int64) (pcommon.SpanID, error) {
	if !phase.validFor(e.SessionType) || lapNumber <= 0 {
		return pcommon.SpanID{}, errIdentity
	}
	return spanID(e, driverNumber, "phase", string(phase), "lap", decimal(lapNumber))
}

func SectorSpanID(e SessionIdentity, driverNumber int64, phase QualifyingPhase, lapNumber, sector int64) (pcommon.SpanID, error) {
	if !phase.validFor(e.SessionType) || lapNumber <= 0 || sector < 1 || sector > 3 {
		return pcommon.SpanID{}, errIdentity
	}
	return spanID(e, driverNumber, "phase", string(phase), "lap", decimal(lapNumber), "sector", decimal(sector))
}
