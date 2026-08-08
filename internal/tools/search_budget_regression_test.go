package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Defect C2 (DC-6/DC-9): grep and glob appended the walk-errors trailer
// (errs.notice(), capped at 10 entries) AFTER byte-budget accounting, so an
// honest partial result could exceed its declared ResultBudgetBytes and be
// destroyed wholesale by the dispatcher backstop. These tests drive the real
// entry point Registry.Execute('grep'|'glob') -> <tool>.Execute -> executeGrep
// /executeGlob -> walkGrep/walkGlob -> errs.notice().
//
// Failing-first design: a single small match would pass before the fix (the
// output stays far under the budget), so each regression test fills the byte
// budget near capacity with one big matching file (grep) or enough paths
// (glob) so the unbudgeted trailer demonstrably pushes the result over
// maxBytes = 4096.

// makeBrokenSymlinks creates n broken symlinks (non-regular files) named
// 000_bad0..000_badN-1, which sort lexically before zzz.txt. walkGrep records
// "not a regular file" for each via the d.Info() branch - deterministic even
// when running as root, unlike chmod-000.
func makeBrokenSymlinks(t *testing.T, ws string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		link := filepath.Join(ws, fmt.Sprintf("000_bad%d", i))
		if err := os.Symlink("does-not-exist-target", link); err != nil {
			t.Fatal(err)
		}
	}
}

func writeManyMatchingLines(t *testing.T, ws string, name string, lines int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString("match\n")
	}
	if err := os.WriteFile(filepath.Join(ws, name), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGrepResultStaysWithinDeclaredBudgetWithWalkErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture is unix-only")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws, MaxReadBytes: 4096})
	makeBrokenSymlinks(t, ws.Abs, 10)
	writeManyMatchingLines(t, ws.Abs, "zzz.txt", 400)

	out, err := reg.Execute(context.Background(), "grep", json.RawMessage(`{"pattern":"match"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 4096 {
		t.Fatalf("grep result %d bytes exceeds its declared %d-byte budget; tail=%q",
			len(out), 4096, out[max(0, len(out)-200):])
	}
	if !strings.Contains(out, "files skipped") {
		t.Fatalf("walk-errors notice missing; tail=%q", out[max(0, len(out)-200):])
	}
}

func TestGlobResultStaysWithinDeclaredBudgetWithWalkErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based walk-error fixture is unix-only")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// walkGlob records only WalkDir-level walkErr, so unreadable directories
	// (lexically before the .md files) are its only reachable error source.
	blocked := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		dir := filepath.Join(ws.Abs, fmt.Sprintf("blocked%d", i))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		blocked = append(blocked, dir)
	}
	// Pre-flight: chmod must actually deny reads, or the fixture proves
	// nothing (root and some ACL setups ignore file modes).
	for _, dir := range blocked {
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadDir(dir); err == nil {
			t.Skipf("chmod 000 not enforced on %s (root or ACL); skipping permission-based fixture", dir)
		}
	}
	for i := 0; i < 220; i++ {
		name := fmt.Sprintf("nnnnnnnnnnnnnnn%05d.md", i) // 23-char names
		if err := os.WriteFile(filepath.Join(ws.Abs, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws, MaxReadBytes: 4096})
	out, err := reg.Execute(context.Background(), "glob", json.RawMessage(`{"pattern":"**/*.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 4096 {
		t.Fatalf("glob result %d bytes exceeds its declared %d-byte budget; tail=%q",
			len(out), 4096, out[max(0, len(out)-200):])
	}
	if !strings.Contains(out, "files skipped") {
		t.Fatalf("walk-errors notice missing; tail=%q", out[max(0, len(out)-200):])
	}
}

func TestGrepNoMatchesNoErrors(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws, MaxReadBytes: 4096})
	out, err := reg.Execute(context.Background(), "grep", json.RawMessage(`{"pattern":"match"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "no matches" {
		t.Fatalf("got %q, want \"no matches\"", out)
	}
}

func TestGrepMatchCapNoticeStillFitsBudget(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(ws.Abs, fmt.Sprintf("f%d.txt", i)), []byte("match\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tool := &grepTool{ws: ws, maxMatches: 3, maxBytes: 4096}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"match"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "... truncated at 3 matches") {
		t.Fatalf("match-cap notice missing; tail=%q", out[max(0, len(out)-80):])
	}
	if len(out) > 4096 {
		t.Fatalf("result %d bytes exceeds the %d-byte budget", len(out), 4096)
	}
}

func TestTinyBudgetWalkErrorsStillBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture is unix-only")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	makeBrokenSymlinks(t, ws.Abs, 10)
	writeManyMatchingLines(t, ws.Abs, "zzz.txt", 200)
	// A reserve larger than the budget itself must still keep the walk
	// bounded: the first match trips errMaxBytes and the byte notice is
	// returned - the budget must never turn negative and unbounded.
	tool := &grepTool{ws: ws, maxMatches: 0, maxBytes: 64}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"match"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 64 {
		t.Fatalf("result %d bytes exceeds the %d-byte budget: %q", len(out), 64, out)
	}
	if !strings.HasPrefix(out, "... truncated at 64 bytes") {
		t.Fatalf("byte notice missing; got %q", out)
	}
}

func TestWalkErrorNoticeCountCapped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture is unix-only")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	makeBrokenSymlinks(t, ws.Abs, 12) // more than the 10-entry cap
	if err := os.WriteFile(filepath.Join(ws.Abs, "zzz.txt"), []byte("match\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws, MaxReadBytes: 4096})
	out, err := reg.Execute(context.Background(), "grep", json.RawMessage(`{"pattern":"match"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "... 10 files skipped (first: 000_bad0: not a regular file)") {
		t.Fatalf("capped-count notice missing or misnamed; got %q", out)
	}
	if len(out) > 4096 {
		t.Fatalf("result %d bytes exceeds the %d-byte budget", len(out), 4096)
	}
}
