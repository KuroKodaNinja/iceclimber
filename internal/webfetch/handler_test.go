package webfetch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/audit"
	"github.com/KuroKodaNinja/iceclimber/internal/egress"
)

type stubApprover struct {
	choice ApprovalChoice
	seen   ApprovalPrompt
}

func (s *stubApprover) ApproveFetch(_ context.Context, p ApprovalPrompt) ApprovalChoice {
	s.seen = p
	return s.choice
}

// newHeldDeps builds Deps whose policy holds any unlisted URL (gate), backed by a
// temp store, with auditing disabled.
func newHeldDeps(t *testing.T, ap Approver) Deps {
	t.Helper()
	dir := t.TempDir()
	store := egress.NewStore(filepath.Join(dir, "approvals.json"), filepath.Join(dir, "pending.json"))
	policy := egress.NewPolicy(nil, nil, "gate", store)
	return Deps{Policy: policy, Audit: audit.New(""), SandboxID: "test", Approver: ap}
}

func TestRun_Hold_DenyOnce(t *testing.T) {
	ap := &stubApprover{choice: DenyOnce}
	d := newHeldDeps(t, ap)
	out, err := Run(context.Background(), d, "id1", Request{URL: "https://example.com/x"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.Status != "denied" {
		t.Errorf("status = %q, want denied", out.Status)
	}
	if ap.seen.Host != "example.com" || ap.seen.RequestID != "id1" {
		t.Errorf("approver prompt = %+v", ap.seen)
	}
	if len(d.Policy.Store().Deny()) != 0 {
		t.Errorf("DenyOnce must not persist a rule: %v", d.Policy.Store().Deny())
	}
}

func TestRun_Hold_DenyRemember(t *testing.T) {
	d := newHeldDeps(t, &stubApprover{choice: DenyRemember})
	out, _ := Run(context.Background(), d, "id1", Request{URL: "https://example.com/x"})
	if out.Status != "denied" {
		t.Fatalf("status = %q, want denied", out.Status)
	}
	if got := d.Policy.Store().Deny(); len(got) != 1 || got[0] != "https://example.com/*" {
		t.Errorf("deny rule = %v, want [https://example.com/*]", got)
	}
}

func TestRun_Hold_NilApprover_FallsBackToPending(t *testing.T) {
	d := newHeldDeps(t, nil)
	out, _ := Run(context.Background(), d, "id1", Request{URL: "https://example.com/x"})
	if out.Status != "needs_clarification" {
		t.Fatalf("status = %q, want needs_clarification", out.Status)
	}
	if p := d.Policy.Store().Pending(); len(p) != 1 || p[0].ID != "id1" {
		t.Errorf("pending = %+v, want one entry id1", p)
	}
}
