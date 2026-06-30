package java

import (
	"context"
	"os"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// extractAndPush opens the cached tarball and streams it into target via the shared
// remotefs push, which strips the JDK tarball's top-level directory and uses the
// bulk `tar` transfer on SFTP-less sandboxes (per-file writes otherwise).
func (i *Installer) extractAndPush(ctx context.Context, tarball, target string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	var total int64
	if st, err := f.Stat(); err == nil {
		total = st.Size()
	}
	src := i.cfg.Progress.Reader(f, "transferring", total)
	return remotefs.PushTarGz(ctx, i.cfg.FS, src, target)
}
