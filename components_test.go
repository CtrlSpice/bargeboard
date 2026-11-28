package main

import (
	"testing"

	"go.opentelemetry.io/collector/component"
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
