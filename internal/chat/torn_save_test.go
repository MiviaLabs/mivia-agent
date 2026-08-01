package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func callMsg(id, name string) provider.Message {
	var c provider.ToolCall
	c.ID = id
	c.Type = "function"
	c.Function.Name = name
	c.Function.Arguments = "{}"
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{c}}
}

// A history whose trailing tool results were lost - a torn chunk rewrite leaves
// exactly this shape, because writeJSONL truncates in place and readJSONL stops
// at the last complete line without error. The assistant message still announces
// a tool_call whose result is gone, so the API rejects every subsequent turn and
// the session is unusable with no way to recover through the UI.
func TestLoadRepairsOrphanedToolCalls(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "x"})
	sess.SessionDir = t.TempDir()
	sess.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "go"},
		callMsg("c1", "read_file"),
		{Role: provider.RoleTool, ToolCallID: "c1", Name: "read_file", Content: "data"},
	}
	if err := sess.Save("torn"); err != nil {
		t.Fatal(err)
	}

	// Simulate the torn write: drop the trailing tool-result line.
	chunk := filepath.Join(sess.SessionDir, "torn", "chunk_0000.jsonl")
	raw, err := os.ReadFile(chunk)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if err := os.WriteFile(chunk, []byte(strings.Join(lines[:2], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reloaded := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "x"})
	reloaded.SessionDir = sess.SessionDir
	if err := reloaded.Load("torn"); err != nil {
		t.Fatalf("load: %v", err)
	}

	announced := map[string]bool{}
	answered := map[string]bool{}
	for _, m := range reloaded.Messages {
		for _, c := range m.ToolCalls {
			announced[c.ID] = true
		}
		if m.Role == provider.RoleTool {
			answered[m.ToolCallID] = true
		}
	}
	for id := range announced {
		if !answered[id] {
			t.Fatalf("loaded history has an orphaned tool_call %q; it will be rejected on every turn: %+v", id, reloaded.Messages)
		}
	}
}

// A chunk rewrite that fails partway must not destroy the previous state: the
// old chunks were deleted before the new ones were written, so an ENOSPC/EIO
// mid-write left meta.json pointing at chunks that no longer exist and the
// session could never be loaded again.
func TestSaveDoesNotDestroyOldChunksBeforeWritingNew(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "x"})
	sess.SessionDir = t.TempDir()
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "first"}}
	if err := sess.Save("s"); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(sess.SessionDir, "s")
	before, err := filepath.Glob(filepath.Join(dir, "chunk_*.jsonl"))
	if err != nil || len(before) == 0 {
		t.Fatalf("no chunks written: %v %v", before, err)
	}
	// Re-save with new content; the old chunk must never be absent while the
	// new one is missing. Assert the post-state is complete and loadable.
	sess.Messages = append(sess.Messages, provider.Message{Role: provider.RoleAssistant, Content: "second"})
	if err := sess.Save("s"); err != nil {
		t.Fatal(err)
	}
	reloaded := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "x"})
	reloaded.SessionDir = sess.SessionDir
	if err := reloaded.Load("s"); err != nil {
		t.Fatalf("resave must leave a loadable session: %v", err)
	}
	if len(reloaded.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(reloaded.Messages))
	}
	// No temp files left behind.
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) > 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}
