package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

func TestMemoryDumpEndToEnd(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)
	saveTestMemories(t, root)

	var out, errOut strings.Builder
	if err := runMemoryWithIO([]string{"dump", "--workspace", root, "--config", cfgPath}, &out, &errOut); err != nil {
		t.Fatalf("runMemoryWithIO dump: %v", err)
	}
	if !strings.Contains(out.String(), `"title":"Deploy pipeline fix"`) {
		t.Fatalf("dump output missing seeded entry:\n%s", out.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("dump line count = %d, want 3 (one per seeded entry)", len(lines))
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected stderr for a small dump: %q", errOut.String())
	}
}

func TestMemoryDumpDisabledMemoryError(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, false)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"dump", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "memory is disabled") {
		t.Fatalf("dump with disabled memory error = %v", err)
	}
}

func TestMemoryDumpUnknownFlag(t *testing.T) {
	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"dump", "--bogus"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("dump unknown flag error = %v", err)
	}
}

func TestMemoryDumpWarnsOverThreshold(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)

	mc := config.MemoryConfig{
		StoreBackend: "sqlite", StorePath: ".mivia/memory.db",
		MaxEntries: 200, MaxEntryBytes: 20000, MaxSearchResults: 8,
	}
	store, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("openMemoryStore: %v", err)
	}
	// Entries near the per-field size caps (entry.go: summary 400, why
	// 1000, good/bad 2000 each), repeated, push total dumped bytes over the
	// 400 KiB warning threshold without needing an unreasonable entry count.
	summary := strings.Repeat("s", 400)
	why := strings.Repeat("w", 1000)
	good := strings.Repeat("g", 2000)
	bad := strings.Repeat("b", 2000)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		_, err := store.Save(ctx, memory.Entry{
			Title: fmt.Sprintf("big entry %d", i), Scope: memory.ScopeProject,
			Verdict: memory.VerdictNeutral, Summary: summary, Why: why, Good: good, Bad: bad,
		})
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	store.Close()

	var out, errOut strings.Builder
	if err := runMemoryWithIO([]string{"dump", "--workspace", root, "--config", cfgPath}, &out, &errOut); err != nil {
		t.Fatalf("runMemoryWithIO dump: %v", err)
	}
	if !strings.Contains(errOut.String(), "warning threshold") {
		t.Fatalf("expected a warning-threshold message on stderr for a large dump, got: %q", errOut.String())
	}
}
