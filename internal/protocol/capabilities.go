package protocol

import (
	"context"
	"encoding/json"
	"fmt"
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

// ReadCapabilities loads the sandbox's capabilities self-report. A missing or
// unreadable file yields (nil, nil) so a never-bootstrapped sandbox reads as "not
// reported" rather than an error; a present-but-corrupt file returns the unmarshal
// error (the status reader treats that as "not reported" too).
func ReadCapabilities(ctx context.Context, fs remotefs.FS, tree Tree) (*Capabilities, error) {
	data, err := fs.ReadFile(ctx, tree.Capabilities())
	if err != nil {
		return nil, nil // absent/unreadable → not reported
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
// each update their own block without erasing the other's.
func WriteCapabilities(ctx context.Context, fs remotefs.FS, tree Tree, mutate func(*Capabilities)) error {
	c := Capabilities{}
	if cur, err := ReadCapabilities(ctx, fs, tree); err == nil && cur != nil {
		c = *cur // preserve the other writer's block; a corrupt file starts clean
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
