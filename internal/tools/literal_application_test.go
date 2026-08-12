package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/diff"
)

// Trust contract for the write tools, pinned at the tool layer: a tool call
// mutates the filesystem to EXACTLY the literal application of its arguments
// and nothing else. These tests are the lower half of the harness-trust
// contract (the upper half, at the agent-loop level, lives in
// internal/agent/trust_contract_integration_test.go). If any layer starts
// materializing intent beyond the arguments - extra content appended to a
// file, extra files touched, a "helpful" completion of a half-written edit -
// these tests fail before the change can reach the model.
//
// They are expected to pass against the current tools: their job is to pin
// the contract so a regression anywhere in the harness is caught, not to
// fail until a fix lands.

// snapshotWorkspace records rel path -> sha256 for every file under abs.
func snapshotWorkspace(t *testing.T, abs string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		snap[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// assertWorkspaceOnly fails unless the only files whose content changed
// between before and after are exactly the named ones (which the caller has
// already asserted byte-for-byte).
func assertWorkspaceOnly(t *testing.T, before, after map[string]string, changed ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, c := range changed {
		want[c] = true
	}
	for rel, sum := range before {
		if want[rel] {
			continue
		}
		if after[rel] != sum {
			t.Fatalf("file %q changed during the calls without being a target (before %s, after %s)", rel, sum, after[rel])
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok && !want[rel] {
			t.Fatalf("unexpected new file %q appeared during the calls", rel)
		}
	}
}

func TestWriteToolsApplyExactlyTheirLiteralArguments(t *testing.T) {
	ws, reg := setupWS(t)
	before := snapshotWorkspace(t, ws.Abs)

	mustExec(t, reg, "write_file", map[string]any{"path": "a.txt", "content": "alpha\nbeta\n"})
	if got := mustExec(t, reg, "read_file", map[string]any{"path": "a.txt"}); got != "alpha\nbeta\n" {
		t.Fatalf("write_file: read back %q, want the literal content %q", got, "alpha\nbeta\n")
	}

	mustExec(t, reg, "search_replace", map[string]any{"path": "a.txt", "old_string": "beta", "new_string": "BETA"})
	if got := mustExec(t, reg, "read_file", map[string]any{"path": "a.txt"}); got != "alpha\nBETA\n" {
		t.Fatalf("search_replace: read back %q, want the literal result %q", got, "alpha\nBETA\n")
	}

	mustExec(t, reg, "write_file", map[string]any{"path": "b.txt", "content": "gamma\ndelta\n"})
	mustExec(t, reg, "multi_edit", map[string]any{
		"path": "b.txt",
		"edits": []map[string]any{
			{"old_string": "gamma", "new_string": "GAMMA"},
			{"old_string": "delta", "new_string": "DELTA"},
		},
	})
	if got := mustExec(t, reg, "read_file", map[string]any{"path": "b.txt"}); got != "GAMMA\nDELTA\n" {
		t.Fatalf("multi_edit: read back %q, want the literal result %q", got, "GAMMA\nDELTA\n")
	}

	assertWorkspaceOnly(t, before, snapshotWorkspace(t, ws.Abs), "a.txt", "b.txt")
}

// An immediate read after a write must return the post-write state. A stale
// read (the harness serving the agent's old view back to it) would let the
// agent build its next edit on a lie.
func TestReadAfterWriteReturnsPostWriteState(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{"path": "f.txt", "content": "v1\n"})
	if got := mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"}); got != "v1\n" {
		t.Fatalf("read after write_file = %q, want %q", got, "v1\n")
	}
	mustExec(t, reg, "search_replace", map[string]any{"path": "f.txt", "old_string": "v1", "new_string": "v2"})
	if got := mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"}); got != "v2\n" {
		t.Fatalf("read after search_replace = %q, want %q", got, "v2\n")
	}
	mustExec(t, reg, "multi_edit", map[string]any{"path": "f.txt", "edits": []map[string]any{{"old_string": "v2", "new_string": "v3"}}})
	if got := mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"}); got != "v3\n" {
		t.Fatalf("read after multi_edit = %q, want %q", got, "v3\n")
	}
}

// The "+N −M" stats in a write tool's result must be the stats of the ACTUAL
// before/after bytes, recomputed here from what hit disk - not a fabricated
// or wishful summary. If a harness layer reports a smaller diff than the
// change it really made, this test catches the lie.
func TestResultDiffStatsMatchActualBytes(t *testing.T) {
	ws, reg := setupWS(t)

	check := func(t *testing.T, path, before string, args any, wantHeaderPrefix string) {
		t.Helper()
		abs := filepath.Join(ws.Abs, path)
		if err := os.WriteFile(abs, []byte(before), 0o644); err != nil {
			t.Fatal(err)
		}
		beforeBytes, err := os.ReadFile(abs)
		if err != nil {
			t.Fatal(err)
		}
		out := mustExec(t, reg, "search_replace", args)
		afterBytes, err := os.ReadFile(abs)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out, wantHeaderPrefix) {
			t.Fatalf("result header = %q, want prefix %q", headOf(out), wantHeaderPrefix)
		}
		result, err := diff.Compute(string(beforeBytes), string(afterBytes), diff.Options{MaxInputBytes: 512 << 10, Timeout: 100 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		ins, del := diff.Stats(result)
		wantStats := fmt.Sprintf("+%d −%d)", ins, del)
		if !strings.Contains(out, wantStats) {
			t.Fatalf("result %q does not report the actual byte stats %s", headOf(out), wantStats)
		}
	}

	check(t, "a.txt", "alpha\nbeta\ngamma\n", map[string]any{
		"path": "a.txt", "old_string": "beta", "new_string": "BETA",
	}, "updated a.txt (1 replacement,")

	check(t, "b.txt", "alpha\nbeta\ngamma\n", map[string]any{
		"path": "b.txt", "old_string": "beta", "new_string": "BETA",
	}, "updated b.txt (1 replacement,")

	check(t, "c.txt", "one\ntwo\nthree\n", map[string]any{
		"path": "c.txt", "old_string": "two", "new_string": "TWO",
	}, "updated c.txt (1 replacement,")
}

func headOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
