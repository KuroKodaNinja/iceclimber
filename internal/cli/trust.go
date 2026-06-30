package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/spf13/cobra"
)

func newTrustCmd() *cobra.Command {
	var (
		accept     bool
		wantFP     string
		replace    bool
		knownHosts string
	)
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Record the sandbox's SSH host key in known_hosts (the in-CLI ssh-keyscan)",
		Long: "Fetches the host key the sandbox offers, shows its SHA256 fingerprint, and\n" +
			"records it in known_hosts so iceclimber can connect — replacing the\n" +
			"out-of-band `ssh-keyscan`/`ssh` step, which is especially painful for\n" +
			"ephemeral sandboxes. The key is never trusted silently: on a terminal you\n" +
			"confirm the fingerprint; for automation pass --fingerprint (verify against a\n" +
			"value you captured at provision time) or --yes (trust the current network).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			khPath := knownHosts
			if khPath == "" {
				khPath = cfg.SSH.KnownHosts
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			return runTrust(ctx, cmd, cfg, khPath, trustOpts{accept: accept, wantFP: wantFP, replace: replace})
		},
	}
	cmd.Flags().BoolVarP(&accept, "yes", "y", false, "record without prompting (trusts the current network path)")
	cmd.Flags().StringVar(&wantFP, "fingerprint", "", "accept only if the offered key matches this SHA256 fingerprint")
	cmd.Flags().BoolVar(&replace, "replace", false, "replace an existing, different key for this host (rotation / reused ephemeral address)")
	cmd.Flags().StringVar(&knownHosts, "known-hosts", "", "known_hosts file to write (default: the config's, else ~/.ssh/known_hosts)")
	return cmd
}

type trustOpts struct {
	accept  bool
	wantFP  string
	replace bool
}

func runTrust(ctx context.Context, cmd *cobra.Command, cfg *config.Config, khPath string, opt trustOpts) error {
	w := cmd.OutOrStdout()
	dc := dialConfig(cfg)

	// Resolve the effective target (honors ~/.ssh/config + ProxyJump), so we
	// verify and record the key against the resolved HostName:Port — and reach a
	// host behind a bastion. When no explicit known_hosts is set, use the resolved
	// UserKnownHostsFile so it interoperates with the operator's normal ssh.
	host, port, resolvedKH, err := remote.ResolveTarget(ctx, dc)
	if err != nil {
		return fmt.Errorf("resolve ssh config for %s: %w", cfg.SandboxID, err)
	}
	if khPath == "" {
		khPath = resolvedKH
	}

	key, err := remote.FetchHostKey(ctx, dc)
	if err != nil {
		return fmt.Errorf("fetch host key for %s: %w", cfg.SandboxID, err)
	}
	fp := remote.Fingerprint(key)

	state, err := remote.CheckHostKey(khPath, host, port, key)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "sandbox:     %s (%s@%s:%d)\n", cfg.SandboxID, cfg.SSH.User, host, port)
	fmt.Fprintf(w, "host key:    %s\n", key.Type())
	fmt.Fprintf(w, "fingerprint: %s\n", fp)

	switch state {
	case remote.TrustTrusted:
		fmt.Fprintln(w, "already trusted — this exact key is already in known_hosts. Nothing to do.")
		return nil
	case remote.TrustMismatch:
		fmt.Fprintln(w, "WARNING: a DIFFERENT key is already recorded for this host.")
		fmt.Fprintln(w, "         This is expected after a rebuild/rotation, but it is also exactly what a")
		fmt.Fprintln(w, "         man-in-the-middle looks like. Only proceed if you expected the key to change.")
		if !opt.replace {
			return fmt.Errorf("host key changed; re-run with --replace to overwrite the recorded key")
		}
	}

	// Verify against an expected fingerprint when given (the safe automation path).
	if opt.wantFP != "" {
		if !fingerprintMatches(opt.wantFP, fp) {
			return fmt.Errorf("offered fingerprint %s does not match expected %s — NOT recording", fp, opt.wantFP)
		}
		fmt.Fprintln(w, "fingerprint matches --fingerprint ✓")
	} else if !opt.accept {
		ok, err := confirm(cmd, fmt.Sprintf("Record this host key in known_hosts? [y/N] "))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(w, "aborted — host key not recorded.")
			return nil
		}
	}

	if err := remote.RecordHostKey(khPath, host, port, key, opt.replace); err != nil {
		return err
	}
	resolved, _ := remote.ResolveKnownHosts(khPath)
	fmt.Fprintf(w, "recorded host key in %s — iceclimber can now connect.\n", resolved)
	return nil
}

// fingerprintMatches compares a user-supplied fingerprint against the offered one,
// tolerating a missing "SHA256:" prefix.
func fingerprintMatches(want, got string) bool {
	norm := func(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "SHA256:") }
	return norm(want) == norm(got)
}

// confirm reads a y/N answer from the command's input stream.
func confirm(cmd *cobra.Command, prompt string) (bool, error) {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

func portOr22(p int) int {
	if p == 0 {
		return 22
	}
	return p
}
