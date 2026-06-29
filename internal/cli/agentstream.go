package cli

import (
	"encoding/json"
	"strings"
)

// formatAgentLine renders one captured stdout line of an agent for the [NANA] pane.
// Claude's `--output-format stream-json` emits one JSON event per line; we surface the
// human-meaningful parts — the assistant's narration, its tool calls, and the final
// result — as concise lines, and drop the protocol noise (init, tool results). Any
// line that isn't a recognized event (plain text, the `=== nana session ===` banner)
// passes through unchanged, so non-stream-json runs still show.
func formatAgentLine(raw string) []string {
	line := strings.TrimSpace(raw)
	if !strings.HasPrefix(line, "{") {
		return []string{raw}
	}
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type == "" {
		return []string{raw} // not a stream-json event — show as-is
	}
	switch ev.Type {
	case "assistant":
		var out []string
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				for _, t := range strings.Split(strings.TrimSpace(c.Text), "\n") {
					if strings.TrimSpace(t) != "" {
						out = append(out, t)
					}
				}
			case "tool_use":
				out = append(out, "→ "+c.Name+toolArg(c.Input))
			}
		}
		return out
	case "result":
		if r := strings.TrimSpace(ev.Result); r != "" {
			return strings.Split(r, "\n")
		}
	}
	return nil // system / user(tool_result) / unknown — skip
}

// toolArg summarizes a tool_use input: the shell command for Bash, a path for file
// tools, else nothing. Kept short so the pane stays readable.
func toolArg(input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url"} {
		if v, ok := m[k].(string); ok && v != "" {
			return ": " + oneLine(v)
		}
	}
	return ""
}

func oneLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}
