package openf1receiver

import (
	"cmp"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

const maxResponseBytes = 256 * 1024

type Diagnostic string

const (
	DiagIdentity              Diagnostic = "identity"
	DiagTransport             Diagnostic = "transport"
	DiagHTTPStatus            Diagnostic = "http status"
	DiagRateLimit             Diagnostic = "rate limit"
	DiagResponseSize          Diagnostic = "response size"
	DiagDecode                Diagnostic = "decode"
	DiagValidation            Diagnostic = "validation"
	DiagRevisionOverflow      Diagnostic = "revision overflow"
	DiagRequestBudget         Diagnostic = "request budget"
	DiagParticipationConflict Diagnostic = "participation conflict"
)

type Outcome string

const (
	OutcomeFinished        Outcome = "finished"
	OutcomeClassifiedDNF   Outcome = "classified_dnf"
	OutcomeUnclassifiedDNF Outcome = "unclassified_dnf"
	OutcomeDNS             Outcome = "dns"
	OutcomeDSQ             Outcome = "dsq"
	OutcomeUnresolved      Outcome = "unresolved"
)

// Position zero represents a source null and is never exported.
type Row struct {
	DriverNumber int64
	Acronym      string
	Position     int64
	Outcome      Outcome
}

type fields map[string]json.RawMessage

func (f fields) number(k string) int64 { n, _ := f1id.ParsePositiveCanonicalInt64(f[k]); return n }
func (f fields) text(k string) string  { s, _ := f1id.DecodeLosslessString(f[k]); return s }
func (f fields) nullable(k string) (int64, bool) {
	if string(f[k]) == "null" {
		return -1, true
	}
	return f1id.ParseNonNegativeCanonicalInt64(f[k])
}
func (f fields) outcome() (Outcome, int64, bool) {
	p, ok := f.nullable("position")
	laps, okL := f.nullable("number_of_laps")
	if !ok || !okL || p == 0 {
		return "", 0, false
	}
	var flag string
	for _, k := range []string{"dnf", "dns", "dsq"} {
		switch string(f[k]) {
		case "true":
			if flag != "" {
				return "", 0, false
			}
			flag = k
		case "false":
		default:
			return "", 0, false
		}
	}
	p = max(0, p)
	switch flag {
	case "dsq":
		return OutcomeDSQ, p, true
	case "dns":
		return OutcomeDNS, p, p == 0 && laps == 0
	case "dnf":
		if p == 0 {
			return OutcomeUnclassifiedDNF, p, laps >= 0
		}
		return OutcomeClassifiedDNF, p, laps >= 0
	default:
		if p == 0 {
			return OutcomeUnresolved, p, laps > 0
		}
		return OutcomeFinished, p, laps > 0
	}
}

type parsed struct {
	drivers map[int64]string
	rows    []Row
	empty   bool
}

var errInvalid = errors.New("invalid row")

var scheduleParts = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?:\.([0-9]+))?(Z|[+-][0-9]{2}:[0-9]{2})$`)

// Compare whole instants first, retaining all fractional digits for ties.
func validSchedule(start, end string) bool {
	a, b := scheduleParts.FindStringSubmatch(start), scheduleParts.FindStringSubmatch(end)
	if a == nil || b == nil {
		return false
	}
	t, ok := f1id.ParseStrictRFC3339(a[1] + a[3])
	u, valid := f1id.ParseStrictRFC3339(b[1] + b[3])
	return ok && valid && (u.After(t) || u.Equal(t) && strings.TrimRight(b[2], "0") > strings.TrimRight(a[2], "0"))
}

func parse(body []byte, stage string, e f1id.SessionIdentity, key int64, expected map[int64]string) (parsed, Diagnostic) {
	if len(body) > maxResponseBytes {
		return parsed{}, DiagResponseSize
	}
	var result parsed
	keys := "session_key meeting_key "
	switch stage {
	case sessions:
		keys += "year session_type session_name date_start date_end is_cancelled"
	case drivers:
		keys += "driver_number name_acronym"
		result.drivers = map[int64]string{}
	case results:
		keys += "driver_number position number_of_laps dnf dns dsq"
	default:
		return parsed{}, DiagValidation
	}
	known := strings.Fields(keys)
	seen := map[int64]bool{}
	positions := map[int64]bool{}
	count := 0
	err := f1id.VisitArray(body, func(i int, raw json.RawMessage) error {
		count++
		if i >= 32 {
			return errInvalid
		}
		f := fields{}
		err := f1id.VisitObject(raw, func(k string, _, v json.RawMessage) error {
			if slices.Contains(known, k) {
				if f[k] != nil {
					return errInvalid
				}
				f[k] = v
			}
			return nil
		})
		if err != nil {
			return err
		}
		if f.number("session_key") != key || f.number("meeting_key") != e.MeetingKey {
			return errInvalid
		}
		if stage == sessions {
			name := "Race"
			if e.SessionName == "sprint" {
				name = "Sprint"
			}
			if i != 0 || !validSchedule(f.text("date_start"), f.text("date_end")) || f.number("year") != e.Season || f.text("session_type") != "Race" || f.text("session_name") != name || string(f["is_cancelled"]) != "false" {
				return errInvalid
			}
			return nil
		}
		n := f.number("driver_number")
		if n == 0 || seen[n] {
			return errInvalid
		}
		seen[n] = true
		if stage == drivers {
			a := f.text("name_acronym")
			if !f1id.IsValidAcronym(a) {
				return errInvalid
			}
			result.drivers[n] = a
			return nil
		}
		o, p, ok := f.outcome()
		if !ok || expected[n] == "" || p > int64(len(expected)) || p != 0 && positions[p] || p == 1 && o != OutcomeFinished {
			return errInvalid
		}
		positions[p] = true
		result.rows = append(result.rows, Row{n, expected[n], p, o})
		return nil
	})
	if err != nil && err != errInvalid {
		return parsed{}, DiagDecode
	}
	if err == nil && count == 0 {
		return parsed{empty: true}, ""
	}
	if err != nil || stage == drivers && len(expected) > 0 && !maps.Equal(expected, result.drivers) || stage == results && (!positions[1] || count != len(expected)) {
		if stage != results {
			return parsed{}, DiagIdentity
		}
		return parsed{}, DiagValidation
	}
	slices.SortFunc(result.rows, func(a, b Row) int { return cmp.Compare(a.DriverNumber, b.DriverNumber) })
	return result, ""
}
