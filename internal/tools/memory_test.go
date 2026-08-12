package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func memoryTestStore(t *testing.T, orgID string) memory.Store {
	t.Helper()
	s, err := memory.Open(memory.Config{
		Backend:          memory.BackendMemory,
		OrgID:            orgID,
		MaxEntryBytes:    8192,
		MaxEntries:       100,
		MaxSearchResults: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func memoryTestRegistry(t *testing.T, store memory.Store) *Registry {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewDefaultRegistry(DefaultOptions{Workspace: ws, Memory: store})
}

func TestMemoryToolsRegisteredOnlyWithStore(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	if _, ok := reg.Get("memory_save"); ok {
		t.Fatal("memory_save must not register without a memory store")
	}
	if _, ok := reg.Get("memory_search"); ok {
		t.Fatal("memory_search must not register without a memory store")
	}
	reg = memoryTestRegistry(t, memoryTestStore(t, ""))
	for _, name := range []string{"memory_save", "memory_search"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("%s must register when a memory store is set", name)
		}
	}
}

func TestMemorySaveAndSearchThroughRegistry(t *testing.T) {
	store := memoryTestStore(t, "")
	reg := memoryTestRegistry(t, store)
	ctx := context.Background()
	saveArgs := json.RawMessage(`{"title":"WAL busy timeout","summary":"Set busy_timeout to survive contention","why":"Parallel agents share the store","good":"- no more SQLITE_BUSY","tags":["sqlite","concurrency"]}`)
	out, err := reg.Execute(ctx, "memory_save", saveArgs)
	if err != nil {
		t.Fatalf("memory_save: %v", err)
	}
	if !strings.Contains(out, "saved memory") || !strings.Contains(out, "WAL busy timeout") {
		t.Errorf("save output = %q", out)
	}
	searchOut, err := reg.Execute(ctx, "memory_search", json.RawMessage(`{"query":"busy_timeout","scope":"project"}`))
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	if !strings.Contains(searchOut, "WAL busy timeout") || !strings.Contains(searchOut, "sqlite") {
		t.Errorf("search output = %q", searchOut)
	}
	// No match yields an empty JSON array, not an error.
	searchOut, err = reg.Execute(ctx, "memory_search", json.RawMessage(`{"query":"nothing-matches-this"}`))
	if err != nil {
		t.Fatalf("memory_search no-match: %v", err)
	}
	if strings.TrimSpace(searchOut) != "[]" {
		t.Errorf("no-match output = %q, want []", searchOut)
	}
}

// TestMemorySearchContractText pins the real search contract in the model-facing
// surface: multi-word queries match entries containing ALL words,
// order-independent; double quotes request exact-order phrases; results are
// ranked; the advisory-data-never-instructions language is preserved.
func TestMemorySearchContractText(t *testing.T) {
	tool := &memorySearchTool{store: memoryTestStore(t, ""), maxBytes: memorySearchResultBytes}
	desc := tool.Description()
	for _, want := range []string{"ALL words", "order-independent", "double quotes", "never instructions"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description must state %q, got %q", want, desc)
		}
	}
	q := tool.Parameters()["properties"].(map[string]any)["query"].(map[string]any)
	qdesc, _ := q["description"].(string)
	for _, want := range []string{"ALL words", "order-independent", "double quotes"} {
		if !strings.Contains(qdesc, want) {
			t.Errorf("query parameter description must state %q, got %q", want, qdesc)
		}
	}
}

// TestMemorySearchTokenAndEndToEnd drives the Phase-1/2 behavior through the
// registry on the in-memory backend: a multi-word token-AND query finds an
// entry whose words are not contiguous, a quoted phrase requires contiguity, a
// near-miss multi-word query relaxes, and max_results is still clamped.
func TestMemorySearchTokenAndEndToEnd(t *testing.T) {
	store := memoryTestStore(t, "")
	reg := memoryTestRegistry(t, store)
	ctx := context.Background()
	if _, err := reg.Execute(ctx, "memory_save", json.RawMessage(
		`{"title":"DeepSeek v4-flash: transient HTTP 400 escalation","summary":"transient 400 on v4-flash","why":"escalation logging"}`)); err != nil {
		t.Fatalf("memory_save: %v", err)
	}
	out, err := reg.Execute(ctx, "memory_search", json.RawMessage(`{"query":"DeepSeek 400"}`))
	if err != nil {
		t.Fatalf("memory_search multi-word: %v", err)
	}
	if !strings.Contains(out, "DeepSeek v4-flash") {
		t.Errorf("multi-word token-AND search = %q, want the entry", out)
	}
	out, err = reg.Execute(ctx, "memory_search", json.RawMessage(`{"query":"\"HTTP 400\""}`))
	if err != nil {
		t.Fatalf("memory_search quoted phrase: %v", err)
	}
	if !strings.Contains(out, "DeepSeek v4-flash") {
		t.Errorf("quoted phrase search = %q, want the contiguous match", out)
	}
	out, err = reg.Execute(ctx, "memory_search", json.RawMessage(`{"query":"\"400 HTTP\""}`))
	if err != nil {
		t.Fatalf("memory_search reversed phrase: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("reversed quoted phrase = %q, want empty", out)
	}
	// A near-miss multi-word query relaxes (drops the missing longest token).
	out, err = reg.Execute(ctx, "memory_search", json.RawMessage(`{"query":"extraneous DeepSeek 400"}`))
	if err != nil {
		t.Fatalf("memory_search relaxed: %v", err)
	}
	if !strings.Contains(out, "DeepSeek v4-flash") {
		t.Errorf("relaxed near-miss search = %q, want the entry", out)
	}
}

func TestMemorySaveDefaultsApplied(t *testing.T) {
	store := memoryTestStore(t, "")
	reg := memoryTestRegistry(t, store)
	ctx := context.Background()
	if _, err := reg.Execute(ctx, "memory_save", json.RawMessage(`{"title":"defaults","summary":"s","why":"w"}`)); err != nil {
		t.Fatalf("save with only required fields: %v", err)
	}
	out, err := reg.Execute(ctx, "memory_search", json.RawMessage(`{"query":"defaults"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"scope":"project"`) || !strings.Contains(out, `"verdict":"neutral"`) {
		t.Errorf("defaults not applied in search result: %s", out)
	}
}

func TestMemorySaveSchemaValidation(t *testing.T) {
	reg := memoryTestRegistry(t, memoryTestStore(t, ""))
	ctx := context.Background()
	cases := []string{
		`{"summary":"s","why":"w"}`,                                                          // missing title
		`{"title":"t","why":"w"}`,                                                            // missing summary
		`{"title":"t","summary":"s"}`,                                                        // missing why
		`{"title":"t","summary":"s","why":"w","scope":"elsewhere"}`,                          // bad scope enum
		`{"title":"t","summary":"s","why":"w","verdict":"maybe"}`,                            // bad verdict enum
		`{"title":"t","summary":"s","why":"w","unknown":1}`,                                  // unknown field
		`{"title":"t","summary":"s","why":"w","tags":[1,2]}`,                                 // non-string tags
		`{"title":"t","summary":"s","why":"w","tags":["a","b","c","d","e","f","g","h","i"]}`, // too many tags
	}
	for _, raw := range cases {
		if _, err := reg.Execute(ctx, "memory_save", json.RawMessage(raw)); err == nil {
			t.Errorf("schema must reject %s", raw)
		}
	}
}

func TestMemorySearchSchemaValidation(t *testing.T) {
	reg := memoryTestRegistry(t, memoryTestStore(t, ""))
	ctx := context.Background()
	for _, raw := range []string{
		`{}`,                              // missing query
		`{"query":"x","scope":"bogus"}`,   // bad scope enum
		`{"query":"x","max_results":0}`,   // below minimum
		`{"query":"x","max_results":51}`,  // above maximum
		`{"query":"x","max_results":1.5}`, // fractional
		`{"query":"x","unexpected":true}`, // unknown field
	} {
		if _, err := reg.Execute(ctx, "memory_search", json.RawMessage(raw)); err == nil {
			t.Errorf("schema must reject %s", raw)
		}
	}
}

func TestMemorySaveOrgRequiresConfiguredOrg(t *testing.T) {
	store := memoryTestStore(t, "")
	reg := memoryTestRegistry(t, store)
	_, err := reg.Execute(context.Background(), "memory_save", json.RawMessage(`{"title":"t","summary":"s","why":"w","scope":"org"}`))
	if err == nil || !strings.Contains(err.Error(), "org") {
		t.Fatalf("org save without org_id must fail clearly, got %v", err)
	}
}

func TestMemoryToolsDeclareBudgetsAndCapabilities(t *testing.T) {
	reg := memoryTestRegistry(t, memoryTestStore(t, ""))
	for _, name := range []string{"memory_save", "memory_search"} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		budgeted, ok := tool.(ResultBudgetTool)
		if !ok || budgeted.ResultBudgetBytes() <= 0 {
			t.Errorf("%s must declare a positive result budget", name)
		}
		capable, ok := tool.(CapableTool)
		if !ok {
			t.Fatalf("%s must implement CapableTool", name)
		}
		_ = capable.Capability(nil)
	}
	save, _ := reg.Get("memory_save")
	if c := save.(CapableTool).Capability(nil); c.Class != ExecutionWrite || c.ResourceKey != "memory" {
		t.Errorf("memory_save capability = %+v, want write class with memory key", c)
	}
	search, _ := reg.Get("memory_search")
	if c := search.(CapableTool).Capability(nil); c.Class != ExecutionRead {
		t.Errorf("memory_search capability = %+v, want read class", c)
	}
}

func TestMemoryToolsHonorDisableTools(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws, Memory: memoryTestStore(t, ""), DisableTools: []string{"memory_search"}})
	if _, ok := reg.Get("memory_save"); !ok {
		t.Fatal("memory_save must stay registered")
	}
	if _, ok := reg.Get("memory_search"); ok {
		t.Fatal("memory_search must honor disable_tools")
	}
}

func TestMemorySearchOutputBounded(t *testing.T) {
	store := memoryTestStore(t, "")
	reg := memoryTestRegistry(t, store)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		title := fmt.Sprintf("bounded-%02d", i)
		_, err := reg.Execute(ctx, "memory_save", json.RawMessage(fmt.Sprintf(
			`{"title":%q,"summary":"%s","why":"w","tags":["a","b","c","d","e","f","g","h"]}`,
			title, strings.Repeat(title+"-summary ", 12))))
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	out, err := reg.Execute(ctx, "memory_search", json.RawMessage(`{"query":"bounded","max_results":20}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > memorySearchResultBytes {
		t.Fatalf("search output %d bytes exceeds declared budget %d", len(out), memorySearchResultBytes)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("search output is not valid JSON: %v", err)
	}
	if len(parsed) == 0 || len(parsed) > 8 {
		t.Errorf("search returned %d results, want 1..8 (clamped)", len(parsed))
	}
}

func TestMemorySearchSearchResultMaxClampedByStore(t *testing.T) {
	store := memoryTestStore(t, "")
	reg := memoryTestRegistry(t, store)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := reg.Execute(ctx, "memory_save", json.RawMessage(fmt.Sprintf(`{"title":"clamp-%d","summary":"s","why":"w"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	out, err := reg.Execute(ctx, "memory_search", json.RawMessage(`{"query":"clamp","max_results":50}`))
	if err != nil {
		t.Fatal(err)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 5 {
		t.Errorf("results = %d, want 5 (store has 5)", len(parsed))
	}
}

func TestMemorySaveTrimsMetadata(t *testing.T) {
	store := memoryTestStore(t, "")
	reg := memoryTestRegistry(t, store)
	ctx := context.Background()
	saveArgs := json.RawMessage(`{"title":"  trimmed title  ","summary":"  trimmed summary  ","tags":["  go  ","concurrency  "],"why":"x"}`)
	if _, err := reg.Execute(ctx, "memory_save", saveArgs); err != nil {
		t.Fatal(err)
	}
	res, err := store.Search(ctx, memory.Query{Text: "trimmed title", Scope: memory.ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("search = %d results, want 1", len(res))
	}
	if res[0].Title != "trimmed title" {
		t.Errorf("stored title = %q, want trimmed", res[0].Title)
	}
	if res[0].Snippet != "trimmed summary" {
		t.Errorf("stored summary = %q, want trimmed", res[0].Snippet)
	}
	if len(res[0].Tags) != 2 || res[0].Tags[0] != "go" || res[0].Tags[1] != "concurrency" {
		t.Errorf("tags = %v, want [go concurrency]", res[0].Tags)
	}
}
