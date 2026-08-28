package main

import (
	f1livetimingreceiver "github.com/CtrlSpice/bargeboard/receiver/f1livetimingreceiver"
	"go.opentelemetry.io/collector/connector"
	debugexporter "go.opentelemetry.io/collector/exporter/debugexporter"
	"go.opentelemetry.io/collector/otelcol"
	batchprocessor "go.opentelemetry.io/collector/processor/batchprocessor"
	otlpreceiver "go.opentelemetry.io/collector/receiver/otlpreceiver"
	"go.opentelemetry.io/collector/service/telemetry/otelconftelemetry"
)

func components() (otelcol.Factories, error) {
	factories := otelcol.Factories{
		Telemetry: otelconftelemetry.NewFactory(),
	}

	var err error
	factories.Receivers, err = otelcol.MakeFactoryMap(
		f1livetimingreceiver.NewFactory(),
		otlpreceiver.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}

	factories.Processors, err = otelcol.MakeFactoryMap(
		batchprocessor.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}

	factories.Exporters, err = otelcol.MakeFactoryMap(
		debugexporter.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}

	factories.Connectors, err = otelcol.MakeFactoryMap[connector.Factory]()
	if err != nil {
		return otelcol.Factories{}, err
	}

	return factories, nil
}
