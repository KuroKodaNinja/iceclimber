package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// terminalApprover renders Claude-Code-style approval prompts on serve's terminal
// and reads the operator's decision. One instance serves both the dispatcher gate
// (installs and other verbs) and the web.fetch inline approver, so prompts look and
// behave consistently. It runs on serve's single dispatch goroutine — no internal
// concurrency beyond the remember maps (guarded for safety).
type terminalApprover struct {
	in        *bufio.Reader
	out       io.Writer
	sandboxID string
	act       *activity.Logger
	keepalive func() // refresh liveness right before blocking on input

	mu       sync.Mutex
	allowAll map[string]bool // verb types approved "for all this session"
	denyAll  map[string]bool // verb types denied "for all this session"
}

func newTerminalApprover(in io.Reader, out io.Writer, sandboxID string, act *activity.Logger, keepalive func()) *terminalApprover {
	return &terminalApprover{
		in: bufio.NewReader(in), out: out, sandboxID: sandboxID, act: act, keepalive: keepalive,
		allowAll: map[string]bool{}, denyAll: map[string]bool{},
	}
}

// gate is the dispatcher pre-execution hook. It prompts for state-changing verbs;
// ping is trivial and web.fetch self-gates in its handler (ApproveFetch), so both
// are skipped here. A non-nil error denies the request.
func (a *terminalApprover) gate(_ context.Context, req protocol.Request) error {
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
	switch a.ask(prompt{
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
func (a *terminalApprover) ApproveFetch(_ context.Context, p webfetch.ApprovalPrompt) webfetch.ApprovalChoice {
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
	c := a.ask(prompt{
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

// prompt is the rendered approval request.
type prompt struct {
	title         string
	kind          string // "operation" | "egress" (header label)
	fields        [][2]string
	note          string
	rememberLabel string
}

// ask renders a prompt and reads one decision, re-prompting on unknown input.
func (a *terminalApprover) ask(p prompt) choice {
	if a.keepalive != nil {
		a.keepalive()
	}
	a.render(p)
	for {
		fmt.Fprint(a.out, "  ❯ ")
		line, err := a.in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return choiceApproveOnce
		case "a", "all":
			return choiceApproveRemember
		case "n", "no":
			return choiceDenyOnce
		case "d":
			return choiceDenyRemember
		case "?", "h", "help":
			a.help(p)
		default:
			if err != nil {
				// EOF / closed stdin — fail safe.
				fmt.Fprintln(a.out, "(no input — denying)")
				return choiceDenyOnce
			}
			fmt.Fprintln(a.out, "  please answer y / a / n / d  (? for help)")
		}
	}
}

const rule = "─────────────────────────────────────────────────────────────"

// render draws a left-bordered block (no right border, so Unicode in values never
// breaks alignment).
func (a *terminalApprover) render(p prompt) {
	w := a.out
	hdr := "Approve operation"
	if p.kind == "egress" {
		hdr = "Approve egress"
	}
	fmt.Fprintf(w, "\n  ╭%s\n", rule)
	fmt.Fprintf(w, "  │ %s · sandbox %s\n", hdr, a.sandboxID)
	fmt.Fprintf(w, "  │ %s\n", p.title)
	for _, f := range p.fields {
		fmt.Fprintf(w, "  │   %-9s %s\n", f[0], f[1])
	}
	if p.note != "" {
		fmt.Fprintf(w, "  │\n  │ %s\n", p.note)
	}
	fmt.Fprintf(w, "  ╰%s\n", rule)
	fmt.Fprintf(w, "    [y] approve   [a] %s   [n] deny   [d] deny+remember   [?]\n", p.rememberLabel)
}

func (a *terminalApprover) help(p prompt) {
	fmt.Fprintf(a.out, "    y = allow this once · a = %s · n = deny this once · d = deny + remember\n", p.rememberLabel)
}

func (a *terminalApprover) remember(set *map[string]bool, typ string) {
	a.mu.Lock()
	(*set)[typ] = true
	a.mu.Unlock()
}

func (a *terminalApprover) log(kind, typ, detail string) {
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
