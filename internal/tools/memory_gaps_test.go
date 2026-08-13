package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// errGapSearch is the sentinel error the stub store returns, so tests can
// assert the tool propagated the store's failure unchanged.
var errGapSearch = &gapStoreError{}

type gapStoreError struct{}

func (*gapStoreError) Error() string { return "gap store search failed" }

// gapErrStore is a memory.Store stub whose methods fail, for driving the
// error paths of the memory tools directly.
type gapErrStore struct{ err error }

func (s gapErrStore) Save(context.Context, memory.Entry) (memory.Result, error) {
	return memory.Result{}, s.err
}

func (s gapErrStore) Search(context.Context, memory.Query) ([]memory.Result, error) {
	return nil, s.err
}

func (s gapErrStore) Count(context.Context, memory.Scope) (int, error) {
	return 0, s.err
}

func (s gapErrStore) PromoteToCore(context.Context, string) error {
	return s.err
}

func (s gapErrStore) CoreEntries(context.Context, memory.Scope) ([]memory.Result, error) {
	return nil, s.err
}

func (s gapErrStore) Close() error { return nil }

// gapBigResult builds a search result with an oversized snippet and eight
// oversized tags, so its JSON form far exceeds any small result budget.
func gapBigResult(id string) memory.Result {
	tags := make([]string, 8)
	for i := range tags {
		tags[i] = "tag" + id + strings.Repeat("x", 96)
	}
	return memory.Result{
		ID:      id,
		Scope:   memory.ScopeProject,
		Org:     "",
		Title:   "title-" + id,
		Verdict: memory.VerdictGood,
		Tags:    tags,
		Created: "2025-01-01",
		Snippet: strings.Repeat("s", 2000),
	}
}

func TestGapMemorySaveInvalidJSON(t *testing.T) {
	store := memoryTestStore(t, "")
	tool := &memorySaveTool{store: store}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"title":`))
	if err == nil {
		t.Fatal("memory_save must reject malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid arguments") {
		t.Errorf("error = %v, want invalid-arguments wrapping", err)
	}
}

func TestGapMemorySaveVerdictBranch(t *testing.T) {
	store := memoryTestStore(t, "")
	tool := &memorySaveTool{store: store}
	// A non-empty verdict takes the in.Verdict != "" branch of Execute.
	out, err := tool.Execute(context.Background(), json.RawMessage(
		`{"title":"verdict branch","summary":"s","why":"w","verdict":"mixed"}`))
	if err != nil {
		t.Fatalf("memory_save with verdict: %v", err)
	}
	if !strings.HasPrefix(out, "saved memory ") {
		t.Errorf("output = %q, want saved-memory confirmation", out)
	}
}

func TestGapMemorySearchInvalidJSON(t *testing.T) {
	tool := &memorySearchTool{store: memoryTestStore(t, ""), maxBytes: memorySearchResultBytes}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":`))
	if err == nil {
		t.Fatal("memory_search must reject malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid arguments") {
		t.Errorf("error = %v, want invalid-arguments wrapping", err)
	}
}

func TestGapMemorySearchStoreError(t *testing.T) {
	boom := &gapErrStore{err: errGapSearch}
	tool := &memorySearchTool{store: boom, maxBytes: memorySearchResultBytes}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"x","scope":"all"}`))
	if err != errGapSearch {
		t.Fatalf("memory_search error = %v, want store error to propagate", err)
	}
}

func TestGapMarshalSearchResultsSnippetShrinkAndDropTail(t *testing.T) {
	// Three oversized results under a 1500-byte budget: the per-result
	// snippet budget falls below the 16-byte floor (snippet shrink) and the
	// envelope is dropped tail-first until a single result fits.
	results := []memory.Result{gapBigResult("a"), gapBigResult("b"), gapBigResult("c")}
	out := marshalSearchResults(results, 1500)
	if len(out) > 1500 {
		t.Fatalf("envelope is %d bytes, exceeds the 1500-byte budget", len(out))
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}
	if len(parsed) != 1 {
		t.Errorf("envelope holds %d results, want 1 after dropping the tail", len(parsed))
	}
	// The surviving result keeps its full fields: the drop loop must have
	// stopped before the title-only fallback.
	if len(parsed[0]) != 8 {
		t.Errorf("surviving result has %d fields, want all 8 (no title-only fallback)", len(parsed[0]))
	}
}

func TestGapMarshalSearchResultsTitleOnlyFallback(t *testing.T) {
	// Two oversized results under a 200-byte budget: even after dropping to a
	// single result the envelope is still over, so the fallback reduces it to
	// title and scope only.
	results := []memory.Result{gapBigResult("a"), gapBigResult("b")}
	out := marshalSearchResults(results, 200)
	if len(out) > 200 {
		t.Fatalf("envelope is %d bytes, exceeds the 200-byte budget", len(out))
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("envelope holds %d results, want 1 after dropping the tail", len(parsed))
	}
	if len(parsed[0]) != 2 {
		t.Fatalf("fallback result has %d fields, want title and scope only", len(parsed[0]))
	}
	if parsed[0]["title"] != "title-a" || parsed[0]["scope"] != string(memory.ScopeProject) {
		t.Errorf("fallback result = %v, want title-a / project", parsed[0])
	}
}

func TestGapRegisterMemoryToolsClampsSearchBudget(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A MaxToolResultBytes below the compiled-in 16 KiB search budget must
	// clamp the memory_search tool's declared budget (and leave memory_save
	// alone, which is not result-bounded the same way).
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace:          ws,
		Memory:             memoryTestStore(t, ""),
		MaxToolResultBytes: 4096,
	})
	save, ok := reg.Get("memory_save")
	if !ok {
		t.Fatal("memory_save not registered")
	}
	if got := save.(ResultBudgetTool).ResultBudgetBytes(); got != memorySaveResultBytes {
		t.Errorf("memory_save budget = %d, want %d (unclamped)", got, memorySaveResultBytes)
	}
	search, ok := reg.Get("memory_search")
	if !ok {
		t.Fatal("memory_search not registered")
	}
	if got := search.(ResultBudgetTool).ResultBudgetBytes(); got != 4096 {
		t.Errorf("memory_search budget = %d, want 4096 (clamped by MaxToolResultBytes)", got)
	}
}
