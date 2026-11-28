package f1engine

import (
	"cmp"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

type topicFields map[string]json.RawMessage

func topicObject(raw json.RawMessage) (topicFields, error) {
	fields := topicFields{}
	err := visitSessionFields(raw, func(k string, v json.RawMessage) { fields[k] = v })
	if err != nil {
		if err == ErrJSONObject {
			return nil, nil
		}
		return nil, err
	}
	return fields, nil
}

func nestedObject(raw json.RawMessage) topicFields { f, _ := topicObject(raw); return f }
func topicArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	var a []json.RawMessage
	err := json.Unmarshal(raw, &a)
	return a, err == nil
}

func copyMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return make(map[K]V)
	}
	return maps.Clone(m)
}

var weatherFields = [...]struct{ key, name, unit string }{
	{"AirTemp", "air_temperature", "Cel"}, {"TrackTemp", "track_temperature", "Cel"}, {"Humidity", "relative_humidity", "%"},
	{"Pressure", "air_pressure", "hPa"}, {"WindSpeed", "wind_speed", "m/s"}, {"WindDirection", "wind_direction", "deg"}, {"Rainfall", "rainfall_detected", "1"},
}

type weatherReading struct {
	value              int64
	lastEmitted        time.Time
	observed, deadline time.Duration
	fresh              bool
}
type rainfallEdge struct {
	at  time.Time
	wet bool
}
type weatherState struct {
	fields [7]weatherReading
	source [7]struct {
		value int64
		known bool
	} // last valid received value, including replay
	baseline, unsynchronized bool
	ended                    bool
	seen                     map[rainfallEdge]bool
}

func weatherValue(i int, s string) (int64, bool) {
	if i >= 5 {
		v, ok := canonicalIndex(s)
		return v, ok && (i == 5 && v < 360 || i == 6 && v <= 1)
	}
	whole, frac, ok := strings.Cut(s, ".")
	unsigned := strings.TrimPrefix(whole, "-")
	if !ok || len(frac) != 1 || frac[0] < '0' || frac[0] > '9' || unsigned != whole && i >= 2 {
		return 0, false
	}
	if _, ok := canonicalIndex(unsigned); !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(whole+frac, 10, 64)
	return v, err == nil && !(v == 0 && unsigned != whole) && !(i == 2 && v > 1000) && !(i == 3 && v == 0)
}
func omitWeather(s weatherState) weatherState { return weatherState{seen: s.seen, ended: s.ended} }
func (s weatherState) withSessionStatus(status string) weatherState {
	if status == "Ends" {
		s = omitWeather(s)
		s.ended = true
	}
	return s
}
func disconnectWeather(s weatherState) weatherState {
	s.unsynchronized = true
	s.baseline = false
	return s
}
func tickWeather(s weatherState, now time.Duration) weatherState {
	for i, r := range s.fields {
		if r.fresh && now >= r.deadline {
			s.fields[i].fresh = false
			if i == 6 {
				s.baseline = false
			}
		}
	}
	return s
}

// Supply equal-publication-time patches from one callback together. Within that
// callback the last explicit field wins; a later callback has its own replay gate.
func reduceWeatherData(s weatherState, o sessionObservation, patches ...json.RawMessage) (weatherState, Effects, error) {
	var e Effects
	var present, valid [7]bool
	var values [7]int64
	var invalidSnapshot, rainInvalid bool
	for _, raw := range patches {
		err := visitSessionFields(raw, func(key string, v json.RawMessage) {
			for i, k := range weatherFields {
				if k.key == key {
					present[i] = true
					values[i], valid[i] = weatherValue(i, sessionText(v))
					if !valid[i] {
						invalidSnapshot = true
						if i == 6 {
							rainInvalid = true
						}
					}
					break
				}
			}
		})
		if err == ErrJSONObject {
			invalidSnapshot = true
		} else if err != nil {
			return s, e, err
		}
	}
	at := o.publication
	if s.ended {
		return s, e, nil
	}
	if o.snapshot() {
		if invalidSnapshot {
			s.unsynchronized = true
			return s, e, nil
		}
		s = omitWeather(s)
		at = o.observed
	} else if s.unsynchronized {
		return s, e, nil
	}
	s = tickWeather(s, o.monotonic)
	for i, k := range weatherFields {
		r := s.fields[i]
		if !present[i] {
			continue
		}
		v, ok := values[i], valid[i]
		if ok {
			s.source[i].value, s.source[i].known = v, true
		}
		if !o.snapshot() && !at.After(r.lastEmitted) {
			continue
		}
		if i == 6 && rainInvalid {
			s.baseline = false
		}
		if !ok {
			continue
		}
		m := sessionGauge(o.Identity, "f1.session.weather."+k.name, k.unit, at, v)
		if i < 5 {
			m.kind, m.floatVal, m.intVal = metricDoubleGauge, float64(v)/10, 0
		}
		e.metrics = append(e.metrics, m)
		if i == 6 {
			edge := rainfallEdge{at.UTC(), v == 1}
			if s.baseline && r.value != v && !s.seen[edge] {
				s.seen = copyMap(s.seen)
				s.seen[edge] = true
				e.logs = append(e.logs, recordLog(o, "f1.weather.rainfall.changed", at, "publication_time", []keyValue{boolAttr("f1.weather.rainfall_detected", v == 1), boolAttr("f1.weather.previous_rainfall_detected", r.value == 1)}))
			}
			s.baseline = true
		}
		s.fields[i] = weatherReading{v, at, o.monotonic, o.monotonic + 125*time.Second, true}
	}
	return s, e, nil
}

func recordLog(o sessionObservation, body string, t time.Time, q string, attrs []keyValue) logEffect {
	return logEffect{identity: o.Identity, dataSource: "livetiming", body: body, timestamp: t, observedTimestamp: o.observed, timeQuality: q, attrs: attrs}
}
func resolvedDriver(reg driverRegistryState, num int64) (driverRegistryEntry, bool) {
	if reg.frozen && reg.synchronized {
		if i, ok := driverRegistryEntryIndex(reg, num); ok {
			return reg.drivers[i], true
		}
	}
	return driverRegistryEntry{}, false
}
func driverAttrs(d driverRegistryEntry) []keyValue {
	return []keyValue{intAttr("f1.driver.number", d.number), strAttr("f1.driver.acronym", d.tla)}
}

const recordLimit = 4096

// Per-update diagnostic bits contain no source strings or identity values.
type recordIssues uint8

const (
	recordShape recordIssues = 1 << iota
	recordIdentity
	recordInvalidField
	recordTimeFallback
	recordOverflow
)

type indexedRecords[R any] struct {
	// A nil record is a consumed identity; non-nil records are immutable.
	rows                     map[int64]*R
	unsynchronized, overflow bool
	issues                   recordIssues
}

func clearPending[R any](s indexedRecords[R]) indexedRecords[R] {
	s.rows = copyMap(s.rows)
	for i, r := range s.rows {
		if r != nil {
			s.rows[i] = new(R)
		}
	}
	s.unsynchronized = false
	return s
}

type indexedPatch struct {
	index  int64
	raw    json.RawMessage
	remove bool
}

func recordPatches(raw json.RawMessage, arrayOK, mapOK, allowDelete bool) (patches []indexedPatch, valid bool) {
	if a, ok := topicArray(raw); ok && arrayOK {
		for i, v := range a {
			patches = append(patches, indexedPatch{index: int64(i), raw: v})
		}
		return patches, true
	}
	if !mapOK {
		return nil, false
	}
	valid = true
	err := VisitRawJSONObject(raw, func(key, v json.RawMessage) error {
		k := sessionText(key)
		if k == "_deleted" && allowDelete {
			if names, ok := topicArray(v); ok {
				for _, name := range names {
					if n, ok := canonicalIndex(sessionText(name)); ok {
						patches = append(patches, indexedPatch{index: n, remove: true})
					}
				}
			}
			return nil
		}
		i, ok := canonicalIndex(k)
		if !ok {
			valid = false
			return nil
		}
		patches = append(patches, indexedPatch{index: i, raw: v})
		return nil
	})
	slices.SortStableFunc(patches, func(a, b indexedPatch) int {
		if a.index == b.index && a.remove != b.remove {
			if a.remove {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.index, b.index)
	})
	return patches, valid && err == nil
}

// Complete collections preflight the identity union before replacing any payload.
func applyRecords[R any](s indexedRecords[R], o sessionObservation, raw json.RawMessage, radio bool, apply func(int64, R, topicFields) (R, bool)) indexedRecords[R] {
	s.issues = 0
	array := len(raw) > 0 && raw[0] == '['
	p, valid := recordPatches(raw, !radio || o.snapshot() || o.initialArchiveRadio, true, !radio && !o.snapshot())
	if !valid {
		s.issues |= recordShape
	}
	if o.snapshot() && !valid {
		s.unsynchronized = true
		return s
	}
	if !o.snapshot() && (s.unsynchronized || !valid && len(p) == 0) {
		return s
	}
	if o.snapshot() || array {
		union := map[int64]bool{}
		for _, row := range p {
			if _, ok := s.rows[row.index]; !ok && !row.remove {
				union[row.index] = true
			}
		}
		if len(s.rows)+len(union) > recordLimit {
			if !s.overflow {
				s.issues |= recordOverflow
			}
			if o.snapshot() {
				s.unsynchronized = true
			} else {
				s.overflow = true
			}
			return s
		}
		s = clearPending(s)
	} else {
		s.rows = copyMap(s.rows)
	}
	for _, row := range p {
		prev, exists := s.rows[row.index]
		if row.remove {
			if prev != nil {
				s.rows[row.index] = new(R)
			}
			continue
		}
		if exists && prev == nil {
			continue
		}
		if !exists && (s.overflow || len(s.rows) >= recordLimit) {
			if !s.overflow {
				s.issues |= recordOverflow
			}
			s.overflow = true
			continue
		}
		if !radio && s.overflow && !o.snapshot() {
			continue
		}
		var r R
		if prev != nil {
			r = *prev
		}
		f := nestedObject(row.raw)
		if radio && !o.snapshot() && (f == nil || f["_deleted"] != nil) {
			s.issues |= recordShape
			if array {
				s.rows[row.index] = nil
			}
			continue
		}
		next, done := apply(row.index, r, f)
		if done || o.snapshot() || radio && array {
			s.rows[row.index] = nil
		} else {
			s.rows[row.index] = &next
		}
	}
	return s
}

type captureRecord struct {
	number, path       string
	hasNumber, hasPath bool
	utc                recordTime
}
type teamRadioState = indexedRecords[captureRecord]

func omitTeamRadio(s teamRadioState) teamRadioState { return clearPending(s) }
func disconnectTeamRadio(s teamRadioState) teamRadioState {
	s = clearPending(s)
	s.unsynchronized = true
	return s
}

var capturePath = regexp.MustCompile(`^TeamRadio/(?:[A-Z]{3}|[A-Z]{6}[0-9]{2})_([1-9][0-9]*)_[0-9]{8}_[0-9]{6}\.mp3$`)

const recordDate = `[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}`

var captureTime = regexp.MustCompile(`^` + recordDate + `(?:\.[0-9]{1,7})?Z$`)
var controlTime = regexp.MustCompile(`^` + recordDate + `(?:\.[0-9]{3})?$`)

type recordTime struct {
	at    time.Time
	valid bool
}

func recordUTC(s string, radio bool) recordTime {
	if radio {
		if !captureTime.MatchString(s) {
			return recordTime{}
		}
	} else {
		if !controlTime.MatchString(s) {
			return recordTime{}
		}
		s += "Z"
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return recordTime{t, err == nil}
}
func publicationFallback(t recordTime, publication time.Time) (time.Time, string) {
	if !t.valid {
		return publication, "publication_time"
	}
	return t.at, "observed"
}

type exportedRootContains func(int64, time.Time) bool

func reduceTeamRadio(s teamRadioState, o sessionObservation, raw json.RawMessage, reg driverRegistryState, root exportedRootContains) (teamRadioState, Effects, error) {
	f, err := topicObject(raw)
	var e Effects
	if err != nil {
		return s, e, err
	}
	if k, ok := f["_kf"]; o.snapshot() && ok && string(k) != "true" {
		s.issues = recordShape
		s.unsynchronized = true
		return s, e, nil
	}
	var issues recordIssues
	s = applyRecords(s, o, f["Captures"], true, func(i int64, r captureRecord, f topicFields) (captureRecord, bool) {
		if o.snapshot() {
			return r, true
		}
		if v, ok := f["RacingNumber"]; ok {
			r.hasNumber = true
			r.number = sessionText(v)
			if _, valid := parseCanonicalDriverNumber(r.number); !valid {
				r.number = ""
			}
		}
		if v, ok := f["Path"]; ok {
			r.hasPath = true
			r.path = sessionText(v)
			if len(r.path) > 58 || !capturePath.MatchString(r.path) {
				r.path = ""
			}
		}
		if v, ok := f["Utc"]; ok {
			r.utc = recordUTC(sessionText(v), true)
		}
		if !r.hasNumber || !r.hasPath {
			return r, false
		}
		n, ok := parseCanonicalDriverNumber(r.number)
		match := capturePath.FindStringSubmatch(r.path)
		if ok && match != nil && match[1] == r.number {
			if !r.utc.valid {
				issues |= recordTimeFallback
			}
			t, q := publicationFallback(r.utc, o.publication)
			log := recordLog(o, "f1.team_radio.available", t, q, []keyValue{intAttr("f1.team_radio.index", i), strAttr("f1.team_radio.path", r.path)})
			if d, ok := resolvedDriver(reg, n); ok {
				log.attrs = append(log.attrs, driverAttrs(d)...)
				if root != nil && root(n, t) {
					log.hasTrace = true
					log.traceID = DeterministicTraceID(o.Identity, n)
					log.spanID = DeterministicRootSpanID(o.Identity, n)
				}
			} else {
				issues |= recordIdentity
			}
			e.logs = append(e.logs, log)
		} else {
			issues |= recordInvalidField
		}
		return r, true
	})
	s.issues |= issues
	return s, e, nil
}

type controlMessage struct {
	body                string
	redacted, truncated bool
	repair              repairClues
}

func sanitizeControl(s string) controlMessage {
	body := strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }), " ")
	fold := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}, body)
	for _, term := range []string{"http://", "https://", "authorization", "access token", "access_token", "access-token", "cookie", "secret", "signature", "api key", "api_key", "api-key"} {
		if strings.Contains(fold, term) {
			return controlMessage{body: "[redacted-message]", redacted: true}
		}
	}
	if len(body) > 4096 {
		end := 4096
		for !utf8.RuneStart(body[end]) {
			end--
		}
		return controlMessage{body: strings.Clone(body[:end]), truncated: true, repair: parseRepair(s)}
	}
	return controlMessage{body: strings.Clone(body), repair: parseRepair(s)}
}

type repairClues struct {
	number, reasonLap, durationMS int64
	hasDuration, reinstate        bool
	acronym, phase                string
}

const (
	pos          = `[1-9][0-9]*`
	dur          = `[0-9]+:[0-5][0-9]\.[0-9]{3}`
	clockPattern = `(?:[0-9]|[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]`
	turn         = `TRACK LIMITS AT TURN ` + pos
	yellow       = `DOUBLE YELLOW AT TURN ` + pos
	completed    = ` LAP ` + pos + ` ` + clockPattern
	timeReasons  = `(?:` + turn + `(?:` + completed + `|(?: LAP ` + pos + `)? \(NEXT LAP\))|TRACK LIMITS AT PIT ENTRY` + completed + `|` + yellow + `(?:` + completed + `)?)`
	lapReasons   = `(?:` + turn + `(?:` + completed + `(?: \(PIT\))?| \(NEXT LAP PIT\))|` + yellow + `(?:` + completed + `(?: \(PIT\))?)?)`
)

var numberedRepair = regexp.MustCompile(`^CAR (` + pos + `) \(([A-Z]{3})\) (?:TIME (` + dur + `) (?:DELETED - ` + timeReasons + `|WILL BE REINSTATED)|LAP (?:DELETED - ` + lapReasons + `|` + pos + ` WILL BE REINSTATED))$`)
var phasedDuration = regexp.MustCompile(`^CAR ([A-Z]{3}) TIME (` + dur + `) DELETED - ` + turn + ` \(NEXT LAP\) \((Q[12])\)$`)
var phasedPit = regexp.MustCompile(`^CAR ([A-Z]{3}) TIME DELETED - ` + turn + ` \(NEXT LAP PIT\) \(Q2\)$`)
var lapClue = regexp.MustCompile(` LAP (` + pos + `) `)
var numericPart = regexp.MustCompile(`[0-9]+`)

func parseRepair(s string) repairClues {
	var r repairClues
	var duration string
	switch {
	case numberedRepair.MatchString(s):
		m := numberedRepair.FindStringSubmatch(s)
		r.number, _ = strconv.ParseInt(m[1], 10, 64)
		r.acronym, duration = strings.Clone(m[2]), m[3]
		r.reinstate = strings.HasSuffix(s, "WILL BE REINSTATED")
	case phasedDuration.MatchString(s):
		m := phasedDuration.FindStringSubmatch(s)
		r.acronym, duration, r.phase = strings.Clone(m[1]), m[2], strings.ToLower(m[3])
	case phasedPit.MatchString(s):
		r.acronym = strings.Clone(phasedPit.FindStringSubmatch(s)[1])
		r.phase = "q2"
	default:
		return r
	}
	for _, n := range numericPart.FindAllString(s, -1) {
		if _, err := strconv.ParseInt(n, 10, 64); err != nil {
			return repairClues{}
		}
	}
	if m := lapClue.FindStringSubmatch(s); m != nil {
		r.reasonLap, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if duration != "" {
		p := numericPart.FindAllString(duration, -1)
		min, _ := strconv.ParseInt(p[0], 10, 64)
		sec, _ := strconv.ParseInt(p[1], 10, 64)
		ms, _ := strconv.ParseInt(p[2], 10, 64)
		if min > (1<<63-1-sec*1000-ms)/60000 {
			return repairClues{}
		}
		r.durationMS = min*60000 + sec*1000 + ms
		if r.durationMS == 0 || r.durationMS > (1<<63-1)/int64(time.Millisecond) {
			return repairClues{}
		}
		r.hasDuration = true
	}
	return r
}

type lapRepairSummary struct {
	driver, lap, boundary, durationMS int64
	phase                             string
	hasDuration                       bool
	exported                          bool
}
type deletedLap struct {
	index int64
	clues repairClues
}
type repairKey struct {
	driver, lap int64
	phase       string
}
type controlRecord struct {
	category                  string
	message                   controlMessage
	hasMessage, hasNumber     bool
	number                    int64
	utc                       recordTime
	lap, sector               lapField
	flag, scope, status, mode string
	driverScope, blue         bool
}

var controlEnum = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9 _-]{0,62}[A-Za-z0-9])?$`)
var enumSeparator = regexp.MustCompile(`[ _-]+`)

func controlEnumValue(v string) string {
	if !controlEnum.MatchString(v) {
		return ""
	}
	return enumSeparator.ReplaceAllString(strings.ToLower(v), "_")
}
func patchControl(r controlRecord, f topicFields) controlRecord {
	if names, ok := topicArray(f["_deleted"]); ok {
		for _, name := range names {
			switch sessionText(name) {
			case "Category":
				r.category = ""
			case "Message":
				r.message = controlMessage{}
				r.hasMessage = false
			case "Utc":
				r.utc = recordTime{}
			case "RacingNumber":
				r.hasNumber = false
				r.number = 0
			case "Lap":
				r.lap = lapField{}
			case "Sector":
				r.sector = lapField{}
			case "Flag":
				r.flag = ""
				r.blue = false
			case "Scope":
				r.scope = ""
				r.driverScope = false
			case "Status":
				r.status = ""
			case "Mode":
				r.mode = ""
			}
		}
	}
	if v, ok := f["Category"]; ok {
		c, err := DecodeLosslessJSONString(v)
		r.category = map[string]string{"Flag": "flag", "Other": "other", "Drs": "drs", "SafetyCar": "safety_car", "CarEvent": "car_event"}[c]
		if r.category == "" && err == nil {
			r.category = "unknown"
		}
	}
	if v, ok := f["Message"]; ok {
		s, err := DecodeLosslessJSONString(v)
		r.hasMessage = err == nil
		r.message = sanitizeControl(s)
	}
	if v, ok := f["RacingNumber"]; ok {
		r.hasNumber = true
		n, valid := parseCanonicalDriverNumber(sessionText(v))
		if !valid {
			n = 0
		}
		r.number = n
	}
	if v, ok := f["Utc"]; ok {
		r.utc = recordUTC(sessionText(v), false)
	}
	if v, ok := f["Lap"]; ok {
		r.lap.value, r.lap.known = canonicalIndex(string(v))
	}
	if v, ok := f["Sector"]; ok {
		r.sector.value, r.sector.known = parseCanonicalDriverNumber(string(v))
	}
	if v, ok := f["Flag"]; ok {
		s := sessionText(v)
		r.flag, r.blue = controlEnumValue(s), s == "BLUE"
	}
	if v, ok := f["Scope"]; ok {
		s := sessionText(v)
		r.scope, r.driverScope = controlEnumValue(s), s == "Driver"
	}
	if v, ok := f["Status"]; ok {
		r.status = controlEnumValue(sessionText(v))
	}
	if v, ok := f["Mode"]; ok {
		r.mode = controlEnumValue(sessionText(v))
	}
	return r
}
func (r controlRecord) attrs(i int64) []keyValue {
	const prefix = "f1.race_control."
	a := []keyValue{intAttr(prefix+"index", i), strAttr(prefix+"category", r.category)}
	if r.lap.known {
		a = append(a, intAttr(prefix+"lap", r.lap.value))
	}
	if r.sector.known {
		a = append(a, intAttr(prefix+"sector", r.sector.value))
	}
	for _, kv := range []keyValue{strAttr(prefix+"flag", r.flag), strAttr(prefix+"scope", r.scope), strAttr(prefix+"status", r.status), strAttr(prefix+"mode", r.mode)} {
		if kv.value.strVal != "" {
			a = append(a, kv)
		}
	}
	if r.message.redacted {
		a = append(a, boolAttr(prefix+"message_redacted", true))
	}
	if r.message.truncated {
		a = append(a, boolAttr(prefix+"message_truncated", true))
	}
	return a
}

type blueCandidate struct {
	driver driverRegistryEntry
	at     time.Time
	index  int64
}
type raceControlState struct {
	indexedRecords[controlRecord]
	summaries      map[int64][]lapRepairSummary
	repairOverflow map[int64]bool
	deleted        map[repairKey]deletedLap
	blue           []blueCandidate
	blueAfterEnd   bool
}

func retainLapRepairSummary(s raceControlState, l lapRepairSummary) raceControlState {
	if s.repairOverflow[l.driver] {
		return s
	}
	rows := s.summaries[l.driver]
	idx := slices.IndexFunc(rows, func(old lapRepairSummary) bool { return old.lap == l.lap && old.phase == l.phase })
	if idx >= 0 && rows[idx].exported {
		return s
	}
	if idx < 0 && len(rows) == 256 {
		s.repairOverflow = copyMap(s.repairOverflow)
		s.repairOverflow[l.driver] = true
		return s
	}
	rows = slices.Clone(rows)
	if idx < 0 {
		rows = append(rows, l)
	} else {
		rows[idx] = l
	}
	s.summaries = copyMap(s.summaries)
	s.summaries[l.driver] = rows
	return s
}
func omitRaceControlMessages(s raceControlState) raceControlState {
	s.indexedRecords = clearPending(s.indexedRecords)
	return s
}
func controlDriver(r controlRecord, reg driverRegistryState) (driverRegistryEntry, bool) {
	c := r.message.repair
	if r.category != "other" || c.acronym == "" {
		return resolvedDriver(reg, r.number)
	}
	if !reg.frozen || !reg.synchronized {
		return driverRegistryEntry{}, false
	}
	var found driverRegistryEntry
	for _, d := range reg.drivers[:reg.count] {
		if d.tla == c.acronym {
			if found.number != 0 {
				return driverRegistryEntry{}, false
			}
			found = d
		}
	}
	return found, found.number != 0 && (!r.hasNumber || found.number == r.number) && (c.number == 0 || found.number == c.number)
}
func (s *raceControlState) repairEvent(id SessionInfoIdentity, i, n int64, c repairClues) (string, []keyValue, pcommon.SpanID) {
	var fallback pcommon.SpanID
	if c.acronym == "" || s.repairOverflow[n] || !c.hasDuration && c.reasonLap == 0 {
		return "", nil, fallback
	}
	var key repairKey
	for _, l := range s.summaries[n] {
		k := repairKey{n, l.lap, l.phase}
		if c.reinstate {
			d, ok := s.deleted[k]
			if !ok || c.hasDuration && (!d.clues.hasDuration || d.clues.durationMS != c.durationMS) || c.reasonLap != 0 && d.clues.reasonLap != c.reasonLap {
				continue
			}
		} else if c.phase != "" && c.phase != l.phase || c.hasDuration && (!l.hasDuration || c.durationMS != l.durationMS) || c.reasonLap != 0 && c.reasonLap != l.boundary {
			continue
		}
		if key.driver != 0 {
			return "", nil, pcommon.SpanID{}
		}
		key = k
		if l.exported {
			fallback = DeterministicLapSpanID(id, n, key.phase, key.lap)
		}
	}
	if key.driver == 0 {
		return "", nil, fallback
	}
	a := []keyValue{intAttr("f1.lap.number", key.lap), strAttr("f1.session.phase", phaseOrNone(key.phase))}
	if c.hasDuration {
		a = append(a, floatAttr("f1.lap.reported_duration", float64(c.durationMS)/1000))
	}
	if c.reasonLap != 0 {
		a = append(a, intAttr("f1.race_control.reason_lap", c.reasonLap))
	}
	s.deleted = copyMap(s.deleted)
	if c.reinstate {
		a = append(a, intAttr("f1.race_control.related_index", s.deleted[key].index))
		delete(s.deleted, key)
		return "f1.lap_time.reinstated", a, fallback
	}
	s.deleted[key] = deletedLap{i, c}
	return "f1.lap_time.deleted", a, fallback
}

type controlEventCandidate struct {
	driver   int64
	event    spanEventEffect
	logIndex int
	fallback pcommon.SpanID
}
type raceControlReduction struct {
	state   raceControlState
	effects Effects
	events  []controlEventCandidate
}

func reduceRaceControlMessages(s raceControlState, o sessionObservation, raw json.RawMessage, reg driverRegistryState, start, end time.Time) (raceControlReduction, error) {
	out := raceControlReduction{state: s}
	out.state.blue = slices.Clip(s.blue)
	f, err := topicObject(raw)
	if err != nil {
		return out, err
	}
	var issues recordIssues
	out.state.indexedRecords = applyRecords(s.indexedRecords, o, f["Messages"], false, func(i int64, r controlRecord, f topicFields) (controlRecord, bool) {
		r = patchControl(r, f)
		if f["Lap"] != nil && !r.lap.known || f["Sector"] != nil && !r.sector.known || f["Flag"] != nil && r.flag == "" || f["Scope"] != nil && r.scope == "" || f["Status"] != nil && r.status == "" || f["Mode"] != nil && r.mode == "" {
			issues |= recordInvalidField
		}
		if !r.hasMessage || r.category == "" {
			return r, false
		}
		t, q := publicationFallback(r.utc, o.publication)
		a := r.attrs(i)
		var name string
		var extra []keyValue
		var fallback pcommon.SpanID
		d, resolved := controlDriver(r, reg)
		if !resolved && (r.hasNumber || r.category == "other" && r.message.repair.acronym != "") {
			issues |= recordIdentity
		}
		if resolved {
			a = append(a, driverAttrs(d)...)
			switch {
			case r.category == "other":
				name, extra, fallback = out.state.repairEvent(o.Identity, i, d.number, r.message.repair)
			case r.category == "flag" && r.driverScope && r.blue:
				name = "f1.blue_flag.shown"
				if !o.snapshot() && !start.IsZero() && end.IsZero() && !t.Before(start) {
					out.state.blue = append(out.state.blue, blueCandidate{d, t, i})
				}
			case r.category == "car_event" || r.category == "flag" && r.driverScope && r.flag != "":
				name = "f1.race_control.driver_notice"
			}
		}
		if !o.snapshot() {
			if !r.utc.valid {
				issues |= recordTimeFallback
			}
			out.effects.logs = append(out.effects.logs, recordLog(o, r.message.body, t, q, a))
			if name != "" {
				attrs := append(slices.Clone(a), strAttr("f1.race_control.message", r.message.body))
				out.events = append(out.events, controlEventCandidate{d.number, spanEventEffect{name, t, q, append(attrs, extra...)}, len(out.effects.logs) - 1, fallback})
			}
		}
		return r, true
	})
	out.state.issues |= issues
	return out, nil
}
func foldBlueFlags(s raceControlState, id SessionInfoIdentity, start, end time.Time) (raceControlState, Effects) {
	s.blueAfterEnd = false
	rows := slices.Clone(s.blue)
	s.blue = nil
	slices.SortFunc(rows, func(a, b blueCandidate) int {
		if v := a.at.Compare(b.at); v != 0 {
			return v
		}
		return cmp.Compare(a.index, b.index)
	})
	if end.IsZero() && len(rows) > 0 {
		end = rows[len(rows)-1].at
	}
	type series struct {
		end time.Time
		n   int64
	}
	index := map[series]int{}
	var e Effects
	for _, r := range rows {
		if r.at.After(end) {
			s.blueAfterEnd = true
			break
		}
		a, b := deltaWindowBounds(start, r.at)
		if end.Before(b) {
			b = end
		}
		if !b.After(a) {
			continue
		}
		k := series{b, r.driver.number}
		j, ok := index[k]
		if !ok {
			j = len(e.metrics)
			index[k] = j
			e.metrics = append(e.metrics, metricEffect{name: "f1.driver.blue_flags", unit: "{flag}", kind: metricIntSumDelta, monotonic: true, dataSource: "livetiming", identity: id, attrs: driverAttrs(r.driver), startTime: a, timestamp: b})
		}
		e.metrics[j].intVal++
	}
	return s, e
}
