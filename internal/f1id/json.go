package f1id

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"
)

var (
	errJSONUTF8   = errors.New("JSON is not UTF-8")
	errJSONSyntax = errors.New("invalid JSON grammar")
	errJSONString = errors.New("expected a quoted JSON string")
	errJSONObject = errors.New("expected a JSON object")
	errJSONArray  = errors.New("expected a JSON array")
	ErrJSONScalar = errors.New("JSON string contains an unpaired surrogate")
)

func validateJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errJSONUTF8
	}
	if !json.Valid(raw) {
		return errJSONSyntax
	}
	return nil
}

func DecodeLosslessString(raw []byte) (string, error) {
	if err := validateJSON(raw); err != nil {
		return "", err
	}
	raw = bytes.TrimSpace(raw)
	if raw[0] != '"' {
		return "", errJSONString
	}
	return decodeValidatedString(raw)
}

func decodeValidatedString(raw []byte) (string, error) {
	if HasInvalidJSONScalars(raw) {
		return "", ErrJSONScalar
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", errJSONSyntax
	}
	return s, nil
}

// HasInvalidJSONScalars reports unpaired UTF-16 escapes in a grammar-validated
// JSON value. Skip complete escapes so that literal backslash-u text stays literal.
func HasInvalidJSONScalars(raw []byte) bool {
	for i := 0; i+1 < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' || len(raw)-i < 5 {
			continue
		}
		unit, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		switch {
		case unit >= 0xdc00 && unit <= 0xdfff:
			return true
		case unit >= 0xd800 && unit <= 0xdbff:
			if len(raw)-i < 7 || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return true
			}
			low, _ := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if low < 0xdc00 || low > 0xdfff {
				return true
			}
			i += 6
		}
	}
	return false
}

// The token walkers are called only after validateJSON checks the entire input.
func skipSpace(raw []byte, i int) int {
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

func stringEnd(raw []byte, start int) int {
	for i := start + 1; ; i++ {
		switch raw[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
}

func valueEnd(raw []byte, start int) int {
	depth := 0
	for i := start; ; i++ {
		switch raw[i] {
		case '"':
			i = stringEnd(raw, i) - 1
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return i
			}
			depth--
		case ',', ' ', '\t', '\r', '\n':
			if depth == 0 {
				return i
			}
		}
	}
}

// VisitObject passes read-only views of each member in source order, including
// duplicate keys. Values remain opaque. An invalid key stops the walk with ErrJSONScalar.
func VisitObject(raw []byte, visit func(key string, rawKey, rawVal json.RawMessage) error) error {
	if err := validateJSON(raw); err != nil {
		return err
	}
	i := skipSpace(raw, 0)
	if raw[i] != '{' {
		return errJSONObject
	}
	for i = skipSpace(raw, i+1); raw[i] != '}'; i = skipSpace(raw, i+1) {
		end := stringEnd(raw, i)
		rawKey := raw[i:end]
		key, err := decodeValidatedString(rawKey)
		if err != nil {
			return err
		}
		colon := skipSpace(raw, end)
		start := skipSpace(raw, colon+1)
		end = valueEnd(raw, start)
		if err := visit(key, rawKey, raw[start:end]); err != nil {
			return err
		}
		i = skipSpace(raw, end)
		if raw[i] == '}' {
			break
		}
	}
	return nil
}

// VisitArray passes read-only views of elements without decoding nested values.
func VisitArray(raw []byte, visit func(index int, rawElem json.RawMessage) error) error {
	if err := validateJSON(raw); err != nil {
		return err
	}
	i := skipSpace(raw, 0)
	if raw[i] != '[' {
		return errJSONArray
	}
	i = skipSpace(raw, i+1)
	for index := 0; raw[i] != ']'; index++ {
		end := valueEnd(raw, i)
		if err := visit(index, raw[i:end]); err != nil {
			return err
		}
		i = skipSpace(raw, end)
		if raw[i] == ']' {
			break
		}
		i = skipSpace(raw, i+1)
	}
	return nil
}

func ParseSignedCanonicalInt64(raw []byte) (int64, bool) {
	if len(raw) == 0 || raw[0] == '+' {
		return 0, false
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || decimal(value) != string(raw) {
		return 0, false
	}
	return value, true
}

func ParsePositiveCanonicalInt64(raw []byte) (int64, bool) {
	value, ok := ParseSignedCanonicalInt64(raw)
	if !ok || value <= 0 {
		return 0, false
	}
	return value, true
}

func ParseNonNegativeCanonicalInt64(raw []byte) (int64, bool) {
	value, ok := ParseSignedCanonicalInt64(raw)
	if !ok || value < 0 {
		return 0, false
	}
	return value, true
}

// time.Parse also accepts comma fractions, sub-nanosecond digits, single-digit
// hours, and overflowing numeric offsets. Check the wire grammar first.
var strictRFC3339 = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func ParseStrictRFC3339(raw string) (time.Time, bool) {
	if !strictRFC3339.MatchString(raw) {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
