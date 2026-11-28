package main

import (
	"context"
	"log"
	"os"

	"github.com/CtrlSpice/bargeboard/internal/cli"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	envprovider "go.opentelemetry.io/collector/confmap/provider/envprovider"
	fileprovider "go.opentelemetry.io/collector/confmap/provider/fileprovider"
	yamlprovider "go.opentelemetry.io/collector/confmap/provider/yamlprovider"
	"go.opentelemetry.io/collector/otelcol"
)

var version = "dev"

func main() {
	settings := otelcol.CollectorSettings{
		BuildInfo: component.BuildInfo{
			Command:     "bargeboard",
			Description: "Formula 1 telemetry collector",
			Version:     version,
		},
		Factories: components,
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				ProviderFactories: []confmap.ProviderFactory{
					envprovider.NewFactory(),
					fileprovider.NewFactory(),
					yamlprovider.NewFactory(),
				},
				DefaultScheme: "file",
			},
		},
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "sessions", "replay", "skills":
			if err := cli.Run(context.Background(), settings, os.Args[1:], os.Stdout); err != nil {
				log.Fatal(err)
			}
			return
		}
	}
	if err := otelcol.NewCommand(settings).Execute(); err != nil {
		log.Fatal(err)
	}
}
