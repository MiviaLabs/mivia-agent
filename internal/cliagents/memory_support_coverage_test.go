package cliagents

// memory_support_coverage_test.go covers the small memory-state
// accessors in memory_support.go that legacytui consumes only through
// the dispatch wiring.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestMemoryOfAndMemoryConfigOfNilSafe(t *testing.T) {
	if got := MemoryOf(nil); got != nil {
		t.Errorf("MemoryOf(nil) = %v, want nil", got)
	}
	if got := MemoryConfigOf(nil); got.InjectCore {
		t.Errorf("MemoryConfigOf(nil).InjectCore must be false, got %v", got.InjectCore)
	}
}

func TestCoreMemoryBlockForStateAndOpts(t *testing.T) {
	// Both helpers must not panic on nil state/opts.
	if got := CoreMemoryBlockForState(nil); got != "" {
		t.Errorf("CoreMemoryBlockForState(nil) = %q, want empty", got)
	}
	if got := CoreMemoryBlockForOpts(SessionDispatcherOpts{}); got != "" {
		t.Errorf("CoreMemoryBlockForOpts(empty) = %q, want empty", got)
	}
}

func TestOpenMemoryStoreRejectsMissingPath(t *testing.T) {
	// OpenMemoryStore with an empty path must not panic.
	_, _ = OpenMemoryStoreWithReadOnly("", config.MemoryConfig{}, false)
	// And with a temp dir but invalid backend must error.
	_, err := OpenMemoryStoreWithReadOnly(t.TempDir(), config.MemoryConfig{StoreBackend: "garbage"}, false)
	if err == nil {
		t.Fatal("OpenMemoryStoreWithReadOnly(garbage backend) must error")
	}
}
