package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Audit regression, round 2 (following 0f6e524 and 2dca36b).
//
// DeriveOutputCeiling makes the dispatcher's hard-fail output backstop clear
// every tool-DECLARED result budget. Three default tools declared none,
// because they capped their results by COUNT (list_dir: entries, glob/grep:
// matches) or by request size (write_file), and none of those bounds bounds
// BYTES: a file name reaches 255 bytes, a workspace-relative path approaches
// PATH_MAX, and an overwrite diff is sized by the file already on disk. Their
// results could therefore exceed the ceiling. Honest oversize is now
// truncated with notice; only runaway (>ceiling×4) is destroyed as
// {"error":"output budget exceeded","status":"failed"}.
//
// Each test below reproduces one confirmed defect through the production
// composition: default registry + runtime.NewToolDispatcher(reg, Policy{}) +
// Invoke, the same path the agent loop builds when Options.Dispatcher is nil.

// budgetRegressionDispatch runs one tool call through a production dispatcher
// and returns the model-visible body plus the derived ceiling.
func budgetRegressionDispatch(t *testing.T, reg *tools.Registry, name, input string) (string, int) {
	t.Helper()
	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	res := d.Invoke(context.Background(), Request{
		ID: "regression", Kind: Tool, Name: name, Input: json.RawMessage(input),
	})
	body := string(res.Output)
	if res.Err != nil && !strings.Contains(body, "output budget exceeded") {
		t.Fatalf("%s failed unexpectedly: %v (body=%q)", name, res.Err, body)
	}
	// The bound to report is the one actually enforced for THIS tool, not the
	// policy cap. The policy value is now only a global maximum that per-tool
	// ceilings may tighten below; asserting against it would let a ceiling
	// binding below honest output pass unnoticed.
	return body, d.OutputCeiling(Tool, name)
}

func assertNotDestroyed(t *testing.T, tool, body string, ceiling int) {
	t.Helper()
	if strings.Contains(body, "output budget exceeded") {
		t.Fatalf("dispatcher destroyed a config-compliant %s result at ceiling %d: %s", tool, ceiling, body)
	}
	if len(body) > ceiling {
		t.Fatalf("%s result is %d bytes, over the derived ceiling %d", tool, len(body), ceiling)
	}
}

// TestRegression_ListDirLongNamesNotDestroyedByDispatcherCeiling reproduces
// defect A. max_list_dir_entries is a first-class [tools] knob; an operator
// who raises it to 5000 and lists a directory of 4000 files with ~100-byte
// names used to get 419999 bytes of honest output replaced, whole, by
// {"error":"output budget exceeded","status":"failed"} at ceiling 331776.
func TestRegression_ListDirLongNamesNotDestroyedByDispatcherCeiling(t *testing.T) {
	ws := regressionWorkspace(t)
	name := strings.Repeat("n", 100)
	const total = 4000
	for i := 0; i < total; i++ {
		if err := os.WriteFile(filepath.Join(ws.Abs, fmt.Sprintf("%s%04d", name, i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws, MaxListDirEntries: 5000, MaxReadBytes: 256 * 1024})

	body, ceiling := budgetRegressionDispatch(t, reg, "list_dir", `{"path":"."}`)
	assertNotDestroyed(t, "list_dir", body, ceiling)

	// Truncated but honest: the listing names real entries and its notice
	// accounts for every entry it withheld.
	if !strings.Contains(body, name+"0000") {
		t.Fatalf("listing carries no real entries; head=%q", body[:min(len(body), 120)])
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	last := lines[len(lines)-1]
	var budget, omitted int
	if n, err := fmt.Sscanf(last, "... truncated at %d bytes (%d more)", &budget, &omitted); n != 2 || err != nil {
		t.Fatalf("expected an honest byte-truncation notice, got %q", last)
	}
	if delivered := len(lines) - 1; delivered+omitted != total {
		t.Fatalf("listing claims %d delivered + %d omitted = %d, but %d entries exist",
			delivered, omitted, delivered+omitted, total)
	}
}

// TestRegression_GlobDeepPathsNotDestroyedByDispatcherCeiling reproduces
// defect B, which needed no operator config at all: glob's cap is a hardcoded
// 200 MATCHES and each match is a workspace-relative path, so a deep tree
// produced 342028 bytes against a 331776 ceiling on the DEFAULT registry.
func TestRegression_GlobDeepPathsNotDestroyedByDispatcherCeiling(t *testing.T) {
	ws := regressionWorkspace(t)
	deep := ws.Abs
	depth, componentLen := 8, 200
	if runtime.GOOS == "darwin" {
		depth, componentLen = 4, 120
	}
	for i := 0; i < depth; i++ {
		deep = filepath.Join(deep, strings.Repeat("d", componentLen))
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	stem := strings.Repeat("f", 95)
	for i := 0; i < 250; i++ {
		if err := os.WriteFile(filepath.Join(deep, fmt.Sprintf("%s%03d.md", stem, i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws, MaxReadBytes: 256 * 1024})

	body, ceiling := budgetRegressionDispatch(t, reg, "glob", `{"pattern":"**/*.md"}`)
	assertNotDestroyed(t, "glob", body, ceiling)

	if !strings.Contains(body, stem) {
		t.Fatalf("glob result carries no real paths; head=%q", body[:min(len(body), 120)])
	}
	if runtime.GOOS != "darwin" && !strings.Contains(body, "... truncated at ") {
		t.Fatalf("glob result claims completeness it does not have; tail=%q", body[max(0, len(body)-120):])
	}
}

// TestRegression_WriteFileOverwriteDiffNotDestroyedByDispatcherCeiling
// reproduces a third instance of the same class, found while building the
// registry-wide invariant below and confirmed on the DEFAULT config: an
// overwrite result carries a unified diff of the file already on disk, so a
// 5-byte write over a 378890-byte file produced 382986 bytes and was
// destroyed. The write had already happened, so the model was told a
// completed write failed.
func TestRegression_WriteFileOverwriteDiffNotDestroyedByDispatcherCeiling(t *testing.T) {
	ws := regressionWorkspace(t)
	var old strings.Builder
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&old, "old line %d %s\n", i, strings.Repeat("o", 80))
	}
	target := filepath.Join(ws.Abs, "f.txt")
	if err := os.WriteFile(target, []byte(old.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws, MaxReadBytes: 256 * 1024})

	body, ceiling := budgetRegressionDispatch(t, reg, "write_file", `{"path":"f.txt","content":"tiny\n"}`)
	assertNotDestroyed(t, "write_file", body, ceiling)

	if !strings.Contains(body, "wrote f.txt (") {
		t.Fatalf("write confirmation lost; head=%q", body[:min(len(body), 120)])
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "tiny\n" {
		t.Fatalf("file content = %q; the reported write must be the real one", string(data))
	}
}

func regressionWorkspace(t *testing.T) *workspace.Root {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ws
}
