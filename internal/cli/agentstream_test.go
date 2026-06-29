package cli

import (
	"reflect"
	"testing"
)

func TestFormatAgentLine(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
	}{
		{
			name: "plain text passes through",
			in:   "=== nana session Mon Jun 29 ===",
			want: []string{"=== nana session Mon Jun 29 ==="},
		},
		{
			name: "assistant text + tool call",
			in:   `{"type":"assistant","message":{"content":[{"type":"text","text":"Installing Python."},{"type":"tool_use","name":"Bash","input":{"command":"popo python.install 3.12"}}]}}`,
			want: []string{"Installing Python.", "→ Bash: popo python.install 3.12"},
		},
		{
			name: "final result",
			in:   `{"type":"result","subtype":"success","result":"All three programs ran.","is_error":false}`,
			want: []string{"All three programs ran."},
		},
		{
			name: "system init is dropped",
			in:   `{"type":"system","subtype":"init","tools":["Bash"]}`,
			want: nil,
		},
		{
			name: "tool_result (user) is dropped",
			in:   `{"type":"user","message":{"content":[{"type":"tool_result","content":"...huge output..."}]}}`,
			want: nil,
		},
		{
			name: "non-event JSON passes through",
			in:   `{"some":"other json"}`,
			want: []string{`{"some":"other json"}`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatAgentLine(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("formatAgentLine(%s) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestToolArgTruncates(t *testing.T) {
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'x'
	}
	got := toolArg([]byte(`{"command":"` + string(long) + `"}`))
	if len(got) > 130 || got[len(got)-3:] != "..." {
		t.Errorf("toolArg did not truncate a long command: len=%d", len(got))
	}
}
