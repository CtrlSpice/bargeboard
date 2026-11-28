package f1engine

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func fieldRoster() driverRegistryState {
	r := driverRegistryState{count: 2, frozen: true, synchronized: true}
	r.drivers[0] = driverRegistryEntry{number: 1, tla: "ABC"}
	r.drivers[1] = driverRegistryEntry{number: 7, tla: "XYZ"}
	return r
}
func fieldTime(sec int64) time.Time { return time.Unix(sec, 0).UTC() }
func positionRow(sec int64, first string) string {
	return fmt.Sprintf(`{"Timestamp":%q,"Entries":{"241":{},"1":%s,"7":{"X":1,"Y":0,"Z":-3,"Status":42}}}`, fieldTime(sec).Format(time.RFC3339), first)
}
func TestCoordinateHistory(t *testing.T) {
	reg, o := fieldRoster(), sessionFixture()
	reduce := func(s positionState, rows ...string) positionState {
		t.Helper()
		next, err := reducePosition(s, o, []byte(`{"Position":[`+strings.Join(rows, ",")+`]}`), reg)
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	const good = `{"X":-13,"Y":0,"Z":37}`
	s := reduce(positionState{}, positionRow(4, `{"X":0,"Y":0,"Z":0}`), positionRow(2, good), positionRow(5, good), positionRow(5, `{"X":-0,"Y":1,"Z":3}`))
	for _, tc := range []struct {
		at        time.Time
		available bool
	}{{fieldTime(1), false}, {fieldTime(2), true}, {fieldTime(3), true}, {fieldTime(3).Add(1), false}, {fieldTime(4), false}, {fieldTime(5), false}} {
		a, ok := lookupDriverPosition(s, 1, tc.at)
		if ok != tc.available {
			t.Fatal(tc, a)
		}
		if ok && a != [4]keyValue{floatAttr("f1.position.x", -1.3), floatAttr("f1.position.y", 0), floatAttr("f1.position.z", 3.7), floatAttr("f1.position.sample_age", tc.at.Sub(fieldTime(2)).Seconds())} {
			t.Fatal(a)
		}
	}
	if _, ok := lookupDriverPosition(s, 7, fieldTime(5)); !ok {
		t.Fatal("invalid sibling crossed driver boundary")
	}
	for _, bad := range []string{`"01":{}`, `"8":{}`, `"1":{}`, `"241":{}`} {
		row := strings.Replace(positionRow(6, good), `"241":{}`, `"241":{},`+bad, 1)
		next := reduce(s, row)
		if !reflect.DeepEqual(next, s) {
			t.Fatal(bad)
		}
	}
	for sec := int64(38); sec >= 17; sec-- {
		s = reduce(s, positionRow(sec, good))
	}
	if h := s.history[1]; len(h) != 16 || h[0].at != fieldTime(23) {
		t.Fatal(h)
	}
	o.source = Snapshot
	bad := reduce(s, positionRow(40, good), positionRow(41, `{"X":"1","Y":2,"Z":3}`))
	if _, ok := lookupDriverPosition(bad, 1, fieldTime(38)); ok || !reflect.DeepEqual(bad.history, s.history) {
		t.Fatal("invalid snapshot partially applied")
	}
	if empty := reduce(bad); empty.unsynchronized || len(empty.history) != 0 {
		t.Fatal(empty)
	}
}

func carRow(t time.Time, cars string) string {
	return fmt.Sprintf(`{"Utc":%q,"Cars":{%s}}`, t.Format(time.RFC3339Nano), cars)
}
func fieldCar(n int64, ch string) string { return fmt.Sprintf(`"%d":{"Channels":{%s}}`, n, ch) }
func carReducer(t *testing.T, o sessionObservation, start time.Time) func(carDataState, ...string) (carDataState, Effects) {
	return func(s carDataState, rows ...string) (carDataState, Effects) {
		t.Helper()
		next, e, err := reduceCarData(s, o, []byte(`{"Entries":[`+strings.Join(rows, ",")+`]}`), fieldRoster(), start, nil)
		if err != nil {
			t.Fatal(err)
		}
		return next, e
	}
}
func TestFieldWindowPopulation(t *testing.T) {
	start := fieldTime(7).Add(500 * time.Millisecond)
	reduce := carReducer(t, sessionFixture(), start)
	v := fieldCar(1, `"2":16,"0":0,"3":0,"4":5,"45":12`)
	s, e := reduce(carDataState{}, carRow(fieldTime(10), fieldCar(1, `"2":31,"0":1024,"3":8,"4":100,"45":8`)), carRow(fieldTime(8), v), carRow(fieldTime(8), fieldCar(7, `"2":0,"3":9,"4":104,"45":10`)))
	if !e.empty() {
		t.Fatal("watermark equal to end finalized", e)
	}
	next, e := reduce(s, carRow(fieldTime(10).Add(1), ""))
	if len(e.metrics) != 4 || len(next.ticks) >= len(s.ticks) {
		t.Fatal(e)
	}
	for _, m := range e.metrics[:4] {
		if m.startTime != start || m.timestamp != fieldTime(10) || !m.hasMinMax || m.identity != sessionFixture().Identity || len(m.attrs) != 0 {
			t.Fatal(m)
		}
	}
	speed, rpm, gear, throttle := e.metrics[0], e.metrics[1], e.metrics[2], e.metrics[3]
	for i, m := range e.metrics[:4] {
		if len(m.exemplars) != 2 {
			t.Fatal(m.name, m.exemplars)
		}
		for j, x := range m.exemplars {
			driver := int64(1)
			if i == 0 && j == 0 {
				driver = 7
			}
			if x.traceID != DeterministicTraceID(m.identity, driver) || x.spanID != DeterministicRootSpanID(m.identity, driver) || x.isInt != (i != 3) || x.timestamp != []time.Time{fieldTime(8), fieldTime(10)}[j] {
				t.Fatal(m.name, x)
			}
			v := x.floatVal
			if x.isInt {
				v = float64(x.intVal)
			}
			if v != []float64{m.min, m.max}[j] {
				t.Fatal(m.name, v)
			}
		}
	}
	if speed.name != "f1.field.speed" || speed.unit != "km/h" || speed.count != 5 || speed.sum != 63 || speed.min != 0 || speed.max != 31 || speed.kind != metricExpHistogramDelta || speed.expScale != 4 || speed.expZeroCount != 2 || speed.expPositiveOffset != 63 || speed.expPositiveBuckets[0] != 2 || speed.expPositiveBuckets[16] != 1 {
		t.Fatal(speed)
	}
	if rpm.name != "f1.field.engine_speed" || rpm.unit != "{revolution}/min" || rpm.count != 3 || rpm.expZeroCount != 2 || rpm.expPositiveOffset != 159 || !slices.Equal(rpm.expPositiveBuckets, []uint64{1}) {
		t.Fatal(rpm)
	}
	if gear.unit != "{gear}" || gear.kind != metricExplicitHistogramDelta || !slices.Equal(gear.bucketCounts, []uint64{2, 0, 0, 0, 0, 0, 0, 0, 1, 0}) {
		t.Fatal(gear)
	}
	if throttle.sum != 1.1 || throttle.count != 3 || !slices.Equal(throttle.bucketCounts, []uint64{0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0}) {
		t.Fatal(throttle)
	}
	next, e = reduce(next, carRow(fieldTime(10), v))
	if !e.empty() || !next.lateSample {
		t.Fatal("late sample accepted")
	}
	next, _ = reduce(next, carRow(fieldTime(11), ""))
	_, e = flushCarData(next, sessionFixture().Identity, start, fieldTime(11))
	if len(e.metrics) != 4 || e.metrics[0].count != 1 || e.metrics[0].sum != 31 || e.metrics[0].timestamp != fieldTime(11) {
		t.Fatal(e)
	}
}
func TestFieldSnapshotAndCoverage(t *testing.T) {
	start := fieldTime(0)
	o := sessionFixture()
	feed := carReducer(t, o, start)
	o.source = Snapshot
	snap := carReducer(t, o, start)
	s, e := snap(carDataState{}, carRow(fieldTime(4), fieldCar(1, `"2":256,"45":12`)))
	if !e.empty() {
		t.Fatal(e)
	}
	s, e = feed(s, carRow(fieldTime(6), fieldCar(1, `"2":1`)), carRow(fieldTime(6), fieldCar(1, `"2":2,"45":7`)), carRow(fieldTime(20), fieldCar(99, `"2":999`)))
	if !s.unknownDriver || len(e.metrics) != 1 || e.metrics[0].sum != 260 || e.metrics[0].count != 3 {
		t.Fatal(e)
	}
	s, e = snap(s, carRow(fieldTime(21), fieldCar(1, `"2":"12"`)))
	if !s.unsynchronized || !e.empty() || s.watermark != fieldTime(20) {
		t.Fatal(s, e)
	}
	s, e = feed(s, carRow(fieldTime(30), fieldCar(1, `"2":128`)))
	if !e.empty() || s.watermark != fieldTime(20) {
		t.Fatal(s, e)
	}
	s, e = snap(s)
	if !e.empty() || s.unsynchronized {
		t.Fatal(s, e)
	}
	for _, tc := range []struct {
		codes []string
		want  float64
	}{{[]string{"0", "1", "8", "10", "12", "14", "7"}, -1}, {[]string{"0", "1", "8", "10", "12", "14", "10", "0", "7", "1"}, 4.0 / 9}} {
		rows := []string{}
		for i, code := range tc.codes {
			rows = append(rows, carRow(fieldTime(int64(i+40)), fieldCar(1, `"45":`+code)))
		}
		rows = append(rows, carRow(fieldTime(int64(len(tc.codes)+40)), fieldCar(1, `"45":0`)))
		state, _ := feed(s, rows...)
		_, e := flushCarData(state, o.Identity, start, time.Time{})
		if tc.want < 0 {
			if !e.empty() {
				t.Fatal(e)
			}
		} else if len(e.metrics) != 1 || e.metrics[0].floatVal != tc.want {
			t.Fatal(e)
		}
	}
}
func TestDeltaBoundsAndScale(t *testing.T) {
	for _, tc := range [][4]int64{{0, 0, 0, 10}, {3, 3, 3, 10}, {3, 10, 3, 10}, {3, 11, 10, 20}, {-4, -1, -4, 0}, {-10, -10, -10, 0}} {
		a, b := deltaWindowBounds(fieldTime(tc[0]), fieldTime(tc[1]))
		if a != fieldTime(tc[2]) || b != fieldTime(tc[3]) {
			t.Fatal(tc, a, b)
		}
	}
	for p := 0; p <= 62; p++ {
		if i := expBucketIndex(1 << p); i != int32(16*p-1) {
			t.Fatal(p, i)
		}
	}
}

func TestDRSIrregularIntervalsAndTerminalClip(t *testing.T) {
	at := func(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
	feed := carReducer(t, sessionFixture(), at(9600))
	row := func(ms int64, ch string) string { return carRow(at(ms), fieldCar(1, ch)) }
	s, _ := feed(carDataState{}, row(9400, `"45":12`), row(9800, `"45":0`))
	s, e := feed(s, row(10300, `"45":14`), row(10600, `"2":20`), row(10650, `"45":-1`), row(10700, `"45":1`), row(11000, `"45":1`))
	check := func(e Effects, ms int64, want float64) {
		t.Helper()
		for _, m := range e.metrics {
			if m.name == "f1.field.drs.active_time_ratio" {
				if want < 0 || m.timestamp != at(ms) || m.floatVal != want || m.unit != "1" || m.kind != metricDoubleGauge {
					t.Fatal(m)
				}
				return
			}
		}
		if want >= 0 {
			t.Fatal("missing DRS", e)
		}
	}
	check(e, 10000, .5)
	_, e = flushCarData(s, sessionFixture().Identity, at(9600), at(10500))
	check(e, 10500, .4)
	_, e = flushCarData(s, sessionFixture().Identity, at(9600), at(10999))
	check(e, 10999, -1)
	_, e = flushCarData(s, sessionFixture().Identity, at(9600), at(12000))
	check(e, 11000, 1.0/3)
}

func TestDRSSnapshotAnchorsOnlyPostSnapshotIntervals(t *testing.T) {
	o := sessionFixture()
	feed := carReducer(t, o, fieldTime(1))
	o.source = Snapshot
	snap := carReducer(t, o, fieldTime(1))
	row := func(sec int64, ch string) string { return carRow(fieldTime(sec), fieldCar(1, ch)) }
	s, _ := feed(carDataState{}, row(3, `"45":12`))
	s, e := snap(s, row(4, `"45":12`), row(5, `"45":8`))
	if !e.empty() {
		t.Fatal(e)
	}
	s, _ = feed(s, row(6, `"45":12`))
	if v, ok := lapDRSRatio(s, 1, fieldTime(5), fieldTime(6)); !ok || v != 0 {
		t.Fatal(v, ok)
	}
	for _, state := range []carDataState{s, omitCarData(s)} {
		if _, ok := lapDRSRatio(state, 1, fieldTime(3), fieldTime(6)); ok {
			t.Fatal("snapshot bridged discontinuity")
		}
	}
	_, e = flushCarData(s, o.Identity, fieldTime(1), fieldTime(6))
	if len(e.metrics) != 1 || e.metrics[0].floatVal != 0 {
		t.Fatal(e)
	}
}

func TestLapDRSCoverageNanosecondBoundary(t *testing.T) {
	start := fieldTime(1)
	for _, tc := range []struct {
		dt    time.Duration
		valid bool
	}{{900 * time.Millisecond, true}, {900*time.Millisecond - 1, false}, {time.Second + 1, false}} {
		state := carDataState{recent: map[int64][]drsSample{1: {
			{start.Add(-1), true, false}, {start, true, true}, {start.Add(tc.dt), false, false}, {start.Add(time.Second + 2), true, true},
		}}}
		v, ok := lapDRSRatio(state, 1, start, start.Add(time.Second))
		if ok != tc.valid || ok && v != 1 {
			t.Fatal(tc, v, ok)
		}
	}
}

func TestTerminalFlushRespectsSourceWatermark(t *testing.T) {
	start := fieldTime(3)
	reduce := carReducer(t, sessionFixture(), start)
	s, e := reduce(carDataState{}, carRow(fieldTime(4), fieldCar(1, `"2":64`)))
	if !e.empty() {
		t.Fatal(e)
	}
	for _, end := range []time.Time{{}, fieldTime(8)} {
		flushed, e := flushCarData(s, sessionFixture().Identity, start, end)
		want := end
		if want.IsZero() {
			want = fieldTime(4)
		}
		if len(e.metrics) != 1 || e.metrics[0].count != 1 || e.metrics[0].timestamp != want || e.metrics[0].startTime != start || len(flushed.ticks) != 0 {
			t.Fatal(e)
		}
	}
	s, _ = reduce(carDataState{}, carRow(start, fieldCar(1, `"2":0`)))
	_, e = flushCarData(s, sessionFixture().Identity, start, start)
	if !e.empty() {
		t.Fatal("zero-length window", e)
	}
}
