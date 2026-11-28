package f1staticreceiver

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/CtrlSpice/bargeboard/internal/circuitmap"
	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

const DefaultEndpoint = "https://livetiming.formula1.com/static"

type Config struct {
	Enabled     bool   `mapstructure:"enabled"`
	Endpoint    string `mapstructure:"endpoint"`
	SessionPath string `mapstructure:"session_path"`
	Pace        string `mapstructure:"pace"`
	Auth        struct {
		TokenFile string `mapstructure:"token_file"`
	} `mapstructure:"auth"`
	CircuitEnrichment struct {
		Enabled  bool   `mapstructure:"enabled"`
		Endpoint string `mapstructure:"endpoint"`
	} `mapstructure:"circuit_enrichment"`
}

func DefaultConfig() Config {
	c := Config{Endpoint: DefaultEndpoint, Pace: "instant"}
	c.CircuitEnrichment.Endpoint = circuitmap.DefaultEndpoint
	return c
}

var segment = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func multiplier(pace string) (int, error) {
	if pace == "instant" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(pace, "x"))
	if err != nil || n < 1 || n > 1000 || strconv.Itoa(n)+"x" != pace {
		return 0, errors.New("invalid replay pace")
	}
	return n, nil
}

func (c Config) Validate() error {
	if err := f1id.ValidateHTTPSOrLoopbackEndpoint("endpoint", c.Endpoint); err != nil {
		return err
	}
	if c.Enabled || c.SessionPath != "" {
		for _, s := range strings.Split(c.SessionPath, "/") {
			if s == "." || s == ".." || !segment.MatchString(s) {
				return errors.New("invalid session_path")
			}
		}
	}
	if c.CircuitEnrichment.Enabled {
		if err := f1id.ValidateHTTPSOrLoopbackEndpoint("circuit_enrichment.endpoint", c.CircuitEnrichment.Endpoint); err != nil {
			return err
		}
	}
	_, err := multiplier(c.Pace)
	return err
}
