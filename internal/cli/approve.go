package cli

import (
	"fmt"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/egress"
	"github.com/spf13/cobra"
)

// These commands operate on the controller-side approvals/pending stores — no
// SSH connection needed.

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
