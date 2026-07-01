package java

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

type fakeRunner struct {
	lastCmd string
	exit    int
	stderr  string
}

func (f *fakeRunner) Run(_ context.Context, cmd string, _ io.Reader) (remote.Result, error) {
	f.lastCmd = cmd
	return remote.Result{Stderr: []byte(f.stderr), ExitCode: f.exit}, nil
}
func (f *fakeRunner) Close() error { return nil }

func TestEnsureEgressTrustStore_Command(t *testing.T) {
	fr := &fakeRunner{}
	err := EnsureEgressTrustStore(context.Background(), fr, "/root/runtimes/java/21/bin/java",
		"/root/certs/egress-ca.pem", "/root/certs/java-truststore.p12")
	if err != nil {
		t.Fatalf("EnsureEgressTrustStore: %v", err)
	}
	cmd := fr.lastCmd
	// keytool must be resolved next to bin/java, import the CA into the target store as PKCS12,
	// and be idempotent (skip when the store already exists).
	for _, want := range []string{
		"[ -f '/root/certs/java-truststore.p12' ] ||",
		"/root/runtimes/java/21/bin/keytool",
		"-importcert -noprompt -trustcacerts -alias iceclimber-egress",
		"-file '/root/certs/egress-ca.pem'",
		"-keystore '/root/certs/java-truststore.p12'",
		"-storetype PKCS12 -storepass '" + EgressTrustStorePass + "'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("keytool command missing %q\ngot: %s", want, cmd)
		}
	}
}

func TestEnsureEgressTrustStore_KeytoolFailure(t *testing.T) {
	fr := &fakeRunner{exit: 1, stderr: "keytool error: java.io.IOException: keystore password was incorrect"}
	err := EnsureEgressTrustStore(context.Background(), fr, "/j/bin/java", "/ca.pem", "/store.p12")
	if err == nil || !strings.Contains(err.Error(), "keystore password was incorrect") {
		t.Errorf("expected the keytool stderr surfaced, got: %v", err)
	}
}
