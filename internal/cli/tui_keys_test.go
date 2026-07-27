package cli

import "testing"

func TestRouteFocusKey(t *testing.T) {
	cases := []struct {
		name            string
		from, want      tuiFocus
		key             string
		tools, consumed bool
	}{
		{"tab composer", focusComposer, focusScrollback, "tab", true, true},
		{"tab scrollback", focusScrollback, focusTools, "tab", true, true},
		{"tab without tools", focusScrollback, focusComposer, "tab", false, true},
		{"shift tab", focusComposer, focusTools, "shift+tab", true, true},
		{"escape tools", focusTools, focusComposer, "esc", true, true},
		{"arrow tools", focusTools, focusTools, "up", true, true},
		{"page scrollback", focusComposer, focusScrollback, "pgdown", false, true},
		{"printable returns composer", focusScrollback, focusComposer, "x", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, consumed := routeFocusKey(tc.from, tc.key, tc.tools)
			if got != tc.want || consumed != tc.consumed {
				t.Fatalf("routeFocusKey(%v, %q, tools=%v) = (%v, %v), want (%v, %v)", tc.from, tc.key, tc.tools, got, consumed, tc.want, tc.consumed)
			}
		})
	}
}

type focusTestCase struct {
	name         string
	from         tuiFocus
	key          string
	tools        bool
	want         tuiFocus
	wantConsumed bool
}

// focusCase is a shorthand builder for focusTestCase.
func focusCase(name string, from, want tuiFocus, key string, tools, consumed bool) focusTestCase {
	return focusTestCase{name: name, from: from, key: key, tools: tools, want: want, wantConsumed: consumed}
}

// runFocusCases runs a slice of focus test cases.
func runFocusCases(t *testing.T, cases []focusTestCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, consumed := routeFocusKey(tc.from, tc.key, tc.tools)
			if got != tc.want || consumed != tc.wantConsumed {
				t.Fatalf("routeFocusKey(%v, %q, tools=%v) = (%v, %v), want (%v, %v)",
					tc.from, tc.key, tc.tools, got, consumed, tc.want, tc.wantConsumed)
			}
		})
	}
}

// addComposerCases appends focus test cases originating from focusComposer.
func addComposerCases(dst []focusTestCase, tools bool) []focusTestCase {
	tag := " w/tools"
	if !tools {
		tag = " no-tools"
	}
	wantTools := focusScrollback
	if tools {
		wantTools = focusTools
	}
	return append(dst,
		focusCase("composer tab"+tag, focusComposer, focusScrollback, "tab", tools, true),
		focusCase("composer shift+tab"+tag, focusComposer, wantTools, "shift+tab", tools, true),
		focusCase("composer esc"+tag, focusComposer, focusComposer, "esc", tools, false),
		focusCase("composer up"+tag, focusComposer, focusComposer, "up", tools, false),
		focusCase("composer down"+tag, focusComposer, focusComposer, "down", tools, false),
		focusCase("composer pgup"+tag, focusComposer, focusScrollback, "pgup", tools, true),
		focusCase("composer pgdown"+tag, focusComposer, focusScrollback, "pgdown", tools, true),
		focusCase("composer home"+tag, focusComposer, focusScrollback, "home", tools, true),
		focusCase("composer end"+tag, focusComposer, focusScrollback, "end", tools, true),
		focusCase("composer printable"+tag, focusComposer, focusComposer, "x", tools, false),
	)
}

// addScrollbackCases appends focus test cases originating from focusScrollback.
func addScrollbackCases(dst []focusTestCase, tools bool) []focusTestCase {
	tag := " w/tools"
	if !tools {
		tag = " no-tools"
	}
	wantTab := focusComposer
	if tools {
		wantTab = focusTools
	}
	return append(dst,
		focusCase("scrollback tab"+tag, focusScrollback, wantTab, "tab", tools, true),
		focusCase("scrollback shift+tab"+tag, focusScrollback, focusComposer, "shift+tab", tools, true),
		focusCase("scrollback esc"+tag, focusScrollback, focusComposer, "esc", tools, true),
		focusCase("scrollback up"+tag, focusScrollback, focusScrollback, "up", tools, true),
		focusCase("scrollback down"+tag, focusScrollback, focusScrollback, "down", tools, true),
		focusCase("scrollback pgup"+tag, focusScrollback, focusScrollback, "pgup", tools, true),
		focusCase("scrollback pgdown"+tag, focusScrollback, focusScrollback, "pgdown", tools, true),
		focusCase("scrollback home"+tag, focusScrollback, focusScrollback, "home", tools, true),
		focusCase("scrollback end"+tag, focusScrollback, focusScrollback, "end", tools, true),
		focusCase("scrollback printable"+tag, focusScrollback, focusComposer, "x", tools, false),
	)
}

// addToolsCases appends focus test cases originating from focusTools.
// When tools=false, focusTools isn't reachable; nextTUIFocus wraps to composer.
func addToolsCases(dst []focusTestCase, tools bool) []focusTestCase {
	tag := " w/tools"
	if !tools {
		tag = " no-tools"
	}
	wantShiftTab := focusScrollback
	if !tools {
		wantShiftTab = focusComposer // focusTools not in pane set; fallback
	}
	return append(dst,
		focusCase("tools tab"+tag, focusTools, focusComposer, "tab", tools, true),
		focusCase("tools shift+tab"+tag, focusTools, wantShiftTab, "shift+tab", tools, true),
		focusCase("tools esc"+tag, focusTools, focusComposer, "esc", tools, true),
		focusCase("tools up"+tag, focusTools, focusTools, "up", tools, true),
		focusCase("tools down"+tag, focusTools, focusTools, "down", tools, true),
		focusCase("tools pgup"+tag, focusTools, focusScrollback, "pgup", tools, true),
		focusCase("tools pgdown"+tag, focusTools, focusScrollback, "pgdown", tools, true),
		focusCase("tools home"+tag, focusTools, focusScrollback, "home", tools, true),
		focusCase("tools end"+tag, focusTools, focusScrollback, "end", tools, true),
		focusCase("tools printable"+tag, focusTools, focusComposer, "x", tools, false),
	)
}

func TestRouteFocusKeyFullMatrix(t *testing.T) {
	var all []focusTestCase
	all = addComposerCases(all, true)
	all = addComposerCases(all, false)
	all = addScrollbackCases(all, true)
	all = addScrollbackCases(all, false)
	all = addToolsCases(all, true)
	all = addToolsCases(all, false)
	runFocusCases(t, all)
}

func TestPrintableFromScrollbackFocusesComposerAndReturnsUnconsumed(t *testing.T) {
	// All printable characters from scrollback: must return composer + consumed=false.
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
			focusCase("scrollback "+ch+" no-tools", focusScrollback, focusComposer, ch, false, false),
			focusCase("scrollback "+ch+" w/tools", focusScrollback, focusComposer, ch, true, false),
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
