package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The stale-write guard. An agent computes an edit from the view of a file it
// last read or wrote. If a foreign writer - a second session, an editor, a
// hook - changed the file on disk since then, applying the edit silently
// overwrites that foreign work and leaves the agent believing it edited the
// version it saw. The guard must refuse the write with a re-read instruction.
//
// These tests are RED until the guard exists: today every write proceeds
// regardless of what changed on disk in between.

func externalTouch(t *testing.T, abs, content string) {
	t.Helper()
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFileAbs(t *testing.T, abs string) string {
	t.Helper()
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// wantStaleGuard fails unless err is a stale-write refusal that names the
// cause, and the file on disk is still exactly want.
func wantStaleGuard(t *testing.T, err error, abs, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("write succeeded; want a stale-write refusal (file changed on disk)")
	}
	if !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("refusal does not explain the cause: %v", err)
	}
	if got := readFileAbs(t, abs); got != want {
		t.Fatalf("refused write still mutated the file: %q, want %q", got, want)
	}
}

func TestSearchReplaceRefusesExternallyChangedFile(t *testing.T) {
	ws, reg := setupWS(t)
	abs := filepath.Join(ws.Abs, "f.txt")
	externalTouch(t, abs, "alpha\nbeta\n")
	// The agent observes the file...
	mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	// ...then a foreign writer changes it before the edit lands.
	externalTouch(t, abs, "alpha\nBETA-external\n")
	_, err := reg.Execute(context.Background(), "search_replace", mustJSON(t, map[string]any{
		"path": "f.txt", "old_string": "BETA-external", "new_string": "gamma",
	}))
	wantStaleGuard(t, err, abs, "alpha\nBETA-external\n")
}

func TestMultiEditRefusesExternallyChangedFile(t *testing.T) {
	ws, reg := setupWS(t)
	abs := filepath.Join(ws.Abs, "f.txt")
	externalTouch(t, abs, "alpha\nbeta\n")
	mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	externalTouch(t, abs, "alpha\nBETA-external\n")
	_, err := reg.Execute(context.Background(), "multi_edit", mustJSON(t, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"old_string": "BETA-external", "new_string": "gamma"},
		},
	}))
	wantStaleGuard(t, err, abs, "alpha\nBETA-external\n")
}

func TestWriteFileRefusesExternallyChangedExistingFile(t *testing.T) {
	ws, reg := setupWS(t)
	abs := filepath.Join(ws.Abs, "f.txt")
	externalTouch(t, abs, "old\n")
	mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	externalTouch(t, abs, "foreign\n")
	_, err := reg.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
		"path": "f.txt", "content": "mine\n",
	}))
	wantStaleGuard(t, err, abs, "foreign\n")
}

// Deleting is as destructive as overwriting: a file changed by a foreign
// writer since the agent last saw it must not be removed on the agent's say-so.
func TestDeleteFileRefusesExternallyChangedFile(t *testing.T) {
	ws, reg := setupWS(t)
	abs := filepath.Join(ws.Abs, "f.txt")
	externalTouch(t, abs, "old\n")
	mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	externalTouch(t, abs, "foreign\n")
	_, err := reg.Execute(context.Background(), "delete_file", mustJSON(t, map[string]any{
		"path": "f.txt",
	}))
	wantStaleGuard(t, err, abs, "foreign\n")
}

// The guard must not fire on the agent's own traffic: first writes to files
// never seen, and consecutive edits where the only writer is the agent.
func TestStaleWriteGuardAllowsFreshWrites(t *testing.T) {
	ws, reg := setupWS(t)

	// Brand-new file, never read: nothing to be stale against.
	mustExec(t, reg, "write_file", map[string]any{"path": "new.txt", "content": "n1\n"})

	// Existing file never observed this session: the overwrite is explicit
	// and the tool's diff shows what it replaces, so it is allowed.
	abs := filepath.Join(ws.Abs, "pre.txt")
	externalTouch(t, abs, "pre\n")
	mustExec(t, reg, "write_file", map[string]any{"path": "pre.txt", "content": "pre2\n"})

	// Read then consecutive edits: both succeed, no false positive between
	// the agent's own writes.
	mustExec(t, reg, "write_file", map[string]any{"path": "seq.txt", "content": "a\nb\n"})
	mustExec(t, reg, "read_file", map[string]any{"path": "seq.txt"})
	mustExec(t, reg, "search_replace", map[string]any{"path": "seq.txt", "old_string": "a", "new_string": "A"})
	mustExec(t, reg, "search_replace", map[string]any{"path": "seq.txt", "old_string": "b", "new_string": "B"})
	if got := mustExec(t, reg, "read_file", map[string]any{"path": "seq.txt"}); got != "A\nB\n" {
		t.Fatalf("consecutive edits corrupted content: %q", got)
	}
}

// After an external change, a re-read refreshes the agent's view; the next
// edit then applies to what the agent actually saw.
func TestStaleWriteGuardClearsAfterReRead(t *testing.T) {
	ws, reg := setupWS(t)
	abs := filepath.Join(ws.Abs, "f.txt")
	externalTouch(t, abs, "alpha\nbeta\n")
	mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	externalTouch(t, abs, "alpha\nBETA-external\n")
	mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	mustExec(t, reg, "search_replace", map[string]any{
		"path": "f.txt", "old_string": "BETA-external", "new_string": "gamma",
	})
	if got := mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"}); got != "alpha\ngamma\n" {
		t.Fatalf("edit after re-read = %q, want %q", got, "alpha\ngamma\n")
	}
}
