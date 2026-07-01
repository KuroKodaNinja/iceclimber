package java

import (
	"context"
	"fmt"
	"path"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// EgressTrustStorePass is the passphrase for the egress Java truststore. The store holds
// only the public egress CA (no secrets), so the passphrase is not a security boundary —
// it exists because PKCS12 requires one; callers pass it to the JVM via
// -Djavax.net.ssl.trustStorePassword.
const EgressTrustStorePass = "changeit"

// EnsureEgressTrustStore builds, in the sandbox, a PKCS12 truststore containing only the
// egress MITM CA, so a JVM tool (Maven) validates the proxy's minted leaves — the Java
// analogue of the SSL_CERT_FILE/NODE_EXTRA_CA_CERTS knobs that already cover the OpenSSL
// and Node ecosystems (the JVM ignores those). It runs the just-installed JDK's own
// keytool against the CA that bootstrap wrote to <root>/certs/egress-ca.pem, and is
// idempotent: an existing store is reused (keytool would otherwise refuse to re-import).
//
// javaBin is the sandbox JDK's bin/java; keytool sits beside it. caPath and storePath are
// sandbox paths. Because all egress is MITM'd, trusting ONLY this CA is correct — the store
// deliberately does not merge the JDK's bundled cacerts.
func EnsureEgressTrustStore(ctx context.Context, runner remote.Runner, javaBin, caPath, storePath string) error {
	keytool := path.Join(path.Dir(javaBin), "keytool")
	// One shell: reuse an existing store, else import the CA into a fresh PKCS12.
	cmd := fmt.Sprintf(`[ -f %s ] || %s -importcert -noprompt -trustcacerts -alias iceclimber-egress -file %s -keystore %s -storetype PKCS12 -storepass %s`,
		remote.ShellQuote(storePath), remote.ShellQuote(keytool), remote.ShellQuote(caPath),
		remote.ShellQuote(storePath), remote.ShellQuote(EgressTrustStorePass))
	res, err := runner.Run(ctx, cmd, nil)
	if err != nil {
		return fmt.Errorf("build egress Java truststore: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("keytool import of the egress CA failed (exit %d): %s", res.ExitCode, lastLine(res.Stderr))
	}
	return nil
}

// lastLine returns the final non-empty line of b (keytool's error summary), for a compact
// error message.
func lastLine(b []byte) string {
	s := string(b)
	last := ""
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if line := s[start:i]; len(line) > 0 {
				last = line
			}
			start = i + 1
		}
	}
	return last
}
