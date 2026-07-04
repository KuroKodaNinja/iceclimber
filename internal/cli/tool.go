package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/catalog"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/spf13/cobra"
)

// newInstallToolCmd installs a utility from the tool catalog (§16) — a data-defined
// static binary/archive (ripgrep, jq, fd, …). The catalog is embedded defaults +
// operator overlays; adding a tool is editing YAML, not code.
func newInstallToolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool <name>",
		Short: "Install a utility from the tool catalog (see `iceclimber catalog list`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, _, sess, done, err := openCommandSession(cmd, 10*time.Minute, "install")
			if err != nil {
				return err
			}
			defer done()

			pr, finish := installProgress(cmd.OutOrStdout(), sess.transport)
			res, err := installTool(ctx, sess, pr, args[0])
			finish()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			verb := "installed"
			if res.AlreadyInstalled {
				verb = "already installed:"
			}
			fmt.Fprintf(w, "%s %s %s at %s\n", verb, res.Name, res.Version, res.Path)
			return nil
		},
	}
	return cmd
}

// newCatalogCmd surfaces the tool catalog (list, with sources + installed state).
func newCatalogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "The tool catalog — data-defined utilities installable with `install tool`",
	}
	cmd.AddCommand(newCatalogListCmd())
	return cmd
}

func newCatalogListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List catalog tools (name, version, description, source); --json for machine output",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The catalog is controller-local (no SSH): loadable from config alone, so
			// `catalog list` works without a reachable sandbox.
			cfg, err := config.Load(cfgFile, sandboxID)
			overlays := []string{catalog.DefaultOverlayDir()}
			if err == nil {
				overlays = append(overlays, cfg.Catalog...)
			}
			cat, lerr := catalog.Load(overlays)
			if lerr != nil {
				return lerr
			}
			w := cmd.OutOrStdout()
			entries := cat.All()
			if jsonOutput {
				type row struct {
					Name, Version, Description, Kind, Source string
				}
				rows := make([]row, 0, len(entries))
				for _, e := range entries {
					rows = append(rows, row{e.Name, e.Version, e.Description, string(e.Kind), e.Source()})
				}
				return json.NewEncoder(w).Encode(rows)
			}
			if len(entries) == 0 {
				fmt.Fprintln(w, "catalog empty")
				return nil
			}
			for _, e := range entries {
				src := ""
				if e.Source() != "embedded" {
					src = "  [" + e.Source() + "]"
				}
				fmt.Fprintf(w, "%-12s %-8s %s%s\n", e.Name, e.Version, e.Description, src)
			}
			fmt.Fprintln(w, "\ninstall with `iceclimber install tool <name>`; extend via ~/.iceclimber/catalog/*.yaml")
			return nil
		},
	}
}
