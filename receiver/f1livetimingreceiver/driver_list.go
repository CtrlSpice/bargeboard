package f1livetimingreceiver

import (
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf8"
)

const maxDriverRegistryEntries = 32

var errInvalidNormalizedDriverList = errors.New("invalid normalized DriverList payload")

type driverListFieldState uint8

const (
	driverListFieldAbsent driverListFieldState = iota
	driverListFieldValid
	driverListFieldInvalid
)

type driverListIssueSet uint8

const (
	driverListIssueUnicode driverListIssueSet = 1 << iota
	driverListIssueShape
	driverListIssueIdentity
	driverListIssueLimit
	driverListIssueConflict
)

type driverListEntryPatch struct {
	number            int64
	object            bool
	duplicate         bool
	tla               string
	tlaState          driverListFieldState
	racingNumberState driverListFieldState
}

type driverListParseResult struct {
	entries [maxDriverRegistryEntries]driverListEntryPatch
	count   uint8
	object  bool
	issues  driverListIssueSet
}

func parseDriverList(payload json.RawMessage) (driverListParseResult, error) {
	if !utf8.Valid(payload) || !json.Valid(payload) {
		return driverListParseResult{}, errInvalidNormalizedDriverList
	}

	var result driverListParseResult
	seen := make(map[int64]int, maxDriverRegistryEntries+1)
	err := visitRawJSONObject(payload, func(key, value json.RawMessage) error {
		name, err := decodeLosslessJSONString(key)
		if errors.Is(err, errJSONScalar) {
			result.issues |= driverListIssueUnicode
			return nil
		}
		if err != nil {
			return err
		}
		number, ok := parseCanonicalDriverNumber(name)
		if !ok {
			return nil
		}

		entry, issues, err := parseDriverListEntry(number, value)
		if err != nil {
			return err
		}
		result.issues |= issues
		if storedIndex, duplicate := seen[number]; duplicate {
			if storedIndex == 0 {
				result.issues |= driverListIssueShape | driverListIssueIdentity
				return nil
			}
			index := storedIndex - 1
			prior := result.entries[index]
			result.entries[index] = driverListEntryPatch{
				number:            number,
				object:            prior.object && entry.object,
				duplicate:         true,
				tlaState:          driverListFieldInvalid,
				racingNumberState: driverListFieldInvalid,
			}
			result.issues |= driverListIssueShape | driverListIssueIdentity
			return nil
		}
		if int(result.count) == len(result.entries) {
			seen[number] = 0
			result.issues |= driverListIssueLimit
			return nil
		}
		result.entries[result.count] = entry
		seen[number] = int(result.count) + 1
		result.count++
		return nil
	})
	if errors.Is(err, errJSONObject) {
		result.issues |= driverListIssueShape
		return result, nil
	}
	if err != nil {
		return driverListParseResult{}, errInvalidNormalizedDriverList
	}
	result.object = true
	return result, nil
}

func parseDriverListEntry(
	number int64,
	raw json.RawMessage,
) (driverListEntryPatch, driverListIssueSet, error) {
	entry := driverListEntryPatch{number: number}
	var issues driverListIssueSet
	var tlaCount uint8
	var racingNumberCount uint8
	err := visitRawJSONObject(raw, func(key, value json.RawMessage) error {
		name, err := decodeLosslessJSONString(key)
		if errors.Is(err, errJSONScalar) {
			issues |= driverListIssueUnicode
			return nil
		}
		if err != nil {
			return err
		}

		switch name {
		case "Tla":
			tla, state, scalarIssue := parseDriverTLA(value)
			issues |= scalarIssue
			if tlaCount == 0 {
				entry.tla = tla
				entry.tlaState = state
			}
			if tlaCount < 2 {
				tlaCount++
			}
			if tlaCount != 1 {
				entry.tla = ""
				entry.tlaState = driverListFieldInvalid
				issues |= driverListIssueShape | driverListIssueIdentity
			}

		case "RacingNumber":
			state, scalarIssue := parseDriverRacingNumber(value, number)
			issues |= scalarIssue
			if racingNumberCount == 0 {
				entry.racingNumberState = state
			}
			if racingNumberCount < 2 {
				racingNumberCount++
			}
			if racingNumberCount != 1 {
				entry.racingNumberState = driverListFieldInvalid
				issues |= driverListIssueShape | driverListIssueIdentity
			}
		}
		return nil
	})
	if errors.Is(err, errJSONObject) {
		entry.tlaState = driverListFieldInvalid
		issues |= driverListIssueShape | driverListIssueIdentity
		return entry, issues, nil
	}
	if err != nil {
		return driverListEntryPatch{}, 0, err
	}
	entry.object = true
	return entry, issues, nil
}

func parseDriverTLA(raw json.RawMessage) (string, driverListFieldState, driverListIssueSet) {
	value, err := decodeLosslessJSONString(raw)
	if errors.Is(err, errJSONScalar) {
		return "", driverListFieldInvalid, driverListIssueUnicode | driverListIssueIdentity
	}
	if err != nil || !validDriverTLA(value) {
		return "", driverListFieldInvalid, driverListIssueIdentity
	}
	return value, driverListFieldValid, 0
}

func parseDriverRacingNumber(
	raw json.RawMessage,
	entryNumber int64,
) (driverListFieldState, driverListIssueSet) {
	value, err := decodeLosslessJSONString(raw)
	if errors.Is(err, errJSONScalar) {
		return driverListFieldInvalid, driverListIssueUnicode | driverListIssueIdentity
	}
	if err != nil {
		return driverListFieldInvalid, driverListIssueIdentity
	}
	number, ok := parseCanonicalDriverNumber(value)
	if !ok || number != entryNumber {
		return driverListFieldInvalid, driverListIssueIdentity
	}
	return driverListFieldValid, 0
}

func validDriverTLA(value string) bool {
	if len(value) < 1 || len(value) > 4 {
		return false
	}
	for index := range len(value) {
		if value[index] < 'A' || value[index] > 'Z' {
			return false
		}
	}
	return true
}

func parseCanonicalDriverNumber(value string) (int64, bool) {
	if len(value) == 0 || value[0] < '1' || value[0] > '9' {
		return 0, false
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return 0, false
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	return number, err == nil
}
