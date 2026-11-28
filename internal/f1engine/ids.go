package f1engine

import (
	"strconv"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

func decimal(n int64) string { return strconv.FormatInt(n, 10) }

func (e SessionInfoIdentity) shared() f1id.SessionIdentity {
	return f1id.SessionIdentity{
		Season:      e.Season,
		MeetingKey:  e.meetingKey,
		SessionType: f1id.SessionType(e.sessionType),
		SessionName: f1id.SessionName(e.sessionName),
	}
}

func canonicalQualifyingPhase(sessionType canonicalSessionType, sessionPart int64) (string, bool) {
	if sessionPart == 0 {
		return "", false
	}
	phase, ok := f1id.ResolveQualifyingPhase(f1id.SessionType(sessionType), sessionPart)
	return string(phase), ok
}

func phaseOrNone(phase string) string {
	if phase == "" {
		return string(f1id.PhaseNone)
	}
	return phase
}

func DeterministicTraceID(e SessionInfoIdentity, driverNumber int64) pcommon.TraceID {
	id, _ := f1id.DriverTraceID(e.shared(), driverNumber)
	return id
}

func DeterministicRootSpanID(e SessionInfoIdentity, driverNumber int64) pcommon.SpanID {
	id, _ := f1id.DriverSessionRootSpanID(e.shared(), driverNumber)
	return id
}

func deterministicPhaseSpanID(e SessionInfoIdentity, driverNumber int64, phase string) pcommon.SpanID {
	id, _ := f1id.QualifyingPhaseSpanID(e.shared(), driverNumber, f1id.QualifyingPhase(phase))
	return id
}

func DeterministicStintSpanID(e SessionInfoIdentity, driverNumber int64, phase string, stintOrdinal int64) pcommon.SpanID {
	id, _ := f1id.StintSpanID(e.shared(), driverNumber, f1id.QualifyingPhase(phaseOrNone(phase)), stintOrdinal)
	return id
}

func DeterministicLapSpanID(e SessionInfoIdentity, driverNumber int64, phase string, lapNumber int64) pcommon.SpanID {
	id, _ := f1id.LapSpanID(e.shared(), driverNumber, f1id.QualifyingPhase(phaseOrNone(phase)), lapNumber)
	return id
}

func DeterministicSectorSpanID(e SessionInfoIdentity, driverNumber int64, phase string, lapNumber, sectorNumber int64) pcommon.SpanID {
	id, _ := f1id.SectorSpanID(e.shared(), driverNumber, f1id.QualifyingPhase(phaseOrNone(phase)), lapNumber, sectorNumber)
	return id
}
