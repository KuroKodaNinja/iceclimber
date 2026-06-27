package cli

import (
	"context"
	"fmt"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/pkg/sftp"
)

// session bundles an open SSH connection, the chosen transport's FS, the
// resolved tree, and the sandbox fingerprint. Shared by bootstrap, serve, install.
type session struct {
	runner    *remote.SSHRunner
	sftp      *sftp.Client // non-nil only for the SFTP transport
	fs        remotefs.FS
	tree      protocol.Tree
	transport string // "sftp" or "exec" (the one actually selected)
	fp        *probe.Fingerprint
	cacheDir  string
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
func openSession(ctx context.Context, cfg *config.Config, transport string) (*session, error) {
	r, err := remote.Dial(ctx, remote.DialConfig{
		Host:         cfg.SSH.Host,
		Port:         cfg.SSH.Port,
		User:         cfg.SSH.User,
		IdentityFile: cfg.SSH.IdentityFile,
		KnownHosts:   cfg.SSH.KnownHosts,
	})
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

	s := &session{runner: r, tree: protocol.Tree{Root: root}, fp: fp, cacheDir: cfg.CacheDir}
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
