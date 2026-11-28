package f1engine

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

type rootInterval struct{ start, end time.Time }
type State struct {
	SessionInfo sessionInfoState
	drivers     driverRegistryState
	series      sessionDataState
	Status      sessionStatusMirror
	track       trackStatusMirror
	laps        lapCountState
	clock       extrapolatedClockState
	weather     weatherState
	radio       teamRadioState
	timing      timingState
	roots       map[int64]rootInterval
	Origin      time.Time
	spanIDs     map[pcommon.SpanID]spanEntity
	idCollision bool
	display     sessionDisplay
}

type driverDisplay struct{ name, constructor string }
type sessionDisplay struct {
	meeting, circuit string
	drivers          map[int64]driverDisplay
}

func displayText(raw json.RawMessage) string {
	text := sessionText(raw)
	safe := sanitizeControl(text)
	if safe.redacted || safe.truncated || safe.body != text {
		return ""
	}
	return text
}

func (s sessionDisplay) rootAttrs(n int64) []keyValue {
	d := s.drivers[n]
	var attrs []keyValue
	for _, a := range []keyValue{strAttr("f1.meeting.name", s.meeting), strAttr("f1.circuit.name", s.circuit), strAttr("f1.driver.name", d.name), strAttr("f1.constructor.name", d.constructor)} {
		if a.value.strVal != "" {
			attrs = append(attrs, a)
		}
	}
	return attrs
}

type Reduction struct {
	RetiredDiagnostics          RacingDiagnostics
	State                       State
	Effects                     Effects
	SessionInfoDisposition      sessionInfoDisposition
	sessionInfoAuthoritative    bool
	sessionInfoRouteTransition  bool
	sessionInfoIssues           sessionInfoIssueSet
	sessionScopedUpdatesAllowed bool
}

func ReduceBatch(
	state State,
	batch Batch,
) (Reduction, error) {
	sessionInfo, authoritative, err := reduceSessionInfoBatch(state.SessionInfo, batch)
	if err != nil {
		return Reduction{}, err
	}

	reduction := Reduction{
		State:                    state,
		sessionInfoAuthoritative: authoritative,
	}
	if authoritative {
		if sessionInfo.disposition == SessionInfoDispositionReplaced {
			retired, effects := terminal(state, "session_replacement")
			reduction.Effects = effects
			reduction.RetiredDiagnostics = retired.Diagnostics()
			reduction.State = State{}
		}
		reduction.State.SessionInfo = sessionInfo.state
		reduction.SessionInfoDisposition = sessionInfo.disposition
		reduction.sessionInfoRouteTransition = sessionInfo.routeTransition
		reduction.sessionInfoIssues = sessionInfo.issues
	}
	reduction.sessionScopedUpdatesAllowed =
		reduction.State.SessionInfo.IdentityAvailable &&
			reduction.State.SessionInfo.Synchronized
	if !reduction.sessionScopedUpdatesAllowed {
		if authoritative && state.SessionInfo.Synchronized {
			reduction.State.suspendTopics()
		}
		return reduction, nil
	}
	s := &reduction.State
	o := sessionObservation{Identity: s.SessionInfo.Identity, source: batch.Source, observed: batch.ObservationTime}
	if s.Origin.IsZero() {
		s.Origin = o.observed
	}
	o.monotonic = o.observed.Sub(s.Origin)
	updates := batch.Updates
	omitTiming := false
	if o.snapshot() {
		for _, topic := range batch.RequestedTopics {
			if !slices.Contains(batch.PresentTopics, topic) {
				if topic == "TimingData" {
					omitTiming = true
				} else {
					s.omit(topic)
				}
			}
		}
		updates = slices.Clone(updates)
		// Dependencies of a snapshot are staged before their consumers.
		slices.SortFunc(updates, func(a, b Update) int {
			if d := snapshotRank(a.Topic) - snapshotRank(b.Topic); d != 0 {
				return d
			}
			return strings.Compare(a.Topic, b.Topic)
		})
	}
	reconcile := o.snapshot() && (slices.Contains(batch.RequestedTopics, "SessionStatus") || slices.Contains(batch.RequestedTopics, "SessionData") || slices.Contains(batch.RequestedTopics, "TrackStatus"))
	reconcileAfterHydration := false
	omitBeforeHydration := func() {
		if omitTiming && (s.series.unsynchronized || s.series.history[sessionField].current != "Ends") {
			s.omit("TimingData")
			omitTiming = false
		}
	}
	for _, u := range updates {
		if omitTiming && snapshotRank(u.Topic) > 1 {
			omitBeforeHydration()
		}
		if reconcile && snapshotRank(u.Topic) > 1 {
			reconcile = false
			if s.series.history[sessionField].current == "Ends" && !s.series.unsynchronized {
				reduction.add(s.reconcile(o))
			} else if terminalStatus(s.series.history[sessionField].current) && !s.series.unsynchronized {
				reconcileAfterHydration = true
				// Deactivate before hydration. Close spans after staging TimingData.
				s.timing.active = false
				s.laps.active = false
			} else {
				prior := s.timing
				e := s.reconcile(o)
				if slices.Contains(batch.PresentTopics, "TimingData") && s.timing.active {
					// The following TimingData snapshot owns the aggregate for this timestamp.
					e.metrics = nil
					s.timing.running, s.timing.runningAt = prior.running, prior.runningAt
					s.timing.runningKnown, s.timing.runningDeferred = prior.runningKnown, prior.runningDeferred
				}
				reduction.add(e)
			}
		}
		o.publication = u.Timestamp
		o.initialArchiveRadio = u.InitialArchiveRadio
		e, err := s.apply(o, u)
		if err != nil {
			return Reduction{}, err
		}
		reduction.add(e)
	}
	omitBeforeHydration()
	if reconcile || reconcileAfterHydration {
		reduction.add(s.reconcile(o))
	}
	if omitTiming {
		s.omit("TimingData")
	}
	if o.snapshot() {
		s.coverLate(o.observed)
	}
	return reduction, nil
}

func snapshotRank(topic string) int {
	switch topic {
	case "SessionInfo", "DriverList":
		return 0
	case "SessionData", "SessionStatus", "TrackStatus":
		return 1
	case "TimingAppData":
		return 2
	default:
		return 3
	}
}

func terminalStatus(status string) bool {
	return status == "Finished" || status == "Finalised" || status == "Ends"
}

func (s *State) coverLate(at time.Time) {
	status := s.series.history[sessionField].current
	if !s.series.unsynchronized && (status == "Started" || status == "Aborted") {
		s.timing.coverLate(s.drivers, at)
	}
}

func (r *Reduction) add(e Effects) {
	if e.empty() {
		return
	}
	if len(e.spans) > 0 {
		r.State.spanIDs = copyMap(r.State.spanIDs)
		e.spans = slices.DeleteFunc(slices.Clone(e.spans), func(s spanEffect) bool {
			_, _, _, ok := spanPath(s, r.State.spanIDs)
			r.State.idCollision = r.State.idCollision || !ok
			return !ok
		})
	}
	for i := range e.spans {
		span := &e.spans[i]
		if span.kind == spanDriverSession {
			span.attrs = append(slices.Clip(span.attrs), r.State.display.rootAttrs(span.driverNumber)...)
			r.State.roots = copyMap(r.State.roots)
			r.State.roots[span.driverNumber] = rootInterval{span.startTime, span.endTime}
		}
	}
	r.Effects = r.Effects.append(e)
}

func (s *State) omit(topic string) {
	switch topic {
	case "DriverList":
		s.drivers.synchronized = false
	case "SessionStatus":
		s.Status = sessionStatusMirror{}
	case "TrackStatus":
		s.track = trackStatusMirror{}
	case "SessionData":
		s.series.records = nil
		s.series.unsynchronized = false
		for i := range s.series.history {
			s.series.history[i].current = ""
			s.series.history[i].broken = false
		}
	case "ExtrapolatedClock":
		s.clock = extrapolatedClockState{last: s.clock.last}
	case "LapCount":
		s.laps.unsynchronized = false
		s.laps.current, s.laps.total = lapField{}, lapField{}
		s.laps.active = false
		s.timing.currentSessionLap = 0
	case "WeatherData":
		s.weather = omitWeather(s.weather)
	case "TeamRadio":
		s.radio = omitTeamRadio(s.radio)
	case "CarData":
		s.timing.cars = omitCarData(s.timing.cars)
	case "Position":
		s.timing.position = omitPosition()
	case "RaceControlMessages":
		s.timing.control = omitRaceControlMessages(s.timing.control)
	case "TimingData":
		s.timing = omitTimingData(s.timing)
	case "TimingAppData":
		s.timing.app = omitTimingAppData()
	case "TimingStats":
		s.timing.stats = omitTimingStats()
	case "PitStop":
		s.timing = omitPitTopic(s.timing, stopReport)
	case "PitStopSeries":
		s.timing = omitPitTopic(s.timing, seriesReport)
	case "PitLaneTimeCollection":
		s.timing = omitPitTopic(s.timing, laneReport)
	}
}

func (s *State) reconcile(o sessionObservation) Effects {
	var e Effects
	h := s.series.history[sessionField]
	if s.series.unsynchronized {
		s.laps.active = false
		s.timing.active = false
		return e
	}
	mirror := s.Status
	if mirror.unsynchronized {
		mirror = sessionStatusMirror{}
	}
	s.timing, e = applyTimingStatusSnapshot(s.timing, o, s.series, mirror, s.drivers)
	if terminalStatus(h.current) || s.series.mirrorsAgree(s.Status, s.track) {
		s.laps = s.laps.withSessionStatus(h.current, true, o.monotonic)
		s.weather = s.weather.withSessionStatus(h.current)
	} else {
		s.laps.active = false
	}
	if s.timing.sessionEnded {
		s.clock = s.clock.suspend()
	}
	return e
}

func (s *State) apply(o sessionObservation, u Update) (Effects, error) {
	var e Effects
	var err error
	p := u.Payload
	switch u.Topic {
	case "SessionInfo":
		meeting := nestedObject(nestedObject(p)["Meeting"])
		s.display.meeting = displayText(meeting["Name"])
		s.display.circuit = displayText(nestedObject(meeting["Circuit"])["ShortName"])
	case "DriverList":
		d, err := parseDriverList(p)
		if err != nil {
			return e, err
		}
		r, err := reduceDriverRegistry(s.drivers, o.source, d)
		s.drivers = r.state
		if !o.snapshot() {
			s.coverLate(o.observed)
		}
		if r.state.synchronized {
			entries := nestedObject(p)
			for _, driver := range r.state.drivers[:r.state.count] {
				f := nestedObject(entries[decimal(driver.number)])
				old := s.display.drivers[driver.number]
				newDisplay := old
				if old.name == "" {
					newDisplay.name = displayText(f["FullName"])
				}
				if old.constructor == "" {
					newDisplay.constructor = displayText(f["TeamName"])
				}
				if newDisplay != old {
					s.display.drivers = copyMap(s.display.drivers)
					s.display.drivers[driver.number] = newDisplay
				}
			}
		}
		return e, err
	case "SessionData":
		r, err := reduceSessionData(s.series, o, p)
		if err != nil {
			return e, err
		}
		s.series, e = r.state, r.effects
		for len(r.sessions)+len(r.tracks) > 0 {
			if len(r.tracks) > 0 && (len(r.sessions) == 0 || r.tracks[0].index <= r.sessions[0].index) {
				s.timing = applyTrackStatusTransition(s.timing, r.tracks[0])
				r.tracks = r.tracks[1:]
				continue
			}
			tr := r.sessions[0]
			r.sessions = r.sessions[1:]
			if tr.current == "Started" && s.timing.deltaStart.IsZero() && !s.timing.competitiveStop {
				s.timing.deltaStart = s.series.indexedStartedUTC
			}
			ended := s.timing.sessionEnded
			var next Effects
			s.timing, next = applySessionStatusTransition(s.timing, o, tr, s.drivers)
			e = e.append(next)
			s.weather = s.weather.withSessionStatus(tr.current)
			s.laps = s.laps.withSessionStatus(tr.current, false, o.monotonic)
			if !ended && s.timing.sessionEnded {
				e = e.append(s.flushFields(tr.utc))
			}
		}
		if s.timing.start.IsZero() {
			s.timing.start = r.feedStartedUTC
		}
	case "SessionStatus":
		s.Status, err = reduceSessionStatus(s.Status, o, p)
	case "TrackStatus":
		s.track, err = reduceTrackStatus(s.track, o, p)
	case "LapCount":
		s.laps, e, err = reduceLapCount(s.laps, o, p)
		var next Effects
		if err == nil && !s.laps.unsynchronized {
			s.timing, next = applyTimingLapCount(s.timing, o, s.laps)
			e = e.append(next)
		}
	case "ExtrapolatedClock":
		if !s.timing.sessionEnded && s.series.history[sessionField].current != "Ends" {
			s.clock, e, err = reduceExtrapolatedClock(s.clock, o, p)
		}
	case "WeatherData":
		s.weather, e, err = reduceWeatherData(s.weather, o, p)
	case "TeamRadio":
		s.radio, e, err = reduceTeamRadio(s.radio, o, p, s.drivers, func(n int64, t time.Time) bool {
			r, ok := s.roots[n]
			return ok && !t.Before(r.start) && !t.After(r.end)
		})
	case "CarData":
		if !s.timing.sessionEnded {
			s.timing.cars, e, err = reduceCarData(s.timing.cars, o, p, s.drivers, s.timing.deltaStart, s.timing.drivers)
		}
	case "Position":
		s.timing.position, err = reducePosition(s.timing.position, o, p, s.drivers)
	case "RaceControlMessages":
		var r raceControlReduction
		r, err = reduceRaceControlMessages(s.timing.control, o, p, s.drivers, s.series.indexedStartedUTC, s.timing.sessionEndUTC)
		s.timing.control = r.state
		s.timing = attachRaceControlEvents(s.timing, r.events, r.effects.logs, s.roots)
		e = r.effects
	case "TimingData":
		s.timing, e, err = reduceTimingData(s.timing, o, p, s.drivers)
	case "TimingAppData":
		s.timing.app, e, err = reduceTimingAppData(s.timing.app, o, p, s.drivers)
	case "TimingStats":
		s.timing.stats, e, err = reduceTimingStats(s.timing.stats, o, p, s.drivers)
	case "PitStop":
		s.timing, e, err = reducePitStop(s.timing, o, p, s.drivers)
	case "PitStopSeries":
		s.timing, e, err = reducePitStopSeries(s.timing, o, p, s.drivers)
	case "PitLaneTimeCollection":
		s.timing, e, err = reducePitLaneTimeCollection(s.timing, o, p, s.drivers)
	}
	return e, err
}

func (s *State) flushFields(end time.Time) Effects {
	s.clock = s.clock.suspend()
	s.laps.active = false
	var car, flags Effects
	s.timing.cars, car = flushCarData(s.timing.cars, s.SessionInfo.Identity, s.timing.deltaStart, end)
	s.timing.control, flags = foldBlueFlags(s.timing.control, s.SessionInfo.Identity, s.series.indexedStartedUTC, end)
	return car.append(flags)
}

func terminal(s State, reason string) (State, Effects) {
	if !s.SessionInfo.IdentityAvailable {
		return s, Effects{}
	}
	var e Effects
	s.timing, e = flushTimingTerminal(s.timing, sessionObservation{Identity: s.SessionInfo.Identity}, time.Time{}, reason)
	r := Reduction{State: s}
	r.add(e.append(r.State.flushFields(s.timing.sessionEndUTC)))
	return r.State, r.Effects
}
func ReduceShutdown(s State) (State, Effects) {
	return terminal(s, "shutdown")
}
func ReduceDisconnect(s State) State {
	s.timing, _ = disconnectTiming(s.timing)
	s.suspendTopics()
	return s
}
func (s *State) suspendTopics() {
	s.timing.position = disconnectPosition(s.timing.position)
	s.drivers.synchronized = false
	s.timing.unsynchronized, s.timing.app.unsynchronized, s.timing.stats.unsynchronized = true, true, true
	s.timing.runningKnown = false
	s.timing.pits.unsynchronized = [3]bool{true, true, true}
	s.timing.control.unsynchronized = true
	s.series.unsynchronized, s.track.unsynchronized, s.Status.unsynchronized = true, true, true
	s.laps.unsynchronized, s.laps.active = true, false
	s.clock = s.clock.suspend()
	s.clock.unsynchronized = true
	s.weather = disconnectWeather(s.weather)
	s.radio = disconnectTeamRadio(s.radio)
	s.timing.cars.unsynchronized = true
}
func ReduceTick(s State, at time.Time) (State, Effects) {
	if !s.SessionInfo.IdentityAvailable || s.Origin.IsZero() {
		return s, Effects{}
	}
	o := sessionObservation{Identity: s.SessionInfo.Identity, observed: at, monotonic: at.Sub(s.Origin)}
	var laps, clock, timing Effects
	if s.SessionInfo.Synchronized {
		s.laps, laps = tickLapCount(s.laps, o)
		s.clock, clock = tickExtrapolatedClock(s.clock, o.Identity, at)
		s.weather = tickWeather(s.weather, o.monotonic)
	}
	if s.SessionInfo.Synchronized && s.drivers.synchronized || !s.timing.tracesEnded && !s.timing.finalised.IsZero() && o.monotonic >= s.timing.graceDeadline {
		s.timing, timing = tickTiming(s.timing, o)
	}
	r := Reduction{State: s}
	r.add(timing)
	return r.State, laps.append(clock).append(r.Effects)
}
