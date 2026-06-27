package remotefs_test

import (
	"net"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
	"github.com/pkg/sftp"
)

func TestExecFS_Local(t *testing.T) {
	remotefstest.RunConformance(t, func(t *testing.T) (remotefs.FS, string) {
		return remotefs.NewExecFS(remotefstest.LocalRunner{}), t.TempDir()
	})
}

func TestSFTPFS_Local(t *testing.T) {
	remotefstest.RunConformance(t, func(t *testing.T) (remotefs.FS, string) {
		return remotefs.NewSFTPFS(newPipeSFTP(t)), t.TempDir()
	})
}

// newPipeSFTP wires a pkg/sftp client to an in-process server over a full-duplex
// net.Pipe, so SFTPFS conformance runs with no SSH and no VM. The server serves
// the host filesystem, so absolute temp-dir paths work. Teardown closes the
// conns directly — closing the pipe unblocks the client's recv goroutine, which
// client.Close() would otherwise wait on forever.
func newPipeSFTP(t *testing.T) *sftp.Client {
	t.Helper()
	serverConn, clientConn := net.Pipe()

	server, err := sftp.NewServer(serverConn)
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	go func() { _ = server.Serve() }()

	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	return client
}
