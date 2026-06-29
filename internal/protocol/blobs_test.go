package protocol

import (
	"path"
	"testing"
)

// TestBlobLayout pins the canonical blob location and the invariant that the
// published body_blob reference (BlobRef) resolves, against $ROOT, to exactly the
// directory blobs are written to (Blobs). A web.fetch blob landed in $ROOT/blobs
// instead of $ROOT/protocol/blobs once because the writer bypassed Blobs(); this
// invariant is what keeps the writer, the published reference, and NANA.md aligned.
func TestBlobLayout(t *testing.T) {
	tr := Tree{Root: "/srv/ice"}

	if got, want := tr.Blobs(), "/srv/ice/protocol/blobs"; got != want {
		t.Errorf("Blobs() = %q, want %q (under protocol/, as NANA.md documents)", got, want)
	}
	if got, want := tr.BlobRef("abc123"), "protocol/blobs/abc123"; got != want {
		t.Errorf("BlobRef() = %q, want %q ($ROOT-relative)", got, want)
	}
	// The invariant: $ROOT + BlobRef(name) == the actual file under Blobs().
	if joined, actual := path.Join(tr.Root, tr.BlobRef("abc123")), path.Join(tr.Blobs(), "abc123"); joined != actual {
		t.Errorf("$ROOT/BlobRef = %q but blob is written at %q — they must match", joined, actual)
	}
	// BlobRef must never be root-anchored (it's relative to $ROOT).
	if ref := tr.BlobRef("x"); path.IsAbs(ref) {
		t.Errorf("BlobRef = %q, must be $ROOT-relative, not absolute", ref)
	}
}
