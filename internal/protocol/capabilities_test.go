package protocol

import (
	"context"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// TestCapabilities_ReadWriteNoClobber: the two writers (bootstrap → host, agent
// install → agent) each update their own block via read-modify-write without erasing
// the other's, and a missing file reads as "not reported" (nil, nil).
func TestCapabilities_ReadWriteNoClobber(t *testing.T) {
	ctx := context.Background()
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := Tree{Root: t.TempDir()}
	if err := EnsureTree(ctx, fs, tree); err != nil {
		t.Fatalf("EnsureTree: %v", err)
	}

	// Missing file → not reported.
	if c, err := ReadCapabilities(ctx, fs, tree); err != nil || c != nil {
		t.Fatalf("ReadCapabilities(missing) = %v, %v; want nil/nil", c, err)
	}

	// Bootstrap writes the host block.
	if err := WriteCapabilities(ctx, fs, tree, func(c *Capabilities) {
		c.Host = CapHost{OS: "linux", Arch: "arm64", Libc: "musl"}
	}); err != nil {
		t.Fatalf("write host: %v", err)
	}

	// Agent install writes the agent block — must NOT clobber host.
	if err := WriteCapabilities(ctx, fs, tree, func(c *Capabilities) {
		c.Agent = &CapAgent{Name: "claude", DisplayName: "Claude Code", Version: "9.9", AuthConfigured: true}
	}); err != nil {
		t.Fatalf("write agent: %v", err)
	}

	c, err := ReadCapabilities(ctx, fs, tree)
	if err != nil || c == nil {
		t.Fatalf("read after writes: %v %v", c, err)
	}
	if c.Host.Arch != "arm64" || c.Host.Libc != "musl" {
		t.Errorf("host block clobbered by the agent write: %+v", c.Host)
	}
	if c.Agent == nil || c.Agent.Name != "claude" || !c.Agent.AuthConfigured {
		t.Errorf("agent block wrong: %+v", c.Agent)
	}
	if c.SchemaVersion != CapabilitiesSchema {
		t.Errorf("schema = %d, want %d", c.SchemaVersion, CapabilitiesSchema)
	}
	if c.WrittenAt == "" {
		t.Error("written_at was not stamped")
	}

	// Re-bootstrap (host only) must preserve the agent block.
	if err := WriteCapabilities(ctx, fs, tree, func(c *Capabilities) {
		c.Host = CapHost{OS: "linux", Arch: "amd64"}
	}); err != nil {
		t.Fatalf("re-bootstrap: %v", err)
	}
	c2, _ := ReadCapabilities(ctx, fs, tree)
	if c2 == nil || c2.Agent == nil {
		t.Error("re-bootstrap clobbered the agent block")
	}
	if c2.Host.Arch != "amd64" {
		t.Errorf("host not updated on re-bootstrap: %+v", c2.Host)
	}
}

// TestCapabilities_Corrupt: a corrupt file surfaces a read error (so the status reader
// degrades to "not reported"), and WriteCapabilities over a corrupt file starts clean
// (replacing the unrecoverable bytes) rather than aborting.
func TestCapabilities_Corrupt(t *testing.T) {
	ctx := context.Background()
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := Tree{Root: t.TempDir()}
	if err := EnsureTree(ctx, fs, tree); err != nil {
		t.Fatalf("EnsureTree: %v", err)
	}
	if err := fs.WriteFile(ctx, tree.Capabilities(), []byte("{not json")); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadCapabilities(ctx, fs, tree); err == nil {
		t.Error("ReadCapabilities on a corrupt file should return an error")
	}

	if err := WriteCapabilities(ctx, fs, tree, func(c *Capabilities) {
		c.Host = CapHost{OS: "linux", Arch: "arm64"}
	}); err != nil {
		t.Fatalf("write over a corrupt file should start clean, not abort: %v", err)
	}
	c, err := ReadCapabilities(ctx, fs, tree)
	if err != nil || c == nil || c.Host.Arch != "arm64" || c.Agent != nil {
		t.Errorf("write-over-corrupt should yield a clean host-only report; got %+v (err %v)", c, err)
	}
}
