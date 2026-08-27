package cliagents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func writeAgentFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadAgentDefinitionsRootNameRestoresRootSurface pins the startup
// selection contract for the compiled root identity: the flag resolves to no
// selection instead of failing agents.Select, while a real built-in name
// selects and an unknown name still errors.
func TestLoadAgentDefinitionsRootNameRestoresRootSurface(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	res, err := LoadAgentDefinitions(ws, config.RootAgentName, nil)
	if err != nil {
		t.Fatalf("LoadAgentDefinitions(root) error = %v, want root surface", err)
	}
	if res.Selected != nil {
		t.Fatalf("root flag must select nothing, got %q", res.Selected.Name)
	}
	if _, ok := res.Registry.Get("general-purpose"); !ok {
		t.Fatalf("built-in general-purpose missing from registry %v", res.Registry.Names())
	}

	selected, err := LoadAgentDefinitions(ws, "general-purpose", nil)
	if err != nil {
		t.Fatalf("LoadAgentDefinitions(general-purpose) error = %v", err)
	}
	if selected.Selected == nil || selected.Selected.Name != "general-purpose" {
		t.Fatalf("general-purpose must be selectable, got %+v", selected.Selected)
	}
	if got := selected.Selected.Provenance.Source; got != config.AgentSourceBuiltIn {
		t.Fatalf("selected provenance = %q, want %q", got, config.AgentSourceBuiltIn)
	}

	if _, err := LoadAgentDefinitions(ws, "no-such-agent", nil); err == nil {
		t.Fatal("unknown agent flag must still error")
	}
}

// TestLoadAgentDefinitionsDefaults pins the root-selection precedence: an
// empty flag selects nothing (compiled root), a file-backed "mivia"
// definition keeps its legacy auto-select above the built-in root, and the
// built-in registry always carries general-purpose.
func TestLoadAgentDefinitionsDefaults(t *testing.T) {
	t.Run("empty flag selects nothing", func(t *testing.T) {
		home, ws := t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		res, err := LoadAgentDefinitions(ws, "", nil)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if res.Selected != nil {
			t.Fatalf("empty flag must select nothing, got %q", res.Selected.Name)
		}
		if _, ok := res.Registry.Get("general-purpose"); !ok {
			t.Fatal("built-in general-purpose missing")
		}
	})
	t.Run("file-backed mivia keeps legacy auto-select", func(t *testing.T) {
		home, ws := t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		writeAgentFile(t, config.WorkspaceAgentsDir(ws), "mivia.md",
			"---\nname: mivia\ndescription: custom root\n---\n\nCustom root prompt.\n")
		res, err := LoadAgentDefinitions(ws, "", nil)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if res.Selected == nil || res.Selected.Name != config.DefaultAgentName {
			t.Fatalf("mivia.md must auto-select, got %+v", res.Selected)
		}
	})
	t.Run("built-in list includes provenance", func(t *testing.T) {
		home, ws := t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		res, err := LoadAgentDefinitions(ws, "", nil)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		builtin, ok := res.Registry.Get("general-purpose")
		if !ok {
			t.Fatal("built-in general-purpose missing")
		}
		if _, err := builtin.DefinitionDigest(); err != nil {
			t.Fatalf("digest error = %v", err)
		}
		if res.Registry.Len() != 1 {
			t.Fatalf("clean load registry = %v", res.Registry.Names())
		}
	})
}
