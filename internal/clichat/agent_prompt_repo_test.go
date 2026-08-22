package clichat

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// Locks the product repo's default agent definition (.mivia/agents/mivia.toml):
// orientation for agents working on mivia-itself - not a living status dump.
//
// See docs/development/agent-self-prompt.md

var livingStateSmells = []struct {
	name string
	re   *regexp.Regexp
}{
	{"test count in parens", regexp.MustCompile(`\(\d+\+?\s*tests?\)`)},
	{"NEW feature banner", regexp.MustCompile(`(?m)^#{1,3}\s*.*\bNEW\b`)},
	{"Key Features section", regexp.MustCompile(`(?i)(?m)^#{1,3}\s*key features\b`)},
	{"Packages inventory section", regexp.MustCompile(`(?i)(?m)^#{1,3}\s*packages\b`)},
	{"What's implemented", regexp.MustCompile(`(?i)what'?s been implemented`)},
	{"Next priorities", regexp.MustCompile(`(?i)next priorities`)},
	{"All commits and what", regexp.MustCompile(`(?i)all commits and what`)},
	{"130+ tests style", regexp.MustCompile(`\d+\+\s*tests`)},
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestRepoMiviaAgentIsMetaOrientationNotState(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".mivia", "agents", "mivia.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (this repo must ship the default mivia agent)", path, err)
	}
	spec, name, err := config.ParseAgentFileTOML(data, "mivia.toml")
	if err != nil {
		t.Fatalf("parse mivia agent: %v", err)
	}
	if name != "mivia" {
		t.Fatalf("name = %q", name)
	}
	if spec.SystemPrompt == nil || strings.TrimSpace(*spec.SystemPrompt) == "" {
		t.Fatal("mivia agent must define system_prompt")
	}
	content := *spec.SystemPrompt
	lower := strings.ToLower(content)

	needles := []string{
		"orchestrator",
		"model-facing",
		"language-generic",
		"adlc",
	}
	for _, n := range needles {
		if !strings.Contains(lower, n) {
			t.Fatalf(".mivia/agents/mivia.toml missing orientation cue %q", n)
		}
	}

	var bad []string
	for _, s := range livingStateSmells {
		if s.re.MatchString(content) {
			bad = append(bad, s.name)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("mivia agent prompt must not hold living project state (got smells: %s)", strings.Join(bad, ", "))
	}
}

func TestDefaultAgentIsMiviaWhenPresent(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)

	agentsDir := filepath.Join(ws, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`
name = "mivia"
description = "default root"
tools = ["read_file"]
system_prompt = "from mivia agent"
`)
	if err := os.WriteFile(filepath.Join(agentsDir, "mivia.toml"), body, 0o600); err != nil {
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

	agentsDir := filepath.Join(ws, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`
name = "mivia"
description = "default root"
tools = ["read_file"]
provider = "deepseek"
model = "deepseek-v4-flash"
system_prompt = "from mivia agent"
`)
	if err := os.WriteFile(filepath.Join(agentsDir, "mivia.toml"), body, 0o600); err != nil {
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
