package cli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderDiffLine(t *testing.T) {
	// Table drives the shipped classify→theme-token renderer (not a reimplementation).
	cases := []struct {
		name        string
		line        string
		wantTokens  []string
		banTokens   []string
		stripPrefix string
	}{
		{
			name:        "file header +++",
			line:        "+++ b/x.go",
			wantTokens:  []string{AnsiBgDark, AnsiBold, AnsiCyan, "+++ b/x.go"},
			banTokens:   []string{ansiBgDiffAdd},
			stripPrefix: "+++",
		},
		{
			name:        "file header ---",
			line:        "--- a/x.go",
			wantTokens:  []string{AnsiBgDark, AnsiBold, AnsiCyan, "--- a/x.go"},
			banTokens:   []string{ansiBgDiffDel},
			stripPrefix: "---",
		},
		{
			name:        "hunk header @@",
			line:        "@@ -1,3 +1,4 @@",
			wantTokens:  []string{AnsiMagenta, "@@ -1,3 +1,4 @@"},
			stripPrefix: "@@",
		},
		{
			name:        "added line",
			line:        "+added",
			wantTokens:  []string{ansiBgDiffAdd, AnsiGreen, "+added"},
			stripPrefix: "+",
		},
		{
			name:        "removed line",
			line:        "-removed",
			wantTokens:  []string{ansiBgDiffDel, AnsiRed, "-removed"},
			stripPrefix: "-",
		},
		{
			name:       "context line",
			line:       " context",
			wantTokens: []string{AnsiBgDark, AnsiDim, " context"},
		},
		{
			name:       "empty line",
			line:       "",
			wantTokens: []string{AnsiBgDark, AnsiDim},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRenderDiffLineCase(t, tc.line, tc.wantTokens, tc.banTokens, tc.stripPrefix)
		})
	}
}

func assertRenderDiffLineCase(t *testing.T, line string, wantTokens, banTokens []string, stripPrefix string) {
	t.Helper()
	got := RenderDiffLine(line)
	if got == "" {
		t.Fatal("renderDiffLine returned empty")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q", got)
	}
	for _, tok := range wantTokens {
		if !strings.Contains(got, tok) {
			t.Fatalf("missing token %q in %q", tok, got)
		}
	}
	for _, tok := range banTokens {
		if strings.Contains(got, tok) {
			t.Fatalf("banned token %q present in %q", tok, got)
		}
	}
	if stripPrefix == "" {
		return
	}
	plain := stripANSI(got)
	if !strings.Contains(plain, stripPrefix) {
		t.Fatalf("after ANSI strip, prefix %q lost: plain=%q", stripPrefix, plain)
	}
	if stripPrefix == "+" || stripPrefix == "-" {
		trimmed := strings.TrimLeft(plain, " ")
		if !strings.HasPrefix(trimmed, stripPrefix) {
			t.Fatalf("+/- gutter lost: plain=%q", plain)
		}
	}
}

// TestRenderDiffLineHunkIsMagenta pins §3: @@ uses hunk/magenta, not context/dim.
func TestRenderDiffLineHunkIsMagenta(t *testing.T) {
	hunk := RenderDiffLine("@@ -1 +1 @@")
	ctx := RenderDiffLine(" context")
	if !strings.Contains(hunk, AnsiMagenta) {
		t.Fatalf("@@ must carry magenta/hunk token, got %q", hunk)
	}
	if strings.Contains(hunk, AnsiDim) {
		t.Fatalf("@@ must not use dim/context styling, got %q", hunk)
	}
	if hunk == ctx {
		t.Fatalf("@@ rendered equal to context path; hunk must be distinct")
	}
	if !strings.Contains(ctx, AnsiDim) {
		t.Fatalf("context fixture must use dim token, got %q", ctx)
	}
}

// TestRenderDiffLineHeaderPrecedence: +++ is header (bold cyan), not bare +.
func TestRenderDiffLineHeaderPrecedence(t *testing.T) {
	header := RenderDiffLine("+++ b/x.go")
	add := RenderDiffLine("+only")
	if strings.Contains(header, ansiBgDiffAdd) {
		t.Fatalf("+++ classified as add: %q", header)
	}
	if !strings.Contains(header, AnsiCyan) || !strings.Contains(header, AnsiBold) {
		t.Fatalf("+++ must be bold cyan header: %q", header)
	}
	if !strings.Contains(add, ansiBgDiffAdd) {
		t.Fatalf("bare + must be add path: %q", add)
	}
}
