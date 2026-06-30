package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/wire"
)

// Capabilities re-exports (the schema lives in the leaf wire package; the FS-aware
// read/write helpers live here, like Deliver/ReadResponse).
type (
	Capabilities = wire.Capabilities
	CapHost      = wire.CapHost
	CapAgent     = wire.CapAgent
)

// CapabilitiesSchema is the version stamped on every write.
const CapabilitiesSchema = wire.CapabilitiesSchema

// ReadCapabilities loads the sandbox's capabilities self-report. An ABSENT file yields
// (nil, nil) so a never-bootstrapped sandbox reads as "not reported"; a transient/
// permission read error or a corrupt (unparseable) file returns the error. The status
// reader treats any error as "not reported".
func ReadCapabilities(ctx context.Context, fs remotefs.FS, tree Tree) (*Capabilities, error) {
	data, err := fs.ReadFile(ctx, tree.Capabilities())
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil, nil // absent → not reported
		}
		return nil, err // transient/permission → real error (not "absent")
	}
	var c Capabilities
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// WriteCapabilities read-modify-writes the capabilities self-report: it loads the
// existing file (or a zero value), applies mutate, stamps the schema + time, and
// publishes it atomically (tmp + rename, like WriteHeartbeat). Read-modify-write lets
// the two writers — bootstrap (host block) and agent install/wrap (agent block) —
// each update their own block without erasing the other's. It does its OWN read (not
// via ReadCapabilities) so it can tell the three cases apart: absent → start clean
// (first write); corrupt → start clean (the atomic write replaces unrecoverable
// bytes); a transient read error → ABORT rather than clobber the other writer's block.
func WriteCapabilities(ctx context.Context, fs remotefs.FS, tree Tree, mutate func(*Capabilities)) error {
	c := Capabilities{}
	if data, err := fs.ReadFile(ctx, tree.Capabilities()); err == nil {
		if json.Unmarshal(data, &c) != nil {
			c = Capabilities{} // corrupt → start clean
		}
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("read capabilities: %w", err) // don't clobber on a flaky read
	}
	mutate(&c)
	c.SchemaVersion = CapabilitiesSchema
	c.WrittenAt = time.Now().UTC().Format(time.RFC3339)

	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := tree.Capabilities() + ".tmp"
	if err := fs.WriteFile(ctx, tmp, data); err != nil {
		return fmt.Errorf("write capabilities: %w", err)
	}
	if err := fs.Rename(ctx, tmp, tree.Capabilities()); err != nil {
		return fmt.Errorf("publish capabilities: %w", err)
	}
	return nil
}
