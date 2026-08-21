package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// withANSI256 pins lipgloss to the ANSI256 color profile for the duration of
// the test and restores the previously detected profile when the test ends.
// Without the restore, tests that run later in the same process (e.g. under
// -shuffle=on) inherit the ANSI256 profile and render SGR output where they
// expect the detected profile (R8: the Dialog cluster failed with stray
// terminal control codes once theme tests had mutated the global profile).
func withANSI256(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

func TestThemeColorIndices(t *testing.T) {
	// Canonical palette pins - silent visual regressions become compile/test failures.
	want := map[string]string{
		"themeColorDim":          "8",
		"themeColorError":        "9",
		"themeColorInfo":         "14",
		"themeColorUser":         "12",
		"themeColorWaitGray":     "243",
		"themeColorCardBg":       "236",
		"themeColorSelBg":        "237",
		"themeColorDiffAdd":      "10",
		"themeColorDiffDel":      "9",
		"themeColorDiffAddBg":    "22",
		"themeColorDiffDelBg":    "88",
		"themeColorDiffHunk":     "5",
		"themeColorTime":         "11",
		"themeColorOk":           "2",
		"themeColorStatusFailed": "9",
		"themeColorStatusDone":   "8",
		"themeThinkingDim":       "6",
		"themeColorBright":       "15",
	}
	got := map[string]string{
		"themeColorDim":          themeColorDim,
		"themeColorError":        themeColorError,
		"themeColorInfo":         themeColorInfo,
		"themeColorUser":         themeColorUser,
		"themeColorWaitGray":     themeColorWaitGray,
		"themeColorCardBg":       themeColorCardBg,
		"themeColorSelBg":        themeColorSelBg,
		"themeColorDiffAdd":      themeColorDiffAdd,
		"themeColorDiffDel":      themeColorDiffDel,
		"themeColorDiffAddBg":    themeColorDiffAddBg,
		"themeColorDiffDelBg":    themeColorDiffDelBg,
		"themeColorDiffHunk":     themeColorDiffHunk,
		"themeColorTime":         themeColorTime,
		"themeColorOk":           themeColorOk,
		"themeColorStatusFailed": themeColorStatusFailed,
		"themeColorStatusDone":   themeColorStatusDone,
		"themeThinkingDim":       themeThinkingDim,
		"themeColorBright":       themeColorBright,
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: got %q want %q", name, got[name], w)
		}
	}
}

func TestThemeErrorRolesDistinct(t *testing.T) {
	// §3.4: inline content error stays "9"; brand/status error stays "160".
	if themeColorError != "9" {
		t.Fatalf("themeColorError (inline) = %q, want 9", themeColorError)
	}
	if brandColorError != "160" {
		t.Fatalf("brandColorError (status) = %q, want 160", brandColorError)
	}
	if themeColorError == brandColorError {
		t.Fatal("inline error and brand error must remain distinct roles")
	}
}

func TestThemeAliasEquality(t *testing.T) {
	// Aliases must be the same style value as their consolidated targets.
	type pair struct {
		name   string
		alias  lipgloss.Style
		target lipgloss.Style
	}
	pairs := []pair{
		{"TUIDimStyle", TUIDimStyle, dimStyle},
		{"ToolDimStyle", ToolDimStyle, dimStyle},
		{"tuiErrorStyle", tuiErrorStyle, errStyle},
		{"ToolErrStyle", ToolErrStyle, errStyle},
		{"tuiInfoStyle", tuiInfoStyle, infoStyle},
		{"tuiAccentStyle", tuiAccentStyle, accentStyle},
		{"tuiWaitingStyle", tuiWaitingStyle, waitStyle},
		{"tuiUserLabel", tuiUserLabel, userLabel},
		{"tuiUserStyle", tuiUserStyle, userStyle},
		{"UserLabelStyle", UserLabelStyle, userLabel},
		{"UserRailStyle", UserRailStyle, userLabel},
	}
	withANSI256(t)
	for _, p := range pairs {
		if p.alias.Render("x") != p.target.Render("x") {
			t.Errorf("%s.Render diverged from target: alias=%q target=%q",
				p.name, p.alias.Render("x"), p.target.Render("x"))
		}
		// Also pin the semantic color index via GetForeground.
		afg, ok1 := p.alias.GetForeground().(lipgloss.Color)
		tfg, ok2 := p.target.GetForeground().(lipgloss.Color)
		if !ok1 || !ok2 || string(afg) != string(tfg) {
			t.Errorf("%s foreground mismatch: alias=%v target=%v", p.name, afg, tfg)
		}
	}
}

func TestThemeConsolidatedStyleColors(t *testing.T) {
	checks := []struct {
		name  string
		style lipgloss.Style
		want  string
	}{
		{"dimStyle", dimStyle, themeColorDim},
		{"errStyle", errStyle, themeColorError},
		{"infoStyle", infoStyle, themeColorInfo},
		{"accentStyle", accentStyle, themeColorInfo},
		{"waitStyle", waitStyle, themeColorWaitGray},
		{"userLabel", userLabel, themeColorUser},
		{"userStyle", userStyle, themeColorUser},
	}
	for _, c := range checks {
		fg, ok := c.style.GetForeground().(lipgloss.Color)
		if !ok {
			t.Errorf("%s: foreground not lipgloss.Color", c.name)
			continue
		}
		if string(fg) != c.want {
			t.Errorf("%s: foreground %q want %q", c.name, string(fg), c.want)
		}
	}
}

func TestThemeANSIVocab(t *testing.T) {
	// Single SGR vocabulary lives in theme; highlight must use these names.
	if AnsiCyan != "\033[36m" || AnsiGreen != "\033[32m" || AnsiReset != "\033[0m" {
		t.Fatal("ansi* SGR constants have unexpected values")
	}
	if AnsiBgDark != "\033[48;5;236m" {
		t.Fatalf("AnsiBgDark = %q", AnsiBgDark)
	}
	// Diff SGR indices match theme color tokens.
	if !strings.Contains(ansiBgDiffAdd, themeColorDiffAddBg) {
		t.Fatalf("ansiBgDiffAdd %q should embed %s", ansiBgDiffAdd, themeColorDiffAddBg)
	}
	if !strings.Contains(ansiBgDiffDel, themeColorDiffDelBg) {
		t.Fatalf("ansiBgDiffDel %q should embed %s", ansiBgDiffDel, themeColorDiffDelBg)
	}
}

// TestThemeByteStabilityMarkdown locks MarkdownWriter ANSI output for a fixture.
func TestThemeByteStabilityMarkdown(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	_, _ = mw.Write([]byte("# Title\n"))
	_, _ = mw.Write([]byte("this is **bold** and *italic* and `code`\n"))
	_, _ = mw.Write([]byte("- item\n"))
	_, _ = mw.Write([]byte("```go\nfunc main() {}\n```\n"))
	got := buf.String()

	// Pin SGR tokens from the single ansi* vocabulary (not a second hl* copy).
	for _, tok := range []string{AnsiBold, AnsiCyan, AnsiItalic, AnsiYellow, AnsiDim, AnsiReset, AnsiBgDark} {
		if !strings.Contains(got, tok) {
			t.Fatalf("markdown output missing %q\nfull=%q", tok, got)
		}
	}
	// Exact fixture snapshot - byte-stable across the theme refactor.
	want := "\n" + AnsiBold + AnsiCyan + "Title" + AnsiReset + "\n" +
		"this is " + AnsiBold + "bold" + AnsiBoldEnd + " and " + AnsiItalic + "italic" + AnsiReset +
		" and " + AnsiDim + AnsiYellow + "code" + AnsiReset + "\n" +
		"  " + AnsiCyan + "•" + AnsiReset + " item\n"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("markdown prefix drift\ngot  %q\nwant %q", got[:min(len(got), len(want)+20)], want)
	}
	// Code fence path uses highlight (same ansi* vocab after Wave 3).
	if !strings.Contains(got, AnsiBgDark+AnsiCyan+"func"+AnsiReset) {
		t.Fatalf("expected highlighted go keyword in markdown code block, got %q", got)
	}
}

// TestThemeByteStabilityHighlight locks highlightLine / highlightCodeBlock bytes.
func TestThemeByteStabilityHighlight(t *testing.T) {
	goCode := "func main() {\n\tvar x int = 42\n}\n"
	got := highlightCodeBlock("go", goCode)
	// Exact multi-line snapshot from pre-refactor baseline.
	wantGo := "  " + AnsiBgDark + AnsiCyan + "func" + AnsiReset + AnsiBgDark + " main() {" + AnsiReset + "\n" +
		"  " + AnsiBgDark + "\t" + AnsiCyan + "var" + AnsiReset + AnsiBgDark + " x " + AnsiBlue + "int" + AnsiReset + AnsiBgDark +
		" = " + AnsiMagenta + "42" + AnsiReset + AnsiBgDark + AnsiReset + "\n" +
		"  " + AnsiBgDark + "}" + AnsiReset + "\n" +
		"  " + AnsiBgDark + AnsiReset
	if got != wantGo {
		t.Fatalf("highlight go drift\ngot  %q\nwant %q", got, wantGo)
	}

	diff := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n context\n"
	gotDiff := highlightCodeBlock("diff", diff)
	// Diff uses shared ansi* + theme-owned diff bg codes.
	if !strings.Contains(gotDiff, AnsiBgDark+AnsiBold+AnsiCyan+"--- a/x") {
		t.Fatalf("diff header missing shared ansi tokens: %q", gotDiff)
	}
	if !strings.Contains(gotDiff, ansiBgDiffDel+AnsiRed+"-old") {
		t.Fatalf("diff del missing tokens: %q", gotDiff)
	}
	if !strings.Contains(gotDiff, ansiBgDiffAdd+AnsiGreen+"+new") {
		t.Fatalf("diff add missing tokens: %q", gotDiff)
	}
	if !strings.Contains(gotDiff, AnsiBgDark+AnsiDim+" context") {
		t.Fatalf("diff context missing tokens: %q", gotDiff)
	}
}

// TestThemeByteStabilityToolAndUserStyles locks tool/TUI error, dim, and user-label paths.
func TestThemeByteStabilityToolAndUserStyles(t *testing.T) {
	withANSI256(t)

	// Inline error glyph path (tool row / chatblock).
	errGlyph := ToolErrStyle.Render("✗")
	if errGlyph != errStyle.Render("✗") {
		t.Fatalf("ToolErrStyle must equal errStyle: %q vs %q", errGlyph, errStyle.Render("✗"))
	}
	// Color 9 under ANSI256 → bright red SGR.
	if !strings.Contains(errGlyph, "✗") || errGlyph == "✗" {
		// With profile set, must emit SGR (not plain text).
		if !strings.Contains(errGlyph, "\x1b[") {
			t.Fatalf("ToolErrStyle.Render expected ANSI SGR, got %q", errGlyph)
		}
	}

	dim := TUIDimStyle.Render("preview")
	if dim != ToolDimStyle.Render("preview") || dim != dimStyle.Render("preview") {
		t.Fatal("dim aliases diverged")
	}

	// Slash / inline error text path.
	slashErr := tuiErrorStyle.Render("unknown command")
	if slashErr != errStyle.Render("unknown command") {
		t.Fatalf("tuiErrorStyle diverged from errStyle")
	}

	// User rail + label (msgcard path).
	rail := UserRailStyle.Render("▌")
	label := UserLabelStyle.Render("you")
	if rail != userLabel.Render("▌") || label != userLabel.Render("you") {
		t.Fatal("user label/rail aliases diverged from userLabel")
	}

	// Full user-card path through formatUserMessageCard.
	lines := formatUserMessageCard("hello", 40, time.Date(2026, 1, 15, 15, 4, 0, 0, time.UTC))
	if len(lines) < 2 {
		t.Fatalf("expected ≥2 card lines, got %d", len(lines))
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "hello") || !strings.Contains(stripANSI(joined), "you") {
		t.Fatalf("user card missing content: %q", joined)
	}
	// Must include styled rail/label (ANSI), not plain only.
	if !strings.Contains(lines[0], "\x1b[") {
		t.Fatalf("user card label row expected ANSI, got %q", lines[0])
	}
}
