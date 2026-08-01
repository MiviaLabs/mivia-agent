package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

const agentExampleRoot = "internal/config/testdata/agent-examples"

func agentExamplesPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, agentExampleRoot)
}

func readAgentExample(t *testing.T, relative string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(agentExamplesPath(t), relative))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func copyAgentExample(t *testing.T, relative, destination string) {
	t.Helper()
	data := readAgentExample(t, relative)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func exampleSkillCatalogue() map[string]SkillCatalogueEntry {
	return map[string]SkillCatalogueEntry{
		"docs-update":   {User: true},
		"verify-change": {User: true},
	}
}

func exampleResolveOptions() ResolveOptions {
	return ResolveOptions{
		Global:             config.AgentsGlobal{LoadWorkspaceConfig: false, FailOnEmptyToolset: true},
		KnownTools:         knownToolSet(tools.AllToolNames()),
		SkillCatalogue:     exampleSkillCatalogue(),
		AllowProjectSkills: false,
	}
}

func TestAgentExampleFixturesParseAndResolve(t *testing.T) {
	files := []struct {
		relative string
		source   config.AgentSource
	}{
		{"user-agents/researcher.toml", config.AgentSourceUser},
		{"user-agents/engineer.toml", config.AgentSourceUser},
		{"workspace-agents/reviewer.toml", config.AgentSourceWorkspace},
	}
	inputs := make([]ResolveInput, 0, len(files))
	for _, file := range files {
		name := filepath.Base(file.relative)
		data := readAgentExample(t, file.relative)
		spec, canonical, err := config.ParseAgentFileTOML(data, name)
		if err != nil {
			t.Fatalf("parse %s: %v", file.relative, err)
		}
		inputs = append(inputs, ResolveInput{Name: canonical, Source: file.source, Path: file.relative, Spec: spec})
	}
	registry, _, err := ResolveAll(inputs, exampleResolveOptions())
	if err != nil {
		t.Fatal(err)
	}
	researcher, ok := registry.Get("researcher")
	if !ok || len(researcher.EffectiveTools) != 5 || researcher.Skills == nil || len(*researcher.Skills) != 1 {
		t.Fatalf("researcher = %#v", researcher)
	}
	engineer, ok := registry.Get("engineer")
	if !ok || engineer.ParentName != "researcher" || engineer.Skills == nil || len(*engineer.Skills) != 0 {
		t.Fatalf("engineer = %#v", engineer)
	}
	if len(engineer.EffectiveTools) != 6 || !contains(engineer.EffectiveTools, "search_replace") {
		t.Fatalf("engineer tools = %v", engineer.EffectiveTools)
	}
	reviewer, ok := registry.Get("reviewer")
	if !ok || reviewer.Provenance.Source != config.AgentSourceWorkspace || len(reviewer.EffectiveTools) != 2 {
		t.Fatalf("reviewer = %#v", reviewer)
	}
}

func TestAgentExampleFixturesDiscoverWithTrustBoundaries(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	copyAgentExample(t, "user-mivia.toml", filepath.Join(config.UserConfigPath()))
	copyAgentExample(t, "user-agents/researcher.toml", filepath.Join(config.UserAgentsDir(), "researcher.toml"))
	copyAgentExample(t, "user-agents/engineer.toml", filepath.Join(config.UserAgentsDir(), "engineer.toml"))
	copyAgentExample(t, "workspace-agents/reviewer.toml", filepath.Join(config.WorkspaceAgentsDir(workspace), "reviewer.toml"))

	registry, global, warnings, err := LoadAndResolveOpts(workspace, LoadResolveOptions{
		SkillCatalogue: exampleSkillCatalogue(), AllowProjectSkills: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if global.LoadWorkspaceConfig {
		t.Fatal("fixture gate should disable workspace prompts/project skills")
	}
	if _, ok := registry.Get("reviewer"); !ok {
		t.Fatal("workspace agent files must load when the gate is off")
	}
	reviewer, _ := registry.Get("reviewer")
	if reviewer.Provenance.Source != config.AgentSourceWorkspace {
		t.Fatalf("reviewer source = %s", reviewer.Provenance.Source)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	// A same-name trusted user file wins while the workspace file remains a
	// discovered shadow, independent of the workspace prompt/skill gate.
	copyAgentExample(t, "workspace-agents/reviewer.toml", filepath.Join(config.UserAgentsDir(), "reviewer.toml"))
	registry, _, warnings, err = LoadAndResolveOpts(workspace, LoadResolveOptions{SkillCatalogue: exampleSkillCatalogue()})
	if err != nil {
		t.Fatal(err)
	}
	reviewer, _ = registry.Get("reviewer")
	if reviewer.Provenance.Source != config.AgentSourceUser || len(warnings) == 0 || !strings.Contains(strings.Join(warnings, "\n"), "shadowed") {
		t.Fatalf("shadow result = source %s warnings %v", reviewer.Provenance.Source, warnings)
	}
}

func TestAgentExampleFixtureMutationsFailAtTheExpectedBoundary(t *testing.T) {
	known := knownToolSet(tools.AllToolNames())
	catalogue := exampleSkillCatalogue()
	tests := []struct {
		name       string
		filename   string
		body       string
		parseError bool
		resolve    bool
	}{
		{"unknown key", "broken.toml", "name=\"broken\"\ndescription=\"x\"\nmystery=true", true, false},
		{"filename mismatch", "expected.toml", "name=\"other\"\ndescription=\"x\"", true, false},
		{"unknown tool", "broken.toml", "name=\"broken\"\ndescription=\"x\"\ntools=[\"not_a_tool\"]", false, true},
		{"unknown skill", "broken.toml", "name=\"broken\"\ndescription=\"x\"\ntools=[\"read_file\"]\nskills=[\"not_a_skill\"]", false, true},
		{"tools plus delta", "broken.toml", "name=\"broken\"\ndescription=\"x\"\ntools=[\"read_file\"]\ntools_add=[\"grep\"]", true, false},
		{"missing parent", "broken.toml", "name=\"broken\"\ndescription=\"x\"\ninherits=\"missing\"\ntools_add=[\"grep\"]", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, canonical, err := config.ParseAgentFileTOML([]byte(tc.body), tc.filename)
			if tc.parseError {
				if err == nil {
					t.Fatal("mutation unexpectedly parsed")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected parser error: %v", err)
			}
			_, _, err = ResolveAll([]ResolveInput{{Name: canonical, Source: config.AgentSourceUser, Spec: spec}}, ResolveOptions{
				Global: config.AgentsGlobal{FailOnEmptyToolset: true}, KnownTools: known,
				SkillCatalogue: catalogue,
			})
			if tc.resolve && err == nil {
				t.Fatal("mutation unexpectedly resolved")
			}
		})
	}
}

func TestAgentExampleFixturesContainNoSensitiveOrEnvironmentSpecificFields(t *testing.T) {
	var entries []string
	err := filepath.Walk(agentExamplesPath(t), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() && filepath.Ext(path) == ".toml" {
			entries = append(entries, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(string(data))
		for _, forbidden := range []string{"system_prompt", "api_key", "password", "provider", "model =", "/home/", "c:\\\\"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s contains forbidden fixture content %q", path, forbidden)
			}
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
