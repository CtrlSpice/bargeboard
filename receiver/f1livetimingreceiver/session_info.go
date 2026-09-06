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
	raw   json.RawMessage
	count uint8
}

func (member *sessionInfoMember) capture(raw json.RawMessage) {
	if member.count == 0 {
		member.raw = raw
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
	isObject, err := visitJSONObject(payload, func(name string, raw json.RawMessage) {
		switch name {
		case "Key":
			key.capture(raw)
		case "Meeting":
			meeting.capture(raw)
		case "Type":
			sourceType.capture(raw)
		case "Name":
			sourceName.capture(raw)
		case "StartDate":
			startDate.capture(raw)
		case "EndDate":
			endDate.capture(raw)
		case "GmtOffset":
			gmtOffset.capture(raw)
		case "_kf":
			keyframe.capture(raw)
		}
	})
	if err != nil {
		return sessionInfoParseResult{}, errInvalidNormalizedSessionInfo
	}
	if !isObject {
		return sessionInfoParseResult{issues: sessionInfoIssueShape}, nil
	}

	var result sessionInfoParseResult
	if key.unique() {
		result.routeKey, result.routeAvailable = parsePositiveCanonicalInt64(key.raw)
	}
	if !result.routeAvailable {
		result.issues |= sessionInfoIssueRoute
	}

	startLocal, startValid := time.Time{}, false
	if startDate.unique() {
		startLocal, startValid = parseSessionInfoLocalTime(startDate.raw)
	}
	meetingKey, meetingName, meetingValid := int64(0), "", false
	if meeting.unique() {
		meetingKey, meetingName, meetingValid = parseSessionInfoMeeting(meeting.raw)
	}
	typeValue, typeValid := "", false
	if sourceType.unique() {
		typeValue, typeValid = parseJSONString(sourceType.raw)
	}
	nameValue, nameValid := "", false
	if sourceName.unique() {
		nameValue, nameValid = parseJSONString(sourceName.raw)
	}
	if startValid && meetingValid && typeValid && nameValid {
		classification, ok := classifySessionInfo(
			int64(startLocal.Year()),
			meetingKey,
			meetingName,
			typeValue,
			nameValue,
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
	if endDate.unique() {
		endLocal, endValid = parseSessionInfoLocalTime(endDate.raw)
	}
	offset, offsetValid := time.Duration(0), false
	if gmtOffset.unique() {
		offset, offsetValid = parseSessionInfoGMTOffset(gmtOffset.raw)
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

func parseSessionInfoMeeting(raw json.RawMessage) (int64, string, bool) {
	var key sessionInfoMember
	var name sessionInfoMember
	isObject, err := visitJSONObject(raw, func(field string, value json.RawMessage) {
		switch field {
		case "Key":
			key.capture(value)
		case "Name":
			name.capture(value)
		}
	})
	if err != nil || !isObject || !key.unique() || !name.unique() {
		return 0, "", false
	}
	meetingKey, keyValid := parsePositiveCanonicalInt64(key.raw)
	meetingName, nameValid := parseJSONString(name.raw)
	return meetingKey, meetingName, keyValid && nameValid
}

func visitJSONObject(
	raw json.RawMessage,
	visit func(string, json.RawMessage),
) (bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return false, nil
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return false, err
		}
		name, ok := token.(string)
		if !ok {
			return false, errInvalidNormalizedSessionInfo
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false, err
		}
		visit(name, value)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return false, errInvalidNormalizedSessionInfo
	}
	return true, nil
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

func parseJSONString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '"' {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func parseSessionInfoLocalTime(raw json.RawMessage) (time.Time, bool) {
	value, ok := parseJSONString(raw)
	if !ok || len(value) != len("YYYY-MM-DDTHH:MM:SS") ||
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
	value, ok := parseJSONString(raw)
	if !ok {
		return 0, false
	}
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
