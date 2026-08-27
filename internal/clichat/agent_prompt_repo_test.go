package clichat

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// This repo deliberately ships NO root-agent override: the dogfood workspace
// runs on the same compiled fallback prompt (buildAgentPrompt) every user
// gets, so drift between the two surfaces cannot exist.
func TestRepoShipsNoRootAgentOverride(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(root, ".agents", "agents", "mivia.md")
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("%s exists: this repo must not override the compiled root prompt - keep dogfooding the shipped fallback", path)
	}
}

func TestDefaultAgentIsMiviaWhenPresent(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)

	agentsDir := filepath.Join(ws, ".agents", "agents")
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nname: mivia\ndescription: default root\ntools: [read_file]\n---\nfrom mivia agent")
	if err := os.WriteFile(filepath.Join(agentsDir, "mivia.md"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	// No --agent flag → auto-select mivia.
	loaded, err := loadAgentDefinitions(ws, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Selected == nil {
		t.Fatal("expected default mivia agent to be selected")
	}
	if loaded.Selected.Name != "mivia" {
		t.Fatalf("selected = %q", loaded.Selected.Name)
	}
	if loaded.Selected.SystemPrompt != "from mivia agent" {
		t.Fatalf("system_prompt = %q", loaded.Selected.SystemPrompt)
	}
}

// A provider-bearing workspace mivia.toml still SELECTS the root orchestrator
// under the default strip: the workspace-declared provider/model selection is
// ignored (credential-routing protection), never REJECTed, so the prompt
// survives. This is the case a blanket REJECT of provider-declaring workspace
// files would silently break (it would lose the root prompt and the roster).
func TestDefaultAgentIsMiviaWhenPresentProviderStripped(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)

	agentsDir := filepath.Join(ws, ".agents", "agents")
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nname: mivia\ndescription: default root\ntools: [read_file]\nprovider: deepseek\nmodel: deepseek-v4-flash\n---\nfrom mivia agent")
	if err := os.WriteFile(filepath.Join(agentsDir, "mivia.md"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	// No --agent flag → auto-select mivia; prompt preserved, binding stripped.
	loaded, err := loadAgentDefinitions(ws, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Selected == nil {
		t.Fatal("expected default mivia agent to be selected")
	}
	if loaded.Selected.Name != "mivia" {
		t.Fatalf("selected = %q", loaded.Selected.Name)
	}
	if loaded.Selected.SystemPrompt != "from mivia agent" {
		t.Fatalf("system_prompt = %q (the root orchestrator prompt must survive the strip)", loaded.Selected.SystemPrompt)
	}
	if loaded.Selected.Provider != "" || loaded.Selected.Model != "" {
		t.Fatalf("binding = %q/%q, want the default strip to drop the workspace-declared provider/model", loaded.Selected.Provider, loaded.Selected.Model)
	}
}
