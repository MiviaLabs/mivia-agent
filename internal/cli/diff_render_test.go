package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestHistoryToolExpand_RendersDiffChanges(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	block := ChatBlock{Kind: ChatBlockTool, ToolName: "search_replace", Text: "updated x.txt (+1 −1)\n--- a/x.txt\n+++ b/x.txt\n@@ -1 +1 @@\n-old\n+new", Collapsed: false}
	out := strings.Join(renderOneChatBlock(block, "model", 80, false), "\n")
	if !strings.Contains(out, "-old") || !strings.Contains(out, "+new") {
		t.Fatalf("diff changes missing: %q", out)
	}
}

// ── Omission-count reconciliation (changeCentricWindow) ───────────────

// concreteDiffBody is a 15-line edit-tool diff: 3 preamble lines, the hunk
// header at index 3, three context lines, the change at index 7, and 7 tail
// lines. With maxLines=6 the change-centric window must show the @@ header
// plus three context lines and report the leading and trailing omissions.
const concreteDiffBody = "updated x.txt\n" +
	"--- a/x.txt\n" +
	"+++ b/x.txt\n" +
	"@@ -1,8 +1,8 @@\n" +
	" a\n" +
	" b\n" +
	" c\n" +
	"-old\n" +
	" d\n" +
	" e\n" +
	" f\n" +
	" g\n" +
	" h\n" +
	" i\n" +
	" j"

func concreteDiffLines() []string {
	return strings.Split(concreteDiffBody, "\n")
}

// omissionMarkerPattern matches "… N lines omitted" as rendered by
// changeCentricWindow and by renderDiffBody (which prefixes it with spaces).
var omissionMarkerPattern = regexp.MustCompile(`^\s*…\s+(\d+)\s+lines\s+omitted\s*$`)

func parseOmissionMarker(line string) (int, bool) {
	m := omissionMarkerPattern.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// reconcileWindow sums the omission-marker counts and counts the shown
// (non-marker) lines of a rendered window, so tests can assert that the
// window never silently drops or double-counts body lines.
func reconcileWindow(win []string) (shown, omitted int) {
	for _, line := range win {
		if n, ok := parseOmissionMarker(line); ok {
			omitted += n
		} else {
			shown++
		}
	}
	return shown, omitted
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDiffWindowOmissionCountsReconcile drives the full render path and
// asserts the truncation window is honest: at most maxLines lines, and the
// omission markers plus the shown lines account for every body line.
func TestDiffWindowOmissionCountsReconcile(t *testing.T) {
	bodyLines := concreteDiffLines()
	if len(bodyLines) != 15 {
		t.Fatalf("fixture drifted: got %d lines, want 15", len(bodyLines))
	}
	const maxLines = 6
	out := renderDiffBody(concreteDiffBody, 80, maxLines)
	plain := strings.Split(stripANSI(strings.Join(out, "\n")), "\n")
	if len(plain) > maxLines {
		t.Fatalf("window exceeds maxLines=%d: got %d lines\n%s", maxLines, len(plain), strings.Join(plain, "\n"))
	}
	shown, omitted := reconcileWindow(plain)
	if shown+omitted != len(bodyLines) {
		t.Fatalf("window does not reconcile: shown=%d omitted=%d total=%d, body has %d lines\n%s",
			shown, omitted, shown+omitted, len(bodyLines), strings.Join(plain, "\n"))
	}
}

// TestChangeCentricWindowGolden pins the fixed output for the concrete body
// at maxLines=6 - the same budget renderCollapsedEditBlock's peekLines uses
// for every collapsed diff in the TUI. Reserving marker slots from a tight
// budget can spend every content slot on leading context before ever
// reaching the line that changed; a "change-centric" window must not do
// that, so this also pins that the change line ("-old") is always the last
// content line shown, at the cost of one line of leading context, rather
// than silently falling into the trailing omitted count.
func TestChangeCentricWindowGolden(t *testing.T) {
	want := []string{
		"… 4 lines omitted",
		" a",
		" b",
		" c",
		"-old",
		"… 7 lines omitted",
	}
	got := changeCentricWindow(concreteDiffLines(), 6)
	if !sameStrings(got, want) {
		t.Fatalf("changeCentricWindow mismatch:\ngot  %q\nwant %q", got, want)
	}
}

// TestDiffWindowTinyBudgets sweeps the concrete body with maxLines 1..5:
// no panic, the window never exceeds the budget, and the omission counts
// always reconcile. It also pins the drop-leading contract (maxLines=2,
// which now shows the change line itself rather than only the @@ header -
// with just one content slot available, the change outranks the header)
// and the marker-only window (maxLines=1, too tight for any content line).
func TestDiffWindowTinyBudgets(t *testing.T) {
	body := concreteDiffLines()
	for _, m := range []int{1, 2, 3, 4, 5} {
		win := changeCentricWindow(body, m)
		if len(win) > m {
			t.Fatalf("maxLines=%d: window len %d exceeds budget: %q", m, len(win), win)
		}
		shown, omitted := reconcileWindow(win)
		if shown+omitted != len(body) {
			t.Fatalf("maxLines=%d: window does not reconcile: shown=%d omitted=%d total=%d, body=%d\n%q",
				m, shown, omitted, shown+omitted, len(body), win)
		}
	}
	if got, want := changeCentricWindow(body, 2), []string{"-old", "… 14 lines omitted"}; !sameStrings(got, want) {
		t.Fatalf("maxLines=2: got %q want %q", got, want)
	}
	if got, want := changeCentricWindow(body, 1), []string{"… 15 lines omitted"}; !sameStrings(got, want) {
		t.Fatalf("maxLines=1: got %q want %q", got, want)
	}
}

// TestDiffWindowEdgeShapes covers the negative/edge shapes: empty body,
// all-context body (no +/- change lines), body shorter than the budget, and
// header-only body. Truncation must never panic and must always reconcile.
func TestDiffWindowEdgeShapes(t *testing.T) {
	if got := changeCentricWindow(nil, 6); len(got) != 0 {
		t.Fatalf("empty body: got %q", got)
	}
	ctx := []string{"@@ -1,4 +1,4 @@", " a", " b", " c", " d"}
	if got := changeCentricWindow(ctx, 3); len(got) > 3 {
		t.Fatalf("all-context window exceeds budget: %q", got)
	} else if shown, omitted := reconcileWindow(got); shown+omitted != len(ctx) {
		t.Fatalf("all-context window does not reconcile: shown=%d omitted=%d body=%d: %q", shown, omitted, len(ctx), got)
	}
	short := []string{"@@ -1,1 +1,1 @@", "+x"}
	if got := changeCentricWindow(short, 10); len(got) != 2 {
		t.Fatalf("short body: got %q", got)
	} else {
		for _, l := range got {
			if _, ok := parseOmissionMarker(l); ok {
				t.Fatalf("short body must not carry markers: %q", got)
			}
		}
	}
	hdr := []string{"--- a/x.go", "+++ b/x.go"}
	if got := changeCentricWindow(hdr, 6); len(got) != 2 {
		t.Fatalf("header-only body: got %q", got)
	} else {
		for _, l := range got {
			if _, ok := parseOmissionMarker(l); ok {
				t.Fatalf("header-only body must not carry markers: %q", got)
			}
		}
	}
}

// TestDiffBodyStartZeroTrailingUnchanged pins the start==0 single-trailing
// marker shape (the existing TestDiffBodyGuttersAndTruncationIsExplicit
// contract): a body with no preamble and a leading change shows 19 content
// lines plus one "… 183 lines omitted" marker and nothing is lost.
func TestDiffBodyStartZeroTrailingUnchanged(t *testing.T) {
	var b strings.Builder
	b.WriteString("--- a/x.go\n+++ b/x.go\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "+added line %d\n", i)
	}
	out := renderDiffBody(b.String(), 80, 20)
	if len(out) > 20 {
		t.Fatalf("window exceeds 20 lines: %d", len(out))
	}
	plain := strings.Split(stripANSI(strings.Join(out, "\n")), "\n")
	shown, omitted := reconcileWindow(plain)
	if shown+omitted != 202 {
		t.Fatalf("counts do not reconcile: shown=%d omitted=%d total=%d, want 202", shown, omitted, shown+omitted)
	}
	if omitted != 183 {
		t.Fatalf("omitted=%d, want 183 (19 shown + 183 = 202)", omitted)
	}
	if !strings.Contains(stripANSI(strings.Join(out, "\n")), "… 183 lines omitted") {
		t.Fatalf("trailing marker must read '… 183 lines omitted':\n%s", stripANSI(strings.Join(out, "\n")))
	}
}

// FuzzDiffWindowOmissionCounts sweeps changeCentricWindow, a pure
// ([]string, int) -> []string function, over arbitrary bodies and budgets.
// Invariants: no panic, the window never exceeds maxLines, every body line
// is either shown or counted by exactly one omission marker, marker counts
// are >= 1, and a leading marker is present only when lines precede the
// shown span. Content lines shaped like omission markers are rewritten
// before windowing so the test's reconciler stays unambiguous (the code
// treats them as plain content either way).
func FuzzDiffWindowOmissionCounts(f *testing.F) {
	seeds := []string{
		"",
		concreteDiffBody,
		"--- a/x.go\n+++ b/x.go\n",
		"@@ -1,4 +1,4 @@\n a\n b\n c\n d",
		"--- a/x.go\n+++ b/x.go\n@@ -1,1 +1,1 @@\n+new",
	}
	for _, s := range seeds {
		for _, m := range []int{1, 6, 20, 32} {
			f.Add(s, m)
		}
	}
	f.Fuzz(func(t *testing.T, body string, maxLines int) {
		if maxLines < 1 {
			maxLines = 1
		}
		if maxLines > 32 {
			maxLines = 32
		}
		lines := make([]string, 0, len(body))
		for _, l := range strings.Split(body, "\n") {
			if _, ok := parseOmissionMarker(l); ok {
				l = "content " + l
			}
			lines = append(lines, l)
		}
		win := changeCentricWindow(lines, maxLines) // must not panic
		if len(win) > maxLines {
			t.Fatalf("window len %d exceeds maxLines %d (body %d lines)", len(win), maxLines, len(lines))
		}
		shown, omitted := reconcileWindow(win)
		if shown+omitted != len(lines) {
			t.Fatalf("window does not reconcile: shown=%d omitted=%d total=%d, body=%d", shown, omitted, shown+omitted, len(lines))
		}
		for _, l := range win {
			if n, ok := parseOmissionMarker(l); ok && n < 1 {
				t.Fatalf("marker %q reports %d omitted lines", l, n)
			}
		}
	})
}
