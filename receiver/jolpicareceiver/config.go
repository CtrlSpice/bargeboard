// Package jolpicareceiver collects published FIA classifications and standings.
package jolpicareceiver

import (
	"errors"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
	"go.opentelemetry.io/collector/component"
)

const DefaultEndpoint = "https://api.jolpi.ca/ergast/f1"

type Config struct {
	Enabled          bool   `mapstructure:"enabled"`
	Endpoint         string `mapstructure:"endpoint"`
	Season           int64  `mapstructure:"season"`
	Round            int64  `mapstructure:"round"`
	MeetingKey       int64  `mapstructure:"meeting_key"`
	IncludeSprint    bool   `mapstructure:"include_sprint"`
	IncludeStandings bool   `mapstructure:"include_standings"`
	CorrelateTraces  bool   `mapstructure:"correlate_traces"`
	PollInterval     string `mapstructure:"poll_interval"`
	MaxAttempts      int    `mapstructure:"max_attempts"`
}

func createDefaultConfig() component.Config {
	return &Config{Endpoint: DefaultEndpoint, IncludeStandings: true, PollInterval: "5m", MaxAttempts: 16}
}

func (c Config) Validate() error {
	if err := f1id.ValidateHTTPSOrLoopbackEndpoint("endpoint", c.Endpoint); err != nil {
		return err
	}
	if !c.Enabled {
		return nil
	}
	if c.Season != 0 && c.Season < 1950 || c.Season > 9999 || c.Round < 0 || c.Round > 99 || c.Season == 0 && c.Round != 0 {
		return errors.New("invalid season or round")
	}
	if c.MeetingKey < 0 || c.CorrelateTraces && c.MeetingKey == 0 {
		return errors.New("invalid meeting_key")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 64 {
		return errors.New("invalid max_attempts")
	}
	if _, err := pollInterval(c.PollInterval); err != nil {
		return err
	}
	return nil
}

func pollInterval(s string) (time.Duration, error) {
	if len(s) > 1 {
		n, ok := f1id.ParsePositiveCanonicalInt64([]byte(s[:len(s)-1]))
		unit := time.Second
		switch s[len(s)-1] {
		case 'm':
			unit = time.Minute
		case 's':
		default:
			ok = false
		}
		if ok && n <= int64(30*time.Minute/unit) && time.Duration(n)*unit >= 30*time.Second {
			return time.Duration(n) * unit, nil
		}
	}
	return 0, errors.New("invalid poll_interval")
}
