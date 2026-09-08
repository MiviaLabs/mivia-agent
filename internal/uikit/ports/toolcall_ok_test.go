package ports

import "testing"

// TestToolCallOK pins the single ok-classifier shared by the live event
// path (internal/uiadapter/event_kind.go's translateToolEnd) and the
// historical replay path (internal/ui/screen/conversation/
// session_state_builder.go's historicalToolCallOK, which now delegates
// here). Body taken verbatim from historicalToolCallOK.
func TestToolCallOK(t *testing.T) {
	cases := []struct {
		name string
		tc   ToolCall
		want bool
	}{
		{
			name: "error prefix is not ok",
			tc:   ToolCall{Name: "write_file", Output: "error: permission denied"},
			want: false,
		},
		{
			name: "run_command exit=0 is ok",
			tc:   ToolCall{Name: "run_command", Output: "exit=0\nsome output"},
			want: true,
		},
		{
			name: "run_command exit=1 is not ok",
			tc:   ToolCall{Name: "run_command", Output: "exit=1\nsome output"},
			want: false,
		},
		{
			name: "non-run_command empty output falls through to the error-prefix check (verbatim historicalToolCallOK semantics: empty is not error-prefixed, so ok)",
			tc:   ToolCall{Name: "write_file", Output: ""},
			want: true,
		},
		{
			name: "run_command garbage exit= is not ok",
			tc:   ToolCall{Name: "run_command", Output: "exit=notanumber\nsome output"},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ToolCallOK(c.tc); got != c.want {
				t.Errorf("ToolCallOK(%+v) = %v, want %v", c.tc, got, c.want)
			}
		})
	}
}
