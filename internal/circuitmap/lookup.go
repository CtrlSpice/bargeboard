package circuitmap

import (
	"math"
	"math/big"
	"strconv"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

const (
	DefaultCornerRadiusMetres        = 75.0
	DefaultMarshalSectorRadiusMetres = 250.0
)

type LookupOptions struct {
	CornerRadiusMetres, MarshalSectorRadiusMetres float64
}

func DefaultLookupOptions() LookupOptions {
	return LookupOptions{DefaultCornerRadiusMetres, DefaultMarshalSectorRadiusMetres}
}

type Match struct {
	HasCorner            bool
	CornerNumber         int64
	CornerLabel          string
	CornerDistanceMetres float64
	HasMarshalSector     bool
	MarshalSector        int64
}

// decimalRat uses the shortest round-trip decimal and returns nil for non-finite inputs.
func decimalRat(v float64) *big.Rat {
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(v, 'g', -1, 64))
	return r
}

func nearest(points []landmark, x, y *big.Rat, radius float64) (landmark, float64) {
	var best landmark
	r := decimalRat(radius)
	if r == nil || r.Sign() < 0 {
		return best, 0
	}
	limit := r.Mul(r, r)
	var dx, dy, xx, yy big.Rat
	// Landmarks are sorted by number and letter. Keep the first equal distance.
	for _, p := range points {
		dx.Sub(x, p.x)
		dy.Sub(y, p.y)
		xx.Mul(&dx, &dx)
		yy.Mul(&dy, &dy)
		switch xx.Add(&xx, &yy).Cmp(limit) {
		case 0:
			if best.number != 0 {
				continue
			}
			fallthrough
		case -1:
			best = p
			limit.Set(&xx)
		}
	}
	if best.number == 0 {
		return best, 0
	}
	fx, _ := dx.Sub(x, best.x).Float64()
	fy, _ := dy.Sub(y, best.y).Float64()
	return best, math.Hypot(fx, fy)
}

func (m Map) Lookup(xMetres, yMetres float64, opts LookupOptions) Match {
	var match Match
	x, y := decimalRat(xMetres), decimalRat(yMetres)
	if !m.Valid() || x == nil || y == nil {
		return match
	}
	if p, d := nearest(m.corners, x, y, opts.CornerRadiusMetres); p.number != 0 {
		match.HasCorner = true
		match.CornerNumber = p.number
		match.CornerLabel = strconv.FormatInt(p.number, 10) + p.letter
		match.CornerDistanceMetres = d
	}
	if p, _ := nearest(m.sectors, x, y, opts.MarshalSectorRadiusMetres); p.number != 0 {
		match.HasMarshalSector = true
		match.MarshalSector = p.number
	}
	return match
}

func ApplyEventAttributes(attrs pcommon.Map, match Match) {
	if match.HasCorner {
		attrs.PutInt("f1.circuit.corner.number", match.CornerNumber)
		attrs.PutStr("f1.circuit.corner.label", match.CornerLabel)
		attrs.PutDouble("f1.circuit.corner.distance", match.CornerDistanceMetres)
	}
	if match.HasMarshalSector {
		attrs.PutInt("f1.circuit.marshal_sector", match.MarshalSector)
	}
}

func IsEligibleTraceEvent(eventName string) bool {
	switch eventName {
	case "f1.pit.entry", "f1.pit.stop", "f1.pit.exit", "f1.pit.visit.incomplete",
		"f1.position.changed", "f1.lap_time.deleted", "f1.lap_time.reinstated",
		"f1.personal_best", "f1.personal_best.revised", "f1.fastest_lap",
		"f1.race_control.driver_notice", "f1.blue_flag.shown":
		return true
	}
	return false
}

// EnrichEvent supplements an accepted event after Position has projected its sample.
func (m Map) EnrichEvent(eventName string, attrs pcommon.Map, opts LookupOptions) {
	if !IsEligibleTraceEvent(eventName) {
		return
	}
	position := func(key string) (float64, bool) {
		v, ok := attrs.Get(key)
		if !ok || v.Type() != pcommon.ValueTypeDouble {
			return 0, false
		}
		d := v.Double()
		return d, !math.IsNaN(d) && !math.IsInf(d, 0)
	}
	x, hasX := position("f1.position.x")
	y, hasY := position("f1.position.y")
	z, hasZ := position("f1.position.z")
	age, hasAge := position("f1.position.sample_age")
	if hasX && hasY && hasZ && hasAge && (x != 0 || y != 0 || z != 0) && age >= 0 && age <= 1 {
		ApplyEventAttributes(attrs, m.Lookup(x, y, opts))
	}
}
