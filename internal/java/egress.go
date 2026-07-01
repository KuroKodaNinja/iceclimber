package java

import (
	"context"
	"fmt"
	"path"
	"strings"

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
	tmp := storePath + ".tmp"
	// One shell: reuse an existing store, else import the CA into a FRESH temp PKCS12 and
	// atomically rename it into place — so a killed/half-written keytool never leaves a
	// truncated file the `-f` guard would then trust forever (the store would be there but
	// unusable, and Maven would fail TLS with no self-heal).
	cmd := fmt.Sprintf(`[ -f %s ] || { rm -f %s && %s -importcert -noprompt -trustcacerts -alias iceclimber-egress -file %s -keystore %s -storetype PKCS12 -storepass %s && mv %s %s; }`,
		remote.ShellQuote(storePath), remote.ShellQuote(tmp), remote.ShellQuote(keytool), remote.ShellQuote(caPath),
		remote.ShellQuote(tmp), remote.ShellQuote(EgressTrustStorePass), remote.ShellQuote(tmp), remote.ShellQuote(storePath))
	res, err := runner.Run(ctx, cmd, nil)
	if err != nil {
		return fmt.Errorf("build egress Java truststore: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("keytool import of the egress CA failed (exit %d): %s", res.ExitCode, lastNonEmptyLine(res.Stderr))
	}
	return nil
}

// lastNonEmptyLine returns the final non-empty line of b (keytool's error summary), trimmed
// of a trailing CR, for a compact error message.
func lastNonEmptyLine(b []byte) string {
	last := ""
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			last = line
		}
	}
	return last
}
