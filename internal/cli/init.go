package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const scaffoldYAML = `# iceclimber controller config (operator-owned; never written by Nana).
sandbox_id: my-sandbox-1

ssh:
  host: sandbox.example.internal
  user: agent
  port: 22
  # Path to a private key. If omitted, ssh-agent (SSH_AUTH_SOCK) is used.
  identity_file: ~/.ssh/id_ed25519
  # Host-key file to verify against. If omitted, ~/.ssh/known_hosts is used.
  # Unknown hosts are rejected — record the key first (ssh once, or ssh-keyscan).
  known_hosts: ""

# Where the iceclimber tree lives in the sandbox. Leave empty to let bootstrap
# choose the first writable candidate ($HOME/.iceclimber, then /opt/iceclimber).
remote_root: ""

# Local wheel/runtime cache, shared across sandboxes by platform fingerprint.
cache_dir: ~/.iceclimber-cache
`

func newInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold an iceclimber.yaml in the current directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := os.Stat(cfgFile); err == nil && !force {
				return fmt.Errorf("%s already exists; use --force to overwrite", cfgFile)
			}
			if err := os.WriteFile(cfgFile, []byte(scaffoldYAML), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", cfgFile, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s — set ssh host/user/identity_file, then run `iceclimber probe`\n", cfgFile)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config")
	return cmd
}
