package f1livetimingreceiver

import (
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
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
		})
	}
}
