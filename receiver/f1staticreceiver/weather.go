package f1staticreceiver

import (
	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type weatherState struct {
	at        [7]time.Time
	rain      int64
	rainKnown bool
	rainCoord int64
}

var tenth = regexp.MustCompile(`^-?(0|[1-9][0-9]*)\.[0-9]$`)

func (s *session) weatherPatch(f fields, t time.Time, coord int64) {
	w := &s.weather
	for i, x := range [...]struct{ key, name, unit string }{{"AirTemp", "air_temperature", "Cel"}, {"TrackTemp", "track_temperature", "Cel"}, {"Humidity", "relative_humidity", "%"}, {"Pressure", "air_pressure", "hPa"}, {"WindSpeed", "wind_speed", "m/s"}, {"WindDirection", "wind_direction", "deg"}, {"Rainfall", "rainfall_detected", "1"}} {
		raw, ok := f[x.key]
		if !ok || !t.After(w.at[i]) {
			continue
		}
		v := text(raw)
		var value any
		if i < 5 {
			if !tenth.MatchString(v) || v == "-0.0" || (i >= 2 && v[0] == '-') {
				continue
			}
			n, err := strconv.ParseInt(strings.Replace(v, ".", "", 1), 10, 64)
			if err != nil || (i == 2 && n > 1000) || (i == 3 && n == 0) {
				continue
			}
			value = float64(n) / 10
		} else {
			n, ok := f1id.ParseNonNegativeCanonicalInt64([]byte(v))
			if !ok || (i == 5 && n >= 360) || (i == 6 && n > 1) {
				if i == 6 {
					w.rainKnown = false
				}
				continue
			}
			value = n
			if i == 6 {
				if w.rainKnown && coord-w.rainCoord < int64(125*time.Second) && n != w.rain {
					s.log(fact{"f1.weather.rainfall.changed", t, attrs{"f1.time.quality": "publication_time", "f1.weather.rainfall_detected": n == 1, "f1.weather.previous_rainfall_detected": w.rain == 1}}, nil)
				}
				w.rain = n
				w.rainKnown = true
				w.rainCoord = coord
			}
		}
		w.at[i] = t
		s.gauge("f1.session.weather."+x.name, x.unit, t, nil, value)
	}
}
