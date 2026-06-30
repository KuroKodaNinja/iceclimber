package python

import (
	"context"
	"os"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// extractAndPush opens the cached tarball and streams it into the sandbox via the
// shared remotefs push, which strips the top-level "python/" component and uses the
// bulk `tar` transfer on SFTP-less sandboxes (per-file writes otherwise).
func (i *Installer) extractAndPush(ctx context.Context, tarball, target string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	// Report transfer progress against the compressed tarball size — the same gz
	// reader is consumed by both the bulk-tar (exec) and per-file (SFTP) paths.
	var total int64
	if st, err := f.Stat(); err == nil {
		total = st.Size()
	}
	src := i.cfg.Progress.Reader(f, "transferring", total)
	return remotefs.PushTarGz(ctx, i.cfg.FS, src, target)
}
