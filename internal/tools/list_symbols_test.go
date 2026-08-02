package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type fakeSymbolSearcher struct {
	result    codeintel.SymbolResult
	err       error
	lastLimit int
	lastPref  string
}

func (f *fakeSymbolSearcher) Symbols(ctx context.Context, prefix string, limit int) (codeintel.SymbolResult, error) {
	f.lastPref, f.lastLimit = prefix, limit
	return f.result, f.err
}

func navWorkspace(t *testing.T) *workspace.Root {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func writeWorkspaceFile(t *testing.T, ws *workspace.Root, rel, body string) {
	t.Helper()
	path := filepath.Join(ws.Abs, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func decodeSymbols(t *testing.T, out string) listSymbolsResult {
	t.Helper()
	var got listSymbolsResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	return got
}

func TestListSymbolsRequiresExactlyOneMode(t *testing.T) {
	ws := navWorkspace(t)
	tool := &listSymbolsTool{ws: ws, searcher: &fakeSymbolSearcher{}, outline: codeintel.FileOutline, maxBytes: 10000, limit: 50}

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected an error when neither path nor symbol_prefix is given")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"a.go","symbol_prefix":"A"}`)); err == nil {
		t.Error("expected an error when both path and symbol_prefix are given")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("expected an error for invalid JSON")
	}
}

func TestListSymbolsFileModeNeedsNoAnalyzer(t *testing.T) {
	ws := navWorkspace(t)
	writeWorkspaceFile(t, ws, "pkg/thing.go", "package pkg\n\ntype Thing struct{ Name string }\n\nfunc (t Thing) Do() {}\n")
	// searcher is nil on purpose: file mode must not touch the analyzer.
	tool := &listSymbolsTool{ws: ws, searcher: nil, outline: codeintel.FileOutline, maxBytes: 10000}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"pkg/thing.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := decodeSymbols(t, out)
	if got.Error != "" {
		t.Fatalf("unexpected error field: %s", got.Error)
	}
	if len(got.Symbols) == 0 {
		t.Fatal("no symbols returned")
	}
	for _, s := range got.Symbols {
		if filepath.IsAbs(s.Path) {
			t.Errorf("symbol path %q is absolute; results must be workspace-relative", s.Path)
		}
	}
}

func TestListSymbolsFileModeRejectsEscapesAndDirectories(t *testing.T) {
	ws := navWorkspace(t)
	tool := &listSymbolsTool{ws: ws, outline: codeintel.FileOutline, maxBytes: 10000}

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"../outside.go"}`)); err == nil {
		t.Error("expected a workspace-escape error")
	}
	if err := os.MkdirAll(filepath.Join(ws.Abs, "adir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"adir"}`)); err == nil {
		t.Error("expected an error for a directory path")
	}
}

func TestListSymbolsFileModeBlocksSecretPaths(t *testing.T) {
	ws := navWorkspace(t)
	writeWorkspaceFile(t, ws, ".env.go", "package secret\n\nconst Token = \"x\"\n")
	tool := &listSymbolsTool{
		ws: ws, outline: codeintel.FileOutline, maxBytes: 10000,
		secretPathPatterns: []string{".env"},
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":".env.go"}`)); err == nil {
		t.Fatal("expected a secret-path refusal")
	}
}

func TestListSymbolsParseErrorIsOutputNotFailure(t *testing.T) {
	ws := navWorkspace(t)
	writeWorkspaceFile(t, ws, "broken.go", "package broken\n\nfunc Oops( {\n")
	tool := &listSymbolsTool{ws: ws, outline: codeintel.FileOutline, maxBytes: 10000}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"broken.go"}`))
	if err != nil {
		t.Fatalf("a parse failure belongs in the output body, not the call: %v", err)
	}
	if got := decodeSymbols(t, out); got.Error == "" {
		t.Fatal("expected the parse error in the output body")
	}
}

func TestListSymbolsWorkspaceModeUsesDefaultLimit(t *testing.T) {
	ws := navWorkspace(t)
	fake := &fakeSymbolSearcher{}
	tool := &listSymbolsTool{ws: ws, searcher: fake, outline: codeintel.FileOutline, maxBytes: 10000, limit: 50}

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol_prefix":"Plan"}`)); err != nil {
		t.Fatal(err)
	}
	if fake.lastLimit != 50 {
		t.Errorf("limit passed to the analyzer = %d, want the registered default 50", fake.lastLimit)
	}
	if fake.lastPref != "Plan" {
		t.Errorf("prefix = %q", fake.lastPref)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol_prefix":"Plan","limit":3}`)); err != nil {
		t.Fatal(err)
	}
	if fake.lastLimit != 3 {
		t.Errorf("explicit limit not honored: %d", fake.lastLimit)
	}
}

func TestListSymbolsUnavailableIsOutputNotFailure(t *testing.T) {
	ws := navWorkspace(t)
	fake := &fakeSymbolSearcher{err: fmt.Errorf("analysis unavailable: %w", codeintel.ErrUnavailable)}
	tool := &listSymbolsTool{ws: ws, searcher: fake, maxBytes: 10000, limit: 50}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol_prefix":"Plan"}`))
	if err != nil {
		t.Fatalf("availability errors belong in the output body: %v", err)
	}
	got := decodeSymbols(t, out)
	if !strings.Contains(got.Error, "analysis unavailable") {
		t.Fatalf("output = %s", out)
	}
	if !errors.Is(fake.err, codeintel.ErrUnavailable) {
		t.Fatal("fixture no longer models an availability error")
	}
}

func TestListSymbolsNoAnalyzerIsOutputNotFailure(t *testing.T) {
	ws := navWorkspace(t)
	tool := &listSymbolsTool{ws: ws, searcher: nil, maxBytes: 10000, limit: 50}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol_prefix":"Plan"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no analyzer available") {
		t.Fatalf("output = %s", out)
	}
}

// TestListSymbolsSelfTruncatesToBudget: at a budget far below the natural
// result, output stays valid JSON, stays within the bound, and says it was cut.
func TestListSymbolsSelfTruncatesToBudget(t *testing.T) {
	ws := navWorkspace(t)
	var body strings.Builder
	body.WriteString("package big\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&body, "func Declaration%d%s() int { return %d }\n", i, strings.Repeat("N", 120), i)
	}
	writeWorkspaceFile(t, ws, "big.go", body.String())

	for _, budget := range []int{4000, 1000, 300, 120} {
		tool := &listSymbolsTool{ws: ws, outline: codeintel.FileOutline, maxBytes: budget}
		out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"big.go"}`))
		if err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		if len(out) > budget {
			t.Errorf("budget %d: output is %d bytes", budget, len(out))
		}
		got := decodeSymbols(t, out)
		if !got.Truncated {
			t.Errorf("budget %d: truncation not reported", budget)
		}
	}
}

// TestListSymbolsLimitTruncatesFileOutline pins the count cap in file mode.
func TestListSymbolsLimitTruncatesFileOutline(t *testing.T) {
	ws := navWorkspace(t)
	writeWorkspaceFile(t, ws, "many.go", "package many\n\nfunc A() {}\nfunc B() {}\nfunc C() {}\n")
	tool := &listSymbolsTool{ws: ws, outline: codeintel.FileOutline, maxBytes: 100000}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"many.go","limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	got := decodeSymbols(t, out)
	if len(got.Symbols) != 2 || !got.Truncated {
		t.Fatalf("got %d symbols (truncated=%v), want 2 and truncated", len(got.Symbols), got.Truncated)
	}
}

func TestListSymbolsIsReadOnly(t *testing.T) {
	tool := &listSymbolsTool{maxBytes: 1000}
	if cap := tool.Capability(nil); cap.Class != ExecutionRead {
		t.Fatalf("class = %v, want ExecutionRead", cap.Class)
	}
	if tool.ResultBudgetBytes() != 1000 {
		t.Fatalf("declared budget = %d", tool.ResultBudgetBytes())
	}
}

// TestListSymbolsEmptyResultIsAnEmptyList: no matches must read as an empty
// list, not as JSON null that the model has to interpret.
func TestListSymbolsEmptyResultIsAnEmptyList(t *testing.T) {
	ws := navWorkspace(t)
	tool := &listSymbolsTool{ws: ws, searcher: &fakeSymbolSearcher{}, maxBytes: 10000, limit: 50}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol_prefix":"NothingMatchesThis"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"symbols":[]`) {
		t.Fatalf("output = %s, want an empty symbols list", out)
	}
	var probe struct {
		Symbols *[]codeintel.Symbol `json:"symbols"`
	}
	if err := json.Unmarshal([]byte(out), &probe); err != nil || probe.Symbols == nil {
		t.Fatalf("symbols field is absent or null: %s", out)
	}
}

// TestListSymbolsNonSourceFileIsUnavailableNotAParseError: pointing file mode
// at a file the outline backend does not handle returns the explicit
// unavailable shape (D4), not a Go parser error.
func TestListSymbolsNonSourceFileIsUnavailableNotAParseError(t *testing.T) {
	ws := navWorkspace(t)
	writeWorkspaceFile(t, ws, "script.py", "def do():\n    return 1\n")
	tool := &listSymbolsTool{ws: ws, outline: codeintel.FileOutline, maxBytes: 10000}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"script.py"}`))
	if err != nil {
		t.Fatalf("an unsupported file type belongs in the output body: %v", err)
	}
	got := decodeSymbols(t, out)
	if !strings.Contains(got.Error, "analysis unavailable") {
		t.Fatalf("error = %q, want the shared analysis-unavailable shape", got.Error)
	}
}
