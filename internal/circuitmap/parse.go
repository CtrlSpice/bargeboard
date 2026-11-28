// Package circuitmap matches track-local positions to MultiViewer circuit landmarks.
package circuitmap

import (
	"cmp"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"strconv"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

const maxBodyBytes = 256 << 10

var errMap = errors.New("invalid circuit map")

type landmark struct {
	number int64
	letter string
	x, y   *big.Rat // metres, in the unrotated track-local frame
}

// Map holds immutable landmarks. Its zero value is unavailable.
type Map struct {
	corners, sectors []landmark
	name             string
	rotation         float64
	unicodeIssues    bool
}

func (m Map) Valid() bool { return len(m.corners) != 0 }

// HasUnicodeIssues reports malformed Unicode scalars anywhere in the parsed payload.
func (m Map) HasUnicodeIssues() bool { return m.unicodeIssues }

func validID(key, year int64) bool { return key > 0 && year >= 1000 && year <= 9999 }

// fields retains only recognized members, rejecting a second spelling of the same key.
func fields(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(keys))
	err := f1id.VisitObject(raw, func(key string, _, value json.RawMessage) error {
		if slices.Contains(keys, key) {
			if out[key] != nil {
				return errMap
			}
			out[key] = value
		}
		return nil
	})
	return out, err
}

// Parse validates a single circuit response against the requested identity.
func Parse(raw []byte, circuitKey, year int64) (Map, error) {
	if len(raw) > maxBodyBytes {
		return Map{}, f1id.ErrResponseTooLarge
	}
	if !validID(circuitKey, year) {
		return Map{}, errMap
	}
	f, err := fields(raw, "circuitKey", "year", "circuitName", "rotation", "corners", "marshalSectors")
	if err != nil {
		return Map{}, errMap
	}
	key, _ := f1id.ParsePositiveCanonicalInt64(f["circuitKey"])
	y, _ := f1id.ParsePositiveCanonicalInt64(f["year"])
	if key != circuitKey || y != year {
		return Map{}, errMap
	}
	m := Map{unicodeIssues: f1id.HasInvalidJSONScalars(raw)}
	if v := f["circuitName"]; v != nil {
		m.name, err = f1id.DecodeLosslessString(v)
		if err != nil && !errors.Is(err, f1id.ErrJSONScalar) {
			return Map{}, errMap
		}
	}
	if v := f["rotation"]; v != nil {
		m.rotation, err = strconv.ParseFloat(string(v), 64)
		if err != nil {
			return Map{}, errMap
		}
	}
	m.corners, err = parseLandmarks(f["corners"], true)
	if err != nil || !m.Valid() {
		return Map{}, errMap
	}
	if v := f["marshalSectors"]; v != nil {
		m.sectors, err = parseLandmarks(v, false)
		if err != nil {
			return Map{}, errMap
		}
	}
	return m, nil
}

func parseLandmarks(raw []byte, corners bool) ([]landmark, error) {
	keys := []string{"number", "trackPosition"}
	if corners {
		keys = append(keys, "letter")
	}
	var points []landmark
	err := f1id.VisitArray(raw, func(i int, v json.RawMessage) error {
		if i >= 64 {
			return errMap
		}
		f, err := fields(v, keys...)
		if err != nil {
			return errMap
		}
		n, ok := f1id.ParsePositiveCanonicalInt64(f["number"])
		if !ok || n > 64 {
			return errMap
		}
		p := landmark{number: n}
		if l := f["letter"]; l != nil {
			p.letter, err = f1id.DecodeLosslessString(l)
			if err != nil || (p.letter != "" && (len(p.letter) != 1 || p.letter[0] < 'A' || p.letter[0] > 'Z')) {
				return errMap
			}
		}
		pos, err := fields(f["trackPosition"], "x", "y")
		if err != nil {
			return errMap
		}
		p.x, p.y = coordinateMetres(pos["x"]), coordinateMetres(pos["y"])
		if p.x == nil || p.y == nil {
			return errMap
		}
		points = append(points, p)
		return nil
	})
	if err != nil {
		return nil, errMap
	}
	slices.SortFunc(points, func(a, b landmark) int {
		return cmp.Or(cmp.Compare(a.number, b.number), cmp.Compare(a.letter, b.letter))
	})
	for i := 1; i < len(points); i++ {
		if points[i].number == points[i-1].number && points[i].letter == points[i-1].letter {
			return nil, errMap
		}
	}
	return points, nil
}

// raw is a value from a validated JSON object.
func coordinateMetres(raw json.RawMessage) *big.Rat {
	dm, ok := new(big.Rat).SetString(string(raw))
	if !ok || dm.Cmp(big.NewRat(-1e6, 1)) < 0 || dm.Cmp(big.NewRat(1e6, 1)) > 0 {
		return nil
	}
	return dm.Quo(dm, big.NewRat(10, 1))
}
