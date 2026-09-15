package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var errInvalidNormalizedSessionInfo = errors.New("invalid normalized SessionInfo payload")

type canonicalSessionType string

const (
	canonicalSessionTypeTesting          canonicalSessionType = "testing"
	canonicalSessionTypePractice         canonicalSessionType = "practice"
	canonicalSessionTypeQualifying       canonicalSessionType = "qualifying"
	canonicalSessionTypeSprintQualifying canonicalSessionType = "sprint_qualifying"
	canonicalSessionTypeSprint           canonicalSessionType = "sprint"
	canonicalSessionTypeRace             canonicalSessionType = "race"
)

type canonicalSessionName string

const (
	canonicalSessionNameTestingDay1      canonicalSessionName = "testing_day_1"
	canonicalSessionNameTestingDay2      canonicalSessionName = "testing_day_2"
	canonicalSessionNameTestingDay3      canonicalSessionName = "testing_day_3"
	canonicalSessionNamePractice1        canonicalSessionName = "practice_1"
	canonicalSessionNamePractice2        canonicalSessionName = "practice_2"
	canonicalSessionNamePractice3        canonicalSessionName = "practice_3"
	canonicalSessionNameQualifying       canonicalSessionName = "qualifying"
	canonicalSessionNameSprintQualifying canonicalSessionName = "sprint_qualifying"
	canonicalSessionNameSprint           canonicalSessionName = "sprint"
	canonicalSessionNameRace             canonicalSessionName = "race"
)

type sessionInfoIdentity struct {
	season      int64
	meetingKey  int64
	sessionType canonicalSessionType
	sessionName canonicalSessionName
}

type sessionInfoSchedule struct {
	startUTC  time.Time
	endUTC    time.Time
	utcOffset time.Duration
}

type sessionInfoIssueSet uint8

const (
	sessionInfoIssueShape sessionInfoIssueSet = 1 << iota
	sessionInfoIssueIdentity
	sessionInfoIssueClassification
	sessionInfoIssueRoute
	sessionInfoIssueSchedule
	sessionInfoIssueKeyframe
	sessionInfoIssueUnicode
)

type sessionInfoParseResult struct {
	identity          sessionInfoIdentity
	identityAvailable bool
	routeKey          int64
	routeAvailable    bool
	schedule          sessionInfoSchedule
	scheduleAvailable bool
	issues            sessionInfoIssueSet
}

type sessionInfoMember struct {
	raw       json.RawMessage
	count     uint8
	text      string
	textValid bool
}

func (member *sessionInfoMember) capture(raw json.RawMessage, issues *sessionInfoIssueSet) {
	// Inspect every recognized scalar occurrence, including duplicates. Nested
	// content of unsupported shapes remains opaque; U3 owns payload-wide findings.
	text, valid, stringIssues := parseJSONString(raw)
	*issues |= stringIssues
	if member.count == 0 {
		member.raw = raw
		member.text, member.textValid = text, valid
	}
	if member.count < 2 {
		member.count++
	}
}

func (member sessionInfoMember) unique() bool {
	return member.count == 1
}

func parseSessionInfo(payload json.RawMessage) (sessionInfoParseResult, error) {
	if !utf8.Valid(payload) || !json.Valid(payload) {
		return sessionInfoParseResult{}, errInvalidNormalizedSessionInfo
	}

	var key sessionInfoMember
	var meeting sessionInfoMember
	var sourceType sessionInfoMember
	var sourceName sessionInfoMember
	var startDate sessionInfoMember
	var endDate sessionInfoMember
	var gmtOffset sessionInfoMember
	var keyframe sessionInfoMember
	var result sessionInfoParseResult
	meetingKey, meetingName, meetingValid := int64(0), "", false
	isObject, err := visitSessionInfoObject(payload, &result.issues, func(name string, raw json.RawMessage) {
		switch name {
		case "Key":
			key.capture(raw, &result.issues)
		case "Meeting":
			meeting.capture(raw, &result.issues)
			// Visit each recognized Meeting occurrence for bounded findings, even
			// when a duplicate prevents the logical bundle from being accepted.
			meetingKey, meetingName, meetingValid = parseSessionInfoMeeting(raw, &result.issues)
		case "Type":
			sourceType.capture(raw, &result.issues)
		case "Name":
			sourceName.capture(raw, &result.issues)
		case "StartDate":
			startDate.capture(raw, &result.issues)
		case "EndDate":
			endDate.capture(raw, &result.issues)
		case "GmtOffset":
			gmtOffset.capture(raw, &result.issues)
		case "_kf":
			keyframe.capture(raw, &result.issues)
		}
	})
	if err != nil {
		return sessionInfoParseResult{}, errInvalidNormalizedSessionInfo
	}
	if !isObject {
		return sessionInfoParseResult{issues: sessionInfoIssueShape}, nil
	}

	if key.unique() {
		result.routeKey, result.routeAvailable = parsePositiveCanonicalInt64(key.raw)
	}
	if !result.routeAvailable {
		result.issues |= sessionInfoIssueRoute
	}

	startLocal, startValid := time.Time{}, false
	if startDate.unique() && startDate.textValid {
		startLocal, startValid = parseSessionInfoLocalTimeValue(startDate.text)
	}
	if startValid && meeting.unique() && meetingValid &&
		sourceType.unique() && sourceType.textValid && sourceName.unique() && sourceName.textValid {
		classification, ok := classifySessionInfo(
			int64(startLocal.Year()),
			meetingKey,
			meetingName,
			sourceType.text,
			sourceName.text,
		)
		if ok {
			result.identity = sessionInfoIdentity{
				season:      int64(startLocal.Year()),
				meetingKey:  meetingKey,
				sessionType: classification.sessionType,
				sessionName: classification.sessionName,
			}
			result.identityAvailable = true
		} else {
			result.issues |= sessionInfoIssueClassification
		}
	} else {
		result.issues |= sessionInfoIssueIdentity
	}

	endLocal, endValid := time.Time{}, false
	if endDate.unique() && endDate.textValid {
		endLocal, endValid = parseSessionInfoLocalTimeValue(endDate.text)
	}
	offset, offsetValid := time.Duration(0), false
	if gmtOffset.unique() && gmtOffset.textValid {
		offset, offsetValid = parseSessionInfoGMTOffsetValue(gmtOffset.text)
	}
	if startValid && endValid && offsetValid {
		startUTC := startLocal.Add(-offset)
		endUTC := endLocal.Add(-offset)
		if endUTC.After(startUTC) {
			result.schedule = sessionInfoSchedule{
				startUTC:  startUTC,
				endUTC:    endUTC,
				utcOffset: offset,
			}
			result.scheduleAvailable = true
		}
	}
	if !result.scheduleAvailable {
		result.issues |= sessionInfoIssueSchedule
	}

	if keyframe.count > 0 && (!keyframe.unique() || !bytes.Equal(bytes.TrimSpace(keyframe.raw), []byte("true"))) {
		result.issues |= sessionInfoIssueKeyframe
	}
	return result, nil
}

type sessionClassification struct {
	sessionType canonicalSessionType
	sessionName canonicalSessionName
}

func classifySessionInfo(
	season int64,
	meetingKey int64,
	meetingName string,
	sourceType string,
	sourceName string,
) (sessionClassification, bool) {
	if sourceType == "Practice" {
		if day, ok := testingDay(season, meetingName, sourceName); ok {
			return sessionClassification{
				sessionType: canonicalSessionTypeTesting,
				sessionName: day,
			}, true
		}
		if season == 2020 && meetingKey == 1057 && sourceName == "Practice" {
			return sessionClassification{
				sessionType: canonicalSessionTypePractice,
				sessionName: canonicalSessionNamePractice1,
			}, true
		}
	}

	if !strings.HasSuffix(meetingName, " Grand Prix") {
		return sessionClassification{}, false
	}
	switch {
	case sourceType == "Practice" && sourceName == "Practice 1":
		return sessionClassification{canonicalSessionTypePractice, canonicalSessionNamePractice1}, true
	case sourceType == "Practice" && sourceName == "Practice 2":
		return sessionClassification{canonicalSessionTypePractice, canonicalSessionNamePractice2}, true
	case sourceType == "Practice" && sourceName == "Practice 3":
		return sessionClassification{canonicalSessionTypePractice, canonicalSessionNamePractice3}, true
	case sourceType == "Qualifying" && sourceName == "Qualifying":
		return sessionClassification{canonicalSessionTypeQualifying, canonicalSessionNameQualifying}, true
	case sourceType == "Qualifying" && (sourceName == "Sprint Shootout" || sourceName == "Sprint Qualifying"):
		return sessionClassification{canonicalSessionTypeSprintQualifying, canonicalSessionNameSprintQualifying}, true
	case season == 2021 && sourceType == "Race" && sourceName == "Sprint Qualifying":
		return sessionClassification{canonicalSessionTypeSprint, canonicalSessionNameSprint}, true
	case sourceType == "Race" && sourceName == "Sprint":
		return sessionClassification{canonicalSessionTypeSprint, canonicalSessionNameSprint}, true
	case sourceType == "Race" && sourceName == "Race":
		return sessionClassification{canonicalSessionTypeRace, canonicalSessionNameRace}, true
	default:
		return sessionClassification{}, false
	}
}

func testingDay(season int64, meetingName string, sourceName string) (canonicalSessionName, bool) {
	var prefix string
	switch {
	case season >= 2021 && season <= 2022 && meetingName == "Pre-Season Test":
		prefix = "Practice "
	case season >= 2023 && season <= 2024 && meetingName == "Pre-Season Testing":
		prefix = "Practice "
	case season >= 2025 && season <= 2026 && meetingName == "Pre-Season Testing":
		prefix = "Day "
	default:
		return "", false
	}
	if len(sourceName) != len(prefix)+1 || !strings.HasPrefix(sourceName, prefix) {
		return "", false
	}
	switch sourceName[len(prefix)] {
	case '1':
		return canonicalSessionNameTestingDay1, true
	case '2':
		return canonicalSessionNameTestingDay2, true
	case '3':
		return canonicalSessionNameTestingDay3, true
	default:
		return "", false
	}
}

func parseSessionInfoMeeting(raw json.RawMessage, issues *sessionInfoIssueSet) (int64, string, bool) {
	var key sessionInfoMember
	var name sessionInfoMember
	isObject, err := visitSessionInfoObject(raw, issues, func(field string, value json.RawMessage) {
		switch field {
		case "Key":
			key.capture(value, issues)
		case "Name":
			name.capture(value, issues)
		}
	})
	if err != nil || !isObject || !key.unique() || !name.unique() {
		return 0, "", false
	}
	meetingKey, keyValid := parsePositiveCanonicalInt64(key.raw)
	return meetingKey, name.text, keyValid && name.textValid
}

func visitSessionInfoObject(
	raw json.RawMessage,
	issues *sessionInfoIssueSet,
	visit func(string, json.RawMessage),
) (bool, error) {
	// SessionInfo already validated the entire payload before its former
	// Token/Decode visitor. Keep that whole-payload depth profile, not the hub's
	// per-member budget. Decode keys before exact ASCII matching, never via repair.
	err := visitRawJSONObject(raw, func(key, value json.RawMessage) error {
		name, err := decodeLosslessJSONString(key)
		if errors.Is(err, errJSONScalar) {
			// Every recognized key is ASCII: an unpaired surrogate cannot name
			// one. Skip this member without inspecting its opaque value.
			*issues |= sessionInfoIssueUnicode
			return nil
		}
		if err != nil {
			return err
		}
		visit(name, value)
		return nil
	})
	if errors.Is(err, errJSONObject) {
		return false, nil
	}
	return err == nil, err
}

func parsePositiveCanonicalInt64(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] < '1' || raw[0] > '9' {
		return 0, false
	}
	for _, character := range raw[1:] {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseJSONString(raw json.RawMessage) (string, bool, sessionInfoIssueSet) {
	value, err := decodeLosslessJSONString(raw)
	if errors.Is(err, errJSONScalar) {
		return "", false, sessionInfoIssueUnicode
	}
	return value, err == nil, 0
}

func parseSessionInfoLocalTime(raw json.RawMessage) (time.Time, bool) {
	value, ok, _ := parseJSONString(raw)
	if !ok {
		return time.Time{}, false
	}
	return parseSessionInfoLocalTimeValue(value)
}

func parseSessionInfoLocalTimeValue(value string) (time.Time, bool) {
	if len(value) != len("YYYY-MM-DDTHH:MM:SS") ||
		value[4] != '-' || value[7] != '-' || value[10] != 'T' ||
		value[13] != ':' || value[16] != ':' {
		return time.Time{}, false
	}
	for index, character := range []byte(value) {
		switch index {
		case 4, 7, 10, 13, 16:
			continue
		}
		if character < '0' || character > '9' {
			return time.Time{}, false
		}
	}
	year, err := strconv.Atoi(value[:4])
	if err != nil || year < 1000 {
		return time.Time{}, false
	}
	parsed, err := time.Parse("2006-01-02T15:04:05", value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func parseSessionInfoGMTOffset(raw json.RawMessage) (time.Duration, bool) {
	value, ok, _ := parseJSONString(raw)
	if !ok {
		return 0, false
	}
	return parseSessionInfoGMTOffsetValue(value)
}

func parseSessionInfoGMTOffsetValue(value string) (time.Duration, bool) {
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	if len(value) != len("HH:MM:00") || value[2] != ':' || value[5:] != ":00" {
		return 0, false
	}
	hours, hoursValid := twoASCIIDigits(value[0:2])
	minutes, minutesValid := twoASCIIDigits(value[3:5])
	if !hoursValid || !minutesValid || minutes > 59 || hours > 14 || (hours == 14 && minutes != 0) {
		return 0, false
	}
	offset := time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute
	if negative {
		if offset == 0 {
			return 0, false
		}
		offset = -offset
	}
	return offset, true
}

func twoASCIIDigits(value string) (int, bool) {
	if len(value) != 2 || value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' {
		return 0, false
	}
	return int(value[0]-'0')*10 + int(value[1]-'0'), true
}
