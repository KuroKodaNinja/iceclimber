// Package wire is the pure request/response wire format shared by the controller
// (Popo) and the in-sandbox client (cmd/popo): the envelope, the on-sandbox tree
// layout, ids, and name helpers. It has NO filesystem/transport dependency, so the
// tiny sandbox client can reuse it without linking SSH/SFTP. The FS-coupled pieces
// (atomic Deliver/PickUp, EnsureTree, the dispatcher) live in internal/protocol,
// which re-exports these types so existing callers are unchanged.
package wire

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// SchemaVersion is the envelope version Popo writes and expects.
const SchemaVersion = 1

// Response.Status values (plan §4).
const (
	StatusOK                 = "ok"
	StatusError              = "error"
	StatusNeedsClarification = "needs_clarification"
)

// Request is an outbox envelope (plan §4).
type Request struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	CreatedAt     time.Time       `json:"created_at"`
	Params        json.RawMessage `json:"params,omitempty"`
}

// Response is an inbox envelope, sharing the request's id (plan §4).
type Response struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	Status        string          `json:"status"`
	CompletedAt   time.Time       `json:"completed_at"`
	Result        json.RawMessage `json:"result,omitempty"`
	Error         *Error          `json:"error,omitempty"`
	Clarification *Clarification  `json:"clarification,omitempty"`
}

// Error describes why Popo could not service a request.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Clarification carries a question back to Nana (status=needs_clarification).
type Clarification struct {
	Question string `json:"question"`
}

// OK builds a successful response with result marshaled into Result.
func OK(id string, result any) Response {
	raw, err := json.Marshal(result)
	if err != nil {
		return Errf(id, "internal", "marshal result: %v", err)
	}
	return Response{SchemaVersion: SchemaVersion, ID: id, Status: StatusOK, CompletedAt: time.Now().UTC(), Result: raw}
}

// NeedsClarification builds a held response that asks the operator/Nana a question.
func NeedsClarification(id, question string) Response {
	return Response{SchemaVersion: SchemaVersion, ID: id, Status: StatusNeedsClarification, CompletedAt: time.Now().UTC(), Clarification: &Clarification{Question: question}}
}

// Errf builds an error response (Popo failed to service the request).
func Errf(id, code, format string, args ...any) Response {
	return Response{SchemaVersion: SchemaVersion, ID: id, Status: StatusError, CompletedAt: time.Now().UTC(), Error: &Error{Code: code, Message: fmt.Sprintf(format, args...)}}
}

// Tree is the on-sandbox layout rooted at an install root (plan §3). All paths are
// absolute POSIX paths (path, not path/filepath).
type Tree struct {
	Root string
}

func (t Tree) protocolDir() string { return path.Join(t.Root, "protocol") }

// Outbox carries requests (Nana -> Popo); Inbox carries responses (Popo -> Nana).
func (t Tree) Outbox() Maildir { return Maildir{base: path.Join(t.protocolDir(), "outbox")} }
func (t Tree) Inbox() Maildir  { return Maildir{base: path.Join(t.protocolDir(), "inbox")} }

// Heartbeat is the liveness file Popo writes (plan §4.7).
func (t Tree) Heartbeat() string { return path.Join(t.protocolDir(), "heartbeat") }

// Blobs is the content-addressed store; State holds convenience copies.
func (t Tree) Blobs() string { return path.Join(t.protocolDir(), "blobs") }
func (t Tree) State() string { return path.Join(t.Root, "state") }

// BlobRef is the $ICECLIMBER_HOME-relative path of a blob, as published in a response's
// body_blob field — the agent reads it at $ICECLIMBER_HOME/<BlobRef>. Derived from Blobs() so
// the published reference can never drift from where blobs are actually written.
func (t Tree) BlobRef(name string) string {
	return strings.TrimPrefix(path.Join(t.Blobs(), name), t.Root+"/")
}

// Skill holds the dropped skill docs (NANA.md + the PROTOCOL.md fallback);
// Capabilities is Nana's optional self-report.
func (t Tree) Skill() string        { return path.Join(t.Root, "skill") }
func (t Tree) SkillFile() string    { return path.Join(t.Skill(), "NANA.md") }
func (t Tree) ProtocolFile() string { return path.Join(t.Skill(), "PROTOCOL.md") }
func (t Tree) Capabilities() string { return path.Join(t.protocolDir(), "capabilities.json") }

// CapabilitiesSchema is the version of the capabilities.json self-report.
const CapabilitiesSchema = 1

// Capabilities is Nana's self-report, written to Tree.Capabilities(): the sandbox
// host facts (written by bootstrap) and the installed coding agent's identity
// (written by agent install/wrap). The two blocks are updated independently via a
// read-modify-write, so neither writer clobbers the other. The status panel renders
// Summary().
type Capabilities struct {
	SchemaVersion int       `json:"schema_version"`
	WrittenAt     string    `json:"written_at"`
	Host          CapHost   `json:"host"`
	Agent         *CapAgent `json:"agent,omitempty"`
}

// CapHost is the sandbox platform (written by bootstrap from the probe fingerprint).
type CapHost struct {
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
	Libc string `json:"libc,omitempty"`
}

// CapAgent is the installed coding agent's self-declared identity (written by
// agent install/wrap — the only place the agent name/version/auth is known).
type CapAgent struct {
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	Version        string `json:"version,omitempty"`
	AuthConfigured bool   `json:"auth_configured"`
}

// Summary renders the one-line status report: the agent identity (or "no agent yet")
// with the host platform as trailing context, e.g.
// "Claude Code 1.2.3 · auth ✓ · linux/arm64 (glibc)".
func (c Capabilities) Summary() string {
	var parts []string
	if c.Agent != nil {
		name := c.Agent.DisplayName
		if name == "" {
			name = c.Agent.Name
		}
		s := name
		if c.Agent.Version != "" {
			s += " " + c.Agent.Version
		}
		auth := "auth ✗"
		if c.Agent.AuthConfigured {
			auth = "auth ✓"
		}
		parts = append(parts, s+" · "+auth)
	} else {
		parts = append(parts, "no agent yet")
	}
	if h := c.Host.String(); h != "" {
		parts = append(parts, h)
	}
	return strings.Join(parts, " · ")
}

// String renders the host platform compactly, e.g. "linux/arm64 (glibc)".
func (h CapHost) String() string {
	s := h.OS
	if h.Arch != "" {
		if s != "" {
			s += "/"
		}
		s += h.Arch
	}
	if h.Libc != "" && s != "" {
		s += " (" + h.Libc + ")"
	}
	return s
}

// Maildir is one tmp/new/cur triple.
type Maildir struct{ base string }

func (m Maildir) Tmp() string { return path.Join(m.base, "tmp") }
func (m Maildir) New() string { return path.Join(m.base, "new") }
func (m Maildir) Cur() string { return path.Join(m.base, "cur") }

// NewID returns a fresh ULID. ULIDs sort lexically by creation time, so "oldest
// queued" is a plain directory listing (plan §3).
func NewID() string { return ulid.Make().String() }

// RequestName is the filename for a request/response with the given id.
func RequestName(id string) string { return id + ".json" }
