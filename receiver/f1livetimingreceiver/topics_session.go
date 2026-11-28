package f1livetimingreceiver

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"
)

// The caller supplies the observation and monotonic times for this callback.
type sessionObservation struct {
	identity              sessionInfoIdentity
	source                liveTimingUpdateSource
	publication, observed time.Time
	monotonic             time.Duration
}

func (o sessionObservation) snapshot() bool { return o.source == liveTimingUpdateSourceSnapshot }

func visitSessionFields(raw json.RawMessage, visit func(string, json.RawMessage)) error {
	return visitRawJSONObject(raw, func(key, value json.RawMessage) error {
		name, err := decodeLosslessJSONString(key)
		if err == nil {
			visit(name, value)
		}
		return nil
	})
}

func sessionText(raw json.RawMessage) string {
	s, _ := decodeLosslessJSONString(raw)
	return s
}

func sessionUTC(raw json.RawMessage) (time.Time, bool) {
	t, err := normalizeLiveTimingTimestamp(liveTimingUpdateSourceFeed, sessionText(raw))
	return t, err == nil
}

func sessionStatus(s string) string {
	switch s {
	case "Inactive", "Started", "Aborted", "Finished", "Finalised", "Ends":
		return s
	}
	return ""
}

type trackCondition struct {
	code  int64
	state string
}

func trackStatus(s string) trackCondition {
	switch s {
	case "AllClear":
		return trackCondition{1, "all_clear"}
	case "Yellow":
		return trackCondition{2, "yellow"}
	case "SCDeployed":
		return trackCondition{4, "safety_car"}
	case "Red":
		return trackCondition{5, "red"}
	case "VSCDeployed":
		return trackCondition{6, "virtual_safety_car"}
	case "VSCEnding":
		return trackCondition{7, "virtual_safety_car_ending"}
	}
	return trackCondition{}
}

type sessionStatusMirror struct {
	status, started string
	unsynchronized  bool
}

func reduceSessionStatus(state sessionStatusMirror, o sessionObservation, raw json.RawMessage) (sessionStatusMirror, error) {
	before := state
	if o.snapshot() {
		state = sessionStatusMirror{}
	}
	err := visitSessionFields(raw, func(k string, v json.RawMessage) {
		switch k {
		case "Status":
			state.status = sessionStatus(sessionText(v))
		case "Started":
			state.started = sessionText(v)
		}
	})
	if errors.Is(err, errJSONObject) {
		before.unsynchronized = before.unsynchronized || o.snapshot()
		return before, nil
	}
	if err != nil || before.unsynchronized && !o.snapshot() {
		return before, err
	}
	return state, nil
}

type trackStatusMirror struct {
	code, message  string
	unsynchronized bool
}

func (m trackStatusMirror) condition() trackCondition {
	track := trackStatus(m.message)
	if m.code != decimal(track.code) {
		return trackCondition{}
	}
	return track
}
func reduceTrackStatus(state trackStatusMirror, o sessionObservation, raw json.RawMessage) (trackStatusMirror, error) {
	before := state
	if o.snapshot() {
		state = trackStatusMirror{}
	}
	err := visitSessionFields(raw, func(k string, v json.RawMessage) {
		switch k {
		case "Status":
			state.code = sessionText(v)
		case "Message":
			state.message = sessionText(v)
		}
	})
	if errors.Is(err, errJSONObject) {
		before.unsynchronized = before.unsynchronized || o.snapshot()
		return before, nil
	}
	if err != nil || before.unsynchronized && !o.snapshot() {
		return before, err
	}
	return state, nil
}

type seriesRecord struct {
	utc     time.Time
	hasUTC  bool
	status  [2]string
	present [2]bool
}

const (
	trackField = iota
	sessionField
)

type statusEdge struct {
	utc               time.Time
	previous, current string
}
type statusHistory struct {
	current   string
	broken    bool
	watermark time.Time
	edges     map[statusEdge]bool
}
type sessionDataState struct {
	indexedStartedUTC time.Time
	unsynchronized    bool
	records           map[int64]seriesRecord
	consumed          map[int64][2]bool
	history           [2]statusHistory
}

func (s sessionDataState) mirrorsAgree(session sessionStatusMirror, track trackStatusMirror) bool {
	return !s.unsynchronized && !session.unsynchronized && !track.unsynchronized && session.status != "" && track.condition().code != 0 &&
		session.status == s.history[sessionField].current && track.condition() == trackStatus(s.history[trackField].current)
}

type sessionStatusTransition struct {
	index             int64
	utc               time.Time
	previous, current string
}
type trackStatusTransition struct {
	index             int64
	utc               time.Time
	previous, current trackCondition
}
type sessionDataReduction struct {
	feedStartedUTC time.Time
	state          sessionDataState
	effects        liveTimingEffects
	sessions       []sessionStatusTransition
	tracks         []trackStatusTransition
	unknownStatus  bool
}

func parseSeriesRecord(r seriesRecord, raw json.RawMessage) seriesRecord {
	err := visitSessionFields(raw, func(k string, v json.RawMessage) {
		switch k {
		case "Utc":
			r.utc, r.hasUTC = sessionUTC(v)
		case "SessionStatus":
			r.status[sessionField], r.present[sessionField] = sessionStatus(sessionText(v)), true
		case "TrackStatus":
			name := sessionText(v)
			if trackStatus(name).code == 0 {
				name = ""
			}
			r.status[trackField], r.present[trackField] = name, true
		}
	})
	if err != nil {
		return seriesRecord{}
	}
	return r
}

func canonicalIndex(s string) (int64, bool) {
	if s == "0" {
		return 0, true
	}
	return parseCanonicalDriverNumber(s)
}

func reduceSessionData(s sessionDataState, o sessionObservation, raw json.RawMessage) (sessionDataReduction, error) {
	records := maps.Clone(s.records)
	if records == nil || o.snapshot() {
		records = make(map[int64]seriesRecord)
	}
	touched := make(map[int64]bool)
	var seriesErr error
	err := visitSessionFields(raw, func(k string, v json.RawMessage) {
		if k != "StatusSeries" || seriesErr != nil {
			return
		}
		if v[0] == '[' {
			var rows []json.RawMessage
			seriesErr = json.Unmarshal(v, &rows)
			clear(records)
			clear(touched)
			for i, row := range rows {
				records[int64(i)] = parseSeriesRecord(seriesRecord{}, row)
				touched[int64(i)] = true
			}
			return
		}
		seriesErr = visitSessionFields(v, func(key string, row json.RawMessage) {
			if i, ok := canonicalIndex(key); ok {
				records[i] = parseSeriesRecord(records[i], row)
				touched[i] = true
			}
		})
	})
	if err == nil {
		err = seriesErr
	}
	if errors.Is(err, errJSONObject) {
		s.unsynchronized = s.unsynchronized || o.snapshot()
		return sessionDataReduction{state: s}, nil
	}
	if err != nil || s.unsynchronized && !o.snapshot() {
		return sessionDataReduction{state: s}, err
	}
	s.unsynchronized = false
	s.records = records
	s.consumed = maps.Clone(s.consumed)
	if s.consumed == nil {
		s.consumed = make(map[int64][2]bool)
	}
	for i, h := range s.history {
		s.history[i].edges = maps.Clone(h.edges)
		if h.edges == nil {
			s.history[i].edges = make(map[statusEdge]bool)
		}
	}
	out := sessionDataReduction{state: s}
	for _, i := range slices.Sorted(maps.Keys(touched)) {
		r := records[i]
		if !r.hasUTC {
			continue
		}
		used := s.consumed[i]
		for kind, present := range r.present {
			if !present || used[kind] {
				continue
			}
			used[kind] = true
			h := &out.state.history[kind]
			current := r.status[kind]
			edge := statusEdge{r.utc, h.current, current}
			seen := h.edges[edge]
			if current != "" && current != h.current {
				h.edges[edge] = true
			}
			if r.utc.Before(h.watermark) {
				continue
			}
			h.watermark = r.utc
			if kind == sessionField && current == "Started" && out.state.indexedStartedUTC.IsZero() {
				out.state.indexedStartedUTC = r.utc
			}
			if kind == sessionField && current == "Started" && !o.snapshot() && out.feedStartedUTC.IsZero() {
				out.feedStartedUTC = r.utc
			}
			previous, broken := h.current, h.broken
			h.current, h.broken = current, current == ""
			out.unknownStatus = out.unknownStatus || h.broken
			if current == "" || broken || current == previous || seen || o.snapshot() {
				continue
			}
			log := logEffect{identity: o.identity, dataSource: "livetiming", timeQuality: "observed", timestamp: r.utc, observedTimestamp: o.observed}
			if kind == trackField {
				prev, curr := trackStatus(previous), trackStatus(current)
				out.tracks = append(out.tracks, trackStatusTransition{i, r.utc, prev, curr})
				log.body = "f1.track_status.changed"
				log.attrs = []keyValue{intAttr("f1.track_status.code", curr.code), strAttr("f1.track_status.state", curr.state)}
				if prev.code != 0 {
					log.attrs = append(log.attrs, intAttr("f1.track_status.previous_code", prev.code), strAttr("f1.track_status.previous_state", prev.state))
				}
			} else {
				out.sessions = append(out.sessions, sessionStatusTransition{i, r.utc, previous, current})
				log.body = "f1.session.status.changed"
				log.attrs = []keyValue{strAttr("f1.session.status", current)}
				if previous != "" {
					log.attrs = append(log.attrs, strAttr("f1.session.previous_status", previous))
				}
			}
			out.effects.logs = append(out.effects.logs, log)
		}
		s.consumed[i] = used
	}
	return out, nil
}

type lapField struct {
	value int64
	known bool
}
type lapGauge struct {
	last    time.Time
	pending bool
}
type lapCountState struct {
	unsynchronized           bool
	current, total           lapField
	currentGauge, totalGauge lapGauge
	active, started          bool
	deadline                 time.Duration
}

func (s lapCountState) available() (lapField, lapField) {
	c, t := s.current, s.total
	if c.known && t.known {
		if t.value == 0 || c.value > t.value {
			return lapField{}, lapField{}
		}
	} else {
		c.known = c.known && c.value > 0
		t.known = t.known && t.value > 0
	}
	return c, t
}

func raceLike(e sessionInfoIdentity) bool {
	return e.sessionType == canonicalSessionTypeRace || e.sessionType == canonicalSessionTypeSprint
}
func sessionGauge(e sessionInfoIdentity, name, unit string, t time.Time, v int64) metricEffect {
	return metricEffect{name: name, kind: metricIntGauge, identity: e, dataSource: "livetiming", unit: unit, timestamp: t, intVal: v}
}

const (
	currentLapMetric = "f1.session.current_lap"
	totalLapsMetric  = "f1.session.intended_total_laps"
	clockMetric      = "f1.session.clock.remaining"
)

func (g lapGauge) project(e sessionInfoIdentity, name string, at time.Time, f lapField, changed bool) (lapGauge, []metricEffect) {
	g.pending = f.known && (changed || g.pending)
	if !g.pending || !raceLike(e) || !at.After(g.last) {
		return g, nil
	}
	g.last, g.pending = at, false
	return g, []metricEffect{sessionGauge(e, name, "{lap}", at, f.value)}
}

func reduceLapCount(s lapCountState, o sessionObservation, raw json.RawMessage) (lapCountState, liveTimingEffects, error) {
	before := s
	invalid := false
	if o.snapshot() {
		s.current, s.total = lapField{}, lapField{}
	}
	err := visitSessionFields(raw, func(k string, v json.RawMessage) {
		if k != "CurrentLap" && k != "TotalLaps" {
			return
		}
		n, ok := canonicalIndex(string(v))
		invalid = invalid || !ok
		if k == "CurrentLap" {
			s.current = lapField{n, ok}
		} else {
			s.total = lapField{n, ok}
		}
	})
	if errors.Is(err, errJSONObject) {
		before.unsynchronized = before.unsynchronized || o.snapshot()
		return before, liveTimingEffects{}, nil
	}
	if err != nil || before.unsynchronized && !o.snapshot() {
		return before, liveTimingEffects{}, err
	}
	if o.snapshot() && invalid {
		before.unsynchronized = true
		return before, liveTimingEffects{}, nil
	}
	s.unsynchronized = false
	at := o.publication
	if o.snapshot() {
		at = o.observed
	}
	c, t := s.available()
	pc, pt := before.available()
	var current, total []metricEffect
	s.currentGauge, current = s.currentGauge.project(o.identity, currentLapMetric, at, c, c != pc || o.snapshot())
	s.totalGauge, total = s.totalGauge.project(o.identity, totalLapsMetric, at, t, t != pt || o.snapshot())
	if s.active && (pc != c || pt != t || o.snapshot()) {
		s.deadline = o.monotonic + 10*time.Second
	}
	return s, liveTimingEffects{metrics: append(current, total...)}, nil
}

func (s lapCountState) heartbeatActive(active bool, now time.Duration) lapCountState {
	if active && !s.active {
		s.deadline = now + 10*time.Second
	}
	s.active = active
	return s
}

func (s lapCountState) withSessionStatus(status string, snapshot bool, now time.Duration) lapCountState {
	switch status {
	case "Started":
		s.started = true
		return s.heartbeatActive(true, now)
	case "Aborted":
		s.started = s.started || snapshot
		return s.heartbeatActive(s.started, now)
	case "Inactive":
		return s.heartbeatActive(s.started, now)
	case "Finished", "Finalised", "Ends":
		s.started = false
	}
	return s.heartbeatActive(false, now)
}

func tickLapCount(s lapCountState, o sessionObservation) (lapCountState, liveTimingEffects) {
	var out liveTimingEffects
	if !s.unsynchronized && s.active && o.monotonic >= s.deadline {
		c, _ := s.available()
		s.currentGauge, out.metrics = s.currentGauge.project(o.identity, currentLapMetric, o.observed, c, true)
		s.deadline = o.monotonic + 10*time.Second
	}
	return s, out
}

type extrapolatedClockState struct {
	unsynchronized                      bool
	utc, last                           time.Time
	remaining                           int64
	extrapolating, knownMode, scheduled bool
}

func (s extrapolatedClockState) suspend() extrapolatedClockState {
	s.scheduled, s.knownMode = false, false
	return s
}

func remainingSeconds(s string) (int64, bool) {
	if len(s) != 8 || s[2] != ':' || s[5] != ':' {
		return 0, false
	}
	h, okH := twoASCIIDigits(s[:2])
	m, okM := twoASCIIDigits(s[3:5])
	sec, okS := twoASCIIDigits(s[6:])
	if !okH || !okM || !okS || m >= 60 || sec >= 60 {
		return 0, false
	}
	return int64(h*3600 + m*60 + sec), true
}

func reduceExtrapolatedClock(s extrapolatedClockState, o sessionObservation, raw json.RawMessage) (extrapolatedClockState, liveTimingEffects, error) {
	before := s
	if o.snapshot() {
		s = extrapolatedClockState{last: s.last}
	}
	var utc time.Time
	var remaining int64
	var hasUTC, hasRemaining, invalid bool
	err := visitSessionFields(raw, func(k string, v json.RawMessage) {
		switch k {
		case "Utc":
			utc, hasUTC = sessionUTC(v)
			invalid = invalid || !hasUTC
		case "Remaining":
			remaining, hasRemaining = remainingSeconds(sessionText(v))
			invalid = invalid || !hasRemaining
		case "Extrapolating":
			mode := bytes.Equal(v, []byte("true"))
			s.knownMode = mode || bytes.Equal(v, []byte("false"))
			invalid = invalid || !s.knownMode
			if !s.knownMode || s.extrapolating != mode {
				s.scheduled = false
			}
			s.extrapolating = mode
		}
	})
	if errors.Is(err, errJSONObject) || err == nil && o.snapshot() && (invalid || !hasUTC || !hasRemaining || !s.knownMode) {
		if o.snapshot() {
			before = before.suspend()
			before.unsynchronized = true
		}
		return before, liveTimingEffects{}, nil
	}
	if err != nil || before.unsynchronized && !o.snapshot() {
		return before, liveTimingEffects{}, err
	}
	if !hasUTC || !hasRemaining || !s.knownMode {
		return s, liveTimingEffects{}, nil
	}
	s.unsynchronized = false
	s.utc, s.remaining, s.scheduled = utc, remaining, true
	at := utc
	if o.snapshot() {
		at = o.observed
	}
	s, out := projectClock(s, o.identity, at, o.snapshot())
	return s, out, nil
}

func projectClock(s extrapolatedClockState, e sessionInfoIdentity, at time.Time, snapshot bool) (extrapolatedClockState, liveTimingEffects) {
	if s.unsynchronized || !s.scheduled || at.Before(s.utc) {
		return s, liveTimingEffects{}
	}
	elapsed := at.Sub(s.utc) / time.Second
	value := s.remaining
	if s.extrapolating {
		value = max(0, value-int64(elapsed))
	}
	if !snapshot {
		if s.extrapolating {
			at = s.utc.Add(elapsed * time.Second)
		} else {
			at = s.utc
		}
	}
	if !at.After(s.last) {
		return s, liveTimingEffects{}
	}
	s.last, s.scheduled = at, s.extrapolating && value > 0
	return s, liveTimingEffects{metrics: []metricEffect{sessionGauge(e, clockMetric, "s", at, value)}}
}

func tickExtrapolatedClock(s extrapolatedClockState, e sessionInfoIdentity, at time.Time) (extrapolatedClockState, liveTimingEffects) {
	return projectClock(s, e, at, false)
}
