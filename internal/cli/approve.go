package cli

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/egress"
	"github.com/spf13/cobra"
)

// These commands operate on the controller-side approvals/pending stores — no
// SSH connection needed.

// newEgressCmd groups the egress-policy surface: held requests (pending/approve/deny)
// and the persisted allow/deny rules (rules list/forget). The rules subcommands are the
// CLI parity for the console's egress panel, which could already list + forget rules —
// the CLI previously couldn't at all. pending/approve/deny also remain available as
// top-level commands (hidden aliases) so existing scripts keep working.
func newEgressCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "Egress policy: held requests (pending/approve/deny) + persisted rules",
	}
	cmd.AddCommand(newPendingCmd(), newApproveCmd(), newDenyCmd(), newEgressRulesCmd())
	return cmd
}

// newEgressRulesCmd manages the persisted allow/deny rules (approvals.json).
func newEgressRulesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Persisted egress allow/deny rules",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the persisted allow/deny rules (and pending count)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			store := egressStore(cfg)
			allow, deny, pending := store.Allow(), store.Deny(), store.Pending()
			w := cmd.OutOrStdout()
			if jsonOutput {
				return json.NewEncoder(w).Encode(map[string]any{
					"allow": allow, "deny": deny, "pending": len(pending),
				})
			}
			if len(allow) == 0 && len(deny) == 0 {
				fmt.Fprintln(w, "no persisted rules")
			}
			for _, r := range allow {
				fmt.Fprintf(w, "allow  %s\n", r)
			}
			for _, r := range deny {
				fmt.Fprintf(w, "deny   %s\n", r)
			}
			if n := len(pending); n > 0 {
				fmt.Fprintf(w, "(%d pending request(s) — `iceclimber egress pending`)\n", n)
			}
			return nil
		},
	}

	var kind string
	forget := &cobra.Command{
		Use:   "forget <pattern>",
		Short: "Remove a persisted allow/deny rule (exact pattern; see `rules list`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			store := egressStore(cfg)
			pattern := args[0]
			inAllow, inDeny := slices.Contains(store.Allow(), pattern), slices.Contains(store.Deny(), pattern)
			switch {
			case kind == "allow" || (kind == "" && inAllow && !inDeny):
				if !inAllow {
					return fmt.Errorf("no allow rule %q (see `iceclimber egress rules list`)", pattern)
				}
				if err := store.RemoveAllow(pattern); err != nil {
					return err
				}
			case kind == "deny" || (kind == "" && inDeny && !inAllow):
				if !inDeny {
					return fmt.Errorf("no deny rule %q (see `iceclimber egress rules list`)", pattern)
				}
				if err := store.RemoveDeny(pattern); err != nil {
					return err
				}
			case inAllow && inDeny:
				return fmt.Errorf("%q is both an allow and a deny rule — disambiguate with --kind allow|deny", pattern)
			default:
				return fmt.Errorf("no rule %q (see `iceclimber egress rules list`)", pattern)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "forgot rule %s\n", pattern)
			return nil
		},
	}
	forget.Flags().StringVar(&kind, "kind", "", "which list to remove from: allow|deny (needed only when a pattern is in both)")

	cmd.AddCommand(list, forget)
	return cmd
}

func newPendingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pending",
		Short: "List controller-venue fetches held for egress approval",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			entries := egressStore(cfg).Pending()
			w := cmd.OutOrStdout()
			if jsonOutput {
				if entries == nil {
					entries = []egress.PendingEntry{}
				}
				return json.NewEncoder(w).Encode(entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(w, "no pending requests")
				return nil
			}
			for _, e := range entries {
				fmt.Fprintf(w, "%s  %s  (%s)\n", e.ID, e.URL, e.TS)
			}
			return nil
		},
	}
}

func newApproveCmd() *cobra.Command {
	var remember string
	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve a held fetch — persists an allow rule (host-level by default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			store := egressStore(cfg)
			entry, ok, err := store.RemovePending(args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no pending request with id %s (see `iceclimber pending`)", args[0])
			}
			rule := remember
			if rule == "" {
				rule = egress.HostGlob(entry.URL)
			}
			if err := store.AddAllow(rule); err != nil {
				return err
			}
			_ = activity.New(activityPath(cfg)).Append(activity.Event{
				Kind: activity.KindApproved, ID: entry.ID, Type: "web.fetch", Detail: entry.URL,
			})
			fmt.Fprintf(cmd.OutOrStdout(), "approved %s\n  allow rule: %s\n  re-submit the fetch to proceed.\n", entry.URL, rule)
			return nil
		},
	}
	cmd.Flags().StringVar(&remember, "remember", "", "persist a custom allow glob instead of the host (e.g. https://*.example.com/*)")
	return cmd
}

func newDenyCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "deny <id>",
		Short: "Deny a held fetch — persists a host deny rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			store := egressStore(cfg)
			entry, ok, err := store.RemovePending(args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no pending request with id %s", args[0])
			}
			rule := egress.HostGlob(entry.URL)
			if err := store.AddDeny(rule); err != nil {
				return err
			}
			_ = activity.New(activityPath(cfg)).Append(activity.Event{
				Kind: activity.KindDenied, ID: entry.ID, Type: "web.fetch", Detail: entry.URL,
			})
			fmt.Fprintf(cmd.OutOrStdout(), "denied %s\n  deny rule: %s\n  reason: %s\n", entry.URL, rule, reason)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason for the denial (recorded for the operator)")
	return cmd
}
