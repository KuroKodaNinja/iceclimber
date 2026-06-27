package remotefs_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
	"github.com/pkg/sftp"
)

// localRunner runs ExecFS's commands through the host's own /bin/sh. On macOS
// that's a BSD userland — a second POSIX target alongside the VM's BusyBox, so
// the local suite already guards against GNU-isms.
type localRunner struct{}

func (localRunner) Run(ctx context.Context, cmd string, stdin io.Reader) (remote.Result, error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	if stdin != nil {
		c.Stdin = stdin
	}
	var out, errb bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errb
	err := c.Run()
	res := remote.Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}

func (localRunner) Close() error { return nil }

func TestExecFS_Local(t *testing.T) {
	remotefstest.RunConformance(t, func(t *testing.T) (remotefs.FS, string) {
		return remotefs.NewExecFS(localRunner{}), t.TempDir()
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
