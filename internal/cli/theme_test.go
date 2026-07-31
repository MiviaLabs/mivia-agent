package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestThemeColorIndices(t *testing.T) {
	// Canonical palette pins — silent visual regressions become compile/test failures.
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
		{"tuiDimStyle", tuiDimStyle, dimStyle},
		{"toolDimStyle", toolDimStyle, dimStyle},
		{"tuiErrorStyle", tuiErrorStyle, errStyle},
		{"toolErrStyle", toolErrStyle, errStyle},
		{"tuiInfoStyle", tuiInfoStyle, infoStyle},
		{"tuiAccentStyle", tuiAccentStyle, accentStyle},
		{"tuiWaitingStyle", tuiWaitingStyle, waitStyle},
		{"tuiUserLabel", tuiUserLabel, userLabel},
		{"tuiUserStyle", tuiUserStyle, userStyle},
		{"userLabelStyle", userLabelStyle, userLabel},
		{"userRailStyle", userRailStyle, userLabel},
	}
	lipgloss.SetColorProfile(termenv.ANSI256)
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
	if ansiCyan != "\033[36m" || ansiGreen != "\033[32m" || ansiReset != "\033[0m" {
		t.Fatal("ansi* SGR constants have unexpected values")
	}
	if ansiBgDark != "\033[48;5;236m" {
		t.Fatalf("ansiBgDark = %q", ansiBgDark)
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
	for _, tok := range []string{ansiBold, ansiCyan, ansiItalic, ansiYellow, ansiDim, ansiReset, ansiBgDark} {
		if !strings.Contains(got, tok) {
			t.Fatalf("markdown output missing %q\nfull=%q", tok, got)
		}
	}
	// Exact fixture snapshot — byte-stable across the theme refactor.
	want := "\n" + ansiBold + ansiCyan + "Title" + ansiReset + "\n" +
		"this is " + ansiBold + "bold" + ansiBoldEnd + " and " + ansiItalic + "italic" + ansiReset +
		" and " + ansiDim + ansiYellow + "code" + ansiReset + "\n" +
		"  " + ansiCyan + "•" + ansiReset + " item\n"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("markdown prefix drift\ngot  %q\nwant %q", got[:min(len(got), len(want)+20)], want)
	}
	// Code fence path uses highlight (same ansi* vocab after Wave 3).
	if !strings.Contains(got, ansiBgDark+ansiCyan+"func"+ansiReset) {
		t.Fatalf("expected highlighted go keyword in markdown code block, got %q", got)
	}
}

// TestThemeByteStabilityHighlight locks highlightLine / highlightCodeBlock bytes.
func TestThemeByteStabilityHighlight(t *testing.T) {
	goCode := "func main() {\n\tvar x int = 42\n}\n"
	got := highlightCodeBlock("go", goCode)
	// Exact multi-line snapshot from pre-refactor baseline.
	wantGo := "  " + ansiBgDark + ansiCyan + "func" + ansiReset + ansiBgDark + " main() {" + ansiReset + "\n" +
		"  " + ansiBgDark + "\t" + ansiCyan + "var" + ansiReset + ansiBgDark + " x " + ansiBlue + "int" + ansiReset + ansiBgDark +
		" = " + ansiMagenta + "42" + ansiReset + ansiBgDark + ansiReset + "\n" +
		"  " + ansiBgDark + "}" + ansiReset + "\n" +
		"  " + ansiBgDark + ansiReset
	if got != wantGo {
		t.Fatalf("highlight go drift\ngot  %q\nwant %q", got, wantGo)
	}

	diff := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n context\n"
	gotDiff := highlightCodeBlock("diff", diff)
	// Diff uses shared ansi* + theme-owned diff bg codes.
	if !strings.Contains(gotDiff, ansiBgDark+ansiBold+ansiCyan+"--- a/x") {
		t.Fatalf("diff header missing shared ansi tokens: %q", gotDiff)
	}
	if !strings.Contains(gotDiff, ansiBgDiffDel+ansiRed+"-old") {
		t.Fatalf("diff del missing tokens: %q", gotDiff)
	}
	if !strings.Contains(gotDiff, ansiBgDiffAdd+ansiGreen+"+new") {
		t.Fatalf("diff add missing tokens: %q", gotDiff)
	}
	if !strings.Contains(gotDiff, ansiBgDark+ansiDim+" context") {
		t.Fatalf("diff context missing tokens: %q", gotDiff)
	}
}

// TestThemeByteStabilityToolAndUserStyles locks tool/TUI error, dim, and user-label paths.
func TestThemeByteStabilityToolAndUserStyles(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)

	// Inline error glyph path (tool row / chatblock).
	errGlyph := toolErrStyle.Render("✗")
	if errGlyph != errStyle.Render("✗") {
		t.Fatalf("toolErrStyle must equal errStyle: %q vs %q", errGlyph, errStyle.Render("✗"))
	}
	// Color 9 under ANSI256 → bright red SGR.
	if !strings.Contains(errGlyph, "✗") || errGlyph == "✗" {
		// With profile set, must emit SGR (not plain text).
		if !strings.Contains(errGlyph, "\x1b[") {
			t.Fatalf("toolErrStyle.Render expected ANSI SGR, got %q", errGlyph)
		}
	}

	dim := tuiDimStyle.Render("preview")
	if dim != toolDimStyle.Render("preview") || dim != dimStyle.Render("preview") {
		t.Fatal("dim aliases diverged")
	}

	// Slash / inline error text path.
	slashErr := tuiErrorStyle.Render("unknown command")
	if slashErr != errStyle.Render("unknown command") {
		t.Fatalf("tuiErrorStyle diverged from errStyle")
	}

	// User rail + label (msgcard path).
	rail := userRailStyle.Render("▌")
	label := userLabelStyle.Render("you")
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
