package pkg

import (
	"fmt"
	"strings"
)

// FlagSpec describes one allowlisted package-manager flag the agent may pass through
// via extra_args. TakesValue marks flags whose following token is a value (e.g.
// --index-url URL), so that value is permitted without being an allowlisted flag.
type FlagSpec struct {
	TakesValue bool
}

// ValidateExtraArgs checks that every token in args is either an allowlisted flag
// (long or short, with or without an inline =value) or the value of a value-taking
// flag. Bare positional arguments are rejected — packages come from the request
// specs, never from extra_args — so the agent can't smuggle in extra targets or
// shell. Returns a clear error naming the offending token. The args are NOT shell —
// callers still quote each token when building the command line.
func ValidateExtraArgs(args []string, allow map[string]FlagSpec) error {
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if !strings.HasPrefix(tok, "-") {
			return fmt.Errorf("extra_args: unexpected argument %q (packages go in the request, not extra_args)", tok)
		}
		name := tok
		inlineValue := false
		if eq := strings.IndexByte(tok, '='); eq >= 0 {
			name = tok[:eq]
			inlineValue = true
		}
		spec, ok := allow[name]
		if !ok {
			return fmt.Errorf("extra_args: flag %q is not allowed", name)
		}
		if spec.TakesValue && !inlineValue {
			if i+1 >= len(args) {
				return fmt.Errorf("extra_args: flag %q needs a value", name)
			}
			i++ // consume the value token
		}
	}
	return nil
}

// ExtraArgsHaveFlag reports whether args contains the given flag (in `--flag` or
// `--flag=value` form). Used to let an agent-supplied --index-url satisfy the
// "an index is available" check that otherwise keys on operator config.
func ExtraArgsHaveFlag(args []string, names ...string) bool {
	for _, a := range args {
		base := a
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			base = a[:eq]
		}
		for _, n := range names {
			if base == n {
				return true
			}
		}
	}
	return false
}
