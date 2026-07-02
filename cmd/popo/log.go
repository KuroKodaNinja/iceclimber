package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/KuroKodaNinja/iceclimber/internal/logfile"
)

// logCmd is `popo log <file> [--max-bytes N] [--keep K]`: a capping tee. It copies stdin
// to stdout unchanged (so an interactive shell still sees the stream) while appending each
// line to a rotating, reopen-safe file via internal/logfile.Writer. This caps the sandbox
// session.log at the writer — where a plain `tee -a` cannot — with the same rotation and
// cross-process safety as the controller-side sinks. The launcher pipes headless nana
// output through it; it falls back to `tee -a` only if popo is somehow absent.
func logCmd(args []string) error {
	file := ""
	maxBytes := logfile.DefaultMaxBytes
	keep := logfile.DefaultKeep
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--max-bytes":
			i++
			if i >= len(args) {
				return fmt.Errorf("--max-bytes needs a value")
			}
			n, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil {
				return fmt.Errorf("--max-bytes %q: %w", args[i], err)
			}
			maxBytes = n
		case "--keep":
			i++
			if i >= len(args) {
				return fmt.Errorf("--keep needs a value")
			}
			k, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("--keep %q: %w", args[i], err)
			}
			keep = k
		default:
			if file != "" {
				return fmt.Errorf("unexpected argument %q (usage: popo log <file> [--max-bytes N] [--keep K])", a)
			}
			file = a
		}
	}
	if file == "" {
		return fmt.Errorf("usage: popo log <file> [--max-bytes N] [--keep K]")
	}

	w := logfile.New(file, maxBytes, keep)
	defer w.Close()
	return teeLines(os.Stdin, os.Stdout, w)
}

// teeLines is the capping-tee core: it copies in→out line by line while appending each line
// to w. ReadBytes hands us each line (incl. its trailing newline, or the remainder at EOF).
// A write error to the log must not break the pass-through — the agent's stream comes first.
func teeLines(in io.Reader, out io.Writer, w *logfile.Writer) error {
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = out.Write(line)
			_ = w.Append(line)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
