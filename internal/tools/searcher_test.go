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
		// Table cells: <tr><td>1.</td><td>Title</td></tr> strips to "1.Title" (no space between cells)
		{"<tr><td>1.</td><td>Title</td></tr>", "1.Title"},
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

func TestParseDDGResults(t *testing.T) {
	html := `<html><body>
<table>
<tr><td>1.</td><td class="result-link"><a href="https://example.com/test">Example Test</a></td></tr>
<tr><td></td><td class="result-snippet">This is a test snippet.</td></tr>
<tr><td>2.</td><td class="result-link"><a href="https://golang.org">Go Programming</a></td></tr>
<tr><td></td><td class="result-snippet">The Go programming language.</td></tr>
</table>
</body></html>`
	results := parseDDGResults(html, 5)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(results), results)
	}
	if !strings.Contains(results[0], "Example Test") {
		t.Fatalf("expected 'Example Test' in first result, got: %q", results[0])
	}
	if !strings.Contains(results[1], "Go Programming") {
		t.Fatalf("expected 'Go Programming' in second result, got: %q", results[1])
	}
}

func TestParseDDGResultsMax(t *testing.T) {
	html := `<html><body><table>
<tr><td>1.</td><td class="result-link"><a href="https://a.com">A</a></td></tr>
<tr><td></td><td class="result-snippet">Snippet A</td></tr>
<tr><td>2.</td><td class="result-link"><a href="https://b.com">B</a></td></tr>
<tr><td></td><td class="result-snippet">Snippet B</td></tr>
<tr><td>3.</td><td class="result-link"><a href="https://c.com">C</a></td></tr>
<tr><td></td><td class="result-snippet">Snippet C</td></tr>
</table></body></html>`
	results := parseDDGResults(html, 2)
	if len(results) != 2 {
		t.Fatalf("expected max 2 results, got %d", len(results))
	}
}

func TestStripHTMLTagsEntities(t *testing.T) {
	input := "&lt;b&gt;bold&lt;/b&gt;"
	want := "<b>bold</b>"
	got := stripHTMLTags(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIsTextContentTypeEmpty(t *testing.T) {
	if isTextContentType("") {
		t.Fatal("expected false for empty content type")
	}
}
