package f1staticreceiver

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

type publicationTable struct {
	rows     map[int64]fields
	overflow bool
}
type recordState struct{ rc, radio publicationTable }

// Pending indexed records keep only fields needed for completion. Sanitize
// Message before retaining it, including when Category arrives much later.
func mergePublication(row, patch fields, radio bool) {
	for k, v := range patch {
		switch k {
		case "Message":
			if radio {
				continue
			}
			delete(row, "repair")
			delete(row, "redacted")
			delete(row, "truncated")
			value, err := f1id.DecodeLosslessString(v)
			if err != nil {
				delete(row, k)
				continue
			}
			flags := attrs{}
			body := sanitize(value, flags)
			row[k], _ = json.Marshal(body)
			if body == value {
				row["repair"] = row[k]
			}
			if flags["f1.race_control.message_redacted"] == true {
				row["redacted"] = json.RawMessage("true")
			}
			if flags["f1.race_control.message_truncated"] == true {
				row["truncated"] = json.RawMessage("true")
			}
		case "Utc", "RacingNumber", "Category", "Scope", "Mode", "Status", "Flag", "Path":
			// The longest valid optional enum is 64 ASCII characters. An
			// overlong string is still present but cannot match the grammar.
			value, err := f1id.DecodeLosslessString(v)
			if err != nil {
				row[k] = json.RawMessage("null")
				continue
			}
			if len(value) > 64 {
				value = "!"
			}
			row[k], _ = json.Marshal(value)
		case "Lap", "Sector":
			if len(v) <= 19 {
				row[k] = v
			} else {
				row[k] = json.RawMessage("null")
			}
		}
	}
}

var radioPath = regexp.MustCompile(`^TeamRadio/(?:[A-Z]{3}|[A-Z]{6}[0-9]{2})_([1-9][0-9]*)_[0-9]{8}_[0-9]{6}\.mp3$`)
var radioUTC = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,7})?Z$`)
var enumWord = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9 _-]{0,62}[A-Za-z0-9])?$`)
var separators = regexp.MustCompile(`[ _-]+`)

func sanitize(msg string, a attrs) string {
	msg = strings.Join(strings.FieldsFunc(msg, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }), " ")
	lower := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, msg)
	for _, unsafe := range []string{"http://", "https://", "authorization", "access token", "access_token", "access-token", "cookie", "secret", "signature", "api key", "api_key", "api-key"} {
		if strings.Contains(lower, unsafe) {
			a["f1.race_control.message_redacted"] = true
			return "[redacted-message]"
		}
	}
	if len(msg) > 4096 {
		n := 4096
		for !utf8.RuneStart(msg[n]) {
			n--
		}
		msg = msg[:n]
		a["f1.race_control.message_truncated"] = true
	}
	return msg
}
func (s *session) publications(topic string, f fields, t time.Time) {
	radio := topic == "TeamRadio"
	table := &s.records.rc
	raw := f["Messages"]
	initialArray := false
	if radio {
		table = &s.records.radio
		raw = f["Captures"]
		initialArray = table.rows == nil && len(raw) > 0 && raw[0] == '['
		if table.rows == nil {
			table.rows = map[int64]fields{}
		}
		if !initialArray && (len(raw) == 0 || raw[0] != '{') {
			return
		}
	}
	if table.rows == nil {
		table.rows = map[int64]fields{}
	}
	updates := numbered(raw)
	if len(raw) > 0 && raw[0] == '[' {
		size := len(table.rows)
		for i := range updates {
			if _, ok := table.rows[i]; !ok {
				size++
			}
		}
		if size > 4096 {
			if !table.overflow {
				s.issue("publication index limit")
			}
			table.overflow = true
			return
		}
		for i, row := range table.rows {
			if row != nil {
				table.rows[i] = fields{}
			}
		}
	}
	for _, i := range slices.Sorted(maps.Keys(updates)) {
		row, exists := table.rows[i]
		if !exists {
			if len(table.rows) >= 4096 {
				if !table.overflow {
					s.issue("publication index limit")
				}
				table.overflow = true
			}
			if table.overflow {
				continue
			}
			row = fields{}
			table.rows[i] = row
		}
		if row == nil {
			continue
		}
		if table.overflow && !radio {
			continue
		}
		patch := object(updates[i])
		if !radio && string(patch["_deleted"]) == "true" {
			clear(row)
			continue
		}
		mergePublication(row, patch, radio)
		a := attrs{"f1.time.quality": "publication_time"}
		at := t
		utc := text(row["Utc"])
		k := text(row["RacingNumber"])
		d := s.drivers[k]
		if d != nil {
			maps.Copy(a, d.attrs())
		}
		if radio {
			if initialArray {
				table.rows[i] = nil
			}
			if row["Path"] == nil || row["RacingNumber"] == nil {
				continue
			}
			table.rows[i] = nil
			path := text(row["Path"])
			m := radioPath.FindStringSubmatch(path)
			if _, ok := f1id.ParsePositiveCanonicalInt64([]byte(k)); !ok || m == nil || m[1] != k {
				continue
			}
			if radioUTC.MatchString(utc) {
				if obs, ok := f1id.ParseStrictRFC3339(utc); ok && validReplayTime(obs) {
					at = obs
					a["f1.time.quality"] = "observed"
				}
			}
			a["f1.team_radio.path"] = path
			a["f1.team_radio.index"] = i
			s.log(fact{"f1.team_radio.available", at, a}, d)
			continue
		}
		cat, err := f1id.DecodeLosslessString(row["Category"])
		msg, msgErr := f1id.DecodeLosslessString(row["Message"])
		if err != nil || msgErr != nil {
			continue
		}
		table.rows[i] = nil
		for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04:05.000"} {
			if obs, err := time.Parse(layout, utc); err == nil && obs.Format(layout) == utc && validReplayTime(obs) {
				at = obs
				a["f1.time.quality"] = "observed"
				break
			}
		}
		body := msg
		if row["redacted"] != nil {
			a["f1.race_control.message_redacted"] = true
		}
		if row["truncated"] != nil {
			a["f1.race_control.message_truncated"] = true
		}
		category := map[string]string{"Flag": "flag", "CarEvent": "car_event", "Drs": "drs", "Other": "other", "SafetyCar": "safety_car"}[cat]
		if category == "" {
			category = "unknown"
		}
		a["f1.race_control.index"] = i
		a["f1.race_control.category"] = category
		for _, k := range []string{"Scope", "Mode", "Flag", "Status"} {
			v := text(row[k])
			if enumWord.MatchString(v) {
				a["f1.race_control."+strings.ToLower(k)] = separators.ReplaceAllString(strings.ToLower(v), "_")
			}
		}
		for _, k := range []string{"Lap", "Sector"} {
			if n, ok := f1id.ParseNonNegativeCanonicalInt64(row[k]); ok && (k != "Sector" || n > 0) {
				a["f1.race_control."+strings.ToLower(k)] = n
			}
		}
		if cat == "Other" && row["repair"] != nil {
			s.repair(i, msg, body, row, a, at)
		}
		n, _ := a["f1.driver.number"].(int64)
		s.log(fact{body, at, a}, s.drivers[decimal(n)])
		if d != nil && (cat == "CarEvent" || (cat == "Flag" && text(row["Scope"]) == "Driver")) {
			name := "f1.race_control.driver_notice"
			if cat == "Flag" && text(row["Flag"]) == "BLUE" {
				name = "f1.blue_flag.shown"
				if !s.start.IsZero() && !at.Before(s.start) && s.ends.IsZero() {
					s.blue = append(s.blue, blueFlag{at, d})
				}
			}
			eventAttrs := maps.Clone(a)
			eventAttrs["f1.race_control.message"] = body
			d.event(name, at, eventAttrs)
		}
	}
}
