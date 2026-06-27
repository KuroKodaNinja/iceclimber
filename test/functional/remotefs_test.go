//go:build functional

package functional

import (
	"context"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// TestRemoteFS_Conformance runs the shared conformance suite over BOTH real SSH
// channels against the Alpine VM. This is what proves the two transports behave
// identically on real BusyBox — and exercises stdin-over-exec (cat > file) and
// BusyBox mv for the first time.
func TestRemoteFS_Conformance(t *testing.T) {
	sb := requireSandbox(t)
	for _, transport := range []string{"exec", "sftp"} {
		t.Run(transport, func(t *testing.T) {
			fs, cleanup := dialFS(t, sb, transport)
			defer cleanup()
			remotefstest.RunConformance(t, func(t *testing.T) (remotefs.FS, string) {
				base := "/tmp/ic-conf-" + protocol.NewID()
				if err := fs.Mkdir(context.Background(), base); err != nil {
					t.Fatalf("create base %s: %v", base, err)
				}
				return fs, base
			})
		})
	}
}

// dialFS opens an SSH connection to the sandbox and returns a RemoteFS over the
// requested transport, plus a cleanup func.
func dialFS(t *testing.T, sb sandboxConn, transport string) (remotefs.FS, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := remote.Dial(ctx, remote.DialConfig{
		Host:         sb.Host,
		Port:         sb.Port,
		User:         sb.User,
		IdentityFile: sb.IdentityFile,
		KnownHosts:   sb.KnownHosts,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if transport == "sftp" {
		sc, err := r.NewSFTP()
		if err != nil {
			_ = r.Close()
			t.Fatalf("NewSFTP: %v", err)
		}
		return remotefs.NewSFTPFS(sc), func() { _ = sc.Close(); _ = r.Close() }
	}
	return remotefs.NewExecFS(r), func() { _ = r.Close() }
}
