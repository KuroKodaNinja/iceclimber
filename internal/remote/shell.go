package remote

import "strings"

// ShellQuote single-quotes s for safe interpolation into a POSIX sh command.
// Used by anything that builds remote shell commands (probe, ExecFS).
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
