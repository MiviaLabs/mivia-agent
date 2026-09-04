package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// The glob tool's Execute, which is a paging surface as much as a search
// one: the model asks for a page of paths and decides what to read next
// from what comes back. An arm that reports "no matches" where a page was
// merely truncated tells the model the repository does not contain a file
// that it does.

func globWorkspace(t *testing.T, names ...string) *workspace.Root {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		full := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &workspace.Root{Abs: dir}
}

func runGlob(t *testing.T, tool *globTool, in map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Execute(context.Background(), raw)
}

// TestGlobRequiresAPattern: without one there is nothing to match, and
// defaulting to "everything" would walk the whole workspace for a caller
// that forgot an argument.
func TestGlobRequiresAPattern(t *testing.T) {
	tool := &globTool{ws: globWorkspace(t, "a.go"), maxBytes: 4096}
	if _, err := runGlob(t, tool, map[string]any{}); err == nil {
		t.Error("an empty pattern was accepted")
	} else if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("error %q does not say a pattern is required", err)
	}
}

// TestGlobRefusesAPathOutsideTheWorkspace: path is resolved through the
// workspace root, so an escape must fail rather than list the filesystem.
func TestGlobRefusesAPathOutsideTheWorkspace(t *testing.T) {
	tool := &globTool{ws: globWorkspace(t, "a.go"), maxBytes: 4096}
	if _, err := runGlob(t, tool, map[string]any{"pattern": "*", "path": "../.."}); err == nil {
		t.Error("a path escaping the workspace root was accepted")
	}
}

// TestGlobStopsWhenTheContextIsAlreadyCancelled: the walk can be long, so
// a cancelled context must be honoured before it starts rather than after
// it has read the tree.
func TestGlobStopsWhenTheContextIsAlreadyCancelled(t *testing.T) {
	tool := &globTool{ws: globWorkspace(t, "a.go"), maxBytes: 4096}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, json.RawMessage(`{"pattern":"*"}`)); err == nil {
		t.Error("a cancelled context still ran the walk")
	}
}

// TestGlobSaysNoMatchesRatherThanNothing: an empty result is a sentence,
// not an empty string, because a bare "" reads to the model as a tool
// that failed to answer.
func TestGlobSaysNoMatchesRatherThanNothing(t *testing.T) {
	tool := &globTool{ws: globWorkspace(t, "a.go"), maxBytes: 4096}
	out, err := runGlob(t, tool, map[string]any{"pattern": "*.nothing"})
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if out != "no matches" {
		t.Errorf("out = %q, want \"no matches\"", out)
	}
}

// TestGlobPagesWithOffsetAndLimitAndSaysWhatRemains: the continuation
// hint is the only way the model learns there is more, and it must name
// the offset that actually continues the page - an off-by-one there
// silently skips or repeats a path.
func TestGlobPagesWithOffsetAndLimitAndSaysWhatRemains(t *testing.T) {
	tool := &globTool{ws: globWorkspace(t, "a.go", "b.go", "c.go", "d.go"), maxBytes: 1 << 20}

	out, err := runGlob(t, tool, map[string]any{"pattern": "*.go", "limit": 2})
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("page = %q, want two paths and one continuation line", out)
	}
	if !strings.Contains(out, "2 more paths") || !strings.Contains(out, "offset=2") {
		t.Errorf("continuation hint %q does not name the remaining count and the next offset", lines[2])
	}

	// The offset it named must actually continue the listing.
	next, err := runGlob(t, tool, map[string]any{"pattern": "*.go", "limit": 2, "offset": 2})
	if err != nil {
		t.Fatalf("glob page 2: %v", err)
	}
	for _, first := range lines[:2] {
		if strings.Contains(next, first) {
			t.Errorf("page 2 repeats %q from page 1", first)
		}
	}
}

// TestGlobPastTheEndSaysNoMatches: an offset beyond the result set is an
// exhausted page, which is the same answer as an empty search - and the
// convention the model reads as "stop paging".
func TestGlobPastTheEndSaysNoMatches(t *testing.T) {
	tool := &globTool{ws: globWorkspace(t, "a.go"), maxBytes: 1 << 20}
	out, err := runGlob(t, tool, map[string]any{"pattern": "*.go", "offset": 50})
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if out != "no matches" {
		t.Errorf("out = %q, want \"no matches\"", out)
	}
}

// TestGlobReportsAByteTruncationInsteadOfNoMatches is the false-negative
// guard the code calls out: a page past a byte-truncated prefix must say
// the output was cut, because "no matches" would tell the model the
// workspace has no such file when the walk simply stopped early.
func TestGlobReportsAByteTruncationInsteadOfNoMatches(t *testing.T) {
	names := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		names = append(names, "pkg/file-with-a-fairly-long-name-"+string(rune('a'+i%26))+string(rune('a'+i/26))+".go")
	}
	// A byte budget small enough that the walk stops before listing all.
	tool := &globTool{ws: globWorkspace(t, names...), maxBytes: 64}

	out, err := runGlob(t, tool, map[string]any{"pattern": "pkg/*.go", "offset": 500})
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if out == "no matches" {
		t.Fatal("a byte-truncated walk reported \"no matches\"; the model would conclude the files do not exist")
	}
	if !strings.Contains(strings.ToLower(out), "truncat") && !strings.Contains(out, "bytes") {
		t.Errorf("out = %q, want it to say the output was cut", out)
	}
}
