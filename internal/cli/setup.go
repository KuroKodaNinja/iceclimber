package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/agent"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/spf13/cobra"
)

// newSetupCmd is the guided first run: one command that walks config → trust →
// bootstrap → (optional) shell → (optional) agent → console, instead of five commands
// the operator must discover in order. Every step reuses the standalone command's core
// (runTrust, provision, the installers, runConsole), so setup is orchestration, not a
// second implementation. Headless-first: on a terminal it prompts; without one (or with
// --yes) it runs from flags and fails fast naming any flag it still needs.
func newSetupCmd() *cobra.Command {
	var (
		host, user, identity, id string
		port                     int
		yes                      bool
		skipShell, skipAgent     bool
		agentName, tokenFile     string
		noConsole                bool
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Guided first run: config → trust → bootstrap → shell/agent → console",
		Long: "Walks a new sandbox from nothing to serving: scaffolds iceclimber.yaml (prompting\n" +
			"for the SSH essentials, or from --host/--user/...), records the host key (with the\n" +
			"same confirmation `trust` uses), bootstraps the sandbox, optionally installs the\n" +
			"interactive shell and a coding agent, and drops you into the console. Idempotent —\n" +
			"every step skips what's already done, so re-running finishes an interrupted setup.\n" +
			"Headless: pass --yes plus the flags for what you want; missing inputs fail fast.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			interactive := isTerminal(os.Stdin) && isTerminal(os.Stdout) && !yes

			// 1. Config — use an existing file, else scaffold from flags/prompts.
			step(w, 1, "config")
			cfg, err := setupConfig(cmd, interactive, host, user, identity, id, port)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "   using %s (sandbox %q, ssh %s@%s)\n", cfgFile, cfg.SandboxID, cfg.SSH.User, cfg.SSH.Host)

			// 2. Trust — record the host key (no-op when already trusted; interactive
			// confirmation unless --yes, exactly like `iceclimber trust`).
			step(w, 2, "trust host key")
			{
				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				err := runTrust(ctx, cmd, cfg, cfg.SSH.KnownHosts, trustOpts{accept: yes})
				cancel()
				if err != nil {
					return err
				}
			}

			// 3. Connect + bootstrap (idempotent; never destructive — `bootstrap --force` is
			// the explicit reset path, not something a wizard should reach for).
			step(w, 3, "bootstrap")
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()
			sess, err := openLockedSession(ctx, cfg, transport, "setup")
			if err != nil {
				return err
			}
			closeSess := func() {
				if sess != nil {
					_ = sess.Close()
					sess = nil
				}
			}
			defer closeSess()
			if err := provision(ctx, sess); err != nil {
				return err
			}
			if err := writeEgressTrust(ctx, sess, cfg); err != nil {
				return err
			}
			fmt.Fprintf(w, "   sandbox %s bootstrapped at %s (%s/%s/%s)\n",
				cfg.SandboxID, sess.tree.Root, sess.fp.OS, sess.fp.Arch, sess.fp.Libc.Family)

			// 4. Interactive shell (optional): history + completion for `iceclimber shell`.
			if !skipShell && (yes || askYes(cmd, interactive, "Install the interactive shell (bash, for `iceclimber shell`)? [Y/n] ", true)) {
				step(w, 4, "shell")
				pr, finish := installProgress(w, sess.transport)
				res, err := installShell(ctx, sess, pr)
				finish()
				if err != nil {
					return err
				}
				fmt.Fprintf(w, "   bash %s at %s\n", res.Version, res.Path)
			}

			// 5. Coding agent (optional). Interactive: offered with a token-file prompt.
			// Headless: only when --agent is passed (with --token-file, or --skip-auth via
			// the dedicated `agent install` command — a wizard shouldn't default to an
			// unauthenticated agent).
			installAgent := agentName != ""
			if interactive && !skipAgent && !installAgent {
				installAgent = askYes(cmd, interactive, "Install a coding agent (Claude Code) now? [y/N] ", false)
				if installAgent {
					agentName = agent.Claude.Name
				}
			}
			if installAgent && !skipAgent {
				step(w, 5, "agent")
				d, ok := agent.Lookup(agentName)
				if !ok {
					return fmt.Errorf("unknown agent %q (known: %s)", agentName, strings.Join(agent.Names(), ", "))
				}
				tf := tokenFile
				if tf == "" && interactive {
					tf = promptLine(cmd, fmt.Sprintf("   token file (from `claude setup-token`; empty = skip auth): "))
				}
				token := ""
				if tf != "" || os.Getenv(d.TokenEnv) != "" {
					if token, err = resolveAgentToken(d, tf); err != nil {
						return err
					}
				}
				res, err := newAgentInstaller(sess).Install(ctx, d, token)
				if err != nil {
					return err
				}
				recordAgentCaps(ctx, sess, d, res)
				auth := "auth configured"
				if !res.AuthConfigured {
					auth = "auth skipped — set " + d.TokenEnv + " before running it"
				}
				fmt.Fprintf(w, "   %s %s (%s); launch in the sandbox with %s\n", d.DisplayName, res.Version, auth, res.Launcher)
			}

			// 6. Done — into the console (release the session + ops lock first; the console
			// takes the serve lock and dials on its own), or print the next steps.
			fmt.Fprintf(w, "\nsetup complete.\n")
			closeSess()
			if !noConsole && askYes(cmd, interactive, "Open the console now (serve + approve)? [Y/n] ", true) && interactive {
				return runConsole(cmd.Context(), cfg, transport, "")
			}
			fmt.Fprintf(w, "next: run `iceclimber` for the console (serve + approve), or `iceclimber serve` headless.\n")
			fmt.Fprintf(w, "      in the sandbox: `nana -p \"<task>\"` runs the agent; see QUICKSTART.md.\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "sandbox SSH host (or a ~/.ssh/config alias) for the scaffolded config")
	cmd.Flags().StringVar(&user, "user", "", "SSH user for the scaffolded config")
	cmd.Flags().IntVar(&port, "port", 0, "SSH port (0 = ssh_config/default)")
	cmd.Flags().StringVar(&identity, "identity-file", "", "SSH identity file (empty = ssh-agent / ssh_config)")
	cmd.Flags().StringVar(&id, "id", "", "sandbox_id for the scaffolded config (default: derived from the host)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "no prompts: accept the host key, install the shell, use defaults")
	cmd.Flags().BoolVar(&skipShell, "skip-shell", false, "don't install the interactive shell")
	cmd.Flags().BoolVar(&skipAgent, "skip-agent", false, "don't install a coding agent")
	cmd.Flags().StringVar(&agentName, "agent", "", "install this coding agent (e.g. claude); headless requires --token-file or the token env var")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "agent auth token file (a shell 'export VAR=...' line or a bare token)")
	cmd.Flags().BoolVar(&noConsole, "no-console", false, "don't offer/open the console at the end")
	return cmd
}

// setupConfig returns the working config: the existing file when present, else one
// scaffolded from flags (headless) or prompts (interactive) and written to cfgFile.
func setupConfig(cmd *cobra.Command, interactive bool, host, user, identity, id string, port int) (*config.Config, error) {
	if _, err := os.Stat(cfgFile); err == nil {
		return config.Load(cfgFile, sandboxID)
	}
	if host == "" && interactive {
		host = promptLine(cmd, "   sandbox SSH host (or ~/.ssh/config alias): ")
	}
	if host == "" {
		return nil, fmt.Errorf("no config at %s and no --host — pass --host/--user (or run interactively)", cfgFile)
	}
	if user == "" && interactive {
		user = promptLine(cmd, "   SSH user (empty = ssh_config/OS default): ")
	}
	if id == "" {
		// A sandbox_id the operator can address (--sandbox <id>); host-derived default.
		id = strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
				return r
			case r >= 'A' && r <= 'Z':
				return r + ('a' - 'A')
			default:
				return '-'
			}
		}, host)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# scaffolded by `iceclimber setup` — see `iceclimber init` for the full, commented template\n")
	fmt.Fprintf(&b, "sandbox_id: %s\nssh:\n  host: %s\n", id, host)
	if user != "" {
		fmt.Fprintf(&b, "  user: %s\n", user)
	}
	if port != 0 {
		fmt.Fprintf(&b, "  port: %d\n", port)
	}
	if identity != "" {
		fmt.Fprintf(&b, "  identity_file: %s\n", identity)
	}
	if err := os.WriteFile(cfgFile, []byte(b.String()), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", cfgFile, err)
	}
	return config.Load(cfgFile, sandboxID)
}

// step prints a numbered progress heading for one wizard stage.
func step(w io.Writer, n int, name string) { fmt.Fprintf(w, "\n[%d] %s\n", n, name) }

// askYes prompts y/n with a default; non-interactive returns the default.
func askYes(cmd *cobra.Command, interactive bool, prompt string, def bool) bool {
	if !interactive {
		return def
	}
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && err != io.EOF {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

// promptLine reads one trimmed line (empty on EOF/non-interactive).
func promptLine(cmd *cobra.Command, prompt string) string {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && err != io.EOF {
		return ""
	}
	return strings.TrimSpace(line)
}
