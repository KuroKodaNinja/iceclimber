// Package skill embeds NANA.md — the sandbox-side skill document an agent reads
// to drive the iceclimber protocol (plan §10). It is dropped into the sandbox
// tree at bootstrap and printed by `iceclimber skill print`.
package skill

import (
	_ "embed"
	"strings"
)

// nanaBase is the embedded NANA.md template (the minimal, system-prompt-sized
// contract — defaults the agent to the `popo` client). Its network-access section
// is a placeholder (networkMarker) filled per egress mode by NanaMD, because the
// truthful contract differs: in relay mode native tools categorically fail; in
// proxy mode (the default) they work through Popo's gated proxy — a doc that
// forbids what the mode enables would fight the agent.
//
//go:embed NANA.md
var nanaBase string

// The per-egress-mode network-access sections spliced into nanaBase.
//
//go:embed network_relay.md
var networkRelay string

//go:embed network_proxy.md
var networkProxy string

// networkMarker is the placeholder line in NANA.md the mode section replaces.
const networkMarker = "<!-- network-access -->"

// NanaMD renders the NANA.md contract for the sandbox's egress mode: proxyEgress
// true (egress_mode: proxy, the default) documents native-tool egress through the
// gated proxy; false (egress_mode: relay, strict air-gap) forbids native network
// tools outright. Bootstrap drops the rendered doc; `skill print` renders the
// same mode from config so the two always match.
func NanaMD(proxyEgress bool) string {
	section := networkRelay
	if proxyEgress {
		section = networkProxy
	}
	return strings.Replace(nanaBase, networkMarker+"\n", section, 1)
}

// ProtocolMD is the raw file-protocol reference, dropped alongside NANA.md. It is
// the no-exec fallback (and the implementer's reference) — NOT injected into the
// system prompt; the agent reads it only when it can't execute `popo`.
//
//go:embed PROTOCOL.md
var ProtocolMD string
