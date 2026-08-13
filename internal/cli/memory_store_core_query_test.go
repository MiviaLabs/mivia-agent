package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

func TestCoreMemoryBlockEmptyWhenInjectCoreDisabled(t *testing.T) {
	root := t.TempDir()
	mc := config.MemoryConfig{StoreBackend: "sqlite", StorePath: ".mivia/memory.db", InjectCore: false}
	store, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("openMemoryStore: %v", err)
	}
	defer store.Close()

	res, err := store.Save(context.Background(), memory.Entry{
		Title: "promoted fact", Scope: memory.ScopeProject, Verdict: memory.VerdictGood,
		Summary: "a promoted fact", Why: "because",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.PromoteToCore(context.Background(), res.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}

	block := coreMemoryBlock(context.Background(), store, memory.ScopeProject, mc)
	if block != "" {
		t.Fatalf("coreMemoryBlock with InjectCore=false = %q, want empty (a true no-op)", block)
	}
}

func TestCoreMemoryBlockRendersPromotedEntries(t *testing.T) {
	root := t.TempDir()
	mc := config.MemoryConfig{StoreBackend: "sqlite", StorePath: ".mivia/memory.db", InjectCore: true}
	store, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("openMemoryStore: %v", err)
	}
	defer store.Close()

	res, err := store.Save(context.Background(), memory.Entry{
		Title: "promoted fact", Scope: memory.ScopeProject, Verdict: memory.VerdictGood,
		Summary: "a promoted fact worth remembering", Why: "because",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.PromoteToCore(context.Background(), res.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}

	block := coreMemoryBlock(context.Background(), store, memory.ScopeProject, mc)
	if !strings.Contains(block, "promoted fact") || !strings.Contains(block, "a promoted fact worth remembering") {
		t.Fatalf("coreMemoryBlock missing title/summary:\n%s", block)
	}
	// Only title+summary, never the full rendered content (D1b): the raw
	// Markdown body has a "## Why" heading that must not leak into the block.
	if strings.Contains(block, "## Why") {
		t.Fatalf("coreMemoryBlock leaked full entry content, want title+summary only:\n%s", block)
	}
}

func TestCoreMemoryBlockEmptyWhenNoCoreEntries(t *testing.T) {
	root := t.TempDir()
	mc := config.MemoryConfig{StoreBackend: "sqlite", StorePath: ".mivia/memory.db", InjectCore: true}
	store, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("openMemoryStore: %v", err)
	}
	defer store.Close()

	block := coreMemoryBlock(context.Background(), store, memory.ScopeProject, mc)
	if block != "" {
		t.Fatalf("coreMemoryBlock with no core entries = %q, want empty", block)
	}
}
