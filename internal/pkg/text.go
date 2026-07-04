package pkg

import "strings"

// LastLines returns the trailing n non-empty-trimmed lines of b — enough error
// context (a package manager's tail output) without the full log.
func LastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// FirstNonEmpty returns the first argument that is not empty after trimming
// surrounding whitespace, or "" if none qualify. Handy for "config value, else
// default" fallbacks and for picking the first populated message field.
func FirstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
