package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const scaffoldYAML = `# iceclimber controller config (operator-owned; never written by Nana).
sandbox_id: my-sandbox-1

ssh:
  # host may be a literal host/IP or a ~/.ssh/config Host alias. When use_ssh_config
  # is on (default), 'ssh -G' resolves the alias — so jumpboxes/bastions are handled
  # entirely by your existing ProxyJump in ~/.ssh/config; no setting is needed here.
  host: sandbox.example.internal
  user: agent          # optional; ssh_config / the OS default can supply it
  port: 22             # optional; 0 / omitted lets ssh_config decide (else 22)
  # Path to a private key. If omitted, ssh-agent (SSH_AUTH_SOCK) is used.
  identity_file: ~/.ssh/id_ed25519
  # Host-key file to verify against. If omitted, ~/.ssh/known_hosts is used.
  # Unknown hosts are rejected — record the key first with: iceclimber trust
  known_hosts: ""
  # Honor ~/.ssh/config (alias resolution + ProxyJump) via 'ssh -G'. Default true;
  # set false to force a literal direct dial. ssh_config_file overrides the path.
  # use_ssh_config: true
  # ssh_config_file: ""
  # Interactive auth (off by default; key/agent are always tried first). Prompted
  # no-echo on the terminal — works headless too, as long as a terminal exists.
  # password_auth: false
  # keyboard_interactive: false
  # SSH keepalive ping interval (seconds). Omitted/0 = 20s; negative disables.
  # Keeps the link warm through idle windows so a corporate firewall/NAT/bastion
  # doesn't silently drop it; serve also auto-reconnects if a drop happens anyway.
  # keepalive_interval: 20

# Where the iceclimber tree lives in the sandbox. Leave empty to let bootstrap
# choose the first writable candidate ($HOME/.iceclimber, then /opt/iceclimber).
remote_root: ""

# Local wheel/runtime cache, shared across sandboxes by platform fingerprint.
cache_dir: ~/.iceclimber-cache

# Where each language runtime comes from. Omitted = iceclimber installs a managed
# build (the default). Set source: system to use a runtime already on the sandbox
# (brownfield); bootstrap also detects and offers this. Optional path pins the
# interpreter; env_manager (python) picks venv or conda.
# runtimes:
#   python:
#     source: system        # managed | system
#     env_manager: venv     # venv | conda
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
