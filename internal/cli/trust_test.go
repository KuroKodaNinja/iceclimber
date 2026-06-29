package cli

import "testing"

func TestFingerprintMatches(t *testing.T) {
	cases := []struct {
		want, got string
		match     bool
	}{
		{"SHA256:abc", "SHA256:abc", true},
		{"abc", "SHA256:abc", true},          // missing prefix tolerated
		{"SHA256:abc", "abc", true},          // either side may omit it
		{" SHA256:abc ", "SHA256:abc", true}, // whitespace tolerated
		{"SHA256:abc", "SHA256:xyz", false},
		{"", "SHA256:abc", false},
	}
	for _, c := range cases {
		if got := fingerprintMatches(c.want, c.got); got != c.match {
			t.Errorf("fingerprintMatches(%q, %q) = %v, want %v", c.want, c.got, got, c.match)
		}
	}
}
