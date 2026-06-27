package remote

import "testing"

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
