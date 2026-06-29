// Package skill embeds NANA.md — the sandbox-side skill document an agent reads
// to drive the iceclimber protocol (plan §10). It is dropped into the sandbox
// tree at bootstrap and printed by `iceclimber skill print`.
package skill

import _ "embed"

// NanaMD is the embedded NANA.md content (the minimal, system-prompt-sized
// contract — defaults the agent to the `popo` client).
//
//go:embed NANA.md
var NanaMD string

// ProtocolMD is the raw file-protocol reference, dropped alongside NANA.md. It is
// the no-exec fallback (and the implementer's reference) — NOT injected into the
// system prompt; the agent reads it only when it can't execute `popo`.
//
//go:embed PROTOCOL.md
var ProtocolMD string
