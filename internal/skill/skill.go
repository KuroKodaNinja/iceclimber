// Package skill embeds NANA.md — the sandbox-side skill document an agent reads
// to drive the iceclimber protocol (plan §10). It is dropped into the sandbox
// tree at bootstrap and printed by `iceclimber skill print`.
package skill

import _ "embed"

// NanaMD is the embedded NANA.md content.
//
//go:embed NANA.md
var NanaMD string
