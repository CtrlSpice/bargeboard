// Package f1id provides the shared identity and input primitives for F1 signals.
package f1id

import "strings"

type SessionType string
type SessionName string
type QualifyingPhase string

const (
	SessionTypeTesting          SessionType = "testing"
	SessionTypePractice         SessionType = "practice"
	SessionTypeQualifying       SessionType = "qualifying"
	SessionTypeSprintQualifying SessionType = "sprint_qualifying"
	SessionTypeSprint           SessionType = "sprint"
	SessionTypeRace             SessionType = "race"

	SessionNameTestingDay1      SessionName = "testing_day_1"
	SessionNameTestingDay2      SessionName = "testing_day_2"
	SessionNameTestingDay3      SessionName = "testing_day_3"
	SessionNamePractice1        SessionName = "practice_1"
	SessionNamePractice2        SessionName = "practice_2"
	SessionNamePractice3        SessionName = "practice_3"
	SessionNameQualifying       SessionName = "qualifying"
	SessionNameSprintQualifying SessionName = "sprint_qualifying"
	SessionNameSprint           SessionName = "sprint"
	SessionNameRace             SessionName = "race"

	PhaseNone QualifyingPhase = "none"
	PhaseQ1   QualifyingPhase = "q1"
	PhaseQ2   QualifyingPhase = "q2"
	PhaseQ3   QualifyingPhase = "q3"
	PhaseSQ1  QualifyingPhase = "sq1"
	PhaseSQ2  QualifyingPhase = "sq2"
	PhaseSQ3  QualifyingPhase = "sq3"
)

func (st SessionType) Valid() bool {
	switch st {
	case SessionTypeTesting, SessionTypePractice, SessionTypeQualifying, SessionTypeSprintQualifying, SessionTypeSprint, SessionTypeRace:
		return true
	}
	return false
}

func (name SessionName) ValidForType(st SessionType) bool {
	switch st {
	case SessionTypeTesting:
		return name == SessionNameTestingDay1 || name == SessionNameTestingDay2 || name == SessionNameTestingDay3
	case SessionTypePractice:
		return name == SessionNamePractice1 || name == SessionNamePractice2 || name == SessionNamePractice3
	case SessionTypeQualifying, SessionTypeSprintQualifying, SessionTypeSprint, SessionTypeRace:
		return name == SessionName(st)
	}
	return false
}

type SessionIdentity struct {
	Season      int64
	MeetingKey  int64
	SessionType SessionType
	SessionName SessionName
}

func (e SessionIdentity) Valid() bool {
	return e.Season >= 1000 && e.Season <= 9999 && e.MeetingKey > 0 && e.SessionName.ValidForType(e.SessionType)
}

func ResolveQualifyingPhase(st SessionType, sessionPart int64) (QualifyingPhase, bool) {
	if sessionPart >= 1 && sessionPart <= 3 {
		switch st {
		case SessionTypeQualifying:
			return [...]QualifyingPhase{PhaseQ1, PhaseQ2, PhaseQ3}[sessionPart-1], true
		case SessionTypeSprintQualifying:
			return [...]QualifyingPhase{PhaseSQ1, PhaseSQ2, PhaseSQ3}[sessionPart-1], true
		}
	} else if sessionPart == 0 && st.Valid() && st != SessionTypeQualifying && st != SessionTypeSprintQualifying {
		return PhaseNone, true
	}
	return "", false
}

// ClassifySessionInfo matches the exact source spellings in the Session Coverage table.
func ClassifySessionInfo(season, meetingKey int64, meetingName, sourceType, sourceName string) (SessionType, SessionName, bool) {
	if sourceType == "Practice" {
		if season == 2020 && meetingKey == 1057 && sourceName == "Practice" {
			return SessionTypePractice, SessionNamePractice1, true
		}
		var prefix string
		switch {
		case season >= 2021 && season <= 2022 && meetingName == "Pre-Season Test",
			season >= 2023 && season <= 2024 && meetingName == "Pre-Season Testing":
			prefix = "Practice "
		case season >= 2025 && season <= 2026 && meetingName == "Pre-Season Testing":
			prefix = "Day "
		}
		if prefix != "" && strings.HasPrefix(sourceName, prefix) {
			switch sourceName[len(prefix):] {
			case "1":
				return SessionTypeTesting, SessionNameTestingDay1, true
			case "2":
				return SessionTypeTesting, SessionNameTestingDay2, true
			case "3":
				return SessionTypeTesting, SessionNameTestingDay3, true
			}
		}
	}
	if !strings.HasSuffix(meetingName, " Grand Prix") {
		return "", "", false
	}
	switch sourceType {
	case "Practice":
		switch sourceName {
		case "Practice 1":
			return SessionTypePractice, SessionNamePractice1, true
		case "Practice 2":
			return SessionTypePractice, SessionNamePractice2, true
		case "Practice 3":
			return SessionTypePractice, SessionNamePractice3, true
		}
	case "Qualifying":
		switch sourceName {
		case "Qualifying":
			return SessionTypeQualifying, SessionNameQualifying, true
		case "Sprint Shootout", "Sprint Qualifying":
			return SessionTypeSprintQualifying, SessionNameSprintQualifying, true
		}
	case "Race":
		switch sourceName {
		case "Sprint Qualifying":
			if season == 2021 {
				return SessionTypeSprint, SessionNameSprint, true
			}
		case "Sprint":
			return SessionTypeSprint, SessionNameSprint, true
		case "Race":
			return SessionTypeRace, SessionNameRace, true
		}
	}
	return "", "", false
}

func IsValidAcronym(s string) bool {
	if len(s) < 1 || len(s) > 4 {
		return false
	}
	for i := range len(s) {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}
