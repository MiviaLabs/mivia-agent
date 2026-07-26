package cli

import (
	"bytes"
	"strings"
	"testing"
)

func stripANSI(s string) string {
	var out strings.Builder
	skip := 0
	for _, r := range s {
		if r == '\033' {
			skip = 2
			continue
		}
		if skip > 0 {
			if skip == 2 && r == '[' {
				skip = 3
				continue
			}
			if skip >= 3 {
				if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
					skip = 0
				}
				continue
			}
		}
		out.WriteRune(r)
	}
	return out.String()
}

func TestMarkdownWriterPlainText(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	n, err := mw.Write([]byte("hello world\n"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Fatalf("expected 12 bytes written, got %d", n)
	}
	got := buf.String()
	if !strings.Contains(stripANSI(got), "hello world") {
		t.Fatalf("expected 'hello world', got %q", got)
	}
}

func TestMarkdownWriterHeading1(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("# Hello\n"))
	got := buf.String()
	if !strings.Contains(got, ansiBold) {
		t.Fatalf("expected bold ANSI for H1, got %q", got)
	}
	if !strings.Contains(stripANSI(got), "Hello") {
		t.Fatalf("expected 'Hello', got %q", got)
	}
}

func TestMarkdownWriterHeading2(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("## Section\n"))
	got := buf.String()
	if !strings.Contains(got, ansiBold) {
		t.Fatalf("expected bold for H2, got %q", got)
	}
	if !strings.Contains(stripANSI(got), "Section") {
		t.Fatalf("expected 'Section', got %q", got)
	}
}

func TestMarkdownWriterBold(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("this is **bold** text\n"))
	got := buf.String()
	if !strings.Contains(got, ansiBold) {
		t.Fatalf("expected bold ANSI, got %q", got)
	}
	stripped := stripANSI(got)
	if !strings.Contains(stripped, "bold") {
		t.Fatalf("expected 'bold' in output, got %q", stripped)
	}
}

func TestMarkdownWriterItalic(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("this is *italic* text\n"))
	got := buf.String()
	if !strings.Contains(got, ansiItalic) {
		t.Fatalf("expected italic ANSI, got %q", got)
	}
	stripped := stripANSI(got)
	if !strings.Contains(stripped, "italic") {
		t.Fatalf("expected 'italic' in output, got %q", stripped)
	}
}

func TestMarkdownWriterCodeSpan(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("use `code` inline\n"))
	got := buf.String()
	if !strings.Contains(got, ansiYellow) {
		t.Fatalf("expected yellow for code, got %q", got)
	}
	stripped := stripANSI(got)
	if !strings.Contains(stripped, "code") {
		t.Fatalf("expected 'code' in output, got %q", stripped)
	}
}

func TestMarkdownWriterCodeBlock(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("```go\n"))
	mw.Write([]byte("func main() {}\n"))
	mw.Write([]byte("```\n"))
	got := buf.String()
	if !strings.Contains(got, ansiYellow) {
		t.Fatalf("expected yellow for code block, got %q", got)
	}
	stripped := stripANSI(got)
	if !strings.Contains(stripped, "func main()") {
		t.Fatalf("expected 'func main()' in output, got %q", stripped)
	}
}

func TestMarkdownWriterUnorderedList(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("- item one\n"))
	mw.Write([]byte("* item two\n"))
	got := stripANSI(buf.String())
	if !strings.Contains(got, "•") {
		t.Fatalf("expected bullet character, got %q", got)
	}
	if !strings.Contains(got, "item one") || !strings.Contains(got, "item two") {
		t.Fatalf("expected items in output, got %q", got)
	}
}

func TestMarkdownWriterOrderedList(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("1. first\n"))
	mw.Write([]byte("2. second\n"))
	got := stripANSI(buf.String())
	if !strings.Contains(got, "1.") || !strings.Contains(got, "2.") {
		t.Fatalf("expected numbers in output, got %q", got)
	}
}

func TestMarkdownWriterBlockquote(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("> quoted text\n"))
	got := buf.String()
	if !strings.Contains(got, ansiGreen) {
		t.Fatalf("expected green for blockquote, got %q", got)
	}
	stripped := stripANSI(got)
	if !strings.Contains(stripped, "quoted text") {
		t.Fatalf("expected 'quoted text' in output, got %q", stripped)
	}
}

func TestMarkdownWriterHorizontalRule(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("---\n"))
	got := buf.String()
	if !strings.Contains(got, ansiDim) {
		t.Fatalf("expected dim for HR, got %q", got)
	}
}

func TestMarkdownWriterLink(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("see [example](https://example.com)\n"))
	got := buf.String()
	if !strings.Contains(got, ansiUnderline) {
		t.Fatalf("expected underline for link, got %q", got)
	}
	stripped := stripANSI(got)
	if !strings.Contains(stripped, "example") {
		t.Fatalf("expected 'example' in output, got %q", stripped)
	}
}

func TestMarkdownWriterTaskList(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("- [ ] task-item\n"))
	mw.Write([]byte("- [x] done-item\n"))
	got := stripANSI(buf.String())
	if !strings.Contains(got, "☐") {
		t.Fatalf("expected unchecked checkbox, got %q", got)
	}
	if !strings.Contains(got, "✓") {
		t.Fatalf("expected checked checkbox, got %q", got)
	}
	if !strings.Contains(got, "task-item") {
		t.Fatalf("expected task item, got %q", got)
	}
}

func TestMarkdownWriterStreamingPartialLine(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("# Hello"))
	if buf.Len() > 0 {
		t.Fatalf("expected buffer to hold partial line, got %q", buf.String())
	}
	mw.Write([]byte("\n"))
	got := stripANSI(buf.String())
	if !strings.Contains(got, "Hello") {
		t.Fatalf("expected 'Hello' after newline, got %q", got)
	}
}

func TestMarkdownWriterFlush(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("no newline at end"))
	mw.Flush()
	got := stripANSI(buf.String())
	if !strings.Contains(got, "no newline at end") {
		t.Fatalf("expected content after Flush, got %q", got)
	}
}

func TestMarkdownWriterMultiLine(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("# Title\n\n"))
	mw.Write([]byte("Some **bold** and *italic*.\n\n"))
	mw.Write([]byte("- list item\n"))
	got := stripANSI(buf.String())
	if !strings.Contains(got, "Title") {
		t.Fatalf("expected Title, got %q", got)
	}
	if !strings.Contains(got, "bold") {
		t.Fatalf("expected bold, got %q", got)
	}
	if !strings.Contains(got, "italic") {
		t.Fatalf("expected italic, got %q", got)
	}
	if !strings.Contains(got, "•") {
		t.Fatalf("expected bullet, got %q", got)
	}
}

func TestIsHorizontalRule(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"---", true},
		{"***", true},
		{"___", true},
		{"  ---  ", true},
		{"--", false},
		{"--x--", false},
		{"text", false},
		{"", false},
		{"--- ---", true}, // all non-space chars are '-'
		{"- - -", true},   // all non-space chars are '-'
	}
	for _, tt := range tests {
		got := isHorizontalRule(tt.input)
		if got != tt.want {
			t.Errorf("isHorizontalRule(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestEscANSI(t *testing.T) {
	input := "hello\033world"
	got := escANSI(input)
	if strings.Contains(got, "\033") {
		t.Fatalf("expected escaped ANSI, got %q", got)
	}
	if !strings.Contains(got, "␛") {
		t.Fatalf("expected replacement char, got %q", got)
	}
}

func TestMarkdownWriterInlineBoldAndCode(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("install `go build` with **make**\n"))
	got := buf.String()
	if !strings.Contains(got, ansiBold) {
		t.Fatalf("expected bold, got %q", got)
	}
	if !strings.Contains(got, ansiYellow) {
		t.Fatalf("expected yellow for code, got %q", got)
	}
}

func TestMarkdownWriterEmptyInput(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	n, err := mw.Write([]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 bytes, got %d", n)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected empty buffer, got %q", buf.String())
	}
}

func TestMarkdownWriterNestedBoldInList(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("- **important** item\n"))
	got := buf.String()
	if !strings.Contains(got, ansiBold) {
		t.Fatalf("expected bold in list, got %q", got)
	}
	if !strings.Contains(stripANSI(got), "important") {
		t.Fatalf("expected 'important' in output, got %q", stripANSI(got))
	}
}

func TestMarkdownWriterCodeBlockLang(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("```go\n"))
	mw.Write([]byte("package main\n"))
	mw.Write([]byte("```\n"))
	got := stripANSI(buf.String())
	if !strings.Contains(got, "go") {
		t.Fatalf("expected language tag 'go', got %q", got)
	}
	if !strings.Contains(got, "package main") {
		t.Fatalf("expected code content, got %q", got)
	}
}

func TestMarkdownWriterInlineCodeInHeading(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	mw.Write([]byte("## `code` in heading\n"))
	got := buf.String()
	if !strings.Contains(got, ansiBold) {
		t.Fatalf("expected bold for heading, got %q", got)
	}
	if !strings.Contains(got, ansiYellow) {
		t.Fatalf("expected yellow for inline code, got %q", got)
	}
}

func TestRenderMarkdownDiffBlock(t *testing.T) {
	src := "```diff\n--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n```\n"
	got := RenderMarkdown(src, 80)
	if !strings.Contains(got, ansiGreen) {
		t.Fatalf("expected green for +, got %q", got)
	}
	if !strings.Contains(got, ansiRed) {
		t.Fatalf("expected red for -, got %q", got)
	}
	plain := stripANSI(got)
	if !strings.Contains(plain, "old") || !strings.Contains(plain, "new") {
		t.Fatalf("plain=%q", plain)
	}
}

func TestRenderMarkdownCodeAndList(t *testing.T) {
	src := "# Title\n\n- item\n\n`code` and **bold**\n\n```go\nfunc main() {}\n```\n"
	got := RenderMarkdown(src, 80)
	if !strings.Contains(got, ansiYellow) {
		t.Fatalf("expected code yellow, got %q", got)
	}
	if !strings.Contains(stripANSI(got), "Title") {
		t.Fatalf("missing title: %q", stripANSI(got))
	}
}
