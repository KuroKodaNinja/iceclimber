package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/webfetch"
)

func newTestApprover(input string) (*terminalApprover, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return newTerminalApprover(strings.NewReader(input), out, "sbx", nil, nil), out
}

func areq(typ, params string) protocol.Request {
	return protocol.Request{ID: "r1", Type: typ, Params: json.RawMessage(params)}
}

func TestApproverGate_ApproveAndDeny(t *testing.T) {
	a, _ := newTestApprover("y\n")
	if err := a.gate(context.Background(), areq("python.install", `{"version":"3.12"}`)); err != nil {
		t.Fatalf("approve once: %v", err)
	}
	a, _ = newTestApprover("n\n")
	if err := a.gate(context.Background(), areq("pip.install", `{}`)); err == nil {
		t.Fatal("deny once should return an error")
	}
}

func TestApproverGate_SkipsPingAndFetch(t *testing.T) {
	a, out := newTestApprover("") // no input available
	if err := a.gate(context.Background(), areq("ping", `{}`)); err != nil {
		t.Fatalf("ping should auto-pass: %v", err)
	}
	if err := a.gate(context.Background(), areq("web.fetch", `{}`)); err != nil {
		t.Fatalf("web.fetch should be skipped by the gate (self-gates): %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("no prompt expected for ping/web.fetch, got: %q", out.String())
	}
}

func TestApproverGate_RememberSuppresses(t *testing.T) {
	a, _ := newTestApprover("a\n") // approve all — only ONE input line
	r := areq("pip.install", `{"python_version":"3.12","packages":[{"name":"rich"}]}`)
	if err := a.gate(context.Background(), r); err != nil {
		t.Fatalf("approve all: %v", err)
	}
	if err := a.gate(context.Background(), r); err != nil { // remembered: no prompt, no input
		t.Fatalf("remembered approve: %v", err)
	}
}

func TestApproverGate_DenyRememberSuppresses(t *testing.T) {
	a, _ := newTestApprover("d\n")
	r := areq("pip.install", `{}`)
	if err := a.gate(context.Background(), r); err == nil {
		t.Fatal("deny+remember should error")
	}
	if err := a.gate(context.Background(), r); err == nil { // remembered deny, no input
		t.Fatal("remembered deny should error")
	}
}

func TestApproveFetch_Routing(t *testing.T) {
	for in, want := range map[string]webfetch.ApprovalChoice{
		"y\n": webfetch.ApproveOnce,
		"a\n": webfetch.ApproveRemember,
		"n\n": webfetch.DenyOnce,
		"d\n": webfetch.DenyRemember,
	} {
		a, _ := newTestApprover(in)
		got := a.ApproveFetch(context.Background(), webfetch.ApprovalPrompt{Host: "x.com", URL: "https://x.com/y", Method: "GET"})
		if got != want {
			t.Errorf("input %q → %v, want %v", in, got, want)
		}
	}
}

func TestAsk_EOFDenies(t *testing.T) {
	a, _ := newTestApprover("") // immediate EOF
	if got := a.ask(prompt{title: "x"}); got != choiceDenyOnce {
		t.Errorf("EOF should deny, got %v", got)
	}
}

func TestAsk_RepromptsOnUnknown(t *testing.T) {
	a, out := newTestApprover("huh\ny\n")
	if got := a.ask(prompt{title: "x"}); got != choiceApproveOnce {
		t.Errorf("got %v, want approve-once", got)
	}
	if !strings.Contains(out.String(), "please answer") {
		t.Error("expected a re-prompt hint after unknown input")
	}
}

func TestSummarizeRequest_Pip(t *testing.T) {
	title, fields, note := summarizeRequest(areq("pip.install",
		`{"python_version":"3.12","packages":[{"name":"rich","version":"15.0.0"},{"name":"pyfiglet"}]}`))
	if !strings.Contains(title, "packages") || note == "" {
		t.Errorf("title=%q note=%q", title, note)
	}
	var pkgs string
	for _, f := range fields {
		if f[0] == "packages" {
			pkgs = f[1]
		}
	}
	if pkgs != "rich 15.0.0, pyfiglet" {
		t.Errorf("packages summary = %q", pkgs)
	}
}
