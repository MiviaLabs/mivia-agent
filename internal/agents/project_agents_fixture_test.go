package agents

import (
	"os"
	"path/filepath"
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
	var inputs []ResolveInput
	for _, name := range []string{"mivia.toml", "go-engineer.toml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		spec, canonical, err := config.ParseAgentFileTOML(data, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		inputs = append(inputs, ResolveInput{
			Name:   canonical,
			Source: config.AgentSourceWorkspace,
			Path:   filepath.Join(dir, name),
			Spec:   spec,
		})
	}
	reg, _, err := ResolveAll(inputs, ResolveOptions{
		Global: config.AgentsGlobal{FailOnEmptyToolset: true, LoadWorkspaceConfig: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	mivia, ok := reg.Get("mivia")
	if !ok {
		t.Fatal("mivia missing")
	}
	if len(mivia.EffectiveTools) == 0 {
		t.Fatal("mivia should have tools")
	}
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
	// Root mivia omits skills → unrestricted (all trusted).
	if mivia.Skills != nil {
		t.Fatalf("mivia should omit skills (all trusted), got %#v", mivia.Skills)
	}
}
