package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/confmap"
	envprovider "go.opentelemetry.io/collector/confmap/provider/envprovider"
	fileprovider "go.opentelemetry.io/collector/confmap/provider/fileprovider"
	yamlprovider "go.opentelemetry.io/collector/confmap/provider/yamlprovider"
)

func TestShippedConfigResolvesTokenFileFromHome(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME must be exported for the shipped configuration")
	}

	resolver, err := confmap.NewResolver(confmap.ResolverSettings{
		URIs: []string{"file:config.yaml"},
		ProviderFactories: []confmap.ProviderFactory{
			envprovider.NewFactory(),
			fileprovider.NewFactory(),
			yamlprovider.NewFactory(),
		},
		DefaultScheme: "file",
	})
	if err != nil {
		t.Fatalf("create config resolver: %v", err)
	}
	t.Cleanup(func() {
		if err := resolver.Shutdown(context.Background()); err != nil {
			t.Errorf("shut down config resolver: %v", err)
		}
	})

	resolved, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve shipped config: %v", err)
	}
	value := resolved.Get("receivers::f1livetiming::auth::token_file")
	tokenFile, ok := value.(string)
	if !ok {
		t.Fatalf("resolved token file is not a string: %T", value)
	}
	want := filepath.Join(home, ".config", "bargeboard", "f1tv-token")
	if filepath.Clean(tokenFile) != want {
		t.Fatalf("resolved token file = %q, want %q", tokenFile, want)
	}
}
