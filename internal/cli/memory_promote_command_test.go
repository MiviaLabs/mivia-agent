package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

func TestMemoryPromoteEndToEnd(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)
	saveTestMemories(t, root)

	mc := config.MemoryConfig{StoreBackend: "sqlite", StorePath: ".mivia/memory.db"}
	store, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("openMemoryStore: %v", err)
	}
	results, err := store.Search(context.Background(), memory.Query{Text: "deploy", Scope: memory.ScopeProject})
	if err != nil || len(results) == 0 {
		t.Fatalf("search for seeded entry: results=%v err=%v", results, err)
	}
	id := results[0].ID
	store.Close()

	var out, errOut strings.Builder
	if err := runMemoryWithIO([]string{"promote", id, "--workspace", root, "--config", cfgPath}, &out, &errOut); err != nil {
		t.Fatalf("runMemoryWithIO promote: %v", err)
	}
	if !strings.Contains(out.String(), id) {
		t.Fatalf("promote output = %q, want it to mention %q", out.String(), id)
	}

	// Re-promoting is a no-op, not an error - proves the write actually took.
	store2, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	if err := store2.PromoteToCore(context.Background(), id); err != nil {
		t.Fatalf("re-promote already-core entry: %v", err)
	}
}

func TestMemoryPromoteUnknownID(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"promote", "does-not-exist", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("promote unknown id error = %v, want a not-found error", err)
	}
}

func TestMemoryPromoteMissingID(t *testing.T) {
	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"promote"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "expected exactly one entry id") {
		t.Fatalf("promote missing id error = %v", err)
	}
}

func TestMemoryPromoteTooManyPositionalArgs(t *testing.T) {
	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"promote", "id-a", "id-b"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "expected exactly one entry id") {
		t.Fatalf("promote two ids error = %v", err)
	}
}

func TestMemoryPromoteUnknownFlag(t *testing.T) {
	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"promote", "some-id", "--bogus"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("promote unknown flag error = %v", err)
	}
}

func TestMemoryPromoteDisabledMemoryError(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, false)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"promote", "some-id", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "memory is disabled") {
		t.Fatalf("promote with disabled memory error = %v", err)
	}
}
