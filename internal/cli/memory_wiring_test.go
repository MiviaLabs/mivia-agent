package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

func memoryTestResolved(enabled bool) *config.Resolved {
	return &config.Resolved{
		ProviderName: "deepseek",
		Model:        "deepseek-v4-flash",
		Memory:       config.MemoryConfig{Enabled: &enabled, StoreBackend: "sqlite"},
	}
}

func TestConfigureChatWorkspaceWiresMemoryTools(t *testing.T) {
	root := t.TempDir()
	res := memoryTestResolved(true)
	sess := chat.NewSession(res, nil)
	memClose, err := configureChatWorkspace(sess, root, true, res, nil)
	if err != nil {
		t.Fatalf("configureChatWorkspace: %v", err)
	}
	defer memClose()
	for _, name := range []string{"memory_save", "memory_search"} {
		if _, ok := sess.Tools.Get(name); !ok {
			t.Errorf("%s not registered when memory is enabled", name)
		}
	}
}

func TestConfigureChatWorkspaceOmitsMemoryToolsWhenDisabled(t *testing.T) {
	root := t.TempDir()
	res := memoryTestResolved(false)
	sess := chat.NewSession(res, nil)
	memClose, err := configureChatWorkspace(sess, root, true, res, nil)
	if err != nil {
		t.Fatalf("configureChatWorkspace: %v", err)
	}
	defer memClose()
	if _, ok := sess.Tools.Get("memory_save"); ok {
		t.Fatal("memory_save must not register when memory is disabled")
	}
	if _, ok := sess.Tools.Get("memory_search"); ok {
		t.Fatal("memory_search must not register when memory is disabled")
	}
}

func TestConfigureChatWorkspaceMemoryDisabledStillConfiguresOtherTools(t *testing.T) {
	root := t.TempDir()
	res := memoryTestResolved(false)
	sess := chat.NewSession(res, nil)
	memClose, err := configureChatWorkspace(sess, root, true, res, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer memClose()
	if _, ok := sess.Tools.Get("read_file"); !ok {
		t.Fatal("disabling memory must not disable the file tools")
	}
}

func TestOpenMemoryStoreResolvesRelativeStorePath(t *testing.T) {
	root := t.TempDir()
	enabled := true
	mc := config.MemoryConfig{Enabled: &enabled, StoreBackend: "sqlite", StorePath: ".mivia/memory.db"}
	store, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("openMemoryStore: %v", err)
	}
	defer store.Close()
	if _, err := store.Save(context.Background(), memory.Entry{
		Title:   "wired",
		Scope:   memory.ScopeProject,
		Verdict: memory.VerdictGood,
		Created: "2026-08-09",
		Summary: "wired store works",
		Why:     "harness wiring",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Search(context.Background(), memory.Query{Text: "wired", Scope: memory.ScopeProject})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].Title != "wired" {
		t.Fatalf("search results = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".mivia", "memory.db")); err != nil {
		t.Fatalf("store_path must resolve against the workspace root: %v", err)
	}
}

func TestOpenMemoryStoreExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	enabled := true
	mc := config.MemoryConfig{Enabled: &enabled, StoreBackend: "sqlite", StorePath: "~/memory-projects.db"}
	store, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("openMemoryStore: %v", err)
	}
	defer store.Close()
	if _, err := os.Stat(filepath.Join(home, "memory-projects.db")); err != nil {
		t.Fatalf("store_path must expand ~: %v", err)
	}
}

func TestOpenMemoryStoreRejectsEscapingStorePath(t *testing.T) {
	root := t.TempDir()
	enabled := true
	for _, bad := range []string{"../escape.db", "../../../tmp/escape.db"} {
		mc := config.MemoryConfig{Enabled: &enabled, StoreBackend: "sqlite", StorePath: bad}
		store, err := openMemoryStore(root, mc)
		if err == nil {
			store.Close()
			t.Fatalf("store_path %q must be rejected (escapes the workspace)", bad)
		}
		if !strings.Contains(err.Error(), "escapes") {
			t.Errorf("store_path %q error = %q, want a workspace-escape message", bad, err)
		}
	}
	// Absolute store_path stays allowed (repo-controlled config, like hooks).
	abs := filepath.Join(t.TempDir(), "abs.db")
	mc := config.MemoryConfig{Enabled: &enabled, StoreBackend: "sqlite", StorePath: abs}
	store, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("absolute store_path must be allowed: %v", err)
	}
	store.Close()
}

func TestMemoryStoreErrorSurfacesFromConfigureChatWorkspace(t *testing.T) {
	root := t.TempDir()
	res := memoryTestResolved(true)
	// An invalid backend is rejected at config load, but a directly built
	// Resolved must still fail loudly at wiring, not silently drop the tools.
	res.Memory.StoreBackend = "bogus"
	sess := chat.NewSession(res, nil)
	_, err := configureChatWorkspace(sess, root, true, res, nil)
	if err == nil {
		t.Fatal("an invalid memory backend must fail the workspace wiring")
	}
	if !strings.Contains(err.Error(), "memory") {
		t.Errorf("error %q does not mention memory", err)
	}
}

// TestConfigureChatWorkspaceStashesStoreOnState is plan 77's E1 test: the
// store configureChatWorkspace opens must be reachable from
// agentSessionState.Memory - the single source of truth every
// SystemPrompt-composing call site reads from - and must actually work
// (Save through it is visible via the session's own memory_search tool,
// proving state.Memory and the tool-registered store are wired from the
// same open, not two independent connections that could diverge).
func TestConfigureChatWorkspaceStashesStoreOnState(t *testing.T) {
	root := t.TempDir()
	res := memoryTestResolved(true)
	sess := chat.NewSession(res, nil)
	state := &agentSessionState{}
	memClose, err := configureChatWorkspace(sess, root, true, res, state)
	if err != nil {
		t.Fatalf("configureChatWorkspace: %v", err)
	}
	defer memClose()

	if state.Memory == nil {
		t.Fatal("state.Memory was not populated")
	}
	if state.MemoryConfig.StoreBackend != "sqlite" {
		t.Fatalf("state.MemoryConfig = %+v, want the resolved [memory] config", state.MemoryConfig)
	}

	if _, err := state.Memory.Save(context.Background(), memory.Entry{
		Title: "wiring proof", Scope: memory.ScopeProject, Verdict: memory.VerdictNeutral,
		Summary: "state.Memory and the registered tool store share one open", Why: "test",
	}); err != nil {
		t.Fatalf("save through state.Memory: %v", err)
	}

	tool, ok := sess.Tools.Get("memory_search")
	if !ok {
		t.Fatal("memory_search not registered")
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"wiring proof"}`))
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	if !strings.Contains(out, "wiring proof") {
		t.Fatalf("memory_search did not see the entry saved through state.Memory - two separate opens? output: %s", out)
	}
}

// TestConfigureChatWorkspaceNilStateIsSafe confirms a nil state (the
// existing test-only calling convention throughout this file) is a true
// no-op for the stashing step, not a nil-pointer panic.
func TestConfigureChatWorkspaceNilStateIsSafe(t *testing.T) {
	root := t.TempDir()
	res := memoryTestResolved(true)
	sess := chat.NewSession(res, nil)
	memClose, err := configureChatWorkspace(sess, root, true, res, nil)
	if err != nil {
		t.Fatalf("configureChatWorkspace with nil state: %v", err)
	}
	memClose()
}
