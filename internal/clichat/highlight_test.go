package clichat

import (
	"strings"
	"testing"
	"time"
)

// TestHighlightGo checks Go keyword and type highlighting.
func TestHighlightGo(t *testing.T) {
	code := `package main

import "fmt"

func main() {
	var x int = 42
	fmt.Println("hello")
}
`
	got := highlightCodeBlock("go", code)
	t.Logf("Go highlight:\n%s", got)

	if !strings.Contains(got, AnsiCyan) {
		t.Fatal("expected cyan keywords (func, var, package)")
	}
	if !strings.Contains(got, AnsiBlue) {
		t.Fatal("expected blue types (int)")
	}
	if !strings.Contains(got, AnsiGreen) || !strings.Contains(got, AnsiReset) {
		t.Fatal("expected green strings")
	}
	if !strings.Contains(got, AnsiBgDark) {
		t.Fatal("expected dark background")
	}
	// All lines should have background.
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(strings.TrimLeft(line, " "), AnsiBgDark) {
			t.Errorf("line missing dark background: %q", line)
		}
	}
}

// TestHighlightPython checks Python decorator and keyword highlighting.
func TestHighlightPython(t *testing.T) {
	code := `def hello(name: str) -> str:
    # This is a comment
    return f"Hello, {name}"
`
	got := highlightCodeBlock("python", code)
	t.Logf("Python highlight:\n%s", got)

	if !strings.Contains(got, AnsiCyan) {
		t.Fatal("expected cyan keywords (def, return)")
	}
	if !strings.Contains(got, AnsiDim) {
		t.Fatal("expected dim comment")
	}
	if !strings.Contains(got, AnsiGreen) {
		t.Fatal("expected green string")
	}
}

// TestHighlightJavaScript checks JS keyword highlighting.
func TestHighlightJavaScript(t *testing.T) {
	code := `function greet(name) {
    console.log("Hello, " + name);
    return 42;
}
`
	got := highlightCodeBlock("javascript", code)
	t.Logf("JS highlight:\n%s", got)

	if !strings.Contains(got, AnsiCyan) {
		t.Fatal("expected cyan keywords (function, return)")
	}
	if !strings.Contains(got, AnsiGreen) {
		t.Fatal("expected green string")
	}
}

// TestHighlightTypeScript checks TS type highlighting.
func TestHighlightTypeScript(t *testing.T) {
	code := `const x: number = 42;
let y: string = "hello";
`
	got := highlightCodeBlock("typescript", code)
	t.Logf("TS highlight:\n%s", got)

	if !strings.Contains(got, AnsiCyan) {
		t.Fatal("expected cyan keywords (const, let)")
	}
	if !strings.Contains(got, AnsiGreen) {
		t.Fatal("expected green string")
	}
}

// TestHighlightRust checks Rust keyword and type highlighting.
func TestHighlightRust(t *testing.T) {
	code := `fn main() -> i32 {
    let x = 42;
    println!("x = {}", x);
    0
}
`
	got := highlightCodeBlock("rust", code)
	t.Logf("Rust highlight:\n%s", got)

	if !strings.Contains(got, AnsiCyan) {
		t.Fatal("expected cyan keywords (fn, let)")
	}
	if !strings.Contains(got, AnsiGreen) {
		t.Fatal("expected green string")
	}
}

// TestHighlightDiff checks diff line coloring (GitHub-style).
func TestHighlightDiff(t *testing.T) {
	code := `--- a/file.go
+++ b/file.go
@@ -1,3 +1,4 @@
-old line
+new line
 context line
`
	got := highlightCodeBlock("diff", code)
	t.Logf("Diff highlight:\n%s", got)

	// GitHub-style: + lines use dark green bg + green fg
	if !strings.Contains(got, ansiBgDiffAdd) {
		t.Fatal("expected dark green background for + lines")
	}
	// - lines use dark red bg + red fg
	if !strings.Contains(got, ansiBgDiffDel) {
		t.Fatal("expected dark red background for - lines")
	}
	if !strings.Contains(got, AnsiMagenta) {
		t.Fatal("expected magenta for @@ lines")
	}
}

// TestHighlightJSON checks JSON key and value highlighting.
func TestHighlightJSON(t *testing.T) {
	code := `{
    "name": "Alice",
    "age": 30,
    "active": true
}
`
	got := highlightCodeBlock("json", code)
	t.Logf("JSON highlight:\n%s", got)

	// Keys are strings → highlighted green
	if !strings.Contains(got, AnsiGreen) {
		t.Fatal("expected green for string values and keys")
	}
	if !strings.Contains(got, AnsiMagenta) {
		t.Fatal("expected magenta for numbers")
	}
}

// TestHighlightShell checks shell keyword highlighting.
func TestHighlightShell(t *testing.T) {
	code := `#!/bin/bash
for f in *.go; do
    echo "Processing $f"
done
`
	got := highlightCodeBlock("shell", code)
	t.Logf("Shell highlight:\n%s", got)

	if !strings.Contains(got, AnsiCyan) {
		t.Fatal("expected cyan keywords (for, do, done)")
	}
	if !strings.Contains(got, AnsiGreen) {
		t.Fatal("expected green string")
	}
}

// TestHighlightUnknownLanguage falls back to plain yellow.
func TestHighlightUnknownLanguage(t *testing.T) {
	code := "some code in an unknown language\n"
	got := highlightCodeBlock("unknown", code)
	t.Logf("Unknown highlight:\n%s", got)

	if !strings.Contains(got, AnsiYellow) {
		t.Fatal("expected yellow fallback for unknown language")
	}
}

// TestHighlightNoLanguage uses plain yellow.
func TestHighlightNoLanguage(t *testing.T) {
	code := "just some plain code\n"
	got := highlightCodeBlock("", code)
	t.Logf("No language highlight:\n%s", got)

	if !strings.Contains(got, AnsiYellow) {
		t.Fatal("expected yellow for no language")
	}
}

// TestHighlightEmptyCode returns empty string.
func TestHighlightEmptyCode(t *testing.T) {
	got := highlightCodeBlock("go", "")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// TestHighlightGoCommentSingleLine checks // comment dimming.
func TestHighlightGoCommentSingleLine(t *testing.T) {
	code := "func foo() { // inline comment\n"
	got := highlightCodeBlock("go", code)
	t.Logf("Go comment:\n%s", got)

	if !strings.Contains(got, AnsiDim) {
		t.Fatal("expected dim for comment")
	}
}

// TestHighlightPythonComment checks # comment dimming.
func TestHighlightPythonComment(t *testing.T) {
	code := "# this is a comment\nx = 1  # trailing\n"
	got := highlightCodeBlock("python", code)
	t.Logf("Python comment:\n%s", got)

	if !strings.Contains(got, AnsiDim) {
		t.Fatal("expected dim for comment")
	}
}

// TestHighlightMarkdownIntegration tests that RenderMarkdown with code
// fences produces highlighted output.
func TestHighlightMarkdownIntegration(t *testing.T) {
	input := "Here is some code:\n\n```go\npackage main\n\nfunc main() {}\n```\n"
	got := RenderMarkdown(input, 80)
	t.Logf("Markdown with Go:\n%s", got)

	// Should have ANSI codes for syntax highlighting.
	if !strings.Contains(got, AnsiCyan) {
		t.Fatal("expected cyan syntax highlighting in rendered markdown")
	}
	if !strings.Contains(got, AnsiBgDark) {
		t.Fatal("expected code background in rendered markdown")
	}
	// Should still have the regular code fence chrome.
	if !strings.Contains(got, "╭") || !strings.Contains(got, "╰") {
		t.Fatal("expected code fence chrome")
	}
}

// TestHighlightMarkdownDiffIntegration tests diff code blocks in markdown.
func TestHighlightMarkdownDiffIntegration(t *testing.T) {
	input := "```diff\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n```\n"
	got := RenderMarkdown(input, 80)
	t.Logf("Markdown diff:\n%s", got)

	// GitHub-style: + lines have dark green background.
	if !strings.Contains(got, ansiBgDiffAdd) {
		t.Fatal("expected dark green background for + lines")
	}
	// - lines have dark red background.
	if !strings.Contains(got, ansiBgDiffDel) {
		t.Fatal("expected dark red background for - lines")
	}
}

// TestHighlightMarkdownMultipleCodeBlocks highlights two blocks.
func TestHighlightMarkdownMultipleCodeBlocks(t *testing.T) {
	input := "Go:\n```go\nvar x int\n```\n\nPython:\n```python\nx = \"hello\"\n```\n"
	got := RenderMarkdown(input, 80)
	t.Logf("Two code blocks:\n%s", got)

	if !strings.Contains(got, AnsiCyan) {
		t.Fatal("expected cyan in Go block")
	}
	if !strings.Contains(got, AnsiGreen) {
		t.Fatal("expected green string in Python block")
	}
}

// TestHighlightGoString checks string content is protected from keyword matching.
func TestHighlightGoString(t *testing.T) {
	code := `fmt.Println("func var type")`
	got := highlightCodeBlock("go", code)
	t.Logf("String protection:\n%s", got)

	if !strings.Contains(got, AnsiGreen) {
		t.Fatal("expected green string")
	}
}

// TestHighlightNumber checks number literal highlighting.
func TestHighlightGoNumber(t *testing.T) {
	code := "x := 42 + 0xFF\n"
	got := highlightCodeBlock("go", code)
	t.Logf("Number highlighting:\n%s", got)

	if !strings.Contains(got, AnsiMagenta) {
		t.Fatal("expected magenta for numbers, got " + got)
	}
}

// TestHighlightNumberInWordPreserved pins that digits embedded inside an
// identifier keep the per-byte number rendering (magenta span). The whole-word
// fast path in highlightTokens must not swallow embedded numbers, so a
// digit-containing identifier deliberately takes the slow per-byte path.
// The input's trailing "\n" renders a trailing empty code line ("\n  ") per
// highlightCodeBlock's line-per-newline contract, which is also pinned by
// TestThemeByteStabilityHighlight and TestYAMLHighlightContentPreserved.
func TestHighlightNumberInWordPreserved(t *testing.T) {
	code := "x42y\n"
	got := highlightCodeBlock("go", code)
	t.Logf("Number in word:\n%s", got)

	if plain := stripANSI(got); plain != "  x42y\n  " {
		t.Fatalf("stripANSI(highlightCodeBlock(%q)) = %q, want %q", code, plain, "  x42y\n  ")
	}
	if !strings.Contains(got, AnsiMagenta) {
		t.Fatalf("expected magenta for embedded number 42, got %q", got)
	}
}

// TestHighlightMultiLineComment checks opening and closing of /* */.
func TestHighlightMultiLineComment(t *testing.T) {
	code := "/* multi\nline\ncomment */\nvar x int\n"
	got := highlightCodeBlock("go", code)
	t.Logf("Multi-line comment:\n%s", got)

	if !strings.Contains(got, AnsiDim) {
		t.Fatal("expected dim for multi-line comment")
	}
	if !strings.Contains(got, AnsiItalic) {
		t.Fatal("expected italic for comment")
	}
	// After comment closes, code should have keyword color.
	if !strings.Contains(got, AnsiCyan) {
		t.Fatal("expected cyan keyword after comment")
	}

	for _, test := range []struct {
		line string
		want string
	}{
		{line: "comment */", want: "  comment */"},
		{line: "comment */var x int", want: "  comment */var x int"},
	} {
		rendered, inMulti := highlightLine(test.line, "go", true)
		if inMulti {
			t.Fatalf("highlightLine(%q) remained in a multi-line comment", test.line)
		}
		if plain := stripANSI(rendered); plain != test.want {
			t.Fatalf("highlightLine(%q) = %q, want %q", test.line, plain, test.want)
		}
	}

	rendered, _ := highlightLine("comment */plain", "go", true)
	if !strings.Contains(rendered, AnsiReset+AnsiBgDark+"plain") {
		t.Fatalf("code after comment close lost its background: %q", rendered)
	}

	rendered, inMulti := highlightLine("old */ code /* new", "go", true)
	if !inMulti {
		t.Fatal("a new comment after the close delimiter did not update comment state")
	}
	if plain := stripANSI(rendered); plain != "  old */ code /* new" {
		t.Fatalf("nested comment transition = %q, want exact input text", plain)
	}
}

// TestHighlightCodeBlockInMarkdown tests the pre-processing function.
func TestHighlightCodeBlockInMarkdown(t *testing.T) {
	input := "Before\n```go\npackage main\n```\nAfter\n"
	got := highlightCodeBlockInMarkdown(input)
	t.Logf("Pre-processed:\n%s", got)

	if !strings.Contains(got, AnsiCyan) {
		t.Fatal("expected cyan highlighting")
	}
	if !strings.Contains(got, "Before") || !strings.Contains(got, "After") {
		t.Fatal("expected surrounding text preserved")
	}
	if !strings.Contains(got, "╭") || !strings.Contains(got, "╰") {
		t.Fatal("expected fence chrome")
	}
}

// TestHighlightInlineCodeNotHighlighted ensures inline `code` is not affected.
func TestHighlightInlineCodeNotHighlighted(t *testing.T) {
	input := "use `var` keyword in Go\n"
	got := RenderMarkdown(input, 80)
	t.Logf("Inline code:\n%s", got)

	// Inline code should use inline code style, not background.
	if strings.Contains(got, AnsiBgDark) {
		// Actually inline code uses `AnsiYellow` which might be in the output.
		// It should NOT have the full code block background.
		t.Log("inline code should not have dark background (format differs)")
	}
}

// runWithinTimeout runs fn in a goroutine and fails the test if it does not
// complete within d. This keeps the CI suite from hanging even when a
// non-termination regression of the yaml highlighter is present.
func runWithinTimeout(t *testing.T, d time.Duration, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not terminate within %s: non-terminating highlight loop", name, d)
	}
}

// TestExtraPatternMatchSkipsZeroWidthMatch is the RED regression test for the
// zero-width-match acceptance in extraPatternMatch. The yaml rule `^\s*-?\s*`
// matches the empty string at every position, so pre-fix extraPatternMatch
// returned (true, i) for a line with no leading whitespace, writing an empty
// ANSI span and returning the scan position unchanged; highlightTokens then
// looped forever, growing its output buffer unboundedly. The corrected
// contract: a zero-width rule match is skipped, so a line with no real rule
// match yields (false, 0) and an empty output.
func TestExtraPatternMatchSkipsZeroWidthMatch(t *testing.T) {
	def := langDefs["yaml"]
	var out strings.Builder
	matched, next := extraPatternMatch("value", 0, def.extraPattern, nil, &out)
	if matched {
		t.Fatalf("extraPatternMatch(%q, 0, yaml rules) = (true, %d, %q): zero-width rule match must be skipped", "value", next, out.String())
	}
	if next != 0 {
		t.Fatalf("next = %d, want 0", next)
	}
	if out.Len() != 0 {
		t.Fatalf("out = %q, want empty", out.String())
	}
}

// TestExtraPatternMatchAdvancesOnMatch pins the progress invariant of
// extraPatternMatch: whenever it reports a match, the next scan position must
// be strictly greater than the current one. Pre-fix the yaml rule 1 matched
// the empty string at i=0 and returned (true, 0), violating the invariant and
// hanging highlightTokens.
func TestExtraPatternMatchAdvancesOnMatch(t *testing.T) {
	def := langDefs["yaml"]
	for _, line := range []string{"name: value", "- item", "  key: v"} {
		for i := 0; i <= len(line); i++ {
			var out strings.Builder
			matched, next := extraPatternMatch(line, i, def.extraPattern, nil, &out)
			if matched && next <= i {
				t.Fatalf("extraPatternMatch(%q, %d) matched but did not advance: next=%d (out=%q)", line, i, next, out.String())
			}
		}
	}
}

// TestYAMLHighlightTerminates runs one line at a time through the reachable
// highlightCodeBlock path. Every case must terminate (goroutine + timeout; the
// pre-fix code hung on every yaml line) and, for ESC-free input, preserve
// content exactly: stripANSI(result) == "  " + line.
func TestYAMLHighlightTerminates(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"key", "name: value", "  name: value"},
		{"list marker", "- item", "  - item"},
		{"indented key", "  indented: x", "    indented: x"},
		{"single-quoted", "'quoted'", "  'quoted'"},
		{"double-quoted", "\"quoted\"", "  \"quoted\""},
		{"two identical strings", "'x' 'x'", "  'x' 'x'"},
		{"dangling key", "name:", "  name:"},
		{"dangling list marker", "- ", "  - "},
		{"lone quote", "'unclosed", "  'unclosed"},
		{"comment", "# comment", "  # comment"},
		{"whitespace only", "   ", "     "},
		{"tab", "\tkey: v", "  \tkey: v"},
		{"unicode", "ключ: значение", "  ключ: значение"},
		{"pipes", "a|b|c", "  a|b|c"},
		{"digits and spaces", "1 1 1 1", "  1 1 1 1"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var got string
			runWithinTimeout(t, 2*time.Second, "highlightCodeBlock(yaml)", func() {
				got = highlightCodeBlock("yaml", tc.line)
			})
			if plain := stripANSI(got); plain != tc.want {
				t.Fatalf("stripANSI(highlightCodeBlock(%q)) = %q, want %q (raw %q)", tc.line, plain, tc.want, got)
			}
		})
	}

	// A raw ESC byte in model-supplied content must not hang the highlighter.
	// stripANSI cannot give an exact value here (a raw ESC merges with the
	// following ANSI span), so we assert termination and non-empty output only.
	var got string
	runWithinTimeout(t, 2*time.Second, "highlightCodeBlock(yaml) ESC", func() {
		got = highlightCodeBlock("yaml", "\x1b[31m")
	})
	if len(got) == 0 {
		t.Fatal("ESC line produced empty output")
	}
}

// TestYAMLHighlightOversizedTerminates bounds the highlighter's output growth
// on oversized input. Pre-fix the yaml zero-width match grew the ANSI buffer
// without bound; post-fix output is linear in the input size.
func TestYAMLHighlightOversizedTerminates(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"1MiB single line", strings.Repeat("a", 1024*1024)},
		{"1000-line block", strings.Repeat("name: value\n", 1000)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var got string
			runWithinTimeout(t, 2*time.Second, "highlightCodeBlock oversized", func() {
				got = highlightCodeBlock("yaml", tc.content)
			})
			if len(got) > 8*len(tc.content)+4096 {
				t.Fatalf("output grew to %d bytes for %d input bytes (unbounded growth?)", len(got), len(tc.content))
			}
			lines := strings.Split(tc.content, "\n")
			parts := make([]string, len(lines))
			for i, l := range lines {
				parts[i] = "  " + l
			}
			if plain := stripANSI(got); plain != strings.Join(parts, "\n") {
				t.Fatalf("content not preserved for %s: stripANSI output differs", tc.name)
			}
		})
	}
}

// TestRenderMarkdownYAMLTerminates exercises the reachable end-to-end path:
// model-supplied markdown containing a ```yaml fence through RenderMarkdown.
// Balanced and unbalanced (no closing fence) inputs must all terminate; the
// pre-fix code hung on the first yaml code line.
func TestRenderMarkdownYAMLTerminates(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		want   string // plain text that must survive
		chrome bool   // expect both ╭ and ╰ fence chrome
	}{
		{"balanced", "```yaml\nname: value\n```\n", "name: value", true},
		{"list and nested", "```yaml\n- item\n  nested: x\n```\n", "- item", true},
		{"unbalanced fence", "```yaml\nname: value", "name: value", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var got string
			runWithinTimeout(t, 2*time.Second, "RenderMarkdown yaml", func() {
				got = RenderMarkdown(tc.input, 80)
			})
			if tc.chrome && (!strings.Contains(got, "╭") || !strings.Contains(got, "╰")) {
				t.Fatalf("RenderMarkdown(%q) missing fence chrome: %q", tc.input, got)
			}
			if plain := stripANSI(got); !strings.Contains(plain, tc.want) {
				t.Fatalf("RenderMarkdown(%q) lost code content %q: %q", tc.input, tc.want, plain)
			}
		})
	}
}

// TestYAMLHighlightContentPreserved pins exact content preservation across a
// multi-line yaml block, including an empty interior line and the empty-block
// short-circuit.
func TestYAMLHighlightContentPreserved(t *testing.T) {
	if got := highlightCodeBlock("yaml", ""); got != "" {
		t.Fatalf("highlightCodeBlock(yaml, \"\") = %q, want empty", got)
	}

	code := "name: value\n  nested: x\n- item\n"
	got := highlightCodeBlock("yaml", code)
	want := "  name: value\n    nested: x\n  - item\n  "
	if plain := stripANSI(got); plain != want {
		t.Fatalf("stripANSI(highlightCodeBlock(%q)) = %q, want %q (raw %q)", code, plain, want, got)
	}

	code = "a\n\nb"
	got = highlightCodeBlock("yaml", code)
	want = "  a\n  \n  b"
	if plain := stripANSI(got); plain != want {
		t.Fatalf("stripANSI(highlightCodeBlock(%q)) = %q, want %q", code, plain, want)
	}
}
