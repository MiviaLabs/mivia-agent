package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// Ensures the committed repo agent definitions under .mivia/agents/ still parse
// and resolve. Skips if the workspace is not the mivia-agent tree.
func TestProjectAgentDefinitionsResolve(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// Prefer module root via go.mod walk from cwd
	cwd, _ := os.Getwd()
	// test runs with package dir as cwd for relative; use repo-relative from this file
	dir := filepath.Join(cwd, "..", "..", ".mivia", "agents")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		// Try from module root when tests run with -C or different cwd
		dir = filepath.Join(root, ".mivia", "agents")
	}
	if _, err := os.Stat(filepath.Join(dir, "mivia.toml")); err != nil {
		t.Skip("project agents not present at", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := map[string]bool{
		"docs":        true,
		"go-engineer": true,
		"mivia":       true,
		"researcher":  true,
		"reviewer":    true,
		"security":    true,
		"verifier":    true,
	}
	var inputs []ResolveInput
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".toml" {
			continue
		}
		name := entry.Name()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		spec, canonical, err := config.ParseAgentFileTOML(data, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		seen[canonical] = true
		inputs = append(inputs, ResolveInput{
			Name:   canonical,
			Source: config.AgentSourceWorkspace,
			Path:   filepath.Join(dir, name),
			Spec:   spec,
		})
	}
	if len(seen) != len(wantNames) {
		t.Fatalf("agent roster names = %v, want exactly %v", seen, wantNames)
	}
	for name := range wantNames {
		if !seen[name] {
			t.Fatalf("required agent %q is missing from %s", name, dir)
		}
	}
	reg, _, err := ResolveAll(inputs, ResolveOptions{
		Global: config.AgentsGlobal{FailOnEmptyToolset: true, LoadWorkspaceConfig: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMiviaAgent(t, reg)
	assertGoEngineerAgent(t, reg)
	assertSpecialistScopes(t, reg)
	assertAgentPromptsArePortable(t, inputs)
}

func assertMiviaAgent(t *testing.T, reg *AgentRegistry) {
	mivia, ok := reg.Get("mivia")
	if !ok {
		t.Fatal("mivia missing")
	}
	if len(mivia.EffectiveTools) == 0 {
		t.Fatal("mivia should have tools")
	}
	// Root mivia omits skills → unrestricted (all trusted).
	if mivia.Skills != nil {
		t.Fatalf("mivia should omit skills (all trusted), got %#v", mivia.Skills)
	}
}

func assertGoEngineerAgent(t *testing.T, reg *AgentRegistry) {
	eng, ok := reg.Get("go-engineer")
	if !ok {
		t.Fatal("go-engineer missing")
	}
	for _, n := range eng.EffectiveTools {
		if n == "dispatch_tasks" {
			t.Fatal("go-engineer must not keep mandatory-denylist tools in effective set")
		}
	}
	if len(eng.EffectiveTools) < 5 {
		t.Fatalf("go-engineer tools too small: %v", eng.EffectiveTools)
	}
	// Plan 06: go-engineer ships an explicit skills allowlist (engineering set).
	if eng.Skills == nil || len(*eng.Skills) == 0 {
		t.Fatal("go-engineer must declare a non-empty skills allowlist")
	}
	wantSkill := false
	for _, s := range *eng.Skills {
		if s == "bug-audit" || s == "verify-change" {
			wantSkill = true
			break
		}
	}
	if !wantSkill {
		t.Fatalf("go-engineer skills missing engineering entries: %v", *eng.Skills)
	}
}

func assertSpecialistScopes(t *testing.T, reg *AgentRegistry) {
	t.Helper()
	for _, name := range []string{"researcher", "reviewer", "security"} {
		a, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if a.Skills == nil || len(*a.Skills) == 0 {
			t.Fatalf("%s must declare skills", name)
		}
		for _, forbidden := range []string{"write_file", "search_replace", "run_command"} {
			if hasTool(a.EffectiveTools, forbidden) {
				t.Fatalf("%s unexpectedly has %s: %v", name, forbidden, a.EffectiveTools)
			}
		}
	}
	docs, ok := reg.Get("docs")
	if !ok || hasTool(docs.EffectiveTools, "run_command") {
		t.Fatalf("docs must be write-capable without command execution: %#v", docs)
	}
	verifier, ok := reg.Get("verifier")
	if !ok || !hasTool(verifier.EffectiveTools, "run_command") || hasTool(verifier.EffectiveTools, "write_file") {
		t.Fatalf("verifier must run checks without writes: %#v", verifier)
	}
}

func hasTool(tools []string, want string) bool {
	for _, name := range tools {
		if name == want {
			return true
		}
	}
	return false
}

func assertAgentPromptsArePortable(t *testing.T, inputs []ResolveInput) {
	t.Helper()
	for _, input := range inputs {
		if input.Spec.SystemPrompt == nil {
			continue
		}
		prompt := strings.ToLower(*input.Spec.SystemPrompt)
		for _, forbidden := range []string{
			"github.com/mivialabs/mivia-agent",
			"mivia-agent monorepo",
			"cmd/mivia",
			"go test ./",
			"make verify",
		} {
			if strings.Contains(prompt, forbidden) {
				t.Fatalf("agent %q prompt contains project-specific text %q", input.Name, forbidden)
			}
		}
	}
}
