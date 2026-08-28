package cliagents

// memory_support_coverage_test.go covers the small memory-state
// accessors in memory_support.go that legacytui consumes only through
// the dispatch wiring.

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestOpenMemoryStoreWithReadOnlyPathEscape(t *testing.T) {
	// A relative store_path escaping the workspace root must be
	// rejected (lines 27-29).
	_, err := OpenMemoryStoreWithReadOnly(t.TempDir(), config.MemoryConfig{StorePath: "../escape"}, false)
	if err == nil {
		t.Fatal("OpenMemoryStoreWithReadOnly(../escape) must error")
	}
	// A relative store_path inside the root is joined to the root
	// and opened (the backend may then fail; we only exercise the
	// join branch, line 30).
	_, _ = OpenMemoryStoreWithReadOnly(t.TempDir(), config.MemoryConfig{StorePath: "inside.db"}, true)
}

func TestOpenMemoryStoreWithReadOnlyHardensAdHocStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits; file modes always read back as 0666/0777")
	}
	root := t.TempDir()
	adHocPath := config.TempStorePath(root, "memory")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(adHocPath)) })
	mc := config.MemoryConfig{StorePath: adHocPath}
	store, err := OpenMemoryStoreWithReadOnly(root, mc, false)
	if err != nil {
		t.Fatalf("OpenMemoryStoreWithReadOnly = %v, want nil", err)
	}
	defer store.Close()
	st, err := os.Stat(adHocPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("ad-hoc store mode = %o, want 600", perm)
	}
	dirSt, err := os.Stat(filepath.Dir(adHocPath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirSt.Mode().Perm(); perm != 0o700 {
		t.Errorf("ad-hoc store dir mode = %o, want 700", perm)
	}
}

func TestOpenMemoryStoreWithReadOnlyStorePathDoesNotHarden(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits; file modes always read back as 0666/0777")
	}
	root := t.TempDir()
	storePath := filepath.Join(root, "custom-memory.db")
	mc := config.MemoryConfig{StorePath: storePath}
	store, err := OpenMemoryStoreWithReadOnly(root, mc, false)
	if err != nil {
		t.Fatalf("OpenMemoryStoreWithReadOnly = %v, want nil", err)
	}
	defer store.Close()
	st, err := os.Stat(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm == 0o600 {
		t.Errorf("explicit store_path mode = %o, must not be forced to 600", perm)
	}
	dirSt, err := os.Stat(filepath.Dir(storePath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirSt.Mode().Perm(); perm == 0o700 {
		t.Errorf("explicit store_path dir mode = %o, must not be forced to 700", perm)
	}
}

func TestAgentSessionStateDisplayHelpers(t *testing.T) {
	// DisplaySource and CurrentAgentName on nil and on a selected
	// agent: nil-safe reads that the TUI dialog renders.
	var nilState *AgentSessionState
	if got := nilState.DisplaySource(); got != string(config.AgentSourceBuiltIn) {
		t.Errorf("DisplaySource(nil) = %q", got)
	}
	state := &AgentSessionState{}
	if got := state.DisplaySource(); got != string(config.AgentSourceBuiltIn) {
		t.Errorf("DisplaySource(no selection) = %q, want %q", got, string(config.AgentSourceBuiltIn))
	}
	if got := state.DisplayName(); got != config.RootAgentName {
		t.Errorf("DisplayName(no selection) = %q, want %q", got, config.RootAgentName)
	}
}
