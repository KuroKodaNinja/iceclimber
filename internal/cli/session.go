package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/egress"
	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/runtimes"
	"github.com/KuroKodaNinja/iceclimber/internal/webfetch"
	"github.com/pkg/sftp"
)

// session bundles an open SSH connection, the chosen transport's FS, the
// resolved tree, and the sandbox fingerprint. Shared by bootstrap, serve, install.
type session struct {
	runner           *remote.SSHRunner
	sftp             *sftp.Client // non-nil only for the SFTP transport
	fs               remotefs.FS
	tree             protocol.Tree
	transport        string // "sftp" or "exec" (the one actually selected)
	fp               *probe.Fingerprint
	cacheDir         string
	pip              config.Pip
	npm              config.Npm
	maven            config.Maven
	controllerPython string
	controllerNpm    string
	controllerJava   string
	auditPath        string
	policy           *egress.Policy
	sandboxID        string
	approver         webfetch.Approver // non-nil only in interactive serve
	// Runtime source is resolved LAZILY (runtimeSourcesNow) from these, not cached, so
	// a choice persisted mid-session (e.g. via the console bootstrap modal) takes
	// effect immediately for installs and the serve loop, without a reconnect.
	runtimeStore  *runtimes.Store
	runtimeConfig runtimes.Sources // operator-config overlay (immutable for the session)
}

// runtimeSourcesNow resolves the per-language runtime source at the moment of use:
// config overlay > persisted choice > managed default. The persisted store is read
// fresh each call (cheap) so a mid-session change is honored; a missing/corrupt store
// degrades to the config/managed defaults rather than failing.
func (s *session) runtimeSourcesNow() runtimes.Sources {
	var persisted runtimes.Sources
	if s.runtimeStore != nil {
		persisted, _ = s.runtimeStore.Load()
	}
	return runtimes.Resolve(nil, s.runtimeConfig, persisted, nil, nil)
}

// sessionHolder guards the current session pointer so the reconnect supervisor can
// swap it (Set) on each (re)connect while operator actions (consoleOps) and the
// agent-log bridge read it (Get) concurrently. An action that reads a just-closed
// session simply errors and the operator retries — no shared mutable state beyond
// this pointer hand-off.
type sessionHolder struct {
	mu  sync.Mutex
	cur *session
}

func (h *sessionHolder) Set(s *session) {
	h.mu.Lock()
	h.cur = s
	h.mu.Unlock()
}

func (h *sessionHolder) Get() *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur
}

// systemRuntimePath returns the system interpreter path for a language in system
// mode: the source's explicit Path, else the binary probe discovered on PATH, else
// "" (the manager falls back to the bare name on PATH).
func (s *session) systemRuntimePath(lang string, src runtimes.Source) string {
	if src.Path != "" {
		return src.Path
	}
	if s.fp != nil {
		if rt, ok := s.fp.Runtime(lang); ok {
			return rt.Path
		}
	}
	return ""
}

// condaPath returns the sandbox conda binary the probe discovered (for env_manager
// conda), or "" if none — EnsureEnv errors clearly on the empty case.
func (s *session) condaPath() string {
	if s.fp != nil {
		if rt, ok := s.fp.Runtime("python"); ok {
			return rt.CondaPath
		}
	}
	return ""
}

// Close releases the SFTP client (if any) and the SSH connection.
func (s *session) Close() error {
	if s.sftp != nil {
		_ = s.sftp.Close()
	}
	return s.runner.Close()
}

// openSession dials the sandbox, resolves the install root, and selects a
// RemoteFS transport. "auto" prefers SFTP and falls back to ExecFS; "sftp" and
// "exec" force one (the override exists so the functional suite can exercise
// ExecFS even on a box whose SFTP works).
// dialConfig maps the operator config's SSH block to a remote.DialConfig — the
// single place CLI config becomes connection input, so honoring ~/.ssh/config,
// jumpboxes, and interactive auth applies to every dial site uniformly.
func dialConfig(cfg *config.Config) remote.DialConfig {
	return remote.DialConfig{
		Host:                     cfg.SSH.Host,
		Port:                     cfg.SSH.Port,
		User:                     cfg.SSH.User,
		IdentityFile:             cfg.SSH.IdentityFile,
		KnownHosts:               cfg.SSH.KnownHosts,
		SSHConfigFile:            cfg.SSH.SSHConfigFile,
		UseSSHConfig:             cfg.SSH.UseSSHConfig,
		AllowPassword:            cfg.SSH.PasswordAuth,
		AllowKeyboardInteractive: cfg.SSH.KeyboardInteractive,
		KeepAlive:                time.Duration(cfg.SSH.KeepAliveInterval) * time.Second,
	}
}

func openSession(ctx context.Context, cfg *config.Config, transport string) (*session, error) {
	return openSessionWith(ctx, cfg, transport, nil)
}

// openSessionWith is openSession with an explicit interactive-auth prompter. The
// reconnect supervisor passes a single remote.CachingPrompter across every dial so a
// password typed once is reused on reconnect (see superviseServe). A nil prompter
// uses the default /dev/tty prompter.
func openSessionWith(ctx context.Context, cfg *config.Config, transport string, prompter remote.PasswordPrompter) (*session, error) {
	dc := dialConfig(cfg)
	if prompter != nil {
		dc.Prompter = prompter
	}
	r, err := remote.Dial(ctx, dc)
	if err != nil {
		return nil, fmt.Errorf("connect to sandbox %s: %w", cfg.SandboxID, err)
	}

	// Always fingerprint: install needs OS/arch/libc for the PBS triple even when
	// remote_root is configured.
	fp, err := probe.Run(ctx, r, probe.Options{RemoteRoot: cfg.RemoteRoot})
	if err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("probe sandbox %s: %w", cfg.SandboxID, err)
	}
	root := cfg.RemoteRoot
	if root == "" {
		if root = fp.FirstViableRoot(); root == "" {
			_ = r.Close()
			return nil, fmt.Errorf("no writable install root found; set remote_root in the config")
		}
	}

	s := &session{runner: r, tree: protocol.Tree{Root: root}, fp: fp, cacheDir: cfg.CacheDir, pip: cfg.Pip, npm: cfg.Npm, maven: cfg.Maven, controllerPython: cfg.ControllerPython, controllerNpm: cfg.ControllerNpm, controllerJava: cfg.ControllerJava, auditPath: auditPath(cfg), policy: buildPolicy(cfg), sandboxID: cfg.SandboxID, runtimeStore: runtimesStore(cfg), runtimeConfig: configRuntimeSources(cfg.Runtimes)}
	switch transport {
	case "exec":
		s.fs, s.transport = remotefs.NewExecFS(r), "exec"
	case "sftp":
		sc, err := r.NewSFTP()
		if err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("sftp transport requested but unavailable: %w", err)
		}
		s.sftp, s.fs, s.transport = sc, remotefs.NewSFTPFS(sc), "sftp"
	case "", "auto":
		if sc, err := r.NewSFTP(); err == nil {
			s.sftp, s.fs, s.transport = sc, remotefs.NewSFTPFS(sc), "sftp"
		} else {
			s.fs, s.transport = remotefs.NewExecFS(r), "exec"
		}
	default:
		_ = r.Close()
		return nil, fmt.Errorf("unknown transport %q (want auto|sftp|exec)", transport)
	}
	return s, nil
}

// egressStore opens the operator-owned approvals/pending stores for a config.
// Used by both the session (gating) and the approve/deny/pending CLI (no SSH).
func egressStore(cfg *config.Config) *egress.Store {
	base := cfg.ApprovalsFile
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".iceclimber", cfg.SandboxID, "approvals.json")
	}
	return egress.NewStore(base, filepath.Join(filepath.Dir(base), "pending.json"))
}

// buildPolicy assembles the egress policy from config.
func buildPolicy(cfg *config.Config) *egress.Policy {
	allowed := make([]egress.AllowedDomain, len(cfg.Network.AllowedDomains))
	for i, a := range cfg.Network.AllowedDomains {
		allowed[i] = egress.AllowedDomain{Pattern: a.Pattern, ReachableFrom: a.ReachableFrom}
	}
	rewrites := make([]egress.Rewrite, len(cfg.Rewrites))
	for i, r := range cfg.Rewrites {
		rewrites[i] = egress.Rewrite{Match: r.Match, RewriteTo: r.RewriteTo, Venue: r.Venue}
	}
	return egress.NewPolicy(allowed, rewrites, cfg.Network.UnlistedDomainPolicy, egressStore(cfg))
}

// auditPath returns the configured web.fetch audit log path, or the default
// ~/.iceclimber/audit/<sandbox_id>.jsonl.
func auditPath(cfg *config.Config) string {
	if cfg.AuditLog != "" {
		return cfg.AuditLog
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".iceclimber", "audit", cfg.SandboxID+".jsonl")
}

// activityPath returns the configured activity log path, or the default
// ~/.iceclimber/<sandbox_id>/activity.jsonl (alongside approvals/pending).
func activityPath(cfg *config.Config) string {
	if cfg.ActivityLog != "" {
		return cfg.ActivityLog
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".iceclimber", cfg.SandboxID, "activity.jsonl")
}

// runtimesStore is the controller-side store of the operator's per-language
// runtime-source choice (~/.iceclimber/<sandbox_id>/runtimes.json), written at
// bootstrap and read by install/serve to pick the env strategy.
func runtimesStore(cfg *config.Config) *runtimes.Store {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return runtimes.NewStore(filepath.Join(home, ".iceclimber", cfg.SandboxID, "runtimes.json"))
}

// configRuntimeSources converts the operator config's runtimes block into the
// runtimes model (only languages with an explicit source are included).
func configRuntimeSources(rc config.RuntimesConfig) runtimes.Sources {
	out := runtimes.Sources{}
	add := func(lang string, p config.RuntimePref) {
		if p.Source == "" {
			return
		}
		out[lang] = runtimes.Source{Mode: runtimes.Mode(p.Source), Path: p.Path, EnvManager: p.EnvManager}
	}
	add("python", rc.Python)
	add("node", rc.Node)
	add("java", rc.Java)
	return out
}

// agentLogPath is the controller-side file the serve loop bridges the sandbox agent
// stream into (~/.iceclimber/<sandbox_id>/agent.log) — the default --agent-log for
// the console, `tui`, and `logs`, so the [NANA] pane shows the agent with no flag.
func agentLogPath(cfg *config.Config) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".iceclimber", cfg.SandboxID, "agent.log")
}
