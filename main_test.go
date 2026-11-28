package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/CtrlSpice/otel-desktop-viewer/desktopexporter/duckdbextension"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	envprovider "go.opentelemetry.io/collector/confmap/provider/envprovider"
	fileprovider "go.opentelemetry.io/collector/confmap/provider/fileprovider"
	yamlprovider "go.opentelemetry.io/collector/confmap/provider/yamlprovider"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/pipeline"
	"go.opentelemetry.io/collector/service/pipelines"
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
	if value := resolved.Get("receivers::f1livetiming::auth::optional_token"); value != true {
		t.Fatalf("optional_token = %v, want true", value)
	}
}

func TestShippedConfigUsesPersistentViewer(t *testing.T) {
	provider, err := otelcol.NewConfigProvider(otelcol.ConfigProviderSettings{
		ResolverSettings: confmap.ResolverSettings{
			URIs: []string{"file:config.yaml"},
			ProviderFactories: []confmap.ProviderFactory{
				envprovider.NewFactory(),
				fileprovider.NewFactory(),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shut down config provider: %v", err)
		}
	})
	factories, err := components()
	if err != nil {
		t.Fatal(err)
	}
	config, err := provider.Get(t.Context(), factories)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	duckdbID := component.MustNewID("duckdb")
	wantExtension := &duckdbextension.Config{
		Endpoint:     "127.0.0.1:8000",
		OTLPHTTPPort: 4318,
		OTLPGRPCPort: 4317,
		Db:           "./bargeboard.duckdb",
		DbMaxSize:    "0",
		Telemetry:    duckdbextension.TelemetryDisabled,
	}
	if len(config.Extensions) != 1 || !reflect.DeepEqual(config.Extensions[duckdbID], wantExtension) {
		t.Fatalf("viewer extension = %#v, want %#v", config.Extensions, wantExtension)
	}
	if !slices.Equal(config.Service.Extensions, []component.ID{duckdbID}) {
		t.Fatalf("enabled extensions = %v, want [%v]", config.Service.Extensions, duckdbID)
	}
	desktopID := component.MustNewID("desktop")
	wantExporter := factories.Exporters[desktopID.Type()].CreateDefaultConfig()
	if len(config.Exporters) != 1 || !reflect.DeepEqual(config.Exporters[desktopID], wantExporter) {
		t.Fatalf("configured exporters = %#v, want only desktop with upstream defaults %#v", config.Exporters, wantExporter)
	}
	if len(config.Service.Pipelines) != 3 {
		t.Fatalf("configured pipelines = %v, want all three signals", config.Service.Pipelines)
	}
	for _, signal := range []pipeline.Signal{pipeline.SignalTraces, pipeline.SignalMetrics, pipeline.SignalLogs} {
		id := pipeline.NewID(signal)
		got := config.Service.Pipelines[id]
		want := &pipelines.PipelineConfig{
			Receivers:  []component.ID{component.MustNewID("f1livetiming"), component.MustNewID("otlp")},
			Processors: []component.ID{component.MustNewID("batch")},
			Exporters:  []component.ID{desktopID},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s pipeline = %#v, want %#v", id, got, want)
		}
	}
}
