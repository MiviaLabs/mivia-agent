package cli

import "testing"

func TestRouteFocusKey(t *testing.T) {
	cases := []struct {
		name       string
		from, want tuiFocus
		key        string
		consumed   bool
	}{
		{"tab composer", focusComposer, focusScrollback, "tab", true},
		{"tab scrollback", focusScrollback, focusComposer, "tab", true},
		{"shift+tab composer", focusComposer, focusScrollback, "shift+tab", true},
		{"shift+tab scrollback", focusScrollback, focusComposer, "shift+tab", true},
		{"escape scrollback", focusScrollback, focusComposer, "esc", true},
		{"escape composer", focusComposer, focusComposer, "esc", false},
		{"arrow scrollback", focusScrollback, focusScrollback, "up", true},
		{"arrow composer", focusComposer, focusComposer, "up", false},
		{"page scrollback", focusComposer, focusScrollback, "pgdown", true},
		{"page from scrollback", focusScrollback, focusScrollback, "pgdown", true},
		{"home from scrollback", focusScrollback, focusScrollback, "home", true},
		{"end from scrollback", focusScrollback, focusScrollback, "end", true},
		{"printable from scrollback", focusScrollback, focusComposer, "x", false},
		{"printable from composer", focusComposer, focusComposer, "x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, consumed := routeFocusKey(tc.from, tc.key)
			if got != tc.want || consumed != tc.consumed {
				t.Fatalf("routeFocusKey(%v, %q) = (%v, %v), want (%v, %v)", tc.from, tc.key, got, consumed, tc.want, tc.consumed)
			}
		})
	}
}

// runFocusCases is a helper for table-driven focus tests with the 2-pane model.
type focusTestCase struct {
	name         string
	from, want   tuiFocus
	key          string
	wantConsumed bool
}

func runFocusCases(t *testing.T, cases []focusTestCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, consumed := routeFocusKey(tc.from, tc.key)
			if got != tc.want || consumed != tc.wantConsumed {
				t.Fatalf("routeFocusKey(%v, %q) = (%v, %v), want (%v, %v)",
					tc.from, tc.key, got, consumed, tc.want, tc.wantConsumed)
			}
		})
	}
}

func addComposerCases(dst []focusTestCase) []focusTestCase {
	return append(dst,
		focusTestCase{"composer tab", focusComposer, focusScrollback, "tab", true},
		focusTestCase{"composer shift+tab", focusComposer, focusScrollback, "shift+tab", true},
		focusTestCase{"composer esc", focusComposer, focusComposer, "esc", false},
		focusTestCase{"composer up", focusComposer, focusComposer, "up", false},
		focusTestCase{"composer down", focusComposer, focusComposer, "down", false},
		focusTestCase{"composer pgup", focusComposer, focusScrollback, "pgup", true},
		focusTestCase{"composer pgdown", focusComposer, focusScrollback, "pgdown", true},
		focusTestCase{"composer home", focusComposer, focusScrollback, "home", true},
		focusTestCase{"composer end", focusComposer, focusScrollback, "end", true},
		focusTestCase{"composer printable", focusComposer, focusComposer, "x", false},
	)
}

func addScrollbackCases(dst []focusTestCase) []focusTestCase {
	return append(dst,
		focusTestCase{"scrollback tab", focusScrollback, focusComposer, "tab", true},
		focusTestCase{"scrollback shift+tab", focusScrollback, focusComposer, "shift+tab", true},
		focusTestCase{"scrollback esc", focusScrollback, focusComposer, "esc", true},
		focusTestCase{"scrollback up", focusScrollback, focusScrollback, "up", true},
		focusTestCase{"scrollback down", focusScrollback, focusScrollback, "down", true},
		focusTestCase{"scrollback pgup", focusScrollback, focusScrollback, "pgup", true},
		focusTestCase{"scrollback pgdown", focusScrollback, focusScrollback, "pgdown", true},
		focusTestCase{"scrollback home", focusScrollback, focusScrollback, "home", true},
		focusTestCase{"scrollback end", focusScrollback, focusScrollback, "end", true},
		focusTestCase{"scrollback printable", focusScrollback, focusComposer, "x", false},
	)
}

func TestRouteFocusKeyFullMatrix(t *testing.T) {
	var all []focusTestCase
	all = addComposerCases(all)
	all = addScrollbackCases(all)
	runFocusCases(t, all)
}

func TestPrintableFromScrollbackFocusesComposerAndReturnsUnconsumed(t *testing.T) {
	chars := []string{}
	for c := 'a'; c <= 'z'; c++ {
		chars = append(chars, string(c))
	}
	for c := 'A'; c <= 'Z'; c++ {
		chars = append(chars, string(c))
	}
	for c := '0'; c <= '9'; c++ {
		chars = append(chars, string(c))
	}
	chars = append(chars, ".", ",", "!", "?", "-", "_", "@", "#", " ")

	var all []focusTestCase
	for _, ch := range chars {
		all = append(all,
			focusTestCase{"scrollback " + ch, focusScrollback, focusComposer, ch, false},
		)
	}
	runFocusCases(t, all)
}

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
		{"space no selection", -1, " ", true, false},
		{"space no selection draft", -1, " ", false, false},
		{"space selected", 0, " ", true, true},
		{"space selected draft", 2, " ", false, true},
		{"e no selection", -1, "e", true, false},
		{"e selected", 0, "e", false, true},
		{"E no selection", -1, "E", true, false},
		{"E selected", 1, "E", true, true},
		{"G empty", -1, "G", true, true},
		{"G empty selected", 0, "G", true, true},
		{"G draft", -1, "G", false, false},
		{"G draft selected", 0, "G", false, false},
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
