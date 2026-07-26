package cli

import "testing"

func TestVisualLineCount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lines []string
		want  int
	}{
		{"empty", nil, 0},
		{"single lines", []string{"a", "b"}, 2},
		{"multiline slot", []string{"a\nb\nc", "d"}, 4},
		{"trailing newline still one extra", []string{"x\n"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := visualLineCount(tc.lines); got != tc.want {
				t.Fatalf("visualLineCount(%q) = %d, want %d", tc.lines, got, tc.want)
			}
		})
	}
}

func TestConsumeToolNavKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		selectedTool int
		key          string
		empty        bool
		want         bool
	}{
		// space/e/E: only when a tool row is selected
		{"space no selection", -1, " ", true, false},
		{"space no selection draft", -1, " ", false, false},
		{"space selected", 0, " ", true, true},
		{"space selected draft", 2, " ", false, true},
		{"e no selection", -1, "e", true, false},
		{"e selected", 0, "e", false, true},
		{"E no selection", -1, "E", true, false},
		{"E selected", 1, "E", true, true},
		// G: only when textarea is empty
		{"G empty", -1, "G", true, true},
		{"G empty selected", 0, "G", true, true},
		{"G draft", -1, "G", false, false},
		{"G draft selected", 0, "G", false, false},
		// unrelated keys never consumed by this helper
		{"letter a", -1, "a", true, false},
		{"letter a selected", 0, "a", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := consumeToolNavKey(tc.selectedTool, tc.key, tc.empty)
			if got != tc.want {
				t.Fatalf("consumeToolNavKey(%d, %q, empty=%v) = %v, want %v",
					tc.selectedTool, tc.key, tc.empty, got, tc.want)
			}
		})
	}
}
