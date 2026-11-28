package f1livetimingreceiver

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

const (
	defaultEndpoint          = "wss://livetiming.formula1.com/signalrcore"
	defaultNegotiateEndpoint = "https://livetiming.formula1.com/signalrcore/negotiate"
)

type Config struct {
	Endpoint          string                  `mapstructure:"endpoint"`
	NegotiateEndpoint string                  `mapstructure:"negotiate_endpoint"`
	Auth              AuthConfig              `mapstructure:"auth"`
	CircuitEnrichment CircuitEnrichmentConfig `mapstructure:"circuit_enrichment"`
}

type AuthConfig struct {
	OptionalToken bool   `mapstructure:"optional_token"`
	TokenFile     string `mapstructure:"token_file"`
}

type CircuitEnrichmentConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Endpoint string `mapstructure:"endpoint"`
}

func (cfg *Config) Validate() error {
	if err := validateEndpoint("endpoint", cfg.Endpoint, "ws", "wss"); err != nil {
		return err
	}
	if err := validateEndpoint("negotiate_endpoint", cfg.NegotiateEndpoint, "http", "https"); err != nil {
		return err
	}
	if err := validateEndpointPair(cfg.Endpoint, cfg.NegotiateEndpoint); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Auth.TokenFile) == "" {
		return fmt.Errorf("auth.token_file must not be empty")
	}
	if cfg.CircuitEnrichment.Enabled {
		if err := f1id.ValidateHTTPSOrLoopbackEndpoint("circuit_enrichment.endpoint", cfg.CircuitEnrichment.Endpoint); err != nil {
			return err
		}
	}
	return nil
}

func validateEndpointPair(streamRaw, negotiateRaw string) error {
	stream, _ := url.Parse(streamRaw)
	negotiate, _ := url.Parse(negotiateRaw)
	if !strings.EqualFold(stream.Host, negotiate.Host) {
		return fmt.Errorf("endpoint and negotiate_endpoint must use the same authority")
	}

	secure := stream.Scheme == "wss" && negotiate.Scheme == "https"
	insecure := stream.Scheme == "ws" && negotiate.Scheme == "http"
	if !secure && !insecure {
		return fmt.Errorf("endpoint and negotiate_endpoint must use matching security")
	}
	if insecure && !isLoopbackHost(stream.Hostname()) {
		return fmt.Errorf("insecure endpoints are only allowed on loopback hosts")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func validateEndpoint(name, raw string, allowedSchemes ...string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("%s must be an absolute URL", name)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not include user information", name)
	}
	if parsed.RawQuery != "" {
		return fmt.Errorf("%s must not include query parameters", name)
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("%s must not include a fragment", name)
	}
	for _, scheme := range allowedSchemes {
		if parsed.Scheme == scheme {
			return nil
		}
	}
	return fmt.Errorf("%s uses unsupported scheme %q", name, parsed.Scheme)
}
