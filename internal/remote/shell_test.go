package remote

import (
	"os/exec"
	"testing"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/home/agent/.iceclimber", `'/home/agent/.iceclimber'`},
		{"/has 'quote'", `'/has '\''quote'\'''`},
	}
	for _, tt := range tests {
		if got := ShellQuote(tt.in); got != tt.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestShellQuoteRoundTrip is the real safety property: a quoted string, handed back
// to /bin/sh, must reproduce the original verbatim — no injection, no expansion.
// The adversarial inputs (command substitution, separators, etc.) must come back
// literally.
func TestShellQuoteRoundTrip(t *testing.T) {
	inputs := []string{
		"", "plain", "a b c", "it's", "''", "a'b'c",
		"$(whoami)", "`id`", "${HOME}", "a; rm -rf /", "a && b", "a|b", "a>b",
		"tab\tand\nnewline", `back\slash`, "ünîcödé/路径", "--flag", "*?[glob]",
	}
	for _, in := range inputs {
		// printf %s <quoted> echoes exactly the bytes sh parsed from the quoted form.
		out, err := exec.Command("sh", "-c", "printf %s "+ShellQuote(in)).Output()
		if err != nil {
			t.Fatalf("sh failed for %q: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("round-trip of %q via ShellQuote = %q (not injection-safe)", in, string(out))
		}
	}
}
