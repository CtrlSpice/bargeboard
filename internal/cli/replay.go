package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1index"
	static "github.com/CtrlSpice/bargeboard/receiver/f1staticreceiver"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/yamlprovider"
	"go.opentelemetry.io/collector/otelcol"
)

func replay(settings otelcol.CollectorSettings) *cobra.Command {
	var db, listen, pace, endpoint string
	var once, noCircuit, anyType bool
	cmd := &cobra.Command{Use: "replay [selector]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		selector := "latest"
		if len(args) > 0 {
			selector = args[0]
		}
		q, err := f1index.ParseSelector(selector)
		if err != nil {
			return err
		}
		if anyType {
			if q.Year != 0 {
				return errors.New("--any-type requires latest")
			}
			q.Type = ""
		}
		c := static.DefaultConfig()
		c.Enabled = true
		c.Endpoint = endpoint
		c.Pace = pace
		c.CircuitEnrichment.Enabled = !noCircuit
		if db == "" {
			return errors.New("empty database path")
		}
		if err := c.Validate(); err != nil {
			return err
		}
		match, err := f1index.Resolve(cmd.Context(), endpoint, q, time.Now())
		if err != nil {
			return err
		}
		c.SessionPath = strings.TrimSuffix(match.Session.Path, "/")
		if c.SessionPath == "" {
			return errors.New("empty session path")
		}
		if err := c.Validate(); err != nil {
			return err
		}
		type config = map[string]any
		pipe := config{"receivers": []string{"f1static"}, "processors": []string{"batch"}, "exporters": []string{"desktop"}}
		raw, err := json.Marshal(config{
			"receivers": config{"f1static": config{
				"enabled": true, "endpoint": endpoint, "session_path": c.SessionPath, "pace": pace,
				"circuit_enrichment": config{"enabled": !noCircuit},
			}},
			"extensions": config{"duckdb": config{"db": db, "endpoint": listen, "db_max_size": "0"}},
			"exporters":  config{"desktop": config{}},
			"processors": config{"batch": config{}},
			"service": config{
				"extensions": []string{"duckdb"},
				"telemetry":  config{"metrics": config{"level": "none"}},
				"pipelines":  config{"logs": pipe, "metrics": pipe, "traces": pipe},
			},
		})
		if err != nil {
			return err
		}
		settings.ConfigProviderSettings.ResolverSettings = confmap.ResolverSettings{URIs: []string{"yaml:" + strings.ReplaceAll(string(raw), "$", "$$")}, ProviderFactories: []confmap.ProviderFactory{yamlprovider.NewFactory()}}
		type result struct {
			s   static.ReplaySummary
			err error
		}
		completed := make(chan result, 1)
		factories := settings.Factories
		settings.Factories = func() (otelcol.Factories, error) {
			f, err := factories()
			if err == nil {
				f.Receivers[static.Type] = static.NewFactoryWithCompletion(func(s static.ReplaySummary, e error) { completed <- result{s, e} })
			}
			return f, err
		}
		col, err := otelcol.NewCollector(settings)
		if err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- col.Run(cmd.Context()) }()
		select {
		case err := <-done:
			if err == nil && once {
				return errors.New("collector stopped before replay completed")
			}
			return err
		case r := <-completed:
			if once || r.err != nil {
				col.Shutdown()
			}
			if err := errors.Join(r.err, <-done); err != nil {
				return err
			}
			if once {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					static.ReplaySummary
					DBPath string `json:"db_path"`
				}{r.s, db})
			}
			return nil
		}
	}}
	f := cmd.Flags()
	f.StringVar(&db, "db", "./bargeboard.duckdb", "DuckDB file")
	f.StringVar(&listen, "listen", "127.0.0.1:8000", "Viewer address")
	f.StringVar(&pace, "pace", "instant", "Replay speed: instant or 1x to 1000x")
	f.BoolVar(&once, "once", false, "Flush and exit after replay")
	f.BoolVar(&noCircuit, "no-circuit", false, "Skip circuit enrichment")
	f.BoolVar(&anyType, "any-type", false, "Select the latest completed session of any type")
	f.StringVar(&endpoint, "endpoint", static.DefaultEndpoint, "Archive endpoint")
	return cmd
}
