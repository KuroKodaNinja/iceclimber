package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/spf13/cobra"
)

// The cache commands manage the controller-side artifact cache (internal/relay): the
// fingerprint-keyed downloads every relay-based install reuses — runtime tarballs, the
// static bash, conda packages. It is shared across sandboxes by design (a fleet
// provisions N boxes from one download), so it grows with runtimes × versions × arches
// and needs visibility + trimming. Everything in it is re-downloadable: pruning is
// always safe, only ever costing a re-fetch.

func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage the shared artifact cache (relay downloads: runtimes, shell, conda packages)",
	}
	cmd.AddCommand(newCacheListCmd(), newCachePruneCmd())
	// `gc` predates the real implementation (§9 listed it); keep it as a hidden alias.
	gc := newCachePruneCmd()
	gc.Use = "gc"
	gc.Hidden = true
	cmd.AddCommand(gc)
	return cmd
}

// cacheEntry is one cached file, classified for display/pruning.
type cacheEntry struct {
	Name  string    `json:"name"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
	Stale bool      `json:"stale"` // a .tmp / unpinned stray (always prunable)
	lock  bool      // a .lock companion (never listed; removed with its asset)
}

// cacheDirFor resolves the cache directory: the config's cache_dir when a config is
// loadable, else the shared default every Fetcher uses. Cache commands deliberately work
// without a config — the cache is controller-global, not per-sandbox.
func cacheDirFor() string {
	if cfg, err := config.Load(cfgFile, sandboxID); err == nil && cfg.CacheDir != "" {
		return cfg.CacheDir
	}
	return filepath.Join(os.TempDir(), "iceclimber-cache")
}

// readCache scans dir into entries (files only; missing dir = empty cache).
func readCache(dir string) ([]cacheEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []cacheEntry
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		name := de.Name()
		out = append(out, cacheEntry{
			Name:  name,
			Size:  info.Size(),
			MTime: info.ModTime(),
			Stale: strings.HasSuffix(name, ".tmp") || strings.Contains(name, ".unpinned-"),
			lock:  strings.HasSuffix(name, ".lock"),
		})
	}
	return out, nil
}

func newCacheListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List cached artifacts (size, age); --json for machine-readable output",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := cacheDirFor()
			entries, err := readCache(dir)
			if err != nil {
				return err
			}
			// Assets only in the listing (locks are plumbing); biggest first — the
			// question `list` answers is "what is eating the disk".
			var assets []cacheEntry
			var total int64
			for _, e := range entries {
				if e.lock {
					continue
				}
				assets = append(assets, e)
				total += e.Size
			}
			sort.Slice(assets, func(i, j int) bool { return assets[i].Size > assets[j].Size })

			w := cmd.OutOrStdout()
			if jsonOutput {
				if assets == nil {
					assets = []cacheEntry{}
				}
				return json.NewEncoder(w).Encode(map[string]any{
					"dir": dir, "total_bytes": total, "entries": assets,
				})
			}
			fmt.Fprintf(w, "cache: %s\n", dir)
			if len(assets) == 0 {
				fmt.Fprintln(w, "empty")
				return nil
			}
			for _, e := range assets {
				stale := ""
				if e.Stale {
					stale = "  (stale)"
				}
				fmt.Fprintf(w, "%10s  %8s ago  %s%s\n",
					progress.HumanBytes(e.Size), progress.HumanDur(time.Since(e.MTime).Round(time.Second)), e.Name, stale)
			}
			fmt.Fprintf(w, "total: %s in %d artifact(s) — `iceclimber cache prune [--older-than 720h]` to trim (everything is re-downloadable)\n",
				progress.HumanBytes(total), len(assets))
			return nil
		},
	}
}

func newCachePruneCmd() *cobra.Command {
	var olderThan time.Duration
	var yes bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove cached artifacts (all, or --older-than); always safe — they re-download on demand",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := cacheDirFor()
			entries, err := readCache(dir)
			if err != nil {
				return err
			}
			cutoff := time.Now().Add(-olderThan)
			var victims []cacheEntry
			var bytes int64
			for _, e := range entries {
				if e.lock {
					continue // locks go with their asset, below
				}
				// Stale strays (interrupted .tmp / unpinned temp files) are always pruned;
				// real assets only when older than the cutoff (olderThan 0 = everything).
				if e.Stale || olderThan == 0 || e.MTime.Before(cutoff) {
					victims = append(victims, e)
					bytes += e.Size
				}
			}
			w := cmd.OutOrStdout()
			if len(victims) == 0 {
				fmt.Fprintln(w, "nothing to prune")
				return nil
			}
			if !yes {
				if !isTerminal(os.Stdin) {
					return fmt.Errorf("pruning %d artifact(s) (%s) needs confirmation — pass --yes when scripting",
						len(victims), progress.HumanBytes(bytes))
				}
				ok, err := confirm(cmd, fmt.Sprintf("Remove %d artifact(s), %s? Everything re-downloads on demand. [y/N] ",
					len(victims), progress.HumanBytes(bytes)))
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(w, "aborted")
					return nil
				}
			}
			removed := 0
			for _, e := range victims {
				if err := os.Remove(filepath.Join(dir, e.Name)); err != nil && !os.IsNotExist(err) {
					fmt.Fprintf(w, "  skip %s: %v\n", e.Name, err)
					continue
				}
				_ = os.Remove(filepath.Join(dir, e.Name+".lock")) // the asset's flock companion
				removed++
			}
			fmt.Fprintf(w, "pruned %d artifact(s), %s freed\n", removed, progress.HumanBytes(bytes))
			return nil
		},
	}
	cmd.Flags().DurationVar(&olderThan, "older-than", 0, "only artifacts older than this (e.g. 720h); 0 = everything")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "no confirmation (required when scripting)")
	return cmd
}
