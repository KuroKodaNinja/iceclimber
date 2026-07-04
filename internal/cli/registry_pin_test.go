package cli

import (
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/probe"
)

// TestRuntimePinFor covers the runtimePinFor + pinAssetName glue: a covered platform resolves the
// operator pin to a relay.Pin (full version, url, sha, and AssetName == pinAssetName(url)); a
// version or platform miss falls through (ok=false); and a language with no pins yields a nil
// closure (zero overhead).
func TestRuntimePinFor(t *testing.T) {
	const url = "https://mirror.internal/cpython/cpython-3.12.13-linux-x86_64.tar.gz"
	pins := map[string]map[string]config.RuntimePin{
		"python": {
			"3.12": {
				Full: "3.12.13",
				Platforms: map[string]config.RuntimePinAsset{
					"linux/x86_64/glibc": {URL: url, SHA256: "deadbeef"},
				},
			},
		},
	}
	sess := &session{
		fp:          &probe.Fingerprint{OS: "linux", Arch: "x86_64", Libc: probe.Libc{Family: "glibc"}},
		runtimePins: pins,
	}

	// Hit: the covered platform resolves to the pinned asset.
	fn := runtimePinFor(sess, "python")
	if fn == nil {
		t.Fatal("runtimePinFor(python) = nil, want a closure (python has pins)")
	}
	pin, ok := fn("3.12")
	if !ok {
		t.Fatal("fn(3.12) ok = false, want a pin hit on the covered platform")
	}
	if pin.FullVersion != "3.12.13" || pin.URL != url || pin.SHA256 != "deadbeef" {
		t.Errorf("pin = %+v, want full 3.12.13 / the url / sha deadbeef", pin)
	}
	if want := pinAssetName(url); pin.AssetName != want {
		t.Errorf("pin.AssetName = %q, want %q (pinAssetName(url))", pin.AssetName, want)
	}

	// Version miss: a version the table doesn't pin falls through.
	if _, ok := fn("3.11"); ok {
		t.Error("fn(3.11) ok = true, want false (version not pinned)")
	}

	// Platform miss: a box the pin table doesn't cover falls through.
	miss := &session{
		fp:          &probe.Fingerprint{OS: "linux", Arch: "aarch64", Libc: probe.Libc{Family: "musl"}},
		runtimePins: pins,
	}
	if _, ok := runtimePinFor(miss, "python")("3.12"); ok {
		t.Error("fn(3.12) on an uncovered platform ok = true, want false")
	}

	// A language with no pins → nil closure.
	if runtimePinFor(sess, "node") != nil {
		t.Error("runtimePinFor(node) = non-nil, want nil (node has no pins)")
	}
}

// TestPinAssetName pins the cache-key contract: "pin-"+basename. Two URLs with the same basename
// collide to the same key — callers must reason about that, so it's asserted explicitly.
func TestPinAssetName(t *testing.T) {
	if got := pinAssetName("https://a/x.tar.gz"); got != "pin-x.tar.gz" {
		t.Errorf("pinAssetName = %q, want pin-x.tar.gz", got)
	}
	if pinAssetName("https://a/x.tar.gz") != pinAssetName("https://b/x.tar.gz") {
		t.Error("same basename on different hosts should map to the same pin key")
	}
	if got := pinAssetName("bare"); got != "pin-bare" {
		t.Errorf("pinAssetName(bare) = %q, want pin-bare", got)
	}
}
