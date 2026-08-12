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

// A foreign deletion of a file the agent read must be refused, not silently
// recreated: write_file's ENOENT branch used to treat every missing file as a
// legitimate create, so a removed-by-foreign-writer file was recreated with
// the agent's content while the agent still believed it was editing the
// version it saw. RED before the fix (write proceeds and recreates the file);
// GREEN after (write refused with the disappearance message, file stays gone).
func TestWriteFileRefusesExternallyDeletedObservedFile(t *testing.T) {
	ws, reg := setupWS(t)
	abs := filepath.Join(ws.Abs, "f.txt")
	externalTouch(t, abs, "old\n")
	mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	// A foreign writer removes the file the agent had read.
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
		"path": "f.txt", "content": "mine\n",
	}))
	if err == nil {
		t.Fatal("write_file recreated an externally deleted observed file; want a refusal")
	}
	if !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("refusal does not name the disappearance: %v", err)
	}
	if _, statErr := os.Lstat(abs); !os.IsNotExist(statErr) {
		t.Fatalf("refused write recreated the file (lstat err=%v)", statErr)
	}
}

// The delete counterpart: an externally deleted observed file must be refused
// with the disappearance message, not a bare os.Remove ENOENT that reads like
// "nothing happened" and gives the agent no reason to re-read. RED before the
// fix (delete surfaces a bare ENOENT); GREEN after (disappearance refusal).
func TestDeleteFileRefusesExternallyDeletedObservedFile(t *testing.T) {
	ws, reg := setupWS(t)
	abs := filepath.Join(ws.Abs, "f.txt")
	externalTouch(t, abs, "old\n")
	mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Execute(context.Background(), "delete_file", mustJSON(t, map[string]any{
		"path": "f.txt",
	}))
	if err == nil {
		t.Fatal("delete_file succeeded on an externally deleted observed file; want a refusal")
	}
	if !strings.Contains(err.Error(), "no longer exists") || !strings.Contains(err.Error(), "removed by a writer you have not seen") {
		t.Fatalf("refusal is a bare ENOENT, not the disappearance message: %v", err)
	}
}

// The disappearance refusal must not be sticky: re-reading the now-missing
// path drops the stale observation (dropIfGone), so the next write proceeds as
// an informed create. Without the escape hatch a foreign deletion would leave
// an observation that read_file on a missing path never refreshes, refusing
// every later write/delete on that path for the process lifetime. RED before
// the fix (the first write is not refused at all, so the escape path is never
// exercised); GREEN after (refusal -> re-read of the missing path -> informed
// create succeeds).
func TestStaleGuardClearsAfterReReadOfMissingFile(t *testing.T) {
	ws, reg := setupWS(t)
	abs := filepath.Join(ws.Abs, "f.txt")
	externalTouch(t, abs, "old\n")
	mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	// First: the guard refuses the write on the externally deleted file.
	_, err := reg.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
		"path": "f.txt", "content": "mine\n",
	}))
	if err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("write_file on externally deleted observed file: err=%v", err)
	}
	// Re-reading the missing path drops the observation...
	if _, err := reg.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{"path": "f.txt"})); err == nil {
		t.Fatal("read_file of a missing path succeeded; want an error")
	}
	// ...so the next write is an informed create, not a stale-write refusal.
	mustExec(t, reg, "write_file", map[string]any{"path": "f.txt", "content": "new\n"})
	if got := readFileAbs(t, abs); got != "new\n" {
		t.Fatalf("informed create wrote %q, want %q", got, "new\n")
	}
}
