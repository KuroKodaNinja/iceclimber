package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// terminalAsker presents approval prompts on a terminal (render to out, read the
// decision from in) — the asker used by supervised `serve`.
type terminalAsker struct {
	in  *bufio.Reader
	out io.Writer
}

func newTerminalAsker(in io.Reader, out io.Writer) *terminalAsker {
	return &terminalAsker{in: bufio.NewReader(in), out: out}
}

// ask renders a prompt and reads one decision, re-prompting on unknown input.
func (t *terminalAsker) ask(p prompt) choice {
	t.render(p)
	for {
		fmt.Fprint(t.out, "  ❯ ")
		line, err := t.in.ReadString('\n')
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
			fmt.Fprintf(t.out, "    y = allow this once · a = %s · n = deny this once · d = deny + remember\n", p.rememberLabel)
		default:
			if err != nil {
				// EOF / closed stdin — fail safe.
				fmt.Fprintln(t.out, "(no input — denying)")
				return choiceDenyOnce
			}
			fmt.Fprintln(t.out, "  please answer y / a / n / d  (? for help)")
		}
	}
}

const rule = "─────────────────────────────────────────────────────────────"

// render draws a left-bordered block (no right border, so Unicode in values never
// breaks alignment).
func (t *terminalAsker) render(p prompt) {
	w := t.out
	hdr := "Approve operation"
	if p.kind == "egress" {
		hdr = "Approve egress"
	}
	fmt.Fprintf(w, "\n  ╭%s\n", rule)
	fmt.Fprintf(w, "  │ %s · sandbox %s\n", hdr, p.sandbox)
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
