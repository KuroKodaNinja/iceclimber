package cli

import (
	"bytes"
	"strings"
	"testing"
)

func newTestAsker(input string) (*terminalAsker, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return newTerminalAsker(strings.NewReader(input), out), out
}

func TestTerminalAsker_Parse(t *testing.T) {
	for in, want := range map[string]choice{
		"y\n": choiceApproveOnce,
		"a\n": choiceApproveRemember,
		"n\n": choiceDenyOnce,
		"d\n": choiceDenyRemember,
	} {
		ta, _ := newTestAsker(in)
		if got := ta.ask(prompt{title: "x"}); got != want {
			t.Errorf("input %q → %v, want %v", in, got, want)
		}
	}
}

func TestTerminalAsker_EOFDenies(t *testing.T) {
	ta, _ := newTestAsker("")
	if got := ta.ask(prompt{title: "x"}); got != choiceDenyOnce {
		t.Errorf("EOF should deny, got %v", got)
	}
}

func TestTerminalAsker_Reprompt(t *testing.T) {
	ta, out := newTestAsker("huh\ny\n")
	if got := ta.ask(prompt{title: "x"}); got != choiceApproveOnce {
		t.Errorf("got %v, want approve-once", got)
	}
	if !strings.Contains(out.String(), "please answer") {
		t.Error("expected a re-prompt hint after unknown input")
	}
}

func TestTerminalAsker_Render(t *testing.T) {
	ta, out := newTestAsker("y\n")
	ta.ask(prompt{
		sandbox: "demo", title: "Install Python", kind: "operation",
		fields: [][2]string{{"version", "3.12"}}, rememberLabel: "approve all python.install",
	})
	s := out.String()
	for _, want := range []string{"demo", "Install Python", "3.12", "approve all python.install"} {
		if !strings.Contains(s, want) {
			t.Errorf("render missing %q:\n%s", want, s)
		}
	}
}
