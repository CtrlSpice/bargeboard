package main

import (
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/yamlprovider"
)

func TestComponents(t *testing.T) {
	factories, err := components()
	if err != nil {
		t.Fatalf("components() error = %v", err)
	}

	assertRegistered(t, "receiver", factories.Receivers, "otlp")
	assertRegistered(t, "receiver", factories.Receivers, "f1livetiming")
	assertRegistered(t, "receiver", factories.Receivers, "f1static")
	assertRegistered(t, "receiver", factories.Receivers, "openf1")
	assertRegistered(t, "receiver", factories.Receivers, "jolpica")
	assertRegistered(t, "processor", factories.Processors, "batch")
	assertRegistered(t, "exporter", factories.Exporters, "debug")
	assertRegistered(t, "exporter", factories.Exporters, "desktop")
	assertRegistered(t, "extension", factories.Extensions, "duckdb")

	if len(factories.Receivers) != 5 {
		t.Fatalf("receivers = %d, want 5", len(factories.Receivers))
	}
	if len(factories.Extensions) != 1 {
		t.Fatalf("extensions = %d, want 1", len(factories.Extensions))
	}
	if len(factories.Connectors) != 0 {
		t.Fatalf("connectors = %d, want 0", len(factories.Connectors))
	}
	if factories.Telemetry == nil {
		t.Fatal("telemetry factory is nil")
	}
}

func assertRegistered[T any](
	t *testing.T,
	kind string,
	factories map[component.Type]T,
	name string,
) {
	t.Helper()
	typeName := component.MustNewType(name)
	if _, ok := factories[typeName]; !ok {
		t.Errorf("%s %q is not registered", kind, name)
	}
}

func TestCompanionReceiverConfigExample(t *testing.T) {
	factories, err := components()
	if err != nil {
		t.Fatal(err)
	}
	const raw = `
f1static:
  enabled: true
  session_path: "2025/2025-05-04_Miami_Grand_Prix/2025-05-04_Race"
  pace: instant
  circuit_enrichment:
    enabled: true
openf1:
  enabled: true
  season: 2025
  meeting_key: 1259
  session_key: 9998
  session_name: race
  root_drivers: [4]
jolpica:
  enabled: true
  season: 2025
  round: 6
  meeting_key: 1259
  include_sprint: true
  include_standings: true
  correlate_traces: true
`
	resolver, err := confmap.NewResolver(confmap.ResolverSettings{
		URIs:              []string{"yaml:" + raw},
		ProviderFactories: []confmap.ProviderFactory{yamlprovider.NewFactory()},
	})
	if err != nil {
		t.Fatal(err)
	}
	cm, err := resolver.Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"f1static", "openf1", "jolpica"} {
		sub, err := cm.Sub(name)
		if err != nil {
			t.Fatalf("sub %s: %v", name, err)
		}
		cfg := factories.Receivers[component.MustNewType(name)].CreateDefaultConfig()
		if err := sub.Unmarshal(cfg); err != nil {
			t.Fatalf("unmarshal %s: %v", name, err)
		}
		if err := cfg.(interface{ Validate() error }).Validate(); err != nil {
			t.Fatalf("validate %s: %v", name, err)
		}
	}
}
