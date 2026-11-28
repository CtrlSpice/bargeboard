// Package cli implements the archive commands alongside the Collector CLI.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1index"
	static "github.com/CtrlSpice/bargeboard/receiver/f1staticreceiver"
	skill "github.com/CtrlSpice/bargeboard/skills/bargeboard"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/collector/otelcol"
)

func Run(ctx context.Context, settings otelcol.CollectorSettings, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing archive command")
	}
	var cmd *cobra.Command
	switch args[0] {
	case "sessions":
		cmd = sessions()
	case "skills":
		cmd = skills()
	case "replay":
		cmd = replay(settings)
	default:
		return errors.New("unknown archive command")
	}
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args[1:])
	cmd.SetOut(out)
	return cmd.ExecuteContext(ctx)
}

func sessions() *cobra.Command {
	var endpoint string
	var asJSON bool
	cmd := &cobra.Command{Use: "sessions [year|latest]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		now := time.Now().UTC()
		year := now.Year()
		if len(args) > 0 && !strings.EqualFold(args[0], "latest") {
			n, err := strconv.Atoi(args[0])
			if err != nil || n < 1950 || n > 9999 {
				return errors.New("invalid season year")
			}
			year = n
		}
		ms, err := f1index.Fetch(cmd.Context(), endpoint, year)
		if len(args) > 0 && strings.EqualFold(args[0], "latest") && (errors.Is(err, f1index.ErrAbsent) || err == nil && len(ms) == 0) {
			ms, err = f1index.Fetch(cmd.Context(), endpoint, year-1)
		}
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(ms)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "MEETING\tSESSION\tLOCAL START\tGMT OFFSET")
		for _, m := range ms {
			for _, s := range m.Sessions {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.Name, s.Name, s.StartDate, s.GmtOffset)
			}
		}
		return w.Flush()
	}}
	cmd.Flags().StringVar(&endpoint, "endpoint", static.DefaultEndpoint, "Archive endpoint")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print meetings as JSON")
	return cmd
}

func skills() *cobra.Command {
	var dir string
	cmd := &cobra.Command{Use: "skills", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !cmd.Flags().Changed("install") {
			_, err := io.WriteString(cmd.OutOrStdout(), skill.Skill)
			return err
		}
		if dir == "" {
			return errors.New("empty skill directory")
		}
		path := filepath.Join(dir, "bargeboard")
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(skill.Skill), 0644)
	}}
	cmd.Flags().StringVar(&dir, "install", "", "Install under this directory")
	return cmd
}
