// Package protocol implements the maildir request/response protocol (plan §3,
// §4): atomic delivery/pickup, the on-sandbox tree, and the dispatcher that
// services requests over a remotefs.FS. The wire format itself (envelope, Tree,
// ids) lives in internal/wire — a leaf package with no FS dependency, shared with
// the in-sandbox client (cmd/popo) — and is re-exported here so callers use
// protocol.Request, protocol.OK, protocol.Tree, etc. unchanged.
package protocol

import "github.com/KuroKodaNinja/iceclimber/internal/wire"

// Envelope/version re-exports.
const SchemaVersion = wire.SchemaVersion

const (
	StatusOK                 = wire.StatusOK
	StatusError              = wire.StatusError
	StatusNeedsClarification = wire.StatusNeedsClarification
)

type (
	// Request is an outbox envelope (plan §4).
	Request = wire.Request
	// Response is an inbox envelope, sharing the request's id (plan §4).
	Response = wire.Response
	// Error describes why Popo could not service a request.
	Error = wire.Error
	// Clarification carries a question back to Nana (status=needs_clarification).
	Clarification = wire.Clarification
)

// Response builders.
var (
	OK                 = wire.OK
	NeedsClarification = wire.NeedsClarification
	Errf               = wire.Errf
)
