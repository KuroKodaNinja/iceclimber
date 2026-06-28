package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/webfetch"
)

// choice is the operator's answer to a prompt.
type choice int

const (
	choiceApproveOnce choice = iota
	choiceApproveRemember
	choiceDenyOnce
	choiceDenyRemember
)

// prompt is an approval request, presentation-neutral.
type prompt struct {
	sandbox       string
	title         string
	kind          string // "operation" | "egress"
	fields        [][2]string
	note          string
	rememberLabel string
}

// asker presents a prompt to the operator and returns their decision. The terminal
// (stdin) and the TUI (a modal) are two implementations — the approval routing
// below is shared between them.
type asker interface {
	ask(p prompt) choice
}

// approver renders Claude-Code-style approval prompts (via its asker) and applies
// the decision. One instance serves both the dispatcher gate (installs and other
// verbs) and the web.fetch inline approver, so prompts look and behave consistently.
type approver struct {
	asker     asker
	sandboxID string
	act       *activity.Logger
	keepalive func() // refresh liveness right before blocking on the operator

	mu       sync.Mutex
	allowAll map[string]bool // verb types approved "for all this session"
	denyAll  map[string]bool // verb types denied "for all this session"
}

func newApprover(a asker, sandboxID string, act *activity.Logger, keepalive func()) *approver {
	return &approver{
		asker: a, sandboxID: sandboxID, act: act, keepalive: keepalive,
		allowAll: map[string]bool{}, denyAll: map[string]bool{},
	}
}

// present injects the sandbox id, refreshes liveness, and asks. Every prompt goes
// through here so liveness stays honest while the operator decides.
func (a *approver) present(p prompt) choice {
	p.sandbox = a.sandboxID
	if a.keepalive != nil {
		a.keepalive()
	}
	return a.asker.ask(p)
}

// gate is the dispatcher pre-execution hook. It prompts for state-changing verbs;
// ping is trivial and web.fetch self-gates in its handler (ApproveFetch), so both
// are skipped here. A non-nil error denies the request.
func (a *approver) gate(_ context.Context, req protocol.Request) error {
	switch req.Type {
	case "ping", "web.fetch":
		return nil
	}
	a.mu.Lock()
	switch {
	case a.denyAll[req.Type]:
		a.mu.Unlock()
		a.log(activity.KindDenied, req.Type, "(remembered)")
		return fmt.Errorf("operator denied %s", req.Type)
	case a.allowAll[req.Type]:
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	title, fields, note := summarizeRequest(req)
	switch a.present(prompt{
		title: title, kind: "operation", fields: fields, note: note,
		rememberLabel: "approve all " + req.Type,
	}) {
	case choiceApproveRemember:
		a.remember(&a.allowAll, req.Type)
		a.log(activity.KindApproved, req.Type, title)
		return nil
	case choiceApproveOnce:
		a.log(activity.KindApproved, req.Type, title)
		return nil
	case choiceDenyRemember:
		a.remember(&a.denyAll, req.Type)
		a.log(activity.KindDenied, req.Type, title)
		return fmt.Errorf("operator denied %s", req.Type)
	default: // choiceDenyOnce
		a.log(activity.KindDenied, req.Type, title)
		return fmt.Errorf("operator denied %s", req.Type)
	}
}

// ApproveFetch implements webfetch.Approver for held controller-venue fetches.
func (a *approver) ApproveFetch(_ context.Context, p webfetch.ApprovalPrompt) webfetch.ApprovalChoice {
	fields := [][2]string{
		{"method", p.Method},
		{"url", p.URL},
		{"via", "Popo's network (controller venue)"},
	}
	if p.Original != "" {
		fields = append(fields, [2]string{"original", p.Original})
	}
	if p.Reason != "" {
		fields = append(fields, [2]string{"why", p.Reason})
	}
	c := a.present(prompt{
		title: "web.fetch  " + p.Method, kind: "egress", fields: fields,
		note:          "⚠ This leaves YOUR machine's network, not the sandbox's.",
		rememberLabel: "approve + remember host " + p.Host,
	})
	switch c {
	case choiceApproveRemember:
		a.log(activity.KindApproved, "web.fetch", p.URL)
		return webfetch.ApproveRemember
	case choiceApproveOnce:
		a.log(activity.KindApproved, "web.fetch", p.URL)
		return webfetch.ApproveOnce
	case choiceDenyRemember:
		a.log(activity.KindDenied, "web.fetch", p.URL)
		return webfetch.DenyRemember
	default:
		a.log(activity.KindDenied, "web.fetch", p.URL)
		return webfetch.DenyOnce
	}
}

func (a *approver) remember(set *map[string]bool, typ string) {
	a.mu.Lock()
	(*set)[typ] = true
	a.mu.Unlock()
}

func (a *approver) log(kind, typ, detail string) {
	if a.act != nil {
		_ = a.act.Append(activity.Event{Kind: kind, Type: typ, Detail: detail})
	}
}

// summarizeRequest builds the operator-facing context for a request, reading
// params loosely (no coupling to handler param structs).
func summarizeRequest(req protocol.Request) (title string, fields [][2]string, note string) {
	var m map[string]any
	_ = json.Unmarshal(req.Params, &m)
	switch req.Type {
	case "python.install":
		v, _ := m["version"].(string)
		return "Install Python (python-build-standalone)",
			[][2]string{{"version", orDash(v)}},
			"Downloads a portable interpreter and pushes it into the sandbox."
	case "pip.install":
		pv, _ := m["python_version"].(string)
		return "Install Python packages",
			[][2]string{{"python", orDash(pv)}, {"packages", orDash(summarizePkgs(m["packages"]))}},
			"Resolved + fetched by Popo, then installed offline in the sandbox."
	case "node.install":
		v, _ := m["version"].(string)
		return "Install Node.js (portable runtime)",
			[][2]string{{"version", orDash(v)}},
			"Downloads a portable Node and pushes it into the sandbox."
	case "npm.install":
		nv, _ := m["node_version"].(string)
		return "Install npm packages",
			[][2]string{{"node", orDash(nv)}, {"packages", orDash(summarizePkgs(m["packages"]))}},
			"Resolved + fetched by Popo, then relayed into the sandbox."
	default:
		return req.Type, nil, ""
	}
}

func summarizePkgs(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, it := range arr {
		im, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name, _ := im["name"].(string)
		if ver, _ := im["version"].(string); ver != "" {
			name += " " + ver
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
