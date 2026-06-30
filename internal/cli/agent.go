package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/agent"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/spf13/cobra"
)

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Install and manage coding agents in the sandbox (Claude Code today)",
	}
	cmd.AddCommand(newAgentInstallCmd(), newAgentWrapCmd(), newAgentListCmd())
	return cmd
}

func newAgentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the agents iceclimber can install",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			for _, d := range agent.All() {
				fmt.Fprintf(w, "%-8s %s (%s)\n", d.Name, d.DisplayName, d.NpmPrefix)
			}
			return nil
		},
	}
}

func newAgentInstallCmd() *cobra.Command {
	var transport, tokenFile string
	var skipAuth bool
	cmd := &cobra.Command{
		Use:   "install [name]",
		Short: "Install a coding agent into the sandbox (default: claude), with its auth token",
		Long: "Relays the agent's native binary into the sandbox: the controller downloads the\n" +
			"agent's package for the SANDBOX's platform and pushes the binary in — no\n" +
			"on-target install, so it works for an air-gapped sandbox. Then it writes the\n" +
			"subscription auth and verifies the binary runs. The token comes from its env var\n" +
			"(e.g. CLAUDE_CODE_OAUTH_TOKEN, from 'claude setup-token') or --token-file; it is\n" +
			"written only to a 0600 file in the sandbox and never logged. An API key is\n" +
			"rejected — subscription token only.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := agent.Claude.Name
			if len(args) == 1 {
				name = args[0]
			}
			d, ok := agent.Lookup(name)
			if !ok {
				return fmt.Errorf("unknown agent %q (known: %s)", name, strings.Join(agent.Names(), ", "))
			}

			token := ""
			if !skipAuth {
				t, err := resolveAgentToken(d, tokenFile)
				if err != nil {
					return err
				}
				token = t
			}

			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			res, err := newAgentInstaller(sess).Install(ctx, d, token)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "installed %s (%s) %s\n", d.DisplayName, d.Name, res.Version)
			fmt.Fprintf(w, "  agent:  %s\n", res.Bin)
			if res.AuthConfigured {
				fmt.Fprintf(w, "  auth:   configured (%s) → %s\n", d.TokenEnv, res.EnvFile)
			} else {
				fmt.Fprintf(w, "  auth:   skipped — set %s before running the agent\n", d.TokenEnv)
			}
			fmt.Fprintf(w, "  launch: %s   (run in the sandbox — starts the agent, authenticated + wired to NANA.md)\n", res.Launcher)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read the auth token from this file (a shell 'export VAR=...' line or a bare token)")
	cmd.Flags().BoolVar(&skipAuth, "skip-auth", false, "install the agent CLI without configuring an auth token")
	return cmd
}

func newAgentWrapCmd() *cobra.Command {
	var transport, tokenFile, bin string
	var skipAuth bool
	cmd := &cobra.Command{
		Use:   "wrap [name]",
		Short: "Wrap an agent binary already on the sandbox with the iceclimber launcher (no relay)",
		Long: "Drops just the iceclimber wrapper — the auth env, the `run` launcher (NANA.md\n" +
			"wired in), and the `nana` dispatcher — around an agent binary that is ALREADY\n" +
			"present on the sandbox (a pre-baked image, or one installed out of band). Unlike\n" +
			"`install`, it relays nothing. The binary is found on the sandbox PATH by default\n" +
			"(resolved to an absolute path); pass --bin for an explicit path. Auth + token\n" +
			"handling are identical to `install`.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := agent.Claude.Name
			if len(args) == 1 {
				name = args[0]
			}
			d, ok := agent.Lookup(name)
			if !ok {
				return fmt.Errorf("unknown agent %q (known: %s)", name, strings.Join(agent.Names(), ", "))
			}

			token := ""
			if !skipAuth {
				t, err := resolveAgentToken(d, tokenFile)
				if err != nil {
					return err
				}
				token = t
			}

			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			res, err := newAgentInstaller(sess).Wrap(ctx, d, token, bin)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "wrapped %s (%s)\n", d.DisplayName, d.Name)
			fmt.Fprintf(w, "  agent:  %s   (already on the sandbox — not relayed)\n", res.Bin)
			if res.AuthConfigured {
				fmt.Fprintf(w, "  auth:   configured (%s) → %s\n", d.TokenEnv, res.EnvFile)
			} else {
				fmt.Fprintf(w, "  auth:   skipped — set %s before running the agent\n", d.TokenEnv)
			}
			fmt.Fprintf(w, "  launch: %s   (run in the sandbox — starts the agent, wired to NANA.md)\n", res.Launcher)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().StringVar(&bin, "bin", "", "absolute path to the agent binary on the sandbox (default: found on PATH)")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read the auth token from this file (a shell 'export VAR=...' line or a bare token)")
	cmd.Flags().BoolVar(&skipAuth, "skip-auth", false, "wrap the agent without configuring an auth token")
	return cmd
}

// resolveAgentToken resolves the subscription token from --token-file or the
// descriptor's env var, rejecting an empty value or an API key.
func resolveAgentToken(d agent.Descriptor, tokenFile string) (string, error) {
	var token string
	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("read token file: %w", err)
		}
		token = parseTokenFile(string(data), d.TokenEnv)
	} else {
		token = os.Getenv(d.TokenEnv)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("no auth token: set %s (run `claude setup-token`) or pass --token-file — a subscription token, NOT an API key (or use --skip-auth)", d.TokenEnv)
	}
	if agent.LooksLikeAPIKey(token) {
		return "", fmt.Errorf("%s looks like an API key (sk-ant-…); agents must use a subscription OAuth token (run `claude setup-token`)", d.TokenEnv)
	}
	return token, nil
}

// parseTokenFile extracts the token from a file that is either a shell snippet
// (`export VAR=value` / `VAR=value`, as `claude setup-token` users stash) or a
// bare token on its own. Quotes around the value are stripped.
func parseTokenFile(content, varName string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == varName {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	// No matching assignment: treat a single non-empty, non-comment line as the token.
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") && !strings.Contains(line, "=") {
			return line
		}
	}
	return ""
}
