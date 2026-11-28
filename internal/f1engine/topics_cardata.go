package f1engine

import (
	"cmp"
	"encoding/json"
	"maps"
	"math"
	"math/bits"
	"slices"
	"sort"
	"strconv"
	"time"
)

func deltaWindowBounds(sessionStart, t time.Time) (time.Time, time.Time) {
	end := t.UTC().Truncate(10 * time.Second)
	if end.Before(t) || t.Equal(sessionStart) {
		end = end.Add(10 * time.Second)
	}
	start := end.Add(-10 * time.Second)
	if start.Before(sessionStart) {
		start = sessionStart.UTC()
	}
	return start, end
}
func canonicalInteger(raw json.RawMessage) (int64, bool) {
	v, err := strconv.ParseInt(string(raw), 10, 64)
	return v, err == nil && decimal(v) == string(raw)
}

type positionSample struct {
	at        time.Time
	xyz       [3]int64
	available bool
	status    string
}
type positionState struct {
	history        map[int64][]positionSample
	unsynchronized bool
}
type positionFrame struct {
	at      time.Time
	drivers map[int64]positionSample
}

func omitPosition() positionState                      { return positionState{} }
func disconnectPosition(s positionState) positionState { s.unsynchronized = true; return s }

func parsePositionFrame(raw json.RawMessage, reg driverRegistryState) (positionFrame, bool, bool) {
	f := nestedObject(raw)
	at, ok := sessionUTC(f["Timestamp"])
	frame := positionFrame{at, map[int64]positionSample{}}
	keysOK, axesOK := ok, true
	seen := map[int64]bool{}
	err := VisitRawJSONObject(f["Entries"], func(key, v json.RawMessage) error {
		n, ok := parseCanonicalDriverNumber(sessionText(key))
		if !ok {
			keysOK = false
			return nil
		}
		if seen[n] {
			keysOK = false
		}
		seen[n] = true
		if _, ok := resolvedDriver(reg, n); !ok {
			if n < 241 || n > 243 {
				keysOK = false
			}
			return nil
		}
		entry := nestedObject(v)
		p := positionSample{at: at, available: true, status: sessionText(entry["Status"])}
		if p.status != "OnTrack" && p.status != "OffTrack" {
			p.status = "unknown"
		}
		for j, k := range []string{"X", "Y", "Z"} {
			var valid bool
			p.xyz[j], valid = canonicalInteger(entry[k])
			if !valid {
				p.available = false
				axesOK = false
			}
		}
		p.available = p.available && p.xyz != [3]int64{}
		frame.drivers[n] = p
		return nil
	})
	return frame, keysOK && err == nil && len(frame.drivers) == int(reg.count), axesOK
}
func reducePosition(s positionState, o sessionObservation, raw json.RawMessage, reg driverRegistryState) (positionState, error) {
	f, err := topicObject(raw)
	if err != nil {
		return s, err
	}
	if !reg.frozen {
		return s, nil
	}
	if !reg.synchronized {
		s.unsynchronized = true
		return s, nil
	}
	if s.unsynchronized && !o.snapshot() {
		return s, nil
	}
	a, ok := topicArray(f["Position"])
	if !ok {
		if o.snapshot() {
			s.unsynchronized = true
		}
		return s, nil
	}
	var frames []positionFrame
	for _, row := range a {
		frame, keys, axes := parsePositionFrame(row, reg)
		if (!keys || !axes) && o.snapshot() {
			s.unsynchronized = true
			return s, nil
		}
		if keys {
			frames = append(frames, frame)
		}
	}
	slices.SortStableFunc(frames, func(a, b positionFrame) int { return a.at.Compare(b.at) })
	if o.snapshot() {
		s = positionState{}
	}
	s.history = copyMap(s.history)
	for _, frame := range frames {
		for n, p := range frame.drivers {
			h := s.history[n]
			i := sort.Search(len(h), func(i int) bool { return !h[i].at.Before(p.at) })
			if i == 0 && len(h) == 16 && p.at.Before(h[0].at) {
				continue
			}
			h = slices.Clone(h)
			if i < len(h) && h[i].at.Equal(p.at) {
				h[i] = p
			} else {
				h = slices.Insert(h, i, p)
			}
			s.history[n] = h[max(0, len(h)-16):]
		}
	}
	return s, nil
}
func lookupDriverPosition(s positionState, n int64, t time.Time) ([4]keyValue, bool) {
	h := s.history[n]
	i := sort.Search(len(h), func(i int) bool { return h[i].at.After(t) })
	if s.unsynchronized || i == 0 {
		return [4]keyValue{}, false
	}
	p := h[i-1]
	age := t.Sub(p.at)
	if !p.available || age > time.Second {
		return [4]keyValue{}, false
	}
	return [4]keyValue{floatAttr("f1.position.x", float64(p.xyz[0])/10), floatAttr("f1.position.y", float64(p.xyz[1])/10), floatAttr("f1.position.z", float64(p.xyz[2])/10), floatAttr("f1.position.sample_age", age.Seconds())}, true
}

var fieldInstruments = [...]struct {
	code, name, unit string
	bounds           []float64
}{
	{"2", "speed", "km/h", nil}, {"0", "engine_speed", "{revolution}/min", nil},
	{"3", "gear", "{gear}", []float64{0.5, 1.5, 2.5, 3.5, 4.5, 5.5, 6.5, 7.5, 8.5}},
	{"4", "throttle", "1", []float64{0, 0.05, 0.10, 0.20, 0.30, 0.40, 0.50, 0.60, 0.70, 0.80, 0.90, 0.95, 0.99, 1}},
	{"45", "drs.active_time_ratio", "1", nil},
}

func expBucketIndex(v int64) int32 {
	if v&(v-1) == 0 {
		return int32(bits.TrailingZeros64(uint64(v))*16 - 1)
	}
	return int32(math.Ceil(math.Log2(float64(v))*16)) - 1
}
func addExpBucket(m *metricEffect, v int64) {
	if v == 0 {
		m.expZeroCount++
		return
	}
	i := expBucketIndex(v)
	if len(m.expPositiveBuckets) == 0 {
		m.expPositiveOffset = i
	}
	if i < m.expPositiveOffset {
		m.expPositiveBuckets = append(make([]uint64, m.expPositiveOffset-i), m.expPositiveBuckets...)
		m.expPositiveOffset = i
	}
	j := int(i - m.expPositiveOffset)
	if j >= len(m.expPositiveBuckets) {
		m.expPositiveBuckets = append(m.expPositiveBuckets, make([]uint64, j+1-len(m.expPositiveBuckets))...)
	}
	m.expPositiveBuckets[j]++
}

type carValues struct {
	v     [5]int64
	valid [5]bool
}
type carFrame struct {
	at            time.Time
	cars          map[int64]carValues
	unknownDriver bool
}

func parseCarFrame(raw json.RawMessage, reg driverRegistryState) (carFrame, bool, bool) {
	f := nestedObject(raw)
	at, ok := sessionUTC(f["Utc"])
	cars := nestedObject(f["Cars"])
	frame := carFrame{at: at, cars: map[int64]carValues{}}
	coherent := true
	for k, row := range cars {
		n, valid := parseCanonicalDriverNumber(k)
		if _, known := resolvedDriver(reg, n); !valid || !known {
			frame.unknownDriver = true
			continue
		}
		ch := nestedObject(nestedObject(row)["Channels"])
		if ch == nil {
			coherent = false
			continue
		}
		var values carValues
		for i, field := range fieldInstruments {
			if raw, has := ch[field.code]; has {
				v, ok := canonicalInteger(raw)
				values.v[i], values.valid[i] = v, ok && (i == 4 || v >= 0) && (i != 2 || v <= 8) && (i != 3 || v <= 100)
				if !values.valid[i] {
					coherent = false
				}
			}
		}
		frame.cars[n] = values
	}
	return frame, ok && cars != nil, coherent
}

// One sample can affect at most two whole-second ticks. Keep only the latest
// candidate per field at each tick so a long source gap costs no extra work.
type carTickKey struct {
	second time.Time
	driver int64
}
type carTick struct {
	owner observationOwner
	carValues
	latest [5]time.Time
}
type drsSample struct {
	at            time.Time
	known, active bool
}
type drsDuration struct{ eligibleNS, knownNS, activeNS int64 }

func (d *drsDuration) add(sample drsSample, dt int64) {
	d.eligibleNS += dt
	if sample.known {
		d.knownNS += dt
	}
	if sample.active {
		d.activeNS += dt
	}
}
func (d drsDuration) ratio(denominator int64) (float64, bool) {
	if denominator <= 0 || d.knownNS == 0 || d.knownNS < denominator-denominator/10 {
		return 0, false
	}
	return float64(d.activeNS) / float64(d.knownNS), true
}

type drsInterval struct {
	sample         drsSample
	from, to, next time.Time
	driver         int64
}
type carDataState struct {
	ticks                           map[carTickKey]carTick
	recent                          map[int64][]drsSample
	drs                             map[time.Time][]drsInterval
	watermark, snapshotThrough      time.Time
	unsynchronized, invalidSnapshot bool
	lateSample, unknownDriver       bool // bounded diagnostics for the last update
}

func lapDRSRatio(s carDataState, driver int64, lapStart, lapEnd time.Time) (float64, bool) {
	if s.unsynchronized || !lapEnd.After(lapStart) {
		return 0, false
	}
	var d drsDuration
	h := s.recent[driver]
	for i := 1; i < len(h); i++ {
		a, b := h[i-1], h[i]
		dt := b.at.Sub(a.at)
		if !a.at.Before(lapStart) && !b.at.After(lapEnd) && dt > 0 && dt <= time.Second {
			d.add(a, int64(dt))
		}
	}
	return d.ratio(int64(lapEnd.Sub(lapStart)))
}

func (s *carDataState) sampleDRS(frame carFrame, start time.Time, snapshot bool) {
	for n, v := range frame.cars {
		if !slices.Contains(v.valid[:], true) {
			continue
		}
		sample := drsSample{at: frame.at}
		if v.valid[4] {
			switch v.v[4] {
			case 10, 12, 14:
				sample.active = true
				fallthrough
			case 0, 1, 8:
				sample.known = true
			}
		}
		h := s.recent[n]
		if snapshot {
			s.recent[n] = []drsSample{sample}
			continue
		}
		if !sample.at.After(s.snapshotThrough) {
			continue
		}
		i := sort.Search(len(h), func(i int) bool { return !h[i].at.Before(sample.at) })
		// Replace the previous interval when a sample arrives out of order.
		if i < len(h) {
			for end, intervals := range s.drs {
				if end.Before(sample.at.Add(-time.Second)) || end.Add(-10*time.Second).After(h[i].at.Add(time.Second)) {
					continue
				}
				s.drs[end] = slices.DeleteFunc(slices.Clone(intervals), func(x drsInterval) bool {
					return x.driver == n && x.next == h[i].at && x.sample.at.Before(sample.at) ||
						x.driver == n && x.sample.at == sample.at
				})
			}
		}
		h = slices.Clip(h)
		if i < len(h) && h[i].at.Equal(sample.at) {
			h = slices.Clone(h)
			h[i] = sample
		} else {
			h = slices.Insert(h, i, sample)
		}
		for j := max(1, i); j <= min(i+1, len(h)-1); j++ {
			a, b := h[j-1], h[j]
			if b.at.Sub(a.at) > time.Second {
				continue
			}
			from := a.at
			if from.Before(start) {
				from = start
			}
			if from.Before(s.snapshotThrough) {
				from = s.snapshotThrough
			}
			for from.Before(b.at) {
				end := from.Truncate(10 * time.Second).Add(10 * time.Second)
				to := b.at
				if to.After(end) {
					to = end
				}
				if !s.watermark.After(end) {
					s.drs[end] = append(slices.Clip(s.drs[end]), drsInterval{sample: a, from: from, to: to, driver: n, next: b.at})
				}
				from = to
			}
		}
		s.recent[n] = h[max(0, len(h)-2048):]
	}
}

func (s *carDataState) sample(frame carFrame, start time.Time, reg driverRegistryState, drivers map[int64]timingDriver) {
	if frame.at.Before(start) {
		return
	}
	t := frame.at.Truncate(time.Second)
	if t.Before(frame.at) {
		t = t.Add(time.Second)
	}
	for ; t.Sub(frame.at) <= time.Second; t = t.Add(time.Second) {
		if t.Before(start) || !t.After(s.snapshotThrough) {
			continue
		}
		_, end := deltaWindowBounds(start, t)
		if s.watermark.After(end) {
			continue
		}
		for n, v := range frame.cars {
			k := carTickKey{t, n}
			tick := s.ticks[k]
			entry, _ := resolvedDriver(reg, n)
			tick.owner.driver, tick.owner.acronym = n, entry.tla
			tick.owner = tick.owner.at(drivers[n], t)
			for i, valid := range v.valid[:4] {
				if valid && !frame.at.Before(tick.latest[i]) {
					tick.latest[i], tick.valid[i], tick.v[i] = frame.at, valid, v.v[i]
				}
			}
			s.ticks[k] = tick
		}
	}
}

type fieldWindow struct {
	metrics [4]metricEffect
}

func (w *fieldWindow) add(tick carTick, id SessionInfoIdentity, at, start, end time.Time) {
	for i := range w.metrics {
		if !tick.valid[i] {
			continue
		}
		f := fieldInstruments[i]
		m := &w.metrics[i]
		v := float64(tick.v[i])
		if i == 3 {
			v /= 100
		}
		if m.count == 0 {
			*m = metricEffect{name: "f1.field." + f.name, unit: f.unit, dataSource: "livetiming", identity: id, startTime: start, timestamp: end, hasMinMax: true, min: v, max: v}
			if f.bounds == nil {
				m.kind, m.expScale = metricExpHistogramDelta, 4
			} else {
				m.kind, m.bounds, m.bucketCounts = metricExplicitHistogramDelta, slices.Clone(f.bounds), make([]uint64, len(f.bounds)+1)
			}
		}
		if f.bounds == nil {
			addExpBucket(m, tick.v[i])
		} else {
			m.bucketCounts[sort.SearchFloat64s(f.bounds, v)]++
		}
		m.min = min(m.min, v)
		m.max = max(m.max, v)
		x := tick.owner.exemplar(id, at)
		x.floatVal, x.isInt, x.intVal = v, i != 3, tick.v[i]
		m.selectExemplar(x)
		m.count++
		m.sum += v
	}
}
func (w fieldWindow) emit(e *Effects, id SessionInfoIdentity, end time.Time) {
	for _, m := range w.metrics {
		if m.count > 0 {
			e.metrics = append(e.metrics, m)
		}
	}
}
func finalizeCarTicks(s *carDataState, id SessionInfoIdentity, start, watermark time.Time, terminal bool) Effects {
	var keys []carTickKey
	for k := range s.ticks {
		_, end := deltaWindowBounds(start, k.second)
		if !k.second.After(s.watermark) && !k.second.After(watermark) && (terminal || watermark.After(end)) {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b carTickKey) int {
		if c := a.second.Compare(b.second); c != 0 {
			return c
		}
		return cmp.Compare(a.driver, b.driver)
	})
	var e Effects
	var w fieldWindow
	var prev time.Time
	for _, k := range keys {
		a, b := deltaWindowBounds(start, k.second)
		if b.After(watermark) {
			b = watermark
		}
		if b != prev {
			w.emit(&e, id, prev)
			w = fieldWindow{}
			prev = b
		}
		if b.After(a) {
			w.add(s.ticks[k], id, k.second, a, b)
		}
		delete(s.ticks, k)
	}
	w.emit(&e, id, prev)
	ends := slices.Collect(maps.Keys(s.drs))
	slices.SortFunc(ends, time.Time.Compare)
	for _, end := range ends {
		if !terminal && !watermark.After(end) {
			continue
		}
		stop := end
		if stop.After(watermark) {
			stop = watermark
		}
		if stop.After(s.watermark) {
			stop = s.watermark
		}
		var d drsDuration
		for _, x := range s.drs[end] {
			to := x.to
			if to.After(stop) {
				to = stop
			}
			if to.After(x.from) {
				d.add(x.sample, int64(to.Sub(x.from)))
			}
		}
		if ratio, ok := d.ratio(d.eligibleNS); ok {
			e.metrics = append(e.metrics, metricEffect{name: "f1.field.drs.active_time_ratio", unit: "1", kind: metricDoubleGauge, identity: id, dataSource: "livetiming", timestamp: stop, floatVal: ratio})
		}
		delete(s.drs, end)
	}
	return e
}
func reduceCarData(s carDataState, o sessionObservation, raw json.RawMessage, reg driverRegistryState, start time.Time, drivers map[int64]timingDriver) (carDataState, Effects, error) {
	f, err := topicObject(raw)
	var e Effects
	if err != nil {
		return s, e, err
	}
	s.lateSample, s.unknownDriver = false, false
	if !reg.frozen || start.IsZero() || s.unsynchronized && !o.snapshot() {
		return s, e, nil
	}
	if !reg.synchronized {
		s.unsynchronized = true
		return s, e, nil
	}
	a, ok := topicArray(f["Entries"])
	if !ok {
		if o.snapshot() {
			s.unsynchronized, s.invalidSnapshot = true, true
		}
		return s, e, nil
	}
	var frames []carFrame
	for _, row := range a {
		frame, ok, coherent := parseCarFrame(row, reg)
		s.unknownDriver = s.unknownDriver || frame.unknownDriver
		if o.snapshot() && (!ok || !coherent) {
			s.unsynchronized, s.invalidSnapshot = true, true
			return s, e, nil
		}
		if ok {
			frames = append(frames, frame)
		}
	}
	slices.SortStableFunc(frames, func(a, b carFrame) int { return a.at.Compare(b.at) })
	s.ticks = copyMap(s.ticks)
	s.drs = copyMap(s.drs)
	s.recent = copyMap(s.recent)
	if o.snapshot() {
		clear(s.recent)
		s.unsynchronized, s.invalidSnapshot = false, false
		for k := range s.ticks {
			if k.second.After(s.watermark) {
				delete(s.ticks, k)
			}
		}
		if len(frames) > 0 && frames[len(frames)-1].at.After(s.watermark) {
			s.watermark = frames[len(frames)-1].at
		}
		s.snapshotThrough = s.watermark
	}
	for _, frame := range frames {
		_, end := deltaWindowBounds(start, frame.at)
		if !o.snapshot() && s.watermark.After(end) {
			s.lateSample = true
			continue
		}
		s.sample(frame, start, reg, drivers)
		s.sampleDRS(frame, start, o.snapshot())
		if frame.at.After(s.watermark) {
			s.watermark = frame.at
		}
	}
	if !o.snapshot() {
		e = finalizeCarTicks(&s, o.Identity, start, s.watermark, false)
	}
	return s, e, nil
}
func flushCarData(s carDataState, id SessionInfoIdentity, start, end time.Time) (carDataState, Effects) {
	if s.invalidSnapshot {
		return s, Effects{}
	}
	if end.IsZero() {
		end = s.watermark
	}
	s.ticks = maps.Clone(s.ticks)
	s.drs = maps.Clone(s.drs)
	e := finalizeCarTicks(&s, id, start, end, true)
	s.ticks = nil
	return s, e
}

func omitCarData(s carDataState) carDataState {
	ticks := maps.Clone(s.ticks)
	maps.DeleteFunc(ticks, func(k carTickKey, _ carTick) bool { return k.second.After(s.watermark) })
	return carDataState{ticks: ticks, drs: s.drs, watermark: s.watermark, snapshotThrough: s.watermark}
}
