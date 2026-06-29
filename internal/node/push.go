package node

import (
	"context"
	"os"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// extractAndPush opens the cached tarball and streams it into target via the shared
// remotefs push, which strips the tarball's top-level directory and uses the bulk
// `tar` transfer on SFTP-less sandboxes (per-file writes otherwise).
func (i *Installer) extractAndPush(ctx context.Context, tarball, target string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	return remotefs.PushTarGz(ctx, i.cfg.FS, f, target)
}
