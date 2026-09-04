package f1livetimingreceiver

import (
	"strings"
	"testing"
)

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
