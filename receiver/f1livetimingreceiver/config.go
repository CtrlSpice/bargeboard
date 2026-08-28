package f1livetimingreceiver

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	defaultEndpoint          = "wss://livetiming.formula1.com/signalrcore"
	defaultNegotiateEndpoint = "https://livetiming.formula1.com/signalrcore/negotiate"
)

type Config struct {
	Endpoint          string     `mapstructure:"endpoint"`
	NegotiateEndpoint string     `mapstructure:"negotiate_endpoint"`
	Auth              AuthConfig `mapstructure:"auth"`
}

type AuthConfig struct {
	TokenFile string `mapstructure:"token_file"`
}

func (cfg *Config) Validate() error {
	if err := validateEndpoint("endpoint", cfg.Endpoint, "ws", "wss"); err != nil {
		return err
	}
	if err := validateEndpoint("negotiate_endpoint", cfg.NegotiateEndpoint, "http", "https"); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Auth.TokenFile) == "" {
		return fmt.Errorf("auth.token_file must not be empty")
	}
	return nil
}

func validateEndpoint(name, raw string, allowedSchemes ...string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL", name)
	}
	for _, scheme := range allowedSchemes {
		if parsed.Scheme == scheme {
			return nil
		}
	}
	return fmt.Errorf("%s uses unsupported scheme %q", name, parsed.Scheme)
}
