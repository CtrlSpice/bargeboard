package f1livetimingreceiver

import (
	"strings"
	"testing"
)

func TestValidateEndpointHostname(t *testing.T) {
	for _, field := range []struct {
		name    string
		schemes []string
	}{
		{"endpoint", []string{"ws", "wss"}},
		{"negotiate_endpoint", []string{"http", "https"}},
	} {
		for _, scheme := range field.schemes {
			for _, authority := range []string{"", ":443", ":", "[]", "[]:443"} {
				t.Run(field.name+"/"+scheme+"/"+authority, func(t *testing.T) {
					err := validateEndpoint(field.name, scheme+"://"+authority+"/signalrcore", field.schemes...)
					wantErr := field.name + " must be an absolute URL"
					if err == nil || err.Error() != wantErr {
						t.Fatalf("validateEndpoint() error = %v, want %q", err, wantErr)
					}
				})
			}
		}
	}
}

func TestConfigRejectsEmptyEndpointHostname(t *testing.T) {
	for _, authority := range []string{":443", ":", "[]", "[]:443"} {
		for _, field := range []string{"both", "endpoint", "negotiate_endpoint"} {
			t.Run(authority+"/"+field, func(t *testing.T) {
				cfg := createDefaultConfig().(*Config)
				cfg.Auth.TokenFile = "unused-token-file"
				if field != "negotiate_endpoint" {
					cfg.Endpoint = "wss://" + authority + "/private-stream-route"
				}
				if field != "endpoint" {
					cfg.NegotiateEndpoint = "https://" + authority + "/private-negotiate-route"
				}
				want := *cfg
				wantField := field
				if field == "both" {
					wantField = "endpoint"
				}
				wantErr := wantField + " must be an absolute URL"
				err := cfg.Validate()
				if err == nil || err.Error() != wantErr {
					t.Errorf("Validate() error = %v, want %q", err, wantErr)
				}
				if *cfg != want {
					t.Errorf("Validate() config = %#v, want unchanged %#v", *cfg, want)
				}
			})
		}
	}
}

func TestConfigEndpointHostnameCompatibility(t *testing.T) {
	for _, test := range []struct {
		name      string
		stream    string
		negotiate string
		wantErr   string
	}{
		{name: "DNS", stream: "wss://Example.test", negotiate: "https://example.test"},
		{name: "DNS port", stream: "wss://example.test:443", negotiate: "https://example.test:443"},
		{name: "IPv4", stream: "wss://192.0.2.1", negotiate: "https://192.0.2.1"},
		{name: "IPv4 port", stream: "wss://192.0.2.1:443", negotiate: "https://192.0.2.1:443"},
		{name: "IPv6", stream: "wss://[2001:db8::1]", negotiate: "https://[2001:db8::1]"},
		{name: "IPv6 port", stream: "wss://[2001:db8::1]:443", negotiate: "https://[2001:db8::1]:443"},
		{name: "localhost", stream: "ws://LOCALHOST", negotiate: "http://localhost"},
		{name: "localhost port", stream: "ws://localhost:8080", negotiate: "http://localhost:8080"},
		{name: "loopback IPv4", stream: "ws://127.0.0.1", negotiate: "http://127.0.0.1"},
		{name: "loopback IPv4 port", stream: "ws://127.0.0.1:8080", negotiate: "http://127.0.0.1:8080"},
		{name: "loopback IPv6", stream: "ws://[::1]", negotiate: "http://[::1]"},
		{name: "loopback IPv6 port", stream: "ws://[::1]:8080", negotiate: "http://[::1]:8080"},
		// These preserve accepted syntax, not DNS validity or network reachability.
		{name: "underscore", stream: "wss://under_score.test", negotiate: "https://under_score.test"},
		{name: "empty port", stream: "wss://example.test:", negotiate: "https://example.test:"},
		{name: "out of range port", stream: "wss://example.test:65536", negotiate: "https://example.test:65536"},
		{name: "different ports", stream: "wss://example.test:443", negotiate: "https://example.test:444", wantErr: "endpoint and negotiate_endpoint must use the same authority"},
		{name: "implicit versus explicit port", stream: "wss://example.test", negotiate: "https://example.test:443", wantErr: "endpoint and negotiate_endpoint must use the same authority"},
		{name: "different security", stream: "wss://localhost", negotiate: "http://localhost", wantErr: "endpoint and negotiate_endpoint must use matching security"},
		{name: "insecure remote", stream: "ws://example.test", negotiate: "http://example.test", wantErr: "insecure endpoints are only allowed on loopback hosts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{
				Endpoint:          test.stream + "/signalrcore",
				NegotiateEndpoint: test.negotiate + "/signalrcore/negotiate",
				Auth:              AuthConfig{TokenFile: "unused-token-file"},
			}
			want := cfg
			err := cfg.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Errorf("Validate() error = %v", err)
				}
			} else if err == nil || err.Error() != test.wantErr {
				t.Errorf("Validate() error = %v, want %q", err, test.wantErr)
			}
			if cfg != want {
				t.Errorf("Validate() config = %#v, want unchanged %#v", cfg, want)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Config)
		wantErr   string
		forbidden string
	}{
		{
			name: "valid",
			mutate: func(cfg *Config) {
				cfg.Auth.TokenFile = "/run/secrets/f1tv-token"
			},
		},
		{
			name:    "missing token file",
			mutate:  func(*Config) {},
			wantErr: "auth.token_file",
		},
		{
			name: "invalid stream endpoint",
			mutate: func(cfg *Config) {
				cfg.Endpoint = "https://livetiming.formula1.com/signalrcore"
				cfg.Auth.TokenFile = "/run/secrets/f1tv-token"
			},
			wantErr: "unsupported scheme",
		},
		{
			name: "invalid negotiate endpoint",
			mutate: func(cfg *Config) {
				cfg.NegotiateEndpoint = "signalrcore/negotiate"
				cfg.Auth.TokenFile = "/run/secrets/f1tv-token"
			},
			wantErr: "absolute URL",
		},
		{
			name: "mismatched authorities",
			mutate: func(cfg *Config) {
				cfg.NegotiateEndpoint = "https://example.test/signalrcore/negotiate"
				cfg.Auth.TokenFile = "/run/secrets/f1tv-token"
			},
			wantErr: "same authority",
		},
		{
			name: "mismatched security",
			mutate: func(cfg *Config) {
				cfg.Endpoint = "ws://livetiming.formula1.com/signalrcore"
				cfg.Auth.TokenFile = "/run/secrets/f1tv-token"
			},
			wantErr: "matching security",
		},
		{
			name: "insecure remote endpoint",
			mutate: func(cfg *Config) {
				cfg.Endpoint = "ws://example.test/signalrcore"
				cfg.NegotiateEndpoint = "http://example.test/signalrcore/negotiate"
				cfg.Auth.TokenFile = "/run/secrets/f1tv-token"
			},
			wantErr: "loopback",
		},
		{
			name: "endpoint user information",
			mutate: func(cfg *Config) {
				cfg.Endpoint = "wss://user:secret@livetiming.formula1.com/signalrcore"
				cfg.Auth.TokenFile = "/run/secrets/f1tv-token"
			},
			wantErr:   "must not include user information",
			forbidden: "secret",
		},
		{
			name: "endpoint query parameters",
			mutate: func(cfg *Config) {
				cfg.NegotiateEndpoint = "https://livetiming.formula1.com/signalrcore/negotiate?access_token=secret"
				cfg.Auth.TokenFile = "/run/secrets/f1tv-token"
			},
			wantErr:   "must not include query parameters",
			forbidden: "secret",
		},
		{
			name: "endpoint fragment",
			mutate: func(cfg *Config) {
				cfg.Endpoint = "wss://livetiming.formula1.com/signalrcore#secret"
				cfg.Auth.TokenFile = "/run/secrets/f1tv-token"
			},
			wantErr:   "must not include a fragment",
			forbidden: "secret",
		},
		{
			name: "insecure loopback endpoint",
			mutate: func(cfg *Config) {
				cfg.Endpoint = "ws://127.0.0.1:8080/signalrcore"
				cfg.NegotiateEndpoint = "http://127.0.0.1:8080/signalrcore/negotiate"
				cfg.Auth.TokenFile = "/run/secrets/f1tv-token"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := createDefaultConfig().(*Config)
			test.mutate(cfg)
			err := cfg.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.wantErr)
			}
			if test.forbidden != "" && strings.Contains(err.Error(), test.forbidden) {
				t.Errorf("Validate() error exposed forbidden value %q", test.forbidden)
			}
		})
	}
}
