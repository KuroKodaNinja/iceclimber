package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/spf13/cobra"
)

func newProbeCmd() *cobra.Command {
	var roots []string
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Read-only diagnostic: fingerprint the sandbox without changing it",
		Long: "Connects over SSH and reports OS, arch, libc, candidate install-root\n" +
			"viability (real write tests), and whether an iceclimber tree already\n" +
			"exists. Writes nothing durable.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			r, err := remote.Dial(ctx, dialConfig(cfg))
			if err != nil {
				return fmt.Errorf("connect to sandbox %s: %w", cfg.SandboxID, err)
			}
			defer r.Close()

			fp, err := probe.Run(ctx, r, probe.Options{ExplicitRoots: roots, RemoteRoot: cfg.RemoteRoot})
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(fp)
			}
			printFingerprint(cmd, cfg, fp)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&roots, "root", nil, "candidate install root to test first (repeatable)")
	return cmd
}

func printFingerprint(cmd *cobra.Command, cfg *config.Config, fp *probe.Fingerprint) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "sandbox:   %s (%s@%s:%d)\n", cfg.SandboxID, cfg.SSH.User, cfg.SSH.Host, portOr22(cfg.SSH.Port))
	fmt.Fprintf(w, "os/arch:   %s / %s\n", orUnknown(fp.OS), orUnknown(fp.Arch))
	if fp.OS == "linux" {
		conf := "low-confidence"
		if fp.Libc.HighConfidence {
			conf = "ok"
		}
		ver := ""
		if fp.Libc.Version != "" {
			ver = " " + fp.Libc.Version
		}
		fmt.Fprintf(w, "libc:      %s%s (%s)\n", fp.Libc.Family, ver, conf)
	}
	fmt.Fprintf(w, "home:      %s\n", orUnknown(fp.Home))
	fmt.Fprintln(w, "install-root candidates:")
	for _, r := range fp.Roots {
		status := "writable"
		switch {
		case !r.Creatable:
			status = "uncreatable"
		case !r.Writable:
			status = "unwritable"
		}
		fmt.Fprintf(w, "  %-30s %-12s %s\n", r.Path, status, humanKB(r.AvailKB))
	}
	if v := fp.FirstViableRoot(); v != "" {
		fmt.Fprintf(w, "would install to: %s\n", v)
	}
	if len(fp.Runtimes) > 0 {
		fmt.Fprintln(w, "system runtimes (brownfield):")
		for _, rt := range fp.Runtimes {
			extra := ""
			if len(rt.EnvManagers) > 0 {
				extra = "  env: " + strings.Join(rt.EnvManagers, ",")
			}
			fmt.Fprintf(w, "  %-7s %-10s %s%s\n", rt.Lang, orUnknown(rt.Version), rt.Path, extra)
		}
	}
	fmt.Fprintf(w, "existing tree:    %v\n", fp.HasExistingTree)
	if len(fp.Warnings) > 0 {
		fmt.Fprintln(w, "warnings:")
		for _, warn := range fp.Warnings {
			fmt.Fprintf(w, "  ! %s\n", warn)
		}
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func humanKB(kb int64) string {
	if kb <= 0 {
		return "?"
	}
	const unit = 1024
	if kb < unit {
		return fmt.Sprintf("%dK", kb)
	}
	mb := float64(kb) / unit
	if mb < unit {
		return fmt.Sprintf("%.1fM free", mb)
	}
	return fmt.Sprintf("%.1fG free", mb/unit)
}
