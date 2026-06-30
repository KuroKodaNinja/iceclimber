package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/webfetch"
)

// TestApprover_RememberedDenyLogsOnce: a remembered "deny all <type>" must log a
// single denial (the operator's one decision), not re-log on every auto-applied
// request — which was inflating the denied counter.
func TestApprover_RememberedDenyLogsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	fa := &fakeAsker{choices: []choice{choiceDenyRemember}}
	ap := newApprover(fa, "sbx", activity.New(path))

	for i := 0; i < 3; i++ { // first prompts deny-all; the rest are auto-applied
		if err := ap.gate(context.Background(), areq("pip.install", `{}`)); err == nil {
			t.Fatalf("request %d: remembered deny should still deny", i)
		}
	}
	evs, _ := activity.Read(path)
	denied := 0
	for _, e := range evs {
		if e.Kind == activity.KindDenied {
			denied++
		}
	}
	if denied != 1 {
		t.Errorf("remembered deny logged %d denials, want 1 (auto-applied must not re-log)", denied)
	}
	if fa.i != 1 {
		t.Errorf("operator was prompted %d times, want 1 (the rest remembered)", fa.i)
	}
}

// fakeAsker returns programmed choices and records the prompts it saw.
type fakeAsker struct {
	choices []choice
	i       int
	asked   []prompt
}

func (f *fakeAsker) ask(p prompt) choice {
	f.asked = append(f.asked, p)
	c := f.choices[f.i%len(f.choices)]
	f.i++
	return c
}

func newFakeApprover(choices ...choice) (*approver, *fakeAsker) {
	fa := &fakeAsker{choices: choices}
	return newApprover(fa, "sbx", nil), fa
}

func areq(typ, params string) protocol.Request {
	return protocol.Request{ID: "r1", Type: typ, Params: json.RawMessage(params)}
}

func TestApprover_GateApproveAndDeny(t *testing.T) {
	a, _ := newFakeApprover(choiceApproveOnce)
	if err := a.gate(context.Background(), areq("python.install", `{"version":"3.12"}`)); err != nil {
		t.Fatalf("approve once: %v", err)
	}
	a, _ = newFakeApprover(choiceDenyOnce)
	if err := a.gate(context.Background(), areq("pip.install", `{}`)); err == nil {
		t.Fatal("deny once should error")
	}
}

func TestApprover_GateSkipsPingAndFetch(t *testing.T) {
	a, fa := newFakeApprover(choiceDenyOnce) // would deny if it ever asked
	if err := a.gate(context.Background(), areq("ping", `{}`)); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := a.gate(context.Background(), areq("web.fetch", `{}`)); err != nil {
		t.Fatalf("web.fetch: %v", err)
	}
	if len(fa.asked) != 0 {
		t.Errorf("ping/web.fetch must not prompt at the gate, asked %d", len(fa.asked))
	}
}

func TestApprover_RememberSuppresses(t *testing.T) {
	a, fa := newFakeApprover(choiceApproveRemember)
	r := areq("pip.install", `{"python_version":"3.12","packages":[{"name":"rich"}]}`)
	if err := a.gate(context.Background(), r); err != nil {
		t.Fatalf("approve all: %v", err)
	}
	if err := a.gate(context.Background(), r); err != nil { // remembered: no prompt
		t.Fatalf("remembered approve: %v", err)
	}
	if len(fa.asked) != 1 {
		t.Errorf("remembered approve should ask once, asked %d", len(fa.asked))
	}
}

func TestApprover_DenyRememberSuppresses(t *testing.T) {
	a, fa := newFakeApprover(choiceDenyRemember)
	r := areq("pip.install", `{}`)
	if err := a.gate(context.Background(), r); err == nil {
		t.Fatal("deny+remember should error")
	}
	if err := a.gate(context.Background(), r); err == nil {
		t.Fatal("remembered deny should error")
	}
	if len(fa.asked) != 1 {
		t.Errorf("remembered deny should ask once, asked %d", len(fa.asked))
	}
}

func TestApprover_ApproveFetchRouting(t *testing.T) {
	for in, want := range map[choice]webfetch.ApprovalChoice{
		choiceApproveOnce:     webfetch.ApproveOnce,
		choiceApproveRemember: webfetch.ApproveRemember,
		choiceDenyOnce:        webfetch.DenyOnce,
		choiceDenyRemember:    webfetch.DenyRemember,
	} {
		a, _ := newFakeApprover(in)
		got := a.ApproveFetch(context.Background(), webfetch.ApprovalPrompt{Host: "x.com", URL: "https://x.com/y", Method: "GET"})
		if got != want {
			t.Errorf("choice %v → %v, want %v", in, got, want)
		}
	}
}

func TestApprover_PromptContext(t *testing.T) {
	a, fa := newFakeApprover(choiceApproveOnce)
	a.gate(context.Background(), areq("pip.install",
		`{"python_version":"3.12","packages":[{"name":"rich","version":"15.0.0"},{"name":"pyfiglet"}]}`))
	if len(fa.asked) != 1 {
		t.Fatalf("expected one prompt, got %d", len(fa.asked))
	}
	p := fa.asked[0]
	if p.sandbox != "sbx" {
		t.Errorf("prompt.sandbox = %q, want sbx", p.sandbox)
	}
	var pkgs string
	for _, f := range p.fields {
		if f[0] == "packages" {
			pkgs = f[1]
		}
	}
	if pkgs != "rich 15.0.0, pyfiglet" {
		t.Errorf("packages summary = %q", pkgs)
	}
}
