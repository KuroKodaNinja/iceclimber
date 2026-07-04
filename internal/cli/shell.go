package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/shell"
	"github.com/spf13/cobra"
)

// newShellCmd opens an interactive login into the sandbox over iceclimber's own SSH transport
// (so ProxyJump/bastion, password caching, and host-key trust all apply), sources the sandbox
// environment (ICECLIMBER_HOME, popo/nana + installed runtimes on PATH, egress proxy/CA trust),
// and drops the operator into the installed bash — with history + tab-completion — falling back
// to the box's /bin/sh when bash isn't installed. `-c <cmd>` runs one command non-interactively
// in that same environment (scriptable, no TTY needed).
func newShellCmd() *cobra.Command {
	var command string
	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Open an interactive shell in the sandbox with the iceclimber environment set up",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			// A login/command session can be long-lived; don't impose a short deadline.
			ctx := cmd.Context()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()
			root := sess.tree.Root
			if root == "" {
				return fmt.Errorf("could not resolve the sandbox install root (ICECLIMBER_HOME)")
			}

			// Non-interactive one-shot: run the command in the sandbox env, no PTY. Works
			// headless (piped stdin/stdout), which is why it's the scriptable path.
			if command != "" {
				res, err := sess.runner.Run(ctx, shell.EnvPreamble(root)+command+"\n", nil)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				out.Write(res.Stdout)
				cmd.ErrOrStderr().Write(res.Stderr)
				if res.ExitCode != 0 {
					return fmt.Errorf("command exited with status %d", res.ExitCode)
				}
				return nil
			}

			// Interactive: needs a local terminal on both ends for a PTY.
			return interactiveLogin(ctx, sess, root)
		},
	}
	cmd.Flags().StringVarP(&command, "command", "c", "", "run one command in the sandbox environment and exit (non-interactive)")
	return cmd
}

// interactiveLogin drops the operator into a PTY login shell on the sandbox (the shared
// interactive branch of `iceclimber shell` and `sandbox shell`'s auto-serve jump-in). It
// needs a local terminal on both ends for the PTY.
func interactiveLogin(ctx context.Context, sess *session, root string) error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return fmt.Errorf("`iceclimber shell` needs a terminal; use `iceclimber shell -c '<command>'` to run a command non-interactively")
	}
	_, err := sess.runner.Shell(ctx, remote.ShellOpts{
		Command: interactiveLoginCommand(root),
		Term:    os.Getenv("TERM"),
	})
	return err
}

// interactiveLoginCommand is the POSIX-sh script the PTY session execs: prefer the installed
// bash (with its rc → history/completion/env), else source the env and drop into the box's
// /bin/sh with a hint. Both branches end in `exec`, replacing the shell so its exit is the
// session's exit. root is baked in shell-quoted.
func interactiveLoginCommand(root string) string {
	bin := shell.BinPath(root)
	rc := shell.RCPath(root)
	var b strings.Builder
	fmt.Fprintf(&b, "if [ -x %s ]; then\n", remote.ShellQuote(bin))
	fmt.Fprintf(&b, "  exec %s --rcfile %s -i\n", remote.ShellQuote(bin), remote.ShellQuote(rc))
	b.WriteString("else\n")
	b.WriteString(shell.EnvPreamble(root))
	b.WriteString("  printf '%s\\n' 'iceclimber: no bash installed (run: iceclimber install shell) — using /bin/sh' >&2\n")
	b.WriteString("  exec /bin/sh\n")
	b.WriteString("fi\n")
	return b.String()
}
