package f1engine

import (
	"encoding/json"
	"maps"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

func unsignedDigits(s string) (int64, bool) {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// fixedSeconds parses the fractional component before scaling the whole part.
func fixedSeconds(s string, precision int) (int64, bool) {
	whole, fraction, ok := strings.Cut(s, ".")
	if !ok || len(fraction) != precision {
		return 0, false
	}
	w, wok := unsignedDigits(whole)
	f, fok := unsignedDigits(fraction)
	if !wok || !fok {
		return 0, false
	}
	for i := precision; i < 9; i++ {
		f *= 10
	}
	if w > (math.MaxInt64-f)/1e9 {
		return 0, false
	}
	return w*1e9 + f, true
}

func timingDuration(s string) (int64, bool) {
	m, sec, colon := strings.Cut(s, ":")
	if !colon {
		v, ok := fixedSeconds(m, 3)
		return v, ok && v > 0
	}
	minutes, mok := unsignedDigits(m)
	v, ok := fixedSeconds(sec, 3)
	if !mok || !ok || v >= 60e9 || minutes > (math.MaxInt64-v)/60e9 {
		return 0, false
	}
	v += minutes * 60e9
	return v, v > 0
}

type gapKind uint8

const (
	Unknown gapKind = iota
	ElapsedMilliseconds
	LiveLapDeficit
	LeaderLap
	TerminalLapToken
	Cleared
)

func gapToken(s string) (gapKind, int64) {
	switch {
	case s == "":
		return Cleared, 0
	case strings.HasPrefix(s, "+"):
		if n, ok := fixedSeconds(s[1:], 3); ok {
			return ElapsedMilliseconds, n
		}
	case strings.HasPrefix(s, "LAP "):
		if n, ok := canonicalIndex(s[4:]); ok {
			return LeaderLap, n
		}
	case strings.HasSuffix(s, " L"):
		if n, ok := parseCanonicalDriverNumber(s[:len(s)-2]); ok {
			return LiveLapDeficit, n
		}
	case strings.HasSuffix(s, "L"):
		if n, ok := parseCanonicalDriverNumber(s[:len(s)-1]); ok {
			return TerminalLapToken, n
		}
	}
	return Unknown, 0
}

func normalizedCompound(s string) string {
	b := []byte(strings.Trim(s, " \t\r\n\v\f"))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	switch s = string(b); s {
	case "soft", "medium", "hard", "intermediate", "wet":
		return s
	case "", "unknown":
		return "unknown"
	case "test":
		return "test"
	}
	if strings.HasPrefix(s, "test_") || strings.HasPrefix(s, "test-") {
		return "test"
	}
	return "other"
}

func measurementTime(o sessionObservation) time.Time {
	if o.snapshot() {
		return o.observed
	}
	return o.publication
}

func driverGauge(o sessionObservation, d driverRegistryEntry, name, unit string, n int64, attrs ...keyValue) metricEffect {
	m := sessionGauge(o.Identity, "f1.driver."+name, unit, measurementTime(o), n)
	m.attrs = append(driverAttrs(d), attrs...)
	return m
}

func durationGauge(o sessionObservation, d driverRegistryEntry, name string, n int64, attrs ...keyValue) metricEffect {
	m := driverGauge(o, d, name, "s", 0, attrs...)
	m.kind, m.floatVal = metricDoubleGauge, float64(n)/1e9
	return m
}

type timingLines[T any] struct {
	lines          map[int64]T
	unsynchronized bool
}

// Build a candidate replacement before exposing any snapshot effects.
func reduceTimingLines[T any](s timingLines[T], o sessionObservation, raw json.RawMessage, reg driverRegistryState, apply func(T, topicFields, driverRegistryEntry) (T, []metricEffect, bool)) (timingLines[T], Effects, error) {
	var e Effects
	f, err := topicObject(raw)
	if err != nil {
		return s, e, err
	}
	invalid := func() (timingLines[T], Effects, error) {
		if o.snapshot() {
			s.unsynchronized = true
		}
		return s, Effects{}, nil
	}
	lines := nestedObject(f["Lines"])
	if lines == nil {
		return invalid()
	}
	if s.unsynchronized && !o.snapshot() {
		return s, e, nil
	}
	next := timingLines[T]{lines: copyMap(s.lines)}
	if o.snapshot() {
		clear(next.lines)
	}
	for _, key := range deletedNames(lines) {
		if n, ok := parseCanonicalDriverNumber(key); ok {
			delete(next.lines, n)
		}
	}
	for _, d := range reg.drivers[:reg.count] {
		if _, ok := resolvedDriver(reg, d.number); !ok {
			continue
		}
		r, ok := lines[decimal(d.number)]
		if !ok {
			continue
		}
		line := nestedObject(r)
		if line == nil || line["RacingNumber"] != nil && sessionText(line["RacingNumber"]) != decimal(d.number) {
			if o.snapshot() {
				return invalid()
			}
			delete(next.lines, d.number)
			continue
		}
		v, metrics, ok := apply(next.lines[d.number], line, d)
		if !ok && o.snapshot() {
			return invalid()
		}
		next.lines[d.number] = v
		e.metrics = append(e.metrics, metrics...)
	}
	return next, e, nil
}

type sourceRun struct {
	compound, newTyre, unchanged string
	total, start, lap            int64
	hasTotal, hasStart, hasLap   bool
	invalid                      uint8
}

func patchRun(r sourceRun, f topicFields) sourceRun {
	deleted := deletedNames(f)
	for i, k := range [...]string{"Compound", "TotalLaps", "StartLaps", "LapNumber", "New", "TyresNotChanged"} {
		if slices.Contains(deleted, k) {
			r.invalid &^= 1 << i
			switch i {
			case 0:
				r.compound = ""
			case 1:
				r.total, r.hasTotal = 0, false
			case 2:
				r.start, r.hasStart = 0, false
			case 3:
				r.lap, r.hasLap = 0, false
			case 4:
				r.newTyre = ""
			case 5:
				r.unchanged = ""
			}
		}
		raw, exists := f[k]
		if !exists {
			continue
		}
		ok := true
		switch i {
		case 0:
			text, err := DecodeLosslessJSONString(raw)
			r.compound = normalizedCompound(text)
			ok = err == nil
		case 1, 2, 3:
			n, valid := canonicalIndex(string(raw))
			ok = valid
			switch i {
			case 1:
				r.total, r.hasTotal = n, ok
			case 2:
				r.start, r.hasStart = n, ok
			case 3:
				r.lap, r.hasLap = n, ok
			}
		case 4:
			r.newTyre = sessionText(raw)
			ok = r.newTyre == "true" || r.newTyre == "false"
		case 5:
			r.unchanged = sessionText(raw)
			ok = r.unchanged == "0" || r.unchanged == "1"
		}
		r.invalid &^= 1 << i
		if !ok {
			r.invalid |= 1 << i
		}
	}
	return r
}

func omitTimingAppData() timingAppState { return timingAppState{} }
func omitTimingStats() timingStatsState { return timingStatsState{} }

type runSequence struct {
	runs    map[int64]sourceRun
	last    int64
	invalid bool
}
type timingAppState = timingLines[runSequence]

func (s runSequence) active() (sourceRun, bool) {
	r, ok := s.runs[s.last]
	return r, ok && !s.invalid && int64(len(s.runs))-1 == s.last && r.invalid == 0
}

func stintCompound(s timingAppState, driver, ordinal, first, last int64) string {
	seq := s.lines[driver]
	r, ok := seq.runs[ordinal-1]
	if s.unsynchronized || seq.invalid || !ok || r.invalid != 0 || r.hasLap && (r.lap < first || r.lap > last) {
		return "unknown"
	}
	if ordinal > int64(len(seq.runs)) {
		return "unknown"
	}
	for key := int64(0); key < ordinal-1; key++ {
		if _, exists := seq.runs[key]; !exists {
			return "unknown"
		}
	}
	return normalizedCompound(r.compound)
}

func reduceTimingAppData(s timingAppState, o sessionObservation, raw json.RawMessage, reg driverRegistryState) (timingAppState, Effects, error) {
	return reduceTimingLines(s, o, raw, reg, func(seq runSequence, f topicFields, d driverRegistryEntry) (runSequence, []metricEffect, bool) {
		if slices.Contains(deletedNames(f), "Stints") {
			seq = runSequence{}
		}
		if f["Stints"] == nil {
			return seq, nil, true
		}
		patches, ok := recordPatches(f["Stints"], o.snapshot(), true, true)
		if !ok {
			if _, array := topicArray(f["Stints"]); array {
				return seq, nil, false
			}
			seq.invalid = true
			return seq, nil, false
		}
		prior, had := seq.active()
		oldKey := seq.last
		seq.runs = copyMap(seq.runs)
		seen := make(map[int64]bool, len(patches))
		for _, p := range patches {
			if !p.remove && seen[p.index] {
				seq.invalid = true
				return seq, nil, false
			}
			seen[p.index] = !p.remove
			seq.last = max(seq.last, p.index)
			fields := nestedObject(p.raw)
			if p.remove || fields == nil {
				if o.snapshot() && fields == nil && !p.remove {
					return seq, nil, false
				}
				delete(seq.runs, p.index)
				continue
			}
			seq.runs[p.index] = patchRun(seq.runs[p.index], fields)
			if o.snapshot() && seq.runs[p.index].invalid != 0 {
				return seq, nil, false
			}
		}
		if o.snapshot() && len(seq.runs) > 0 {
			if _, ok := seq.active(); !ok {
				return seq, nil, false
			}
		}
		r, ok := seq.active()
		if ok && r.hasTotal && (!had || !prior.hasTotal || prior.total != r.total || seq.last != oldKey) {
			return seq, []metricEffect{driverGauge(o, d, "tyre.age", "{lap}", r.total)}, true
		}
		return seq, nil, true
	})
}

var trapKeys = [...]string{"I1", "I2", "FL", "ST"}

func indexedFields(raw json.RawMessage) topicFields {
	if a, ok := topicArray(raw); ok {
		f := topicFields{}
		for i, v := range a {
			f[strconv.Itoa(i)] = v
		}
		return f
	}
	return nestedObject(raw)
}

type timingStatsState = timingLines[[7]int64]

func reduceTimingStats(s timingStatsState, o sessionObservation, raw json.RawMessage, reg driverRegistryState) (timingStatsState, Effects, error) {
	return reduceTimingLines(s, o, raw, reg, func(v [7]int64, f topicFields, d driverRegistryEntry) ([7]int64, []metricEffect, bool) {
		var metrics []metricEffect
		sectors, speeds := indexedFields(f["BestSectors"]), nestedObject(f["BestSpeeds"])
		if o.snapshot() && (f["BestSectors"] != nil && sectors == nil || f["BestSpeeds"] != nil && speeds == nil) {
			return v, nil, false
		}
		for i := range v {
			table, key := sectors, strconv.Itoa(i)
			group := "BestSectors"
			if i >= 3 {
				table, key = speeds, trapKeys[i-3]
				group = "BestSpeeds"
			}
			if slices.Contains(deletedNames(f), group) {
				v[i] = 0
			}
			for _, name := range deletedNames(table) {
				if key == name {
					v[i] = 0
				}
			}
			entry := nestedObject(table[key])
			if table[key] != nil && entry == nil && o.snapshot() {
				return v, nil, false
			}
			raw := entry["Value"]
			if slices.Contains(deletedNames(entry), "Value") {
				v[i] = 0
			}
			if raw == nil {
				continue
			}
			text, textErr := DecodeLosslessJSONString(raw)
			var n int64
			var m metricEffect
			if i < 3 {
				n, _ = timingDuration(text)
				m = durationGauge(o, d, "best_sector_time", n, intAttr("f1.sector.number", int64(i+1)))
			} else {
				n, _ = unsignedDigits(text)
				m = driverGauge(o, d, "best_speed_trap", "km/h", n, strAttr("f1.speed_trap.location", strings.ToLower(key)))
			}
			if o.snapshot() && (textErr != nil || text != "" && n <= 0) {
				return v, nil, false
			}
			if n > 0 && v[i] != n {
				metrics = append(metrics, m)
			}
			v[i] = n
		}
		return v, metrics, true
	})
}

func deletedNames(f topicFields) []string {
	a, _ := topicArray(f["_deleted"])
	var names []string
	for _, v := range a {
		names = append(names, sessionText(v))
	}
	return names
}

// reduceTimingBatch consumes the timing-owned topics of one normalized batch.
// Snapshot source runs are needed to resolve current stint ordinals regardless
// of the order of members in the subscription response.
func reduceTimingBatch(s timingState, o sessionObservation, b Batch, reg driverRegistryState) (timingState, Effects, error) {
	initial := s
	o.source, o.observed = b.Source, b.ObservationTime
	updates := b.Updates
	if o.snapshot() {
		for _, topic := range b.RequestedTopics {
			if slices.Contains(b.PresentTopics, topic) {
				continue
			}
			switch topic {
			case "TimingData":
				s = omitTimingData(s)
			case "TimingStats":
				s.stats = omitTimingStats()
			case "TimingAppData":
				s.app = omitTimingAppData()
			case "PitStop":
				s = omitPitTopic(s, stopReport)
			case "PitStopSeries":
				s = omitPitTopic(s, seriesReport)
			case "PitLaneTimeCollection":
				s = omitPitTopic(s, laneReport)
			}
		}
		updates = slices.Clone(updates)
		for i, u := range updates {
			if u.Topic == "TimingAppData" {
				copy(updates[1:i+1], updates[:i])
				updates[0] = u
				break
			}
		}
	}
	var all Effects
	for _, u := range updates {
		o.publication = u.Timestamp
		var e Effects
		var err error
		switch u.Topic {
		case "TimingData":
			s, e, err = reduceTimingData(s, o, u.Payload, reg)
		case "TimingAppData":
			s.app, e, err = reduceTimingAppData(s.app, o, u.Payload, reg)
		case "TimingStats":
			s.stats, e, err = reduceTimingStats(s.stats, o, u.Payload, reg)
		case "PitStop":
			s, e, err = reducePitStop(s, o, u.Payload, reg)
		case "PitStopSeries":
			s, e, err = reducePitStopSeries(s, o, u.Payload, reg)
		case "PitLaneTimeCollection":
			s, e, err = reducePitLaneTimeCollection(s, o, u.Payload, reg)
		default:
			continue
		}
		if err != nil {
			return initial, Effects{}, err
		}
		all = all.append(e)
	}
	return s, all, nil
}

type lapTiming struct {
	span                                   spanEffect
	sector                                 [3]int64
	invalidSector                          [3]bool
	at                                     [3]time.Time
	trap                                   [4]int64
	reported                               int64
	valuePresent, empty, personal, overall bool
	first                                  int64
	openMono, endMono                      time.Duration
}
type timingVisit struct {
	number, in, out, lane, stopLane, stop int64
	entry                                 time.Time
}
type elapsedSample struct {
	at      time.Time
	mono    time.Duration
	ns, ref int64
}
type bestCandidate struct{ duration, lap int64 }
type durationWindow struct {
	metricEffect
	total   *big.Rat
	samples []durationObservation
}
type durationObservation struct {
	at    time.Time
	value *big.Rat
	x     exemplarEffect
}
type timingDriver struct {
	driverRegistryEntry
	root, phaseSpan, stint          spanEffect
	lap, pending                    lapTiming
	current, ordinal, position      int64
	stintFirst                      int64
	lastLap                         int64
	lastSectors                     [3]int64
	stopped, inPit                  int8 // 0 unavailable, 1 false, 2 true
	stoppedAt, pitAt, countAt       time.Time
	lastFact                        time.Time
	visit                           timingVisit
	visits                          []timingVisit
	newStint, countKnown, countSent bool
	count, countSentValue           int64
	countRegressed, countDeferred   bool
	lapHigh                         map[string]int64
	best                            map[string]bestCandidate
	elapsed                         [2][]elapsedSample
	leaderLap                       int64
}
type timingState struct {
	invalidSnapshot                                                          bool
	drivers                                                                  map[int64]timingDriver
	unsynchronized, late, active, tracesEnded, runningKnown, runningDeferred bool
	sessionEnded, rootsOpened, lateObservation                               bool
	sessionEndUTC, lateFrom                                                  time.Time
	phase                                                                    string
	start, watermark, finalised, runningAt                                   time.Time
	running, currentSessionLap                                               int64
	nextTick                                                                 time.Duration
	graceDeadline                                                            time.Duration
	phaseBlocked, competitiveStop                                            bool
	app                                                                      timingAppState
	stats                                                                    timingStatsState
	position                                                                 positionState
	cars                                                                     carDataState
	control                                                                  raceControlState
	windows                                                                  []durationWindow
	streamTimes, acceptedTimes                                               map[string]time.Time
	deltaStart                                                               time.Time
	seen                                                                     map[timingEventKey]bool
	pits                                                                     pitReports
}
type timingEventKey struct {
	driver      int64
	phase, name string
	at          time.Time
	a, b, c, d  int64
}

func (s *timingState) once(k timingEventKey) bool {
	if s.seen[k] {
		return false
	}
	s.seen = copyMap(s.seen)
	s.seen[k] = true
	return true
}
func spanAt(k spanKindType, o sessionObservation, d timingDriver, t time.Time) spanEffect {
	return spanEffect{kind: k, identity: o.Identity, driverNumber: d.number, driverAcronym: d.tla, displayName: string(k), startTime: t, timeQuality: "publication_time"}
}
func containsSpan(s spanEffect, t time.Time) bool {
	return s.kind != "" && !t.Before(s.startTime) && (s.endTime.IsZero() || !t.After(s.endTime))
}
func addEvent(s *spanEffect, e spanEventEffect) { s.events = append(slices.Clip(s.events), e) }
func eventAt(name string, t time.Time, a ...keyValue) spanEventEffect {
	return spanEventEffect{name, t, "publication_time", a}
}
func enrichEvent(e spanEventEffect, p positionState, n int64) spanEventEffect {
	if xyz, ok := lookupDriverPosition(p, n, e.timestamp); ok {
		e.attrs = append(slices.Clip(e.attrs), xyz[:]...)
	}
	return e
}
func (d *timingDriver) attach(e spanEventEffect, p positionState) (pcommon.SpanID, bool) {
	attach := func(owner *spanEffect) (pcommon.SpanID, bool) {
		addEvent(owner, enrichEvent(e, p, d.number))
		if e.timestamp.After(d.lastFact) {
			d.lastFact = e.timestamp
		}
		id, n := d.root.identity, d.number
		switch owner.kind {
		case spanLap:
			return DeterministicLapSpanID(id, n, owner.phase, owner.lapNumber), true
		case spanStint:
			return DeterministicStintSpanID(id, n, owner.phase, owner.stintOrdinal), true
		case spanQualifyingPhase:
			return deterministicPhaseSpanID(id, n, owner.phase), true
		default:
			return DeterministicRootSpanID(id, n), true
		}
	}
	if e.name == "f1.lap_time.deleted" || e.name == "f1.lap_time.reinstated" {
		var n int64
		var phase string
		for _, a := range e.attrs {
			switch a.key {
			case "f1.lap.number":
				n = a.value.intVal
			case "f1.session.phase":
				phase = a.value.strVal
			}
		}
		for _, l := range []*spanEffect{&d.pending.span, &d.lap.span} {
			if l.kind != "" && l.lapNumber == n && phaseOrNone(l.phase) == phase {
				return attach(l)
			}
		}
	}
	owners := []*spanEffect{&d.lap.span}
	switch e.name {
	case "f1.pit.entry", "f1.pit.stop", "f1.pit.exit", "f1.pit.visit.incomplete":
		owners = append(owners, &d.pending.span, &d.stint, &d.phaseSpan)
	case "f1.position.changed", "f1.blue_flag.shown", "f1.race_control.driver_notice":
		owners = append(owners, &d.pending.span)
	}
	for _, owner := range append(owners, &d.root) {
		if containsSpan(*owner, e.timestamp) {
			return attach(owner)
		}
	}
	return pcommon.SpanID{}, false
}
func attachRaceControlEvents(s timingState, events []controlEventCandidate, logs []logEffect, roots map[int64]rootInterval) timingState {
	s.drivers = copyMap(s.drivers)
	for i := range logs {
		l := &logs[i]
		for _, a := range l.attrs {
			if a.key == "f1.driver.number" {
				n := a.value.intVal
				if r, ok := roots[n]; ok && !l.timestamp.Before(r.start) && !l.timestamp.After(r.end) {
					l.hasTrace, l.traceID, l.spanID = true, DeterministicTraceID(l.identity, n), DeterministicRootSpanID(l.identity, n)
				}
			}
		}
	}
	for _, v := range events {
		id := v.fallback
		if d, ok := s.drivers[v.driver]; ok {
			if owner, attached := d.attach(v.event, s.position); attached {
				id = owner
			}
			s.drivers[v.driver] = d
		}
		if logs != nil && id != (pcommon.SpanID{}) {
			l := &logs[v.logIndex]
			l.hasTrace, l.traceID, l.spanID = true, DeterministicTraceID(l.identity, v.driver), id
		}
	}
	return s
}

func endSpan(span *spanEffect, t time.Time, e *Effects) {
	if span.kind == "" {
		return
	}
	span.endTime = t
	if t.Before(span.startTime) {
		span.endTime = span.startTime
	}
	e.spans = append(e.spans, *span)
	*span = spanEffect{}
}

func (d *timingDriver) openLap(o sessionObservation, phase string, n int64, t time.Time) {
	if d.root.kind == "" || t.Before(d.root.startTime) || d.ordinal <= 0 {
		return
	}
	if phase != "" && d.phaseSpan.kind == "" {
		d.phaseSpan = spanAt(spanQualifyingPhase, o, *d, t)
		d.phaseSpan.phase = phase
	}
	if d.stint.kind == "" {
		d.stint = spanAt(spanStint, o, *d, t)
		d.stint.phase, d.stint.stintOrdinal = phase, d.ordinal
		d.stintFirst = n
	}
	span := spanAt(spanLap, o, *d, t)
	span.phase, span.stintOrdinal, span.lapNumber = phase, d.ordinal, n
	d.lap = lapTiming{span: span, first: d.stintFirst}
	d.lap.openMono = o.monotonic
}

func timedBounds(name string) []float64 {
	var b, tail []float64
	var lo, hi, step float64
	switch name {
	case "f1.field.lap.drs_active_time_ratio":
		return []float64{0, .01, .02, .05, .10, .15, .20, .30, .50, 1}
	case "f1.driver.lap_time":
		b = []float64{45, 50, 55, 60}
		lo, hi, step = 61, 120, 1
		tail = []float64{125, 130, 135, 140, 150, 165, 180, 210, 240, 300}
	case "f1.driver.sector_time":
		b = []float64{10, 12.5, 15}
		lo, hi, step = 15.5, 50, .5
		tail = []float64{55, 60, 75, 90, 120}
	case "f1.pit.lane_duration":
		b = []float64{10, 12, 14, 16, 18}
		lo, hi, step = 18.5, 40, .5
		tail = []float64{45, 50, 60, 90, 120}
	case "f1.pit.stop_duration":
		b = []float64{.5, 1}
		lo, hi, step = 1.25, 4, .25
		tail = []float64{4.5, 5, 6, 7, 8, 10, 12, 15, 20, 30, 60, 120}
	default:
		return nil
	}
	for v := lo; v <= hi; v += step {
		b = append(b, v)
	}
	return append(b, tail...)
}
func (s *timingState) observe(name string, id SessionInfoIdentity, t time.Time, ns int64, attrs []keyValue, owner observationOwner) {
	s.observeValue(name, "s", id, t, big.NewRat(ns, 1e9), attrs, owner)
}
func (s *timingState) observeValue(name, unit string, id SessionInfoIdentity, t time.Time, value *big.Rat, attrs []keyValue, owner observationOwner) {
	start, end := deltaWindowBounds(s.deltaStart, t)
	if s.streamTimes[name].After(end) {
		s.lateObservation = true
		return
	}
	if s.deltaStart.IsZero() || !end.After(start) || t.Before(start) {
		return
	}
	if t.After(s.acceptedTimes[name]) {
		s.acceptedTimes = copyMap(s.acceptedTimes)
		s.acceptedTimes[name] = t
	}
	s.windows = slices.Clone(s.windows)
	i := slices.IndexFunc(s.windows, func(m durationWindow) bool {
		return m.timestamp.Equal(end) && m.name == name && slices.Equal(m.attrs, attrs)
	})
	if i < 0 {
		b := timedBounds(name)
		i = len(s.windows)
		s.windows = append(s.windows, durationWindow{metricEffect: metricEffect{identity: id, name: name, unit: unit, dataSource: "livetiming", kind: metricExplicitHistogramDelta, startTime: start, timestamp: end, attrs: attrs, bounds: b, bucketCounts: make([]uint64, len(b)+1), hasMinMax: true}, total: new(big.Rat)})
	}
	m := &s.windows[i]
	m.bucketCounts = slices.Clone(m.bucketCounts)
	x := owner.exemplar(id, t)
	x.exact = value
	x.floatVal, _ = value.Float64()
	m.samples = append(slices.Clip(m.samples), durationObservation{t, value, x})
	m.add(value, x)
}
func (m *durationWindow) add(value *big.Rat, x exemplarEffect) {
	m.selectExemplar(x)
	v, _ := value.Float64()
	if m.count == 0 {
		m.min, m.max = v, v
	}
	bucket, _ := slices.BinarySearch(m.bounds, v)
	m.bucketCounts[bucket]++
	m.count++
	m.total = new(big.Rat).Add(m.total, value)
	m.min = min(m.min, v)
	m.max = max(m.max, v)
}
func (s *timingState) drainWindows(at time.Time, terminal bool, e *Effects) {
	s.streamTimes = s.acceptedTimes
	var keep []durationWindow
	for _, m := range s.windows {
		blocked := s.invalidSnapshot
		switch m.name {
		case "f1.pit.lane_duration":
			blocked = s.pits.invalidSnapshot[laneReport] || !terminal && s.pits.unsynchronized[laneReport]
		case "f1.pit.stop_duration":
			blocked = s.pits.invalidSnapshot[stopReport] || !terminal && s.pits.unsynchronized[stopReport]
		}
		if blocked {
			if !terminal {
				keep = append(keep, m)
			}
			continue
		}
		mark := s.streamTimes[m.name]
		if !terminal && !mark.After(m.timestamp) {
			keep = append(keep, m)
			continue
		}
		if terminal {
			if !at.IsZero() {
				mark = at
			}
			if m.timestamp.After(mark) {
				m.timestamp = mark
				if !at.IsZero() && s.streamTimes[m.name].After(mark) {
					m.count, m.total, m.bucketCounts = 0, new(big.Rat), make([]uint64, len(m.bounds)+1)
					m.exemplars = nil
					for _, sample := range m.samples {
						if !sample.at.After(mark) {
							m.add(sample.value, sample.x)
						}
					}
				}
			}
		}
		if m.count != 0 && m.timestamp.After(m.startTime) {
			m.sum, _ = m.total.Float64()
			e.metrics = append(e.metrics, m.metricEffect)
		}
	}
	s.windows = keep
}

func phaseAttrs(p string) []keyValue {
	if p != "" {
		return []keyValue{strAttr("f1.session.phase", p)}
	}
	return nil
}
func (s *timingState) finishLap(d *timingDriver, o sessionObservation, e *Effects) {
	l := d.pending
	if l.span.lapNumber == 0 {
		return
	}
	d.pending = lapTiming{}
	o.source = Feed
	sectors := l.sector
	missing, slot := 0, 0
	var sum int64
	overflow := false
	for i, n := range sectors {
		if n == 0 {
			missing++
			slot = i
		}
		if n > math.MaxInt64-sum {
			overflow = true
		} else {
			sum += n
		}
	}
	repaired := -1
	if missing == 1 && !l.invalidSector[slot] && !overflow && l.reported > sum && l.reported-sum == d.lastSectors[slot] {
		repaired = slot
		sectors[slot] = l.reported - sum
		sum = l.reported
		missing = 0
	}
	ns, quality := l.reported, "reported"
	if missing == 0 {
		switch {
		case ns > 0 && (overflow || ns != sum):
			quality, ns = "conflict", 0
		case !overflow && ns == 0 && (l.empty || !l.valuePresent && sum == d.lastLap):
			quality, ns = "reconstructed", sum
		}
	}
	span := l.span
	span.attrs = slices.Clip(span.attrs)
	boundary := span.endTime
	end := boundary
	estimated := missing == 0 && !overflow
	if estimated {
		var tail int64
		for i := 2; i >= 0; i-- {
			if !l.at[i].IsZero() {
				t := l.at[i].Add(time.Duration(tail))
				if t.Before(end) && !t.Add(-time.Duration(sum)).Before(span.startTime) {
					end = t
				}
			}
			tail += sectors[i]
		}
		estimated = !end.Add(-time.Duration(sum)).Before(span.startTime)
	}
	owner := observationOwner{driver: d.number, acronym: d.tla, phase: span.phase, lap: span.lapNumber, lapResolved: span.kind != ""}
	phase := phaseAttrs(span.phase)
	t := span.startTime
	if estimated {
		t = end.Add(-time.Duration(sum))
		span.startTime, span.timeQuality = t, "estimated"
	}
	for i, n := range sectors {
		if n <= 0 {
			continue
		}
		at := l.at[i]
		if estimated {
			at = t.Add(time.Duration(n))
		} else if at.IsZero() || at.After(end) {
			at = end
		}
		if at.Before(t) {
			at = t
		}
		o.publication = at
		a := append(slices.Clone(phase), intAttr("f1.sector.number", int64(i+1)))
		m := durationGauge(o, d.driverRegistryEntry, "last_sector_time", n, a...)
		e.metrics = append(e.metrics, m)
		sectorOwner := owner
		sectorOwner.sector = int64(i + 1)
		s.observe("f1.driver.sector_time", o.Identity, at, n, m.attrs, sectorOwner)
		if span.kind != "" {
			child := span
			child.kind, child.displayName, child.sectorNumber = spanSector, "sector", int64(i+1)
			child.startTime, child.endTime = t, at
			child.events = nil
			q := "reported"
			if i == repaired {
				q = "reconstructed"
			}
			child.attrs = []keyValue{strAttr("f1.timing.value_quality", q)}
			e.spans = append(e.spans, child)
		}
		t = at
		d.lastSectors[i] = n
	}
	o.publication = end
	if ns > 0 {
		d.lastLap = ns
		m := durationGauge(o, d.driverRegistryEntry, "last_lap_time", ns, phase...)
		e.metrics = append(e.metrics, m)
		s.observe("f1.driver.lap_time", o.Identity, end, ns, append(slices.Clip(m.attrs), strAttr("f1.tyre.compound", stintCompound(s.app, d.number, span.stintOrdinal, l.first, span.lapNumber))), owner)
	}
	if span.kind == "" {
		return
	}
	if ns > 0 {
		if ratio, ok := lapDRSRatio(s.cars, d.number, span.startTime, end); ok {
			s.observeValue("f1.field.lap.drs_active_time_ratio", "1", o.Identity, end, new(big.Rat).SetFloat64(ratio), nil, owner)
		}
	}
	if ns > 0 || quality == "conflict" {
		span.attrs = append(span.attrs, strAttr("f1.timing.value_quality", quality))
	}
	if quality == "conflict" {
		span.attrs = append(span.attrs, floatAttr("f1.lap.reported_duration", float64(l.reported)/1e9))
	}
	for i, v := range l.trap {
		if v > 0 {
			span.attrs = append(span.attrs, intAttr("f1.lap.speed_trap."+strings.ToLower(trapKeys[i])+"_kph", v))
		}
	}
	for i, flag := range []bool{l.personal, l.overall} {
		if flag && ns > 0 {
			name := []string{"f1.personal_best", "f1.fastest_lap"}[i]
			ev := eventAt(name, end)
			ev.timeQuality = span.timeQuality
			addEvent(&span, enrichEvent(ev, s.position, d.number))
		}
	}
	span.endTime = end
	addElapsedBoundaries(&span, *d, l.openMono+span.startTime.Sub(l.span.startTime), l.endMono+span.endTime.Sub(boundary))
	e.spans = append(e.spans, span)
	s.control = retainLapRepairSummary(s.control, lapRepairSummary{d.number, span.lapNumber, span.lapNumber + 1, ns / 1e6, span.phase, ns > 0, true})
}

func boolState(raw json.RawMessage) int8 {
	switch string(raw) {
	case "false":
		return 1
	case "true":
		return 2
	}
	return 0
}

func (s *timingState) incomplete(d *timingDriver, reason string) {
	if reason == "session_replacement" || reason == "finalised_grace" {
		reason = "session_end"
	}
	if v := d.visit; v.number > 0 {
		d.attach(eventAt("f1.pit.visit.incomplete", v.entry, intAttr("f1.pit.visit.number", v.number), strAttr("f1.pit.visit.incomplete_reason", reason)), s.position)
	}
	d.visit = timingVisit{}
	d.inPit = 0
	d.newStint = false
}
func disconnectTiming(s timingState) (timingState, Effects) {
	s.position = disconnectPosition(s.position)
	s.drivers = copyMap(s.drivers)
	s.unsynchronized = true
	s.app.unsynchronized, s.stats.unsynchronized = true, true
	s.pits.unsynchronized = [3]bool{true, true, true}
	s.runningKnown = false
	for n, d := range s.drivers {
		s.incomplete(&d, "disconnect")
		s.drivers[n] = d
	}
	return s, Effects{}
}

// An omitted TimingData topic has no current values. Keep session-owned visit
// high-water marks, event identities and observed trace boundaries.
func omitTimingData(s timingState) timingState {
	s.drivers = copyMap(s.drivers)
	s.invalidSnapshot = false
	s.unsynchronized, s.runningKnown = false, false
	for n, d := range s.drivers {
		d.current, d.position, d.leaderLap, d.stopped, d.inPit = 0, 0, 0, 0, 0
		d.countKnown, d.newStint = false, false
		d.best, d.elapsed, d.visit = nil, [2][]elapsedSample{}, timingVisit{}
		d.lastLap, d.lastSectors = 0, [3]int64{}
		d.lap, d.pending = lapTiming{}, lapTiming{}
		s.drivers[n] = d
	}
	return s
}

// Snapshot status is a baseline, never a transition. Terminal series state
// takes precedence over a missing or stale direct mirror.
func applyTimingStatusSnapshot(s timingState, o sessionObservation, series sessionDataState, mirror sessionStatusMirror, reg driverRegistryState) (timingState, Effects) {
	h := series.history[sessionField]
	o.source = Snapshot
	if h.current != "Finished" && h.current != "Finalised" && h.current != "Ends" && (h.broken || mirror.Status == "" || h.current != mirror.Status) {
		s.runningKnown = false
		s.active = false
		return s, Effects{}
	}
	if (h.current == "Started" || h.current == "Aborted") && !s.tracesEnded && !s.competitiveStop {
		s.active = true
		if s.deltaStart.IsZero() {
			s.deltaStart = series.indexedStartedUTC
		}
	}
	return applySessionStatusTransition(s, o, sessionStatusTransition{current: h.current, utc: h.watermark}, reg)
}
func closeTimingTraces(s timingState, o sessionObservation, at time.Time, reason string) (timingState, Effects) {
	s.drivers = copyMap(s.drivers)
	var e Effects
	for _, n := range slices.Sorted(maps.Keys(s.drivers)) {
		d := s.drivers[n]
		end := at
		fallback := reason == "shutdown" || reason == "session_replacement"
		if fallback {
			end = time.Time{}
		}
		own := func(t time.Time) {
			if t.After(end) {
				end = t
			}
		}
		own(d.lastFact)
		for _, span := range []spanEffect{d.root, d.phaseSpan, d.stint, d.lap.span, d.pending.span} {
			own(span.startTime)
			own(span.endTime)
			for _, event := range span.events {
				own(event.timestamp)
			}
		}
		if s.invalidSnapshot {
			d.visit = timingVisit{}
			d.lap, d.pending = lapTiming{}, lapTiming{}
			d.phaseSpan, d.stint = spanEffect{}, spanEffect{}
		}
		s.incomplete(&d, reason)
		d.root.timeQuality = "estimated"
		if o.snapshot() {
			endSpan(&d.pending.span, end, &e)
			d.pending = lapTiming{}
		} else {
			s.finishLap(&d, o, &e)
		}
		endSpan(&d.lap.span, end, &e)
		d.lap = lapTiming{}
		endSpan(&d.stint, end, &e)
		endSpan(&d.phaseSpan, end, &e)
		endSpan(&d.root, end, &e)
		s.drivers[n] = d
	}
	s.tracesEnded, s.active = true, false
	return s, e
}
func flushTimingTerminal(s timingState, o sessionObservation, at time.Time, reason string) (timingState, Effects) {
	s, e := closeTimingTraces(s, o, at, reason)
	if reason == "shutdown" || reason == "session_replacement" {
		at = s.sessionEndUTC
	}
	if !o.snapshot() {
		s.drainWindows(at, true, &e)
	}
	return s, e
}
func (s *timingState) coverLate(reg driverRegistryState, at time.Time) {
	if s.active && !s.late && !s.rootsOpened && reg.frozen && reg.synchronized {
		s.late, s.lateFrom = true, at
	}
}
func applySessionStatusTransition(s timingState, o sessionObservation, tr sessionStatusTransition, reg driverRegistryState) (timingState, Effects) {
	if s.sessionEnded || s.tracesEnded && tr.current != "Ends" {
		return s, Effects{}
	}
	s.drivers = copyMap(s.drivers)
	at := tr.utc
	if !s.competitiveStop && (tr.current == "Started" || o.snapshot() && tr.current == "Aborted") {
		s.active = true
		if s.deltaStart.IsZero() {
			s.deltaStart = at
			if o.snapshot() {
				s.deltaStart = o.observed
			}
		}
		firstFeed := !o.snapshot() && s.start.IsZero()
		if firstFeed {
			s.start = at
		}
		for _, r := range reg.drivers[:reg.count] {
			if _, ok := resolvedDriver(reg, r.number); !ok {
				continue
			}
			d := s.drivers[r.number]
			d.driverRegistryEntry = r
			if firstFeed && !s.late && reg.frozen && reg.synchronized {
				s.rootsOpened = true
				d.root = spanAt(spanDriverSession, o, d, at)
				d.root.timeQuality = "observed"
				d.ordinal = 1
			}
			s.drivers[r.number] = d
		}
		s.coverLate(reg, o.observed)
	}
	if !o.snapshot() {
		for n, d := range s.drivers {
			if containsSpan(d.root, at) {
				a := []keyValue{strAttr("f1.session.status", tr.current)}
				if tr.previous != "" {
					a = append(a, strAttr("f1.session.previous_status", tr.previous))
				}
				addEvent(&d.root, spanEventEffect{"f1.session.status.changed", at, "observed", a})
				s.drivers[n] = d
			}
		}
	}
	switch tr.current {
	case "Ends":
		s.sessionEnded, s.sessionEndUTC = true, at
		return flushTimingTerminal(s, o, at, "session_end")
	case "Finalised":
		s.active, s.competitiveStop = false, true
		if !raceLike(o.Identity) {
			return closeTimingTraces(s, o, at, "session_end")
		}
		if s.finalised.IsZero() {
			s.finalised = at
			s.graceDeadline = o.monotonic + 5*time.Minute
		}
	case "Finished":
		s.active, s.competitiveStop = false, true
	}
	var e Effects
	if tr.current == "Started" || o.snapshot() && tr.current == "Aborted" {
		if o.snapshot() {
			at = o.observed
		} else {
			for _, d := range s.drivers {
				if d.stoppedAt.After(at) {
					at = d.stoppedAt
				}
			}
		}
		s.carsRunning(reg, o.Identity, at, &e)
	}
	return s, e
}
func applyTrackStatusTransition(s timingState, tr trackStatusTransition) timingState {
	if s.invalidSnapshot {
		return s
	}
	s.drivers = copyMap(s.drivers)
	a := []keyValue{intAttr("f1.track_status.code", tr.current.code), strAttr("f1.track_status.state", tr.current.state)}
	if tr.previous.code != 0 {
		a = append(a, intAttr("f1.track_status.previous_code", tr.previous.code), strAttr("f1.track_status.previous_state", tr.previous.state))
	}
	for n, d := range s.drivers {
		if containsSpan(d.lap.span, tr.utc) {
			addEvent(&d.lap.span, spanEventEffect{"f1.track_status.changed", tr.utc, "observed", a})
			s.drivers[n] = d
		} else if containsSpan(d.pending.span, tr.utc) {
			addEvent(&d.pending.span, spanEventEffect{"f1.track_status.changed", tr.utc, "observed", a})
			s.drivers[n] = d
		}
	}
	return s
}

func (s *timingState) carsRunning(reg driverRegistryState, id SessionInfoIdentity, at time.Time, e *Effects) {
	if !raceLike(id) || !s.active || s.unsynchronized || !reg.frozen || !reg.synchronized || reg.count == 0 {
		s.runningKnown = false
		return
	}
	var count int64
	for _, r := range reg.drivers[:reg.count] {
		switch s.drivers[r.number].stopped {
		case 1:
			count++
		case 2:
		default:
			s.runningKnown = false
			return
		}
	}
	if !s.runningKnown || count != s.running || s.runningDeferred {
		s.runningDeferred = at.Before(s.start) || !at.After(s.runningAt)
		if !s.runningDeferred {
			e.metrics = append(e.metrics, sessionGauge(id, "f1.session.cars_running", "{car}", at, count))
			s.runningAt = at
		}
	}
	s.runningKnown, s.running = true, count
}
func (s timingState) referent(pos int64) int64 {
	var ref int64
	if pos < 1 {
		return 0
	}
	for n, d := range s.drivers {
		if d.position == pos {
			if ref != 0 {
				return 0
			}
			ref = n
		}
	}
	return ref
}

var elapsedNames = [...]string{"gap_to_leader", "interval_to_ahead"}

func freshSample(h []elapsedSample, mono time.Duration) (elapsedSample, bool) {
	if len(h) == 0 {
		return elapsedSample{}, false
	}
	p := h[len(h)-1]
	return p, p.ref != 0 && mono >= p.mono && mono-p.mono < 15*time.Second
}
func addElapsedBoundaries(span *spanEffect, d timingDriver, startMono, endMono time.Duration) {
	for i, h := range d.elapsed {
		var ref int64
		var start int64
		for j, t := range []time.Time{span.startTime, span.endTime} {
			var selected elapsedSample
			for _, p := range h {
				if !p.at.After(t) && !p.at.Before(selected.at) {
					selected = p
				}
			}
			mono := startMono
			if j == 1 {
				mono = endMono
			}
			if selected.ref == 0 || mono < selected.mono || mono-selected.mono >= 15*time.Second {
				continue
			}
			prefix := "f1.lap." + elapsedNames[i] + "."
			key := "start_seconds"
			if j == 1 {
				key = "end_seconds"
			}
			span.attrs = append(span.attrs, floatAttr(prefix+key, float64(selected.ns)/1e9))
			if j == 0 {
				ref, start = selected.ref, selected.ns
			} else if ref == selected.ref {
				span.attrs = append(span.attrs, floatAttr(prefix+"change_seconds", float64(selected.ns-start)/1e9))
			}
		}
	}
}
func (s *timingState) gaps(d *timingDriver, o sessionObservation, f topicFields, e *Effects) {
	for i, key := range []string{"GapToLeader", "IntervalToPositionAhead"} {
		ref := s.referent(1)
		if i == 1 {
			ref = s.referent(d.position - 1)
		}
		h := d.elapsed[i]
		raw := f[key]
		if i == 1 && raw != nil {
			if fields := nestedObject(raw); fields != nil {
				raw = fields["Value"]
				if raw == nil && slices.Contains(deletedNames(fields), "Value") {
					raw = json.RawMessage(`""`)
				}
			}
		}
		confirmed := d.leaderLap > 0 && d.leaderLap == s.currentSessionLap && d.position == 1 && s.referent(1) == d.number
		if raw == nil {
			var previous int64
			if len(h) > 0 {
				previous = h[len(h)-1].ref
			}
			newConfirmation := i == 0 && confirmed && previous != ref
			lostConfirmation := i == 0 && d.leaderLap > 0 && !confirmed && previous != 0
			referentChanged := previous != 0 && previous != ref
			if !newConfirmation && !lostConfirmation && !referentChanged {
				continue
			}
		}
		text, err := DecodeLosslessJSONString(raw)
		k, ns := gapToken(text)
		if i == 0 && confirmed {
			ns = 0
		} else if err != nil || k != ElapsedMilliseconds || d.position == 1 {
			ref = 0
		}
		p := elapsedSample{measurementTime(o), o.monotonic, ns, ref}
		d.elapsed[i] = append(slices.Clone(h[max(0, len(h)-recordLimit+1):]), p)
		if ref != 0 {
			e.metrics = append(e.metrics, durationGauge(o, d.driverRegistryEntry, elapsedNames[i], ns))
		}
	}
}

func applyTimingLapCount(s timingState, o sessionObservation, laps lapCountState) (timingState, Effects) {
	current, _ := laps.available()
	n := current.value
	if !current.known {
		n = 0
	}
	var e Effects
	if n == s.currentSessionLap {
		return s, e
	}
	s.currentSessionLap = n
	if s.sessionEnded || s.unsynchronized || !raceLike(o.Identity) {
		return s, e
	}
	s.drivers = copyMap(s.drivers)
	for _, num := range slices.Sorted(maps.Keys(s.drivers)) {
		d := s.drivers[num]
		if d.leaderLap > 0 {
			s.gaps(&d, o, nil, &e)
			s.drivers[num] = d
		}
	}
	return s, e
}
func tickTiming(s timingState, o sessionObservation) (timingState, Effects) {
	if !s.finalised.IsZero() && !s.tracesEnded && o.monotonic >= s.graceDeadline {
		return closeTimingTraces(s, o, s.finalised, "finalised_grace")
	}
	var e Effects
	if s.unsynchronized || s.sessionEnded || o.monotonic < s.nextTick {
		return s, e
	}
	s.nextTick = o.monotonic + 10*time.Second
	o.publication = o.observed
	for _, n := range slices.Sorted(maps.Keys(s.drivers)) {
		d := s.drivers[n]
		if d.position > 0 {
			e.metrics = append(e.metrics, driverGauge(o, d.driverRegistryEntry, "position", "{position}", d.position))
		}
		for i, h := range d.elapsed {
			if p, ok := freshSample(h, o.monotonic); ok {
				e.metrics = append(e.metrics, durationGauge(o, d.driverRegistryEntry, elapsedNames[i], p.ns))
			}
		}
	}
	return s, e
}

func (s *timingState) bestTimes(d *timingDriver, o sessionObservation, f topicFields, e *Effects) {
	d.best = copyMap(d.best)
	apply := func(p string, raw json.RawMessage, singular bool) {
		fields := nestedObject(raw)
		old := d.best[p]
		v := old
		for _, key := range deletedNames(fields) {
			switch key {
			case "Value":
				v = bestCandidate{}
			case "Lap":
				v.lap = 0
			}
		}
		if fields["Value"] != nil {
			v.duration, _ = timingDuration(sessionText(fields["Value"]))
			if v.duration != old.duration {
				v.lap = 0
			}
		}
		if fields["Lap"] != nil {
			v.lap, _ = canonicalIndex(string(fields["Lap"]))
		}
		if v.duration == 0 {
			v.lap = 0
		}
		d.best[p] = v
		if v.duration <= 0 || v.duration == old.duration {
			return
		}
		e.metrics = append(e.metrics, durationGauge(o, d.driverRegistryEntry, "best_lap_time", v.duration, phaseAttrs(p)...))
		if singular && !o.snapshot() && old.duration > 0 && v.duration > old.duration && s.once(timingEventKey{d.number, p, "f1.personal_best.revised", o.publication, old.duration, v.duration, old.lap, v.lap}) {
			a := []keyValue{floatAttr("f1.personal_best.previous_duration", float64(old.duration)/1e9), floatAttr("f1.personal_best.current_duration", float64(v.duration)/1e9)}
			if old.lap > 0 {
				a = append(a, intAttr("f1.personal_best.previous_lap", old.lap))
			}
			if v.lap > 0 {
				a = append(a, intAttr("f1.personal_best.current_lap", v.lap))
			}
			d.attach(eventAt("f1.personal_best.revised", o.publication, a...), s.position)
		}
	}
	plural := indexedFields(f["BestLapTimes"])
	for _, key := range deletedNames(f) {
		if key == "BestLapTime" {
			delete(d.best, s.phase)
		}
	}
	for _, key := range deletedNames(plural) {
		if n, ok := canonicalIndex(key); ok && n < 3 {
			if p, ok := canonicalQualifyingPhase(o.Identity.sessionType, n+1); ok {
				delete(d.best, p)
			}
		}
	}
	for i := int64(0); i < 3; i++ {
		p, ok := canonicalQualifyingPhase(o.Identity.sessionType, i+1)
		if ok && (p != s.phase || nestedObject(f["BestLapTime"])["Value"] == nil) && plural[decimal(i)] != nil {
			apply(p, plural[decimal(i)], false)
		}
	}
	_, qual := canonicalQualifyingPhase(o.Identity.sessionType, 1)
	if f["BestLapTime"] != nil && (!qual || s.phase != "") {
		apply(s.phase, f["BestLapTime"], true)
	}
}

func (s *timingState) lapPatch(d *timingDriver, o sessionObservation, f topicFields, transition bool, e *Effects) {
	at := measurementTime(o)
	n, valid := parseCanonicalDriverNumber(string(f["NumberOfLaps"]))
	before := d.current
	advance := valid && n > d.lapHigh[s.phase] && (before == 0 || n == before+1) && !o.snapshot() && (d.lap.span.kind == "" || !at.Before(d.lap.span.startTime))
	if advance && before != 0 {
		s.finishLap(d, o, e)
		d.pending = d.lap
		d.lap = lapTiming{}
		if d.pending.span.kind == "" {
			d.pending.span.lapNumber = before
			d.pending.span.phase = s.phase
			d.pending.span.stintOrdinal = d.ordinal
			d.pending.first = before
		}
		d.pending.span.endTime = at
		d.pending.endMono = o.monotonic
	}
	if valid && (o.snapshot() || advance) {
		if o.snapshot() && n != before {
			endSpan(&d.lap.span, d.lastFact, e)
			d.lap = lapTiming{}
		}
		d.current = n
		e.metrics = append(e.metrics, driverGauge(o, d.driverRegistryEntry, "current_lap", "{lap}", n, phaseAttrs(s.phase)...))
		if advance {
			d.lapHigh = copyMap(d.lapHigh)
			d.lapHigh[s.phase] = n
			if d.newStint {
				endSpan(&d.stint, at, e)
				if d.ordinal > 0 {
					d.ordinal++
				} else if seq := s.app.lines[d.number]; !s.app.unsynchronized {
					if _, ok := seq.active(); ok {
						d.ordinal = seq.last + 1
					}
				}
				d.newStint = false
				if !d.visit.entry.IsZero() && (d.visit.in == 0 || n == d.visit.in+1) {
					d.visit.out = n
				}
			}
			d.openLap(o, s.phase, n, at)
			s.cars.resolveTicks(*d)
		}
	}
	fields := nestedObject(f["LastLapTime"])
	l := &d.pending
	if l.span.lapNumber == 0 {
		l = &d.lap
	}
	if raw := fields["Value"]; raw != nil {
		text, err := DecodeLosslessJSONString(raw)
		ns, _ := timingDuration(text)
		if o.snapshot() {
			if ns > 0 {
				d.lastLap = ns
				e.metrics = append(e.metrics, durationGauge(o, d.driverRegistryEntry, "last_lap_time", ns, phaseAttrs(s.phase)...))
			}
		} else if d.pending.span.lapNumber != 0 {
			l.reported, l.valuePresent, l.empty = ns, true, err == nil && text == ""
		}
	}
	if !transition && !o.snapshot() && d.pending.span.lapNumber != 0 {
		if v := fields["PersonalFastest"]; v != nil {
			l.personal = boolState(v) == 2
		}
		if v := fields["OverallFastest"]; v != nil {
			l.overall = boolState(v) == 2
		}
	}
	sectors := indexedFields(f["Sectors"])
	for i := range 3 {
		raw := nestedObject(sectors[decimal(int64(i))])["Value"]
		if raw == nil {
			continue
		}
		text, textErr := DecodeLosslessJSONString(raw)
		ns, ok := timingDuration(text)
		if o.snapshot() {
			d.lastSectors[i] = ns
			if ns > 0 {
				e.metrics = append(e.metrics, durationGauge(o, d.driverRegistryEntry, "last_sector_time", ns, append(phaseAttrs(s.phase), intAttr("f1.sector.number", int64(i+1)))...))
			}
			continue
		}
		target := &d.lap
		if (advance || i == 2 && d.pending.sector[i] == 0 && ns > 0 && !d.pending.span.startTime.Add(time.Duration(ns)).After(d.pending.span.endTime)) && d.pending.span.lapNumber != 0 {
			target = &d.pending
		}
		target.sector[i], target.at[i] = ns, at
		target.invalidSector[i] = textErr != nil || text != "" && !ok
	}
	if !o.snapshot() && !transition {
		speeds := nestedObject(f["Speeds"])
		for i, k := range trapKeys {
			n, ok := unsignedDigits(sessionText(nestedObject(speeds[k])["Value"]))
			if !ok || n == 0 {
				continue
			}
			e.metrics = append(e.metrics, driverGauge(o, d.driverRegistryEntry, "speed_trap", "km/h", n, append(phaseAttrs(s.phase), strAttr("f1.speed_trap.location", strings.ToLower(k)))...))
			target := &d.lap
			if i != 3 && d.pending.span.lapNumber != 0 {
				target = &d.pending
			}
			target.trap[i] = n
		}
	}
	s.bestTimes(d, o, f, e)
}

func validTimingSnapshot(line topicFields) bool {
	if v := line["GapToLeader"]; v != nil {
		if _, err := DecodeLosslessJSONString(v); err != nil {
			return false
		}
	}
	if v := line["IntervalToPositionAhead"]; v != nil {
		f := nestedObject(v)
		if f == nil {
			return false
		}
		if raw := f["Value"]; raw != nil {
			if _, err := DecodeLosslessJSONString(raw); err != nil {
				return false
			}
		}
	}
	for _, k := range []string{"Stopped", "InPit"} {
		if v := line[k]; v != nil && boolState(v) == 0 {
			return false
		}
	}
	for _, k := range []string{"NumberOfPitStops", "NumberOfLaps", "SessionPart"} {
		if v := line[k]; v != nil {
			if _, ok := canonicalIndex(string(v)); !ok {
				return false
			}
		}
	}
	if v := line["Position"]; v != nil {
		txt, err := DecodeLosslessJSONString(v)
		if _, ok := parseCanonicalDriverNumber(txt); err != nil || txt != "" && !ok {
			return false
		}
	}
	for _, k := range []string{"LastLapTime", "BestLapTime", "Sectors", "BestLapTimes", "Speeds"} {
		v := line[k]
		if v == nil {
			continue
		}
		table := topicFields{"0": v}
		switch k {
		case "Speeds":
			table = nestedObject(v)
		case "Sectors", "BestLapTimes":
			table = indexedFields(v)
		}
		if table == nil {
			return false
		}
		for key, entry := range table {
			if key == "_deleted" {
				continue
			}
			fields := nestedObject(entry)
			if fields == nil {
				return false
			}
			if raw := fields["Value"]; raw != nil {
				txt, err := DecodeLosslessJSONString(raw)
				n, ok := timingDuration(txt)
				if k == "Speeds" {
					n, ok = unsignedDigits(txt)
				}
				if err != nil || txt != "" && (!ok || n == 0) {
					return false
				}
			}
			for _, flag := range []string{"PersonalFastest", "OverallFastest"} {
				if fields[flag] != nil && boolState(fields[flag]) == 0 {
					return false
				}
			}
		}
	}
	return true
}

func reduceTimingData(s timingState, o sessionObservation, raw json.RawMessage, reg driverRegistryState) (timingState, Effects, error) {
	var e Effects
	invalid := func() (timingState, Effects, error) {
		if o.snapshot() {
			s.unsynchronized, s.invalidSnapshot, s.runningKnown = true, true, false
		}
		return s, Effects{}, nil
	}
	f, err := topicObject(raw)
	if err != nil {
		return s, e, err
	}
	lines := nestedObject(f["Lines"])
	if !reg.frozen || !reg.synchronized {
		s.runningKnown = false
		if !o.snapshot() {
			return s, e, nil
		}
		for k, raw := range lines {
			if _, ok := parseCanonicalDriverNumber(k); !ok {
				continue
			}
			line := nestedObject(raw)
			if line == nil || line["RacingNumber"] != nil && sessionText(line["RacingNumber"]) != k || !validTimingSnapshot(line) {
				return invalid()
			}
		}
		if lines == nil {
			return invalid()
		}
		return omitTimingData(s), e, nil
	}
	if lines == nil {
		return invalid()
	}
	if s.sessionEnded || s.unsynchronized && !o.snapshot() {
		return s, e, nil
	}
	patches := map[int64]topicFields{}
	removed := deletedNames(lines)
	phase, gotPhase, conflict := s.phase, false, false
	_, qual := canonicalQualifyingPhase(o.Identity.sessionType, 1)
	readPart := func(raw json.RawMessage) {
		if raw != nil && qual {
			n, _ := canonicalInteger(raw)
			p, _ := canonicalQualifyingPhase(o.Identity.sessionType, n)
			conflict = conflict || gotPhase && p != phase
			phase, gotPhase = p, true
		}
	}
	if o.snapshot() {
		phase = ""
	}
	readPart(f["SessionPart"])
	for _, r := range reg.drivers[:reg.count] {
		v, ok := lines[decimal(r.number)]
		if !ok {
			continue
		}
		fields := nestedObject(v)
		if fields == nil || fields["RacingNumber"] != nil && sessionText(fields["RacingNumber"]) != decimal(r.number) {
			if o.snapshot() {
				return invalid()
			}
			continue
		}
		if o.snapshot() && !validTimingSnapshot(fields) {
			return invalid()
		}
		patches[r.number] = fields
		readPart(fields["SessionPart"])
	}
	if conflict {
		if o.snapshot() {
			return invalid()
		}
		s.phaseBlocked = true
		phase = s.phase
	} else if gotPhase || o.snapshot() {
		s.phaseBlocked = false
	}
	at := measurementTime(o)
	transition := s.phase != phase
	s.unsynchronized = false
	s.invalidSnapshot = false
	s.drivers = copyMap(s.drivers)
	if !o.snapshot() && at.After(s.watermark) {
		s.watermark = at
	}
	if s.nextTick == 0 {
		s.nextTick = o.monotonic + 10*time.Second
	}
	for _, r := range reg.drivers[:reg.count] {
		if _, ok := resolvedDriver(reg, r.number); !ok {
			continue
		}
		n := r.number
		line := patches[n]
		d := s.drivers[n]
		d.driverRegistryEntry = r
		if lines[decimal(n)] != nil && line == nil {
			d.stopped = 0
		}
		if slices.Contains(removed, decimal(n)) {
			s.incomplete(&d, "driver_removed")
			d.current, d.position, d.leaderLap, d.stopped = 0, 0, 0, 0
			d.countKnown, d.best, d.elapsed = false, nil, [2][]elapsedSample{}
			endSpan(&d.lap.span, d.lastFact, &e)
			d.lap = lapTiming{}
		}
		for _, key := range deletedNames(line) {
			switch key {
			case "Stopped":
				d.stopped = 0
			case "NumberOfPitStops":
				d.countKnown = false
			case "Position":
				d.position = 0
			case "InPit":
				d.inPit, d.newStint, d.visit = 0, false, timingVisit{}
			case "GapToLeader", "IntervalToPositionAhead":
				if line[key] == nil {
					line[key] = json.RawMessage(`""`)
				}
			}
		}
		if transition {
			d.pending.personal, d.pending.overall = false, false
			closeAt := at
			if o.snapshot() {
				closeAt = d.lastFact
				endSpan(&d.pending.span, closeAt, &e)
				d.pending = lapTiming{}
			} else {
				s.finishLap(&d, o, &e)
			}
			endSpan(&d.lap.span, closeAt, &e)
			endSpan(&d.stint, closeAt, &e)
			endSpan(&d.phaseSpan, closeAt, &e)
			d.current, d.ordinal, d.lastLap = 0, 1, 0
			if o.snapshot() && (s.late || s.start.IsZero()) {
				d.ordinal = 0
			}
			d.lastSectors = [3]int64{}
			d.lap = lapTiming{}
			d.best = copyMap(d.best)
			delete(d.best, phase)
		}
		if o.snapshot() {
			d.best = nil
			d.lastLap, d.lastSectors = 0, [3]int64{}
			d.position, d.leaderLap, d.inPit, d.stopped = 0, 0, 0, 0
			d.elapsed = [2][]elapsedSample{}
			if line["NumberOfPitStops"] == nil {
				d.countKnown = false
			}
			if line["NumberOfLaps"] == nil {
				endSpan(&d.lap.span, d.lastFact, &e)
				d.lap = lapTiming{}
				d.current = 0
			}
			if line == nil {
				s.incomplete(&d, "driver_removed")
			}
			if seq := s.app.lines[n]; !s.app.unsynchronized {
				if _, ok := seq.active(); ok && d.stint.kind == "" {
					d.ordinal = seq.last + 1
				}
			}
		}
		if !s.tracesEnded && s.late && len(line) > 0 && !o.snapshot() && d.root.kind == "" && !at.Before(s.lateFrom) {
			d.root = spanAt(spanDriverSession, o, d, at)
		}
		if v := line["Stopped"]; v != nil {
			d.stopped, d.stoppedAt = boolState(v), at
		}
		if v := line["GapToLeader"]; v != nil {
			k, num := gapToken(sessionText(v))
			d.leaderLap = 0
			if k == LeaderLap {
				d.leaderLap = num
			}
		}
		if v := line["Position"]; v != nil {
			p, _ := parseCanonicalDriverNumber(sessionText(v))
			old := d.position
			d.position = p
			if p != 0 && old != p {
				e.metrics = append(e.metrics, driverGauge(o, r, "position", "{position}", p))
				if old != 0 && !o.snapshot() && s.once(timingEventKey{n, "", "f1.position.changed", at, old, p, 0, 0}) {
					d.attach(eventAt("f1.position.changed", at, intAttr("f1.position.previous", old), intAttr("f1.position.current", p), intAttr("f1.position.delta", p-old), strAttr("f1.position.change.kind", "classification_update")), s.position)
				}
			}
		}
		s.drivers[n] = d
	}
	s.phase = phase
	for _, n := range slices.Sorted(maps.Keys(s.drivers)) {
		d := s.drivers[n]
		line := patches[n]
		if !o.snapshot() && d.pending.span.lapNumber != 0 && s.watermark.After(d.pending.span.endTime.Add(5*time.Second)) {
			s.finishLap(&d, o, &e)
		}
		if raceLike(o.Identity) {
			s.gaps(&d, o, line, &e)
		}
		exit := s.updatePit(&d, o, line, &e)
		if line != nil && !s.phaseBlocked && (!qual || phase != "") {
			s.lapPatch(&d, o, line, transition, &e)
		} else if line != nil && !s.phaseBlocked {
			s.bestTimes(&d, o, line, &e)
		}
		if l := d.pending; l.span.kind != "" {
			s.control = retainLapRepairSummary(s.control, lapRepairSummary{n, l.span.lapNumber, l.span.lapNumber + 1, l.reported / 1e6, l.span.phase, l.reported > 0, false})
		}
		if exit {
			v := d.visit
			if v.out != d.current {
				v.out = 0
			}
			d.visits = append(slices.Clip(d.visits), v)
			if v.number != 0 {
				d.attach(eventAt("f1.pit.exit", at, intAttr("f1.pit.visit.number", v.number)), s.position)
			}
			d.visit = timingVisit{}
		}
		if !o.snapshot() && d.pending.span.lapNumber != 0 && !s.watermark.Before(d.pending.span.endTime.Add(5*time.Second)) {
			s.finishLap(&d, o, &e)
		}
		if !o.snapshot() && len(line) > 0 && at.After(d.lastFact) {
			d.lastFact = at
		}
		s.drivers[n] = d
	}
	s.carsRunning(reg, o.Identity, at, &e)
	if !o.snapshot() {
		s.drainWindows(time.Time{}, false, &e)
	}
	return s, e, nil
}
