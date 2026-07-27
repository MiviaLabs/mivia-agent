package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchLocalGrep(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "hello.go"), []byte("package main\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "world.go"), []byte("package main\nfunc World() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws.Abs, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "pkg", "util.go"), []byte("package pkg\nfunc Util() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"local","query":"func Hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello.go") {
		t.Fatalf("expected hello.go in local search, got: %q", out)
	}

	out, err = reg.Execute(ctx, "search", json.RawMessage(`{"scope":"local","query":"Util","glob":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pkg/util.go") {
		t.Fatalf("expected pkg/util.go in filtered search, got: %q", out)
	}
}

func TestSearchLocalFilenameMatch(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "important.go"), []byte("code"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"important"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "important.go") {
		t.Fatalf("expected important.go in filename match, got: %q", out)
	}
}

func TestSearchLocalNoMatches(t *testing.T) {
	_, reg := setupWS(t)
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"zzz_nonexistent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "no matches found" {
		t.Fatalf("expected 'no matches found', got: %q", out)
	}
}

func TestSearchLocalRequiresQuery(t *testing.T) {
	_, reg := setupWS(t)
	_, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local"}`))
	if err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestSearchLocalSkipsSecretPaths(t *testing.T) {
	ws, reg := setupWS(t)
	_ = os.WriteFile(filepath.Join(ws.Abs, ".env"), []byte("SECRET=1"), 0o600)
	_ = os.WriteFile(filepath.Join(ws.Abs, "main.go"), []byte("package main"), 0o644)
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"SECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, ".env") {
		t.Fatalf("search should skip .env files: %q", out)
	}
}

func TestSearchLocalSkipsBinary(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "data.bin"), []byte{0xff, 0xfe, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "note.txt"), []byte("binary data nearby"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"data"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "data.bin") && !strings.Contains(out, "note.txt") {
		t.Fatalf("expected filename match for data.bin or content match for note.txt, got: %q", out)
	}
}

func TestSearchLocalMaxResults(t *testing.T) {
	ws, reg := setupWS(t)
	var content strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&content, "match-line-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "many.txt"), []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"match-line","max_results":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation, got: %q", out)
	}
}

func TestSearchInvalidScope(t *testing.T) {
	_, reg := setupWS(t)
	_, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"invalid","query":"test"}`))
	if err == nil {
		t.Fatal("expected error for invalid scope")
	}
}

func TestSearchURLValidation(t *testing.T) {
	_, reg := setupWS(t)
	_, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"url"}`))
	if err == nil {
		t.Fatal("expected error for missing url")
	}
	_, err = reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"url","url":"ftp://bad.com"}`))
	if err == nil {
		t.Fatal("expected error for non-http URL")
	}
}

func TestSearchWebRequiresQuery(t *testing.T) {
	_, reg := setupWS(t)
	_, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"web"}`))
	if err == nil {
		t.Fatal("expected error for missing web query")
	}
}

func TestStripHTMLTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<b>bold</b>", "bold"},
		{"<a href=\"x\">link</a>", "link"},
		{"hello &amp; world", "hello & world"},
		{"&lt;tag&gt;", "<tag>"},
		{"multi\n  \nline", "multi line"},
		// Table cells: <tr><td>1.</td><td>Title</td></tr> yields newline after </tr>
		{"<tr><td>1.</td><td>Title</td></tr>", "1.Title\n"},
		{"", ""},
		{"no tags", "no tags"},
		{"Text &nbsp; spaced", "Text  spaced"},
	}
	for _, tt := range tests {
		got := stripHTMLTags(tt.input)
		if got != tt.want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDecodeHTMLEntities(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"&amp;", "&"},
		{"&lt;br/&gt;", "<br/>"},
		{"&quot;hello&quot;", "\"hello\""},
		{"&#x27;apos&#x27;", "'apos'"},
		{"plain text", "plain text"},
	}
	for _, tt := range tests {
		got := decodeHTMLEntities(tt.input)
		if got != tt.want {
			t.Errorf("decodeHTMLEntities(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsTextContentType(t *testing.T) {
	tests := map[string]bool{
		"text/html":                 true,
		"text/plain; charset=utf-8": true,
		"application/json":          true,
		"application/xml":           true,
		"image/png":                 false,
		"application/octet-stream":  false,
		"":                          false,
	}
	for ct, want := range tests {
		got := isTextContentType(ct)
		if got != want {
			t.Errorf("isTextContentType(%q) = %v, want %v", ct, got, want)
		}
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := "hello world"
	if got := truncateUTF8(s, 5); got != "hello" {
		t.Fatalf("got %q", got)
	}
	s = "héllo"
	if got := truncateUTF8(s, 3); got != "hé" {
		t.Fatalf("expected 'hé', got %q (len=%d)", got, len(got))
	}
	if got := truncateUTF8(s, 100); got != s {
		t.Fatalf("expected full string")
	}
}

func TestSearchToolRegistered(t *testing.T) {
	_, reg := setupWS(t)
	_, ok := reg.Get("search")
	if !ok {
		t.Fatal("search tool not registered")
	}
}

func TestSearchOpenAISchema(t *testing.T) {
	_, reg := setupWS(t)
	tools := reg.OpenAITools()
	found := false
	for _, tool := range tools {
		fn, _ := tool["function"].(map[string]any)
		if fn["name"] == "search" {
			found = true
			params, _ := fn["parameters"].(map[string]any)
			props, _ := params["properties"].(map[string]any)
			scope, ok := props["scope"]
			if !ok {
				t.Fatal("search schema missing scope property")
			}
			scopeObj, _ := scope.(map[string]any)
			enumRaw := scopeObj["enum"]
			enumLen := 0
			switch v := enumRaw.(type) {
			case []any:
				enumLen = len(v)
			case []string:
				enumLen = len(v)
			}
			if enumLen != 3 {
				t.Fatalf("expected 3 enum values (local, web, url), got %v (type %T)", enumRaw, enumRaw)
			}
		}
	}
	if !found {
		t.Fatal("search tool not found in OpenAI schema")
	}
}

func TestUnwrapDDGRedirect(t *testing.T) {
	got := unwrapDDGRedirect("https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpath")
	if got != "https://example.com/path" {
		t.Fatalf("got %q", got)
	}
	if unwrapDDGRedirect("https://example.com/direct") != "https://example.com/direct" {
		t.Fatal("direct URL should pass through")
	}
}

// TestSearchLocalPlainTextQuery verifies that queries with regex metacharacters
// are treated as plain text, not regex patterns.
func TestSearchLocalPlainTextQuery(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "calc.go"), []byte("package main\nfunc (a + b) {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Parens and plus are regex metacharacters; they should match literally.
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"(a + b)"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "calc.go") {
		t.Fatalf("expected plain-text query to match content, got: %q", out)
	}
}

// TestSearchLocalCaseInsensitive verifies case-insensitive matching.
func TestSearchLocalCaseInsensitive(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "hello.go"), []byte("package main\nfunc HelloWorld() {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Should match case-insensitively.
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"helloworld"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello.go") {
		t.Fatalf("expected case-insensitive match, got: %q", out)
	}
}

// TestSearchLocalSkipsSymlinks verifies symlinked files are not followed.
func TestSearchLocalSkipsSymlinks(t *testing.T) {
	ws, reg := setupWS(t)
	// Create a file outside the workspace.
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlink to it inside the workspace.
	if err := os.Symlink(outsideFile, filepath.Join(ws.Abs, "evil_symlink.txt")); err != nil {
		t.Skip("symlinks not supported on this platform:", err)
	}
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "evil_symlink") {
		t.Fatalf("search should skip symlinks, got: %q", out)
	}
}

// TestStripHTMLTagsBlockNewlines verifies that block-level tags produce newlines.
func TestStripHTMLTagsBlockNewlines(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<p>First</p><p>Second</p>", "First\nSecond\n"},
		{"<ul><li>A</li><li>B</li></ul>", "A\nB\n\n"},
		{"before<br>after", "before\nafter"},
		{"<div>Content</div>More", "Content\nMore"},
		{"<h1>Title</h1><p>Body</p>", "Title\nBody\n"},
	}
	for _, tt := range tests {
		got := stripHTMLTags(tt.input)
		if got != tt.want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestStripHTMLLiteralLessThan verifies that literal '<' in text is not eaten.
func TestStripHTMLLiteralLessThan(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"x < y", "x < y"},
		{"template<T> foo", "template<T> foo"},
		{"a < b && c > d", "a < b && c > d"},
		{"<b>bold</b> and x < y", "bold and x < y"},
	}
	for _, tt := range tests {
		got := stripHTMLTags(tt.input)
		if got != tt.want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
