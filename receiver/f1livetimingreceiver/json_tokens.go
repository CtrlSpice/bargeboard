package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// These reasons describe input layers, never source text or offsets.
var (
	errJSONUTF8   = errors.New("JSON is not UTF-8")
	errJSONSyntax = errors.New("invalid JSON grammar")
	errJSONString = errors.New("expected a quoted JSON string")
	errJSONScalar = errors.New("JSON string contains an unpaired surrogate")
	errJSONObject = errors.New("expected a JSON object")
)

// decodeLosslessJSONString validates the original token before encoding/json can
// replace unpaired surrogates. It decodes exactly once and does not normalize.
func decodeLosslessJSONString(raw []byte) (string, error) {
	if !utf8.Valid(raw) {
		return "", errJSONUTF8
	}
	if !json.Valid(raw) {
		return "", errJSONSyntax
	}
	raw = bytes.TrimSpace(raw)
	if raw[0] != '"' {
		return "", errJSONString
	}
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue // Includes escaped backslashes: their following text is literal.
		}
		unit := jsonHexUnit(raw[i+1 : i+5])
		i += 4
		switch {
		case unit >= 0xDC00 && unit <= 0xDFFF:
			return "", errJSONScalar
		case unit >= 0xD800 && unit <= 0xDBFF:
			if i+6 >= len(raw)-1 || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return "", errJSONScalar
			}
			low := jsonHexUnit(raw[i+3 : i+7])
			if low < 0xDC00 || low > 0xDFFF {
				return "", errJSONScalar
			}
			i += 6
		}
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errJSONSyntax
	}
	return value, nil
}

// jsonHexUnit is only used after encoding/json has validated escape grammar.
func jsonHexUnit(raw []byte) uint16 {
	var unit uint16
	for _, c := range raw {
		unit <<= 4
		switch {
		case c >= '0' && c <= '9':
			unit |= uint16(c - '0')
		case c >= 'A' && c <= 'F':
			unit |= uint16(c - 'A' + 10)
		default:
			unit |= uint16(c - 'a' + 10)
		}
	}
	return unit
}

// visitRawJSONObject visits source-order members without decoding keys, inserting
// them into a map, or materializing nested strings. Key and value are read-only
// views of raw; callers own scalar validation, duplicate policy, and any copies.
// Full UTF-8 and encoding/json syntax validation precede all callbacks. This
// profile charges the entire object against encoding/json's 10,000-depth limit,
// preserving negotiation/handshake's whole-value Unmarshal contract.
func visitRawJSONObject(raw []byte, visit func(key, value json.RawMessage) error) error {
	if !utf8.Valid(raw) {
		return errJSONUTF8
	}
	if !json.Valid(raw) {
		return errJSONSyntax
	}
	return walkRawJSONObject(raw, false, visit)
}

// visitRawJSONObjectMembers has the same lossless view/callback contract, but
// gives each member value its own encoding/json nesting budget. Hub and snapshot
// parsing previously used Token for the outer object and Decode for each value;
// charging the outer object too would reject previously accepted deep payloads.
// A complete first pass validates outer punctuation and every original key/value
// token before any caller callback. Both passes are linear and nonrecursive,
// with constant space beyond encoding/json's bounded syntax stack. The shell
// bounds bytes; no AST, decoded keys, or member list is built by either profile.
func visitRawJSONObjectMembers(raw []byte, visit func(key, value json.RawMessage) error) error {
	if !utf8.Valid(raw) {
		return errJSONUTF8
	}
	if err := walkRawJSONObject(raw, true, nil); err != nil {
		return err
	}
	return walkRawJSONObject(raw, false, visit)
}

// walkRawJSONObject checks outer grammar and optionally validates member tokens.
// With validateMembers false, the complete input must already be validated under
// the owning depth profile. A nil visitor performs only the validation pass.
func walkRawJSONObject(raw []byte, validateMembers bool, visit func(key, value json.RawMessage) error) error {
	i := skipJSONSpace(raw, 0)
	if i == len(raw) {
		return errJSONSyntax
	}
	if raw[i] != '{' {
		if !json.Valid(raw) {
			return errJSONSyntax
		}
		return errJSONObject
	}
	i = skipJSONSpace(raw, i+1)
	if i < len(raw) && raw[i] == '}' {
		if skipJSONSpace(raw, i+1) != len(raw) {
			return errJSONSyntax
		}
		return nil
	}
	for {
		if i >= len(raw) || raw[i] != '"' {
			return errJSONSyntax
		}
		keyEnd := jsonStringEnd(raw, i)
		if validateMembers && !json.Valid(raw[i:keyEnd]) {
			return errJSONSyntax
		}
		colon := skipJSONSpace(raw, keyEnd)
		if colon == len(raw) || raw[colon] != ':' {
			return errJSONSyntax
		}
		valueStart := skipJSONSpace(raw, colon+1)
		valueEnd := jsonValueEnd(raw, valueStart)
		if validateMembers && !json.Valid(raw[valueStart:valueEnd]) {
			return errJSONSyntax
		}
		if visit != nil {
			if err := visit(raw[i:keyEnd], raw[valueStart:valueEnd]); err != nil {
				return err
			}
		}
		i = skipJSONSpace(raw, valueEnd)
		if i == len(raw) {
			return errJSONSyntax
		}
		switch raw[i] {
		case '}':
			if skipJSONSpace(raw, i+1) != len(raw) {
				return errJSONSyntax
			}
			return nil
		case ',':
			i = skipJSONSpace(raw, i+1)
		default:
			return errJSONSyntax
		}
	}
}

// The boundary helpers locate candidate tokens, not validate them. They are
// bounds-safe even for truncated/malformed input: jsonStringEnd requires an
// opening quote at start, and jsonValueEnd accepts start == len(raw). Unterminated
// candidates end at len(raw); encoding/json rejects their grammar in the first
// pass. Nesting uses only a counter here; encoding/json checks delimiter pairing
// and its unchanged nesting limit for each token in the owning profile.
func skipJSONSpace(raw []byte, i int) int {
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\r' || raw[i] == '\n') {
		i++
	}
	return i
}

func jsonStringEnd(raw []byte, start int) int {
	for i := start + 1; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(raw)
}

func jsonValueEnd(raw []byte, start int) int {
	depth := 0
	for i := start; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			i = jsonStringEnd(raw, i) - 1
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
	return len(raw)
}

// Preserve encoding/json's scalar-null no-op wherever the existing protocol
// decoder used a string destination. Field grammar remains the caller's job.
func unmarshalControlString(raw []byte, destination *string) error {
	if bytes.Equal(bytes.Trim(raw, " \t\r\n"), []byte("null")) {
		return nil
	}
	value, err := decodeLosslessJSONString(raw)
	if err == nil {
		*destination = value
	}
	return err
}

type jsonControlString string

func (s *jsonControlString) UnmarshalJSON(raw []byte) error {
	return unmarshalControlString(raw, (*string)(s))
}

// Descriptive error text is never decoded. For a syntax-validated quoted token,
// only two quotes means empty, even if a nonempty token has malformed scalars.
func jsonStringShape(raw []byte) (isString, empty bool) {
	raw = bytes.TrimSpace(raw)
	return len(raw) >= 2 && raw[0] == '"', bytes.Equal(raw, []byte(`""`))
}
