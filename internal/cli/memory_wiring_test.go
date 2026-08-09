package cli

import (
	"context"
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
	if err := configureChatWorkspace(sess, root, true, res); err != nil {
		t.Fatalf("configureChatWorkspace: %v", err)
	}
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
	if err := configureChatWorkspace(sess, root, true, res); err != nil {
		t.Fatalf("configureChatWorkspace: %v", err)
	}
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
	if err := configureChatWorkspace(sess, root, true, res); err != nil {
		t.Fatal(err)
	}
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

func TestMemoryStoreErrorSurfacesFromConfigureChatWorkspace(t *testing.T) {
	root := t.TempDir()
	res := memoryTestResolved(true)
	// An invalid backend is rejected at config load, but a directly built
	// Resolved must still fail loudly at wiring, not silently drop the tools.
	res.Memory.StoreBackend = "bogus"
	sess := chat.NewSession(res, nil)
	err := configureChatWorkspace(sess, root, true, res)
	if err == nil {
		t.Fatal("an invalid memory backend must fail the workspace wiring")
	}
	if !strings.Contains(err.Error(), "memory") {
		t.Errorf("error %q does not mention memory", err)
	}
}
