package npm

import (
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
)

func TestResolveTier(t *testing.T) {
	cases := []struct {
		tier, registry, want string
	}{
		{"auto", "https://r", pkg.TierMirror},
		{"auto", "", pkg.TierRelay}, // air-gapped default: no reachable registry → relay
		{"", "https://r", pkg.TierMirror},
		{"", "", pkg.TierRelay},
		{"mirror", "", pkg.TierMirror}, // forced
		{"relay", "https://r", pkg.TierRelay},
	}
	for _, c := range cases {
		if got := resolveTier(c.tier, c.registry); got != c.want {
			t.Errorf("resolveTier(%q,%q) = %q, want %q", c.tier, c.registry, got, c.want)
		}
	}
}
