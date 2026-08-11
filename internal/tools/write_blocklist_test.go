package tools

// RED tests (ADLC Step 4a): the write tools check only the symlink-RESOLVED
// path against WritePathDenylist, never the lexical requested path. Each test
// here drives a write-class tool through the lexical path "protected/<file>"
// where "protected" is an in-workspace symlink to the "allowed" directory. The
// blocklist names "protected"; the tools resolve the path to allowed/<file>
// and so (today) let the write through. These tests fail until the tools also
// consult the lexical path.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// setupProtectedSymlink creates the "allowed" directory and an in-workspace
// symlink "protected" -> "allowed". Subtests skip when symlinks are
// unavailable (e.g. Windows without privilege).
func setupProtectedSymlink(t *testing.T, ws *workspace.Root) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(ws.Abs, "allowed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ws.Abs, "allowed"), filepath.Join(ws.Abs, "protected")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

// mustBlocked runs a tool with the given args and asserts it fails with an
// error containing "blocked".
func mustBlocked(t *testing.T, reg *Registry, name string, args any) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), name, raw)
	if err == nil {
		t.Fatalf("%s on lexical protected path succeeded (out=%q); expected 'blocked' error", name, out)
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("%s error %q does not mention 'blocked'", name, err)
	}
}

// TestWriteFileBlocklistLexicalSymlinkRefused pins that write_file refuses the
// lexical path protected/secret.txt even though the symlink resolves it inside
// the workspace to allowed/secret.txt.
func TestWriteFileBlocklistLexicalSymlinkRefused(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{WritePathDenylist: []string{"protected"}})
	setupProtectedSymlink(t, ws)

	mustBlocked(t, reg, "write_file", map[string]any{"path": "protected/secret.txt", "content": "x"})

	if _, err := os.Stat(filepath.Join(ws.Abs, "allowed", "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("lexical protected path bypassed blocklist: allowed/secret.txt exists (stat err=%v)", err)
	}
}

// TestWriteFileBlocklistLexicalSymlinkAbsoluteRefused is the same refusal
// check for the absolute form of the lexical protected path.
func TestWriteFileBlocklistLexicalSymlinkAbsoluteRefused(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{WritePathDenylist: []string{"protected"}})
	setupProtectedSymlink(t, ws)

	abs := filepath.Join(ws.Abs, "protected", "secret.txt")
	mustBlocked(t, reg, "write_file", map[string]any{"path": abs, "content": "x"})

	if _, err := os.Stat(filepath.Join(ws.Abs, "allowed", "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("absolute lexical protected path bypassed blocklist: allowed/secret.txt exists (stat err=%v)", err)
	}
}

// TestSearchReplaceBlocklistLexicalSymlinkRefused pins that search_replace
// refuses the lexical protected path and leaves the real file untouched.
func TestSearchReplaceBlocklistLexicalSymlinkRefused(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{WritePathDenylist: []string{"protected"}})
	setupProtectedSymlink(t, ws)
	if err := os.WriteFile(filepath.Join(ws.Abs, "allowed", "target.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mustBlocked(t, reg, "search_replace", map[string]any{
		"path": "protected/target.txt", "old_string": "old", "new_string": "new",
	})

	got, err := os.ReadFile(filepath.Join(ws.Abs, "allowed", "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old\n" {
		t.Fatalf("allowed/target.txt changed via lexical protected path: %q", string(got))
	}
}

// TestMultiEditBlocklistLexicalSymlinkRefused pins that multi_edit refuses the
// lexical protected path and leaves the real file untouched.
func TestMultiEditBlocklistLexicalSymlinkRefused(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{WritePathDenylist: []string{"protected"}})
	setupProtectedSymlink(t, ws)
	if err := os.WriteFile(filepath.Join(ws.Abs, "allowed", "target.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mustBlocked(t, reg, "multi_edit", map[string]any{
		"path":  "protected/target.txt",
		"edits": []map[string]any{{"old_string": "old", "new_string": "new"}},
	})

	got, err := os.ReadFile(filepath.Join(ws.Abs, "allowed", "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old\n" {
		t.Fatalf("allowed/target.txt changed via lexical protected path: %q", string(got))
	}
}

// TestDeleteFileBlocklistLexicalSymlinkRefused pins that delete_file refuses
// the lexical protected path and leaves the real file in place.
func TestDeleteFileBlocklistLexicalSymlinkRefused(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{WritePathDenylist: []string{"protected"}})
	setupProtectedSymlink(t, ws)
	if err := os.WriteFile(filepath.Join(ws.Abs, "allowed", "target.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	mustBlocked(t, reg, "delete_file", map[string]any{"path": "protected/target.txt"})

	if _, err := os.Stat(filepath.Join(ws.Abs, "allowed", "target.txt")); err != nil {
		t.Fatalf("allowed/target.txt removed via lexical protected path (stat err=%v)", err)
	}
}

// TestWriteFileBlocklistResolvedPathStillRefused pins existing behavior: with
// the denylist naming the RESOLVED directory ("allowed"), the same symlink
// write is refused because the resolved rel path is allowed/secret.txt.
func TestWriteFileBlocklistResolvedPathStillRefused(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{WritePathDenylist: []string{"allowed"}})
	setupProtectedSymlink(t, ws)

	mustBlocked(t, reg, "write_file", map[string]any{"path": "protected/secret.txt", "content": "x"})
}

// TestWriteFileBlocklistAllowedControl verifies an ordinary, non-protected
// path still writes fine with the same denylist configured.
func TestWriteFileBlocklistAllowedControl(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{WritePathDenylist: []string{"protected"}})
	setupProtectedSymlink(t, ws)

	mustExec(t, reg, "write_file", map[string]any{"path": "allowed/control.txt", "content": "ok"})
}
