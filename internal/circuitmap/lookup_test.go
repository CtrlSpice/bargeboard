package circuitmap

import (
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/circuit.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExactLandmarkDistances(t *testing.T) {
	for _, tc := range []struct {
		points       string
		x, y, radius float64
		want         Match
	}{
		{`{"number":1,"trackPosition":{"x":533,"y":0}}`, float64(1283) / 10, 0, 75, Match{HasCorner: true, CornerNumber: 1, CornerLabel: "1", CornerDistanceMetres: 75}},
		{`{"number":2,"trackPosition":{"x":2,"y":0}},{"number":1,"trackPosition":{"x":4,"y":0}}`, float64(3) / 10, 0, 1, Match{HasCorner: true, CornerNumber: 1, CornerLabel: "1", CornerDistanceMetres: 0.1}},
		{`{"number":1,"trackPosition":{"x":0,"y":6}}`, 0, 0.3, 0.3, Match{HasCorner: true, CornerNumber: 1, CornerLabel: "1", CornerDistanceMetres: 0.3}},
		{`{"number":1,"trackPosition":{"x":0,"y":0}}`, math.SmallestNonzeroFloat64, 0, 0, Match{}},
	} {
		m, err := Parse([]byte(strings.Replace(minimal, point, tc.points, 1)), 91, 2026)
		if err != nil {
			t.Fatal(err)
		}
		opts := LookupOptions{CornerRadiusMetres: tc.radius}
		if got := m.Lookup(tc.x, tc.y, opts); got != tc.want {
			t.Errorf("Lookup(%g,%g), radius %g: %+v; want %+v", tc.x, tc.y, tc.radius, got, tc.want)
		}
		a := pcommon.NewMap()
		a.PutDouble("f1.position.x", tc.x)
		a.PutDouble("f1.position.y", tc.y)
		a.PutDouble("f1.position.z", 1)
		a.PutDouble("f1.position.sample_age", 0)
		want := a.AsRaw()
		if tc.want.HasCorner {
			want["f1.circuit.corner.number"] = tc.want.CornerNumber
			want["f1.circuit.corner.label"] = tc.want.CornerLabel
			want["f1.circuit.corner.distance"] = tc.want.CornerDistanceMetres
		}
		m.EnrichEvent("f1.pit.entry", a, opts)
		if got := a.AsRaw(); !reflect.DeepEqual(got, want) {
			t.Errorf("EnrichEvent at (%g,%g), radius %g: %v; want %v", tc.x, tc.y, tc.radius, got, want)
		}
	}
}

func testMap(t *testing.T) Map {
	t.Helper()
	m, err := Parse(fixture(t), 91, 2026)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLookup(t *testing.T) {
	m := testMap(t)
	if !m.Valid() || m.name != "Test 🚀" || m.rotation != 90 {
		t.Fatal("lost map metadata")
	}
	for _, tc := range []struct {
		x, y float64
		want Match
	}{
		{0, 0, Match{true, 2, "2A", 10, true, 3}},
		{6, -8, Match{true, 2, "2B", 0, true, 12}},
		{1075, 0, Match{HasCorner: true, CornerNumber: 1, CornerLabel: "1", CornerDistanceMetres: 75}},
		{1075.001, 0, Match{}},
		{-280, 40, Match{HasMarshalSector: true, MarshalSector: 3}},
		{-280.001, 40, Match{}},
		{math.NaN(), 0, Match{}}, {0, math.NaN(), Match{}}, {0, math.Inf(1), Match{}}, {math.Inf(-1), 0, Match{}},
	} {
		if got := m.Lookup(tc.x, tc.y, DefaultLookupOptions()); got != tc.want {
			t.Errorf("(%g,%g): %+v; want %+v", tc.x, tc.y, got, tc.want)
		}
	}
	if got := m.Lookup(6, -8, LookupOptions{}); got != (Match{HasCorner: true, CornerNumber: 2, CornerLabel: "2B"}) {
		t.Errorf("zero radii: %+v", got)
	}
	if got := (Map{}).Lookup(0, 0, DefaultLookupOptions()); got != (Match{}) {
		t.Errorf("zero map: %+v", got)
	}
}

func TestEventEnrichment(t *testing.T) {
	m := testMap(t)
	keys := []string{"f1.position.x", "f1.position.y", "f1.position.z", "f1.position.sample_age"}
	base := func() pcommon.Map {
		a := pcommon.NewMap()
		for _, k := range keys {
			a.PutDouble(k, 0)
		}
		a.PutDouble(keys[2], 1)
		a.PutDouble(keys[3], 1)
		a.PutStr("existing", "keep")
		return a
	}
	for _, name := range []string{"f1.pit.entry", "f1.pit.stop", "f1.pit.exit", "f1.pit.visit.incomplete", "f1.position.changed", "f1.lap_time.deleted", "f1.lap_time.reinstated", "f1.personal_best", "f1.personal_best.revised", "f1.fastest_lap", "f1.race_control.driver_notice", "f1.blue_flag.shown"} {
		a := base()
		want := a.AsRaw()
		want["f1.circuit.corner.number"] = int64(2)
		want["f1.circuit.corner.label"] = "2A"
		want["f1.circuit.corner.distance"] = 10.0
		want["f1.circuit.marshal_sector"] = int64(3)
		m.EnrichEvent(name, a, DefaultLookupOptions())
		if !reflect.DeepEqual(a.AsRaw(), want) {
			t.Errorf("%s: %v", name, a.AsRaw())
		}
	}
	unchanged := func(name string, a pcommon.Map) {
		t.Helper()
		want := a.AsRaw()
		m.EnrichEvent(name, a, DefaultLookupOptions())
		if !reflect.DeepEqual(a.AsRaw(), want) {
			t.Errorf("enriched %q: %v", name, a.AsRaw())
		}
	}
	for _, k := range keys {
		for _, d := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			a := base()
			a.PutDouble(k, d)
			before := a.Len()
			m.EnrichEvent("f1.pit.entry", a, DefaultLookupOptions())
			if a.Len() != before {
				t.Errorf("enriched %s=%g", k, d)
			}
		}
		a := base()
		a.Remove(k)
		unchanged("f1.pit.entry", a)
		a.PutStr(k, "0")
		unchanged("f1.pit.entry", a)
	}
	a := base()
	a.PutDouble(keys[2], 0)
	unchanged("f1.pit.entry", a)
	for _, age := range []float64{-math.SmallestNonzeroFloat64, math.Nextafter(1, 2)} {
		a := base()
		a.PutDouble(keys[3], age)
		unchanged("f1.pit.entry", a)
	}
	for _, name := range []string{"", "f1.track_status.changed", "f1.session.status", "f1.fastest_lap ", "F1.Pit.Entry"} {
		unchanged(name, base())
	}
}
