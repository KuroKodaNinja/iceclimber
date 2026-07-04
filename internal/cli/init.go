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

# How long a delivered-but-uncollected response lingers before GC reaps it (with its
# request). A Go duration; omitted = 1h; "0" disables the retention sweep (collected
# responses are still pruned promptly).
# maildir_retention: 1h

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

# Pin managed-runtime downloads to an exact mirrored asset (url + sha256 per platform),
# bypassing the live upstream resolver — the air-gapped/compliance path. Keyed
# lang -> requested-version -> platform (os/arch/libc). Pins augment: anything not pinned
# still resolves live. sha256 is required (an unverifiable pin is rejected).
# runtime_pins:
#   python:
#     "3.11":
#       full: 3.11.13         # optional exact patch for the install dir (defaults to the key)
#       platforms:
#         linux/aarch64/musl:
#           url: https://mirror.internal/cpython-3.11.13-musl.tar.gz
#           sha256: <hex>

# --- Egress: how the sandbox reaches the network for packages/web.fetch ---
# egress_mode: proxy defaults; the sandbox's own tools reach real registries through a
# controller-run MITM proxy over the SSH tunnel (still no direct sandbox internet).
# egress_mode: relay is the stricter air-gap — the controller resolves + relays artifacts
# in; the sandbox never opens a connection. See the README "Egress" section.
# egress_mode: proxy            # proxy | relay
# egress_proxy_port: 18080      # sandbox loopback port the proxy is tunneled to (0 = default)
#
# web.fetch venue routing + gating (controller-venue fetch is a tunnel through the sandbox's
# isolation, so it's gated by default):
# network:
#   unlisted_domain_policy: gate   # gate (hold for approval) | deny
#   allowed_domains:
#     - pattern: "*.pypi.org"
#       reachable_from: sandbox     # sandbox | controller
# fetch_rewrites:                   # redirect/re-venue a URL before gating
#   - match: "https://example.com/*"
#     rewrite_to: "https://mirror.internal/*"
#     venue: sandbox

# --- Tier-1 relay: the controller's own tools, used to resolve+download for air-gapped boxes ---
# controller_python: python3      # for cross-platform wheel relay
# controller_npm: npm
# controller_java: java
# controller_mvn: mvn             # for maven.build's offline .m2 priming
# controller_conda: conda         # (or the drop-in "mamba") for air-gapped conda
#
# Sandbox-reachable mirrors (Tier 0) + the controller-side source (Tier 1):
# pip:   { index_url: "", extra_index_url: "", trusted_host: "", controller_index_url: "" }
# npm:   { registry_url: "", controller_registry: "" }
# maven: { repository_url: "", controller_repository: "" }

# --- Serving + logs ---
# dispatch_concurrency: 4         # how many requests Popo services at once (0 = default 4)
# log_max_bytes: 8388608          # per-generation cap for the rotating log sinks (0 = 8 MiB)
# log_keep: 4                     # rotated generations kept (0 = 4)
# audit_log: ""                   # web.fetch audit JSONL (default ~/.iceclimber/audit/<id>.jsonl)
# activity_log: ""                # request/operator activity JSONL (default ~/.iceclimber/<id>/activity.jsonl)
# approvals_file: ""              # operator allow/deny rules (default ~/.iceclimber/<id>/approvals.json)
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
			// The next-steps line must include `trust` — without a recorded host key,
			// the suggested probe fails (unknown hosts are rejected by design).
			fmt.Fprintf(cmd.OutOrStdout(),
				"wrote %s — next:\n  1. edit it: set ssh host/user/identity_file\n  2. iceclimber trust    # record the sandbox's SSH host key\n  3. iceclimber probe    # verify the connection + fingerprint the box\n", cfgFile)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config")
	return cmd
}
