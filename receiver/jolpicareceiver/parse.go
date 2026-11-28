package jolpicareceiver

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

type table string

const (
	race   table = "results"
	sprint table = "sprint"
	wdc    table = "driverstandings"
	wcc    table = "constructorstandings"
)

var (
	errData        = errors.New("invalid Jolpica table")
	errUnpublished = errors.New("Jolpica table not yet published")
	idPattern      = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	pointsPattern  = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)
	lapped         = regexp.MustCompile(`^\+[1-9][0-9]* Laps?$`)
)

type row struct {
	number, position, grid, laps, wins                    int64
	hasGrid, hasLaps                                      bool
	driverID, acronym, team, teamID, positionText, status string
	points                                                float64
}
type snapshot map[table][]row

type object map[string]json.RawMessage

func readObject(raw json.RawMessage) (object, error) {
	o := object{}
	err := f1id.VisitObject(raw, func(k string, _, v json.RawMessage) error {
		if _, exists := o[k]; exists {
			return errData
		}
		o[k] = v
		return nil
	})
	if err != nil {
		return nil, errData
	}
	return o, nil
}
func (o object) text(k string) string { s, _ := f1id.DecodeLosslessString(o[k]); return s }
func array(raw json.RawMessage, max int) ([]object, error) {
	var list []object
	err := f1id.VisitArray(raw, func(i int, v json.RawMessage) error {
		if i >= max {
			return errData
		}
		o, err := readObject(v)
		if err != nil {
			return err
		}
		list = append(list, o)
		return nil
	})
	if err != nil {
		return nil, errData
	}
	return list, nil
}

func integer(s string, lo, hi int64) (int64, bool) {
	n, ok := f1id.ParseNonNegativeCanonicalInt64([]byte(s))
	return n, ok && n >= lo && n <= hi
}
func parsePoints(s string) (float64, bool) {
	if !pointsPattern.MatchString(s) {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func display(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || len(s) > 128 || strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return "", false
	}
	lower := strings.ToLower(s)
	for _, marker := range []string{"http://", "https://", "://", "authorization", "access token", "access_token", "access-token", "cookie", "secret", "signature", "password", "credential", "bearer ", "basic ", "api key", "api_key", "api-key", "apikey"} {
		if strings.Contains(lower, marker) {
			return "", false
		}
	}
	return s, true
}
func statusCategory(s string) string {
	switch s {
	case "Finished":
		return "finished"
	case "Lapped":
		return "lapped"
	case "Disqualified":
		return "disqualified"
	case "Did not start", "DNS", "Did not qualify", "Did not prequalify":
		return "did_not_start"
	case "Withdrew", "Excluded":
		return "withdrawn"
	case "Not classified":
		return "not_classified"
	case "Fatal accident", "Accident", "Collision", "Collision damage", "Spun off", "Damage", "Debris", "Puncture":
		return "collision_or_incident"
	case "Engine fire", "Engine", "Gearbox", "Transmission", "Clutch", "Hydraulics", "Electrical", "Power Unit", "Power loss", "Brakes", "Suspension", "Overheating", "Mechanical", "Exhaust", "Fuel system", "Fuel pressure", "Fuel pump", "Fuel leak", "Water leak", "Water pressure", "Oil leak", "Oil pressure", "Cooling system", "Turbo", "ERS", "Battery", "Driveshaft", "Steering", "Differential", "Radiator", "Wheel", "Wheel nut", "Vibrations", "Undertray", "Front wing", "Rear wing":
		return "mechanical"
	}
	if lapped.MatchString(s) {
		return "lapped"
	}
	return "other"
}

func (t table) classification() bool { return t == race || t == sprint }
func (t table) schema() (outer, list, entries string, max int) {
	switch t {
	case race:
		return "RaceTable", "Races", "Results", 40
	case sprint:
		return "RaceTable", "Races", "SprintResults", 40
	case wdc:
		return "StandingsTable", "StandingsLists", "DriverStandings", 64
	case wcc:
		return "StandingsTable", "StandingsLists", "ConstructorStandings", 32
	}
	panic("unknown Jolpica table")
}
func parse(c Config, t table, body []byte) ([]row, error) {
	root, err := readObject(body)
	if err != nil || f1id.HasInvalidJSONScalars(body) {
		return nil, errData
	}
	m, err := readObject(root["MRData"])
	if err != nil || m.text("series") != "f1" || m.text("offset") != "0" {
		return nil, errData
	}
	limit, ok := integer(m.text("limit"), 1, math.MaxInt64)
	total, valid := integer(m.text("total"), 0, limit)
	if !ok || !valid {
		return nil, errData
	}
	outer, list, entries, maxRows := t.schema()
	matches := func(o object) bool {
		return o.text("season") == strconv.FormatInt(c.Season, 10) && o.text("round") == strconv.FormatInt(c.Round, 10)
	}
	tab, err := readObject(m[outer])
	if err != nil || !matches(tab) {
		return nil, errData
	}
	lists, err := array(tab[list], 1)
	if err != nil {
		return nil, err
	}
	if len(lists) == 0 {
		if total != 0 {
			return nil, errData
		}
		return nil, errUnpublished
	}
	if !matches(lists[0]) {
		return nil, errData
	}
	items, err := array(lists[0][entries], maxRows)
	if err != nil || len(items) == 0 || int64(len(items)) != total {
		return nil, errData
	}
	rows := make([]row, 0, len(items))
	type rowKey struct {
		id     string
		number int64
	}
	seen := map[rowKey]bool{}
	var hasPosition, hasFirst bool
	for _, item := range items {
		r, err := parseRow(t, item)
		if err != nil {
			return nil, err
		}
		key := rowKey{id: r.driverID}
		if t.classification() {
			key.number = r.number
			hasPosition = hasPosition || r.position > 0
			hasFirst = hasFirst || r.position == 1
		} else if t == wcc {
			key.id = r.teamID
		}
		if seen[key] {
			return nil, errData
		}
		seen[key] = true
		rows = append(rows, r)
	}
	if hasPosition && !hasFirst {
		return nil, errData
	}
	return rows, nil
}

func parseRow(t table, o object) (row, error) {
	var r row
	var ok bool
	r.points, ok = parsePoints(o.text("points"))
	if !ok {
		return row{}, errData
	}
	r.positionText = o.text("positionText")
	_, _, _, maxRank := t.schema()
	if o["position"] != nil && !(t.classification() && string(o["position"]) == "null") {
		r.position, ok = integer(o.text("position"), 1, int64(maxRank))
		if !ok {
			return row{}, errData
		}
	}
	if p, num := integer(r.positionText, 1, 99); num {
		if p > int64(maxRank) || r.position != p && !(t.classification() && r.position == 0) {
			return row{}, errData
		}
	} else if !validPositionCode(t, r.positionText) {
		return row{}, errData
	}
	if t != wcc {
		d, err := readObject(o["Driver"])
		if err != nil {
			return row{}, err
		}
		r.driverID = d.text("driverId")
		if !idPattern.MatchString(r.driverID) {
			return row{}, errData
		}
		if d["code"] != nil {
			r.acronym = d.text("code")
			if !f1id.IsValidAcronym(r.acronym) {
				return row{}, errData
			}
		}
		if t == wdc && d["permanentNumber"] != nil {
			r.number, ok = integer(d.text("permanentNumber"), 1, 999)
			if !ok {
				return row{}, errData
			}
		}
	}
	if t != wdc {
		team, err := readObject(o["Constructor"])
		if err != nil {
			return row{}, err
		}
		r.team, ok = display(team.text("name"))
		if !ok {
			return row{}, errData
		}
		if t == wcc {
			r.teamID = team.text("constructorId")
			if !idPattern.MatchString(r.teamID) {
				return row{}, errData
			}
		}
	}
	if t.classification() {
		r.number, ok = integer(o.text("number"), 1, 999)
		if !ok {
			return row{}, errData
		}
		if raw := o["grid"]; raw != nil && string(raw) != "null" {
			r.grid, r.hasGrid = integer(o.text("grid"), 0, 99)
			if !r.hasGrid {
				return row{}, errData
			}
		}
		if raw := o["laps"]; raw != nil && string(raw) != "null" {
			r.laps, r.hasLaps = integer(o.text("laps"), 0, math.MaxInt64)
			if !r.hasLaps {
				return row{}, errData
			}
		}
		r.status, ok = display(o.text("status"))
		if !ok {
			return row{}, errData
		}
	} else {
		r.wins, ok = integer(o.text("wins"), 0, math.MaxInt64)
		if !ok {
			return row{}, errData
		}
	}
	return r, nil
}

func validPositionCode(t table, code string) bool {
	switch code {
	case "R", "D", "E", "W", "F", "N":
		return true
	case "-":
		return !t.classification()
	}
	return false
}
