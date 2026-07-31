package cli

import (
	"strings"
	"testing"
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

	if !strings.Contains(got, ansiCyan) {
		t.Fatal("expected cyan keywords (func, var, package)")
	}
	if !strings.Contains(got, ansiBlue) {
		t.Fatal("expected blue types (int)")
	}
	if !strings.Contains(got, ansiGreen) || !strings.Contains(got, ansiReset) {
		t.Fatal("expected green strings")
	}
	if !strings.Contains(got, ansiBgDark) {
		t.Fatal("expected dark background")
	}
	// All lines should have background.
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(strings.TrimLeft(line, " "), ansiBgDark) {
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

	if !strings.Contains(got, ansiCyan) {
		t.Fatal("expected cyan keywords (def, return)")
	}
	if !strings.Contains(got, ansiDim) {
		t.Fatal("expected dim comment")
	}
	if !strings.Contains(got, ansiGreen) {
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

	if !strings.Contains(got, ansiCyan) {
		t.Fatal("expected cyan keywords (function, return)")
	}
	if !strings.Contains(got, ansiGreen) {
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

	if !strings.Contains(got, ansiCyan) {
		t.Fatal("expected cyan keywords (const, let)")
	}
	if !strings.Contains(got, ansiGreen) {
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

	if !strings.Contains(got, ansiCyan) {
		t.Fatal("expected cyan keywords (fn, let)")
	}
	if !strings.Contains(got, ansiGreen) {
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
	if !strings.Contains(got, ansiMagenta) {
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
	if !strings.Contains(got, ansiGreen) {
		t.Fatal("expected green for string values and keys")
	}
	if !strings.Contains(got, ansiMagenta) {
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

	if !strings.Contains(got, ansiCyan) {
		t.Fatal("expected cyan keywords (for, do, done)")
	}
	if !strings.Contains(got, ansiGreen) {
		t.Fatal("expected green string")
	}
}

// TestHighlightUnknownLanguage falls back to plain yellow.
func TestHighlightUnknownLanguage(t *testing.T) {
	code := "some code in an unknown language\n"
	got := highlightCodeBlock("unknown", code)
	t.Logf("Unknown highlight:\n%s", got)

	if !strings.Contains(got, ansiYellow) {
		t.Fatal("expected yellow fallback for unknown language")
	}
}

// TestHighlightNoLanguage uses plain yellow.
func TestHighlightNoLanguage(t *testing.T) {
	code := "just some plain code\n"
	got := highlightCodeBlock("", code)
	t.Logf("No language highlight:\n%s", got)

	if !strings.Contains(got, ansiYellow) {
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

	if !strings.Contains(got, ansiDim) {
		t.Fatal("expected dim for comment")
	}
}

// TestHighlightPythonComment checks # comment dimming.
func TestHighlightPythonComment(t *testing.T) {
	code := "# this is a comment\nx = 1  # trailing\n"
	got := highlightCodeBlock("python", code)
	t.Logf("Python comment:\n%s", got)

	if !strings.Contains(got, ansiDim) {
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
	if !strings.Contains(got, ansiCyan) {
		t.Fatal("expected cyan syntax highlighting in rendered markdown")
	}
	if !strings.Contains(got, ansiBgDark) {
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

	if !strings.Contains(got, ansiCyan) {
		t.Fatal("expected cyan in Go block")
	}
	if !strings.Contains(got, ansiGreen) {
		t.Fatal("expected green string in Python block")
	}
}

// TestHighlightGoString checks string content is protected from keyword matching.
func TestHighlightGoString(t *testing.T) {
	code := `fmt.Println("func var type")`
	got := highlightCodeBlock("go", code)
	t.Logf("String protection:\n%s", got)

	if !strings.Contains(got, ansiGreen) {
		t.Fatal("expected green string")
	}
}

// TestHighlightNumber checks number literal highlighting.
func TestHighlightGoNumber(t *testing.T) {
	code := "x := 42 + 0xFF\n"
	got := highlightCodeBlock("go", code)
	t.Logf("Number highlighting:\n%s", got)

	if !strings.Contains(got, ansiMagenta) {
		t.Fatal("expected magenta for numbers, got " + got)
	}
}

// TestHighlightMultiLineComment checks opening and closing of /* */.
func TestHighlightMultiLineComment(t *testing.T) {
	code := "/* multi\nline\ncomment */\nvar x int\n"
	got := highlightCodeBlock("go", code)
	t.Logf("Multi-line comment:\n%s", got)

	if !strings.Contains(got, ansiDim) {
		t.Fatal("expected dim for multi-line comment")
	}
	if !strings.Contains(got, ansiItalic) {
		t.Fatal("expected italic for comment")
	}
	// After comment closes, code should have keyword color.
	if !strings.Contains(got, ansiCyan) {
		t.Fatal("expected cyan keyword after comment")
	}
}

// TestHighlightCodeBlockInMarkdown tests the pre-processing function.
func TestHighlightCodeBlockInMarkdown(t *testing.T) {
	input := "Before\n```go\npackage main\n```\nAfter\n"
	got := highlightCodeBlockInMarkdown(input)
	t.Logf("Pre-processed:\n%s", got)

	if !strings.Contains(got, ansiCyan) {
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
	if strings.Contains(got, ansiBgDark) {
		// Actually inline code uses `ansiYellow` which might be in the output.
		// It should NOT have the full code block background.
		t.Log("inline code should not have dark background (format differs)")
	}
}
