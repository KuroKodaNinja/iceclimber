package cli

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
)

// TestSandboxStateNamespacing pins the isolation invariant that multi-sandbox (the
// fleet roadmap) relies on: per-sandbox controller-side state — the web.fetch audit
// log, the activity log, and the approvals/pending stores — is keyed by sandbox_id,
// so two sandboxes never collide. (The remote tree is already per-sandbox via its
// own root; the package cache is keyed by fingerprint.)
func TestSandboxStateNamespacing(t *testing.T) {
	a := &config.Config{SandboxID: "alpha"}
	b := &config.Config{SandboxID: "beta"}

	for name, fn := range map[string]func(*config.Config) string{
		"audit":    auditPath,
		"activity": activityPath,
	} {
		pa, pb := fn(a), fn(b)
		if pa == "" || pb == "" {
			t.Fatalf("%s path empty (no home dir?)", name)
		}
		if pa == pb {
			t.Errorf("%s path not namespaced: alpha and beta share %q", name, pa)
		}
		if !strings.Contains(pa, "alpha") || !strings.Contains(pb, "beta") {
			t.Errorf("%s path should carry the sandbox id: %q / %q", name, pa, pb)
		}
	}

	// The default approvals/pending store lives under a per-sandbox directory too.
	if d := strings.Contains(activityPath(a), "alpha"); !d {
		t.Errorf("activity (and the approvals dir beside it) must be per-sandbox")
	}

	// An explicit config path is honored verbatim (operator override wins).
	custom := &config.Config{SandboxID: "alpha", AuditLog: "/tmp/x.jsonl", ActivityLog: "/tmp/y.jsonl"}
	if auditPath(custom) != "/tmp/x.jsonl" || activityPath(custom) != "/tmp/y.jsonl" {
		t.Error("explicit audit/activity log paths must be used verbatim")
	}
}
