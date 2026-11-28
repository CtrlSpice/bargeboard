// Package openf1receiver observes current Race and Sprint result snapshots.
package openf1receiver

import (
	"errors"
	"slices"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

const DefaultEndpoint = "https://api.openf1.org/v1"
const DefaultFinalisedGrace = "5m"

type DriverConfig struct {
	Number  int64  `mapstructure:"number"`
	Acronym string `mapstructure:"acronym"`
}

type Config struct {
	Enabled        bool             `mapstructure:"enabled"`
	Endpoint       string           `mapstructure:"endpoint"`
	FinalisedGrace string           `mapstructure:"finalised_grace"`
	SessionKey     int64            `mapstructure:"session_key"`
	MeetingKey     int64            `mapstructure:"meeting_key"`
	Season         int64            `mapstructure:"season"`
	SessionName    f1id.SessionName `mapstructure:"session_name"`
	Drivers        []DriverConfig   `mapstructure:"drivers"`
	RootDrivers    []int64          `mapstructure:"root_drivers"`
}

func (c Config) hasRoot(driver int64) bool { return slices.Contains(c.RootDrivers, driver) }

func ParseFinalisedGrace(raw string) (time.Duration, error) {
	if raw == "" {
		raw = DefaultFinalisedGrace
	}
	n, ok := f1id.ParsePositiveCanonicalInt64([]byte(raw[:len(raw)-1]))
	unit := time.Second
	switch raw[len(raw)-1] {
	case 'm':
		unit = time.Minute
	case 's':
	default:
		ok = false
	}
	if !ok || n > int64(30*time.Minute/unit) {
		return 0, errors.New("invalid finalised_grace")
	}
	return time.Duration(n) * unit, nil
}

func (c Config) identity() f1id.SessionIdentity {
	return f1id.SessionIdentity{Season: c.Season, MeetingKey: c.MeetingKey, SessionType: f1id.SessionType(c.SessionName), SessionName: c.SessionName}
}

func raceLike(e f1id.SessionIdentity) bool {
	return e.Valid() && (e.SessionName == "race" || e.SessionName == "sprint")
}

func roster(drivers []DriverConfig) (map[int64]string, bool) {
	if len(drivers) > 32 {
		return nil, false
	}
	m := make(map[int64]string, len(drivers))
	for _, d := range drivers {
		if d.Number <= 0 || !f1id.IsValidAcronym(d.Acronym) || m[d.Number] != "" {
			return nil, false
		}
		m[d.Number] = d.Acronym
	}
	return m, true
}

func (c *Config) Validate() error {
	if len(c.RootDrivers) > 32 {
		return errors.New("root_drivers exceeds 32 entries")
	}
	for i, n := range c.RootDrivers {
		if n <= 0 || slices.Contains(c.RootDrivers[:i], n) {
			return errors.New("root_drivers requires unique positive numbers")
		}
	}
	if err := f1id.ValidateHTTPSOrLoopbackEndpoint("endpoint", c.Endpoint); err != nil {
		return err
	}
	if _, err := ParseFinalisedGrace(c.FinalisedGrace); err != nil {
		return err
	}
	if c.Enabled {
		if !raceLike(c.identity()) || c.SessionKey <= 0 {
			return errors.New("invalid race-like session identity")
		}
		if _, ok := roster(c.Drivers); !ok {
			return errors.New("invalid driver registry")
		}
	}
	return nil
}
