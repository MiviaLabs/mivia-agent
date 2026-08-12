package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

// TestMemorySearchDoesNotWriteDatabaseFile is the end-to-end byte-stability
// regression for the read-only search open: `mivia memory search` must never
// rewrite the committed .mivia/memory.db. Before the fix the command opened
// the store read-write, so the open path (journal_mode=WAL, schema CREATE,
// FTS5 rebuild backfill) and Close's wal_checkpoint(TRUNCATE) churned the
// committed file's bytes even though the command only searched.
func TestMemorySearchDoesNotWriteDatabaseFile(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)

	// Populate the committed database exactly as a save session would. Three
	// entries share a word so the search has matches; a single-entry database
	// happens to survive a read-write reopen byte-identically, which would
	// mask the regression, so the larger state is what makes this test red.
	enabled := true
	mc := config.MemoryConfig{
		Enabled:          &enabled,
		StoreBackend:     "sqlite",
		StorePath:        ".mivia/memory.db",
		MaxSearchResults: 8,
	}
	store, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("openMemoryStore: %v", err)
	}
	entries := []memory.Entry{
		{Title: "Stable deploy pipeline", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Created: "2026-08-04", Summary: "A search command must never rewrite the committed database file.", Why: "read-only search regression"},
		{Title: "Stable sqlite WAL", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Created: "2026-08-04", Summary: "The read-only open keeps the file bytes identical.", Why: "read-only search regression"},
		{Title: "Stable org review", Scope: memory.ScopeProject, Verdict: memory.VerdictNeutral, Created: "2026-08-04", Summary: "Search opens the store without any write.", Why: "read-only search regression"},
	}
	for _, e := range entries {
		if _, err := store.Save(context.Background(), e); err != nil {
			t.Fatalf("save %q: %v", e.Title, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	dbPath := filepath.Join(root, ".mivia", "memory.db")
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut strings.Builder
	err = runMemoryWithIO([]string{"search", "stable", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err != nil {
		t.Fatalf("runMemoryWithIO: %v", err)
	}
	if !strings.Contains(out.String(), "Stable deploy pipeline") {
		t.Fatalf("search output missing the saved entry: %q", out.String())
	}

	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("`mivia memory search` rewrote the committed database file (bytes changed, len %d -> %d)", len(before), len(after))
	}
}

// TestMemorySearchJSONSerializesOrgField pins that an org-scoped Result
// serializes its Org field in --json output: writeMemorySearchJSON maps
// memory.Result.Org to the JSON "org" field and memory.Result.Snippet to the
// "summary" field, mirroring the memory_search tool envelope.
func TestMemorySearchJSONSerializesOrgField(t *testing.T) {
	var out strings.Builder
	results := []memory.Result{{
		ID:      "org-id-1",
		Scope:   memory.ScopeOrg,
		Org:     "github.com/acme",
		Title:   "org note",
		Verdict: memory.VerdictGood,
		Tags:    []string{"org"},
		Created: "2026-08-05",
		Snippet: "org summary text",
	}}
	if err := writeMemorySearchJSON(&out, results); err != nil {
		t.Fatalf("writeMemorySearchJSON: %v", err)
	}
	var decoded []memorySearchJSONProbe
	if err := json.Unmarshal([]byte(out.String()), &decoded); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out.String())
	}
	if len(decoded) != 1 {
		t.Fatalf("results = %d, want 1\n%s", len(decoded), out.String())
	}
	if decoded[0].Org != "github.com/acme" {
		t.Errorf("org field = %q, want github.com/acme\n%s", decoded[0].Org, out.String())
	}
	if decoded[0].Scope != "org" {
		t.Errorf("scope field = %q, want org\n%s", decoded[0].Scope, out.String())
	}
	if decoded[0].Summary != "org summary text" {
		t.Errorf("summary field = %q, want the Snippet text\n%s", decoded[0].Summary, out.String())
	}
}
