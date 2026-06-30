package pkg

import "testing"

func TestValidateExtraArgs(t *testing.T) {
	allow := map[string]FlagSpec{
		"--index-url": {TakesValue: true},
		"-f":          {TakesValue: true},
		"--pre":       {},
	}
	ok := [][]string{
		nil,
		{"--pre"},
		{"--index-url", "https://download.pytorch.org/whl/cpu"},
		{"--index-url=https://x", "--pre"},
		{"-f", "https://x", "--pre"},
	}
	for _, args := range ok {
		if err := ValidateExtraArgs(args, allow); err != nil {
			t.Errorf("ValidateExtraArgs(%v) = %v, want ok", args, err)
		}
	}

	bad := [][]string{
		{"--evil"},                          // unlisted flag
		{"torch"},                           // bare positional (smuggled target)
		{"--index-url"},                     // value-taking flag with no value
		{"--pre", "extra"},                  // trailing positional
		{"--index-url", "https://x", "pkg"}, // positional after a satisfied flag
	}
	for _, args := range bad {
		if err := ValidateExtraArgs(args, allow); err == nil {
			t.Errorf("ValidateExtraArgs(%v) = nil, want error", args)
		}
	}
}

func TestExtraArgsHaveFlag(t *testing.T) {
	if !ExtraArgsHaveFlag([]string{"--pre", "--index-url", "https://x"}, "--index-url", "-i") {
		t.Error("should find --index-url")
	}
	if !ExtraArgsHaveFlag([]string{"--index-url=https://x"}, "--index-url") {
		t.Error("should find --index-url= (inline form)")
	}
	if ExtraArgsHaveFlag([]string{"--pre"}, "--index-url", "-i") {
		t.Error("should not find an absent flag")
	}
}
