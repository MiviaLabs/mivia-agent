package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Ensures the committed repo agent definitions under .agents/agents/ still parse
// and resolve. Skips if the workspace is not the mivia-agent tree.
func TestProjectAgentDefinitionsResolve(t *testing.T) {
	inputs := committedAgentInputs(t)
	wantNames := map[string]bool{
		"auditor":            true,
		"builder":            true,
		"docs":               true,
		"e2e-engineer":       true,
		"go-engineer":        true,
		"memory-curator":     true,
		"panel-reviewer":     true,
		"performance":        true,
		"plan-reviewer":      true,
		"planner":            true,
		"researcher":         true,
		"review-synthesizer": true,
		"reviewer":           true,
		"security":           true,
		"verifier":           true,
		"workflow-engineer":  true,
	}
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		seen[input.Name] = true
	}
	if len(seen) != len(wantNames) {
		t.Fatalf("agent roster names = %v, want exactly %v", seen, wantNames)
	}
	for name := range wantNames {
		if !seen[name] {
			t.Fatalf("required agent %q is missing from roster", name)
		}
	}
	reg, _, err := ResolveAll(inputs, ResolveOptions{
		Global: config.AgentsGlobal{FailOnEmptyToolset: true, LoadWorkspaceConfig: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// No assertMiviaAgent: this repo ships no root-agent override. The root
	// session runs the compiled fallback prompt, whose skill policy is
	// unrestricted by construction (no committed definition to restrict it).
	assertGoEngineerAgent(t, reg)
	assertSpecialistScopes(t, reg)
	assertAgentPromptsArePortable(t, inputs)
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
	for _, name := range []string{"auditor", "performance"} {
		a, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if a.Skills == nil || len(*a.Skills) == 0 {
			t.Fatalf("%s must declare skills", name)
		}
		if !hasTool(a.EffectiveTools, "run_command") {
			t.Fatalf("%s must have run_command for reproduction/measurement: %v", name, a.EffectiveTools)
		}
		for _, forbidden := range []string{"write_file", "search_replace"} {
			if hasTool(a.EffectiveTools, forbidden) {
				t.Fatalf("%s unexpectedly has %s: %v", name, forbidden, a.EffectiveTools)
			}
		}
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

// TestCommittedSkillsDeclareValidTools pins plan 43 phase 1: every checked-in
// skill under .agents/skills must declare explicit, minimal static tool
// requirements, and every declared name must be in the declared-tool catalogue
// (which excludes the activation-only read_skill_resource). This is what makes
// the agent/skill tools-superset contract non-vacuous. The committed catalogue
// must load without warnings: a removed tools: key, an unknown tool name, or a
// duplicate frontmatter key must fail this test.
func TestCommittedSkillsDeclareValidTools(t *testing.T) {
	dir := committedSkillsDir(t)
	reg, warnings, err := skills.LoadMarkdownSources(
		[]skills.Source{{Dir: dir, Origin: skills.OriginProject}},
		skills.LoadOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("committed skills must load without warnings, got: %v", warnings)
	}
	wantNames := []string{
		"architecture-review", "bug-audit", "capture", "concurrency-review",
		"delivery",
		"docs-maintenance", "docs-update", "fast-bug-audit", "feature-delivery",
		"housekeeping",
		"logic-review",
		"memory-housekeeping", "panel-architecture-review",
		"panel-bug-audit", "panel-secure-change", "performance-review",
		"review",
		"review-synthesis",
		"secure-change", "simplification-review",
		"session-analysis", "test-review",
		"verify-change", "verify-code-change", "workflow-feature-delivery",
		"workflow-runs-analysis",
	}
	got := make(map[string]bool)
	for _, def := range reg.List() {
		got[def.Name] = true
	}
	if len(got) != len(wantNames) {
		t.Fatalf("committed skill names = %v, want exactly %v", got, wantNames)
	}
	for _, name := range wantNames {
		def, ok := reg.Get(name)
		if !ok {
			t.Fatalf("required skill %q is missing from %s", name, dir)
		}
		if def.Name == "review-synthesis" {
			if def.Tools != nil || len(def.Resources) != 0 {
				t.Fatalf("skill %q must declare no tools or resources", def.Name)
			}
			continue
		}
		if def.Tools == nil {
			t.Fatalf("skill %q omits tools: metadata; the agent/skill contract is vacuous without it", name)
		}
		if len(def.Tools) == 0 {
			t.Fatalf("skill %q declares an empty tools: list; a committed skill must state its requirements", name)
		}
		for _, tool := range def.Tools {
			if !tools.IsDeclaredToolName(tool) {
				t.Fatalf("skill %q declares unknown tool %q (not in declared-tool catalogue)", name, tool)
			}
		}
	}
}

// committedSkillsDir locates the repo's .agents/skills directory from the test
// working directory, mirroring TestProjectAgentDefinitionsResolve. Project
// skills live as real directories under .agents/skills/ and are read
// directly by the loader's os.Root sandbox (which does not follow symlinks).
func committedSkillsDir(t *testing.T) string {
	t.Helper()
	cwd, _ := os.Getwd()
	dir := filepath.Join(cwd, "..", "..", ".agents", "skills")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Skip("committed skills not present at", dir)
	}
	return dir
}

// committedAgentInputs discovers the committed .agents/agents Markdown files into
// ResolveInputs, mirroring TestProjectAgentDefinitionsResolve's discovery.
func committedAgentInputs(t *testing.T) []ResolveInput {
	t.Helper()
	cwd, _ := os.Getwd()
	dir := filepath.Join(cwd, "..", "..", ".agents", "agents")
	if st, err := os.Stat(filepath.Join(dir, "planner.md")); err != nil || !st.IsDir() {
		root, _ := filepath.Abs("../..")
		dir = filepath.Join(root, ".agents", "agents")
	}
	if _, err := os.Stat(filepath.Join(dir, "planner.md")); err != nil {
		t.Skip("project agents not present at", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var inputs []ResolveInput
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" || strings.EqualFold(entry.Name(), "readme.md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		spec, canonical, err := config.ParseAgentFileMarkdown(data, entry.Name())
		if err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		inputs = append(inputs, ResolveInput{
			Name:   canonical,
			Source: config.AgentSourceWorkspace,
			Path:   filepath.Join(dir, entry.Name()),
			Spec:   spec,
		})
	}
	return inputs
}

// TestCommittedRosterSkillCompatibilityMatrix pins plan 43 phase 3: the
// committed agent roster is mechanically compatible with its skill allowlists.
// It loads the same skill registry and committed agent definitions used by
// runtime, then asserts for every explicit agent/skill pairing that the
// allowlist permits the skill AND the agent's final effective tools cover every
// static skill requirement. Every committed skill is either in the matrix or
// owned by the unrestricted root session. Failures name the exact agent,
// skill, and missing tool.
func TestCommittedRosterSkillCompatibilityMatrix(t *testing.T) {
	skillReg, warnings, err := skills.LoadMarkdownSources(
		[]skills.Source{{Dir: committedSkillsDir(t), Origin: skills.OriginProject}},
		skills.LoadOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("committed skills must load without warnings, got: %v", warnings)
	}
	catalogue := make(map[string]SkillCatalogueEntry, len(skillReg.List()))
	for _, def := range skillReg.List() {
		catalogue[def.Name] = SkillCatalogueEntry{Project: true}
	}
	reg, _, err := ResolveAll(committedAgentInputs(t), ResolveOptions{
		Global:             config.AgentsGlobal{FailOnEmptyToolset: true, LoadWorkspaceConfig: true},
		SkillCatalogue:     catalogue,
		AllowProjectSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	covered := make(map[string]bool)
	for _, agent := range reg.List() {
		if agent.Skills == nil {
			continue
		}
		for _, skillName := range *agent.Skills {
			covered[skillName] = true
			if !SkillAllowed(&agent, skillName) {
				t.Fatalf("agent %q allowlist does not permit skill %q", agent.Name, skillName)
			}
			def, ok := skillReg.Get(skillName)
			if !ok {
				t.Fatalf("agent %q allowlists unknown skill %q", agent.Name, skillName)
			}
			if missing := firstMissingSkillTool(&agent, def.Tools); missing != "" {
				t.Fatalf("agent %q skill %q requires tool %q not in effective tools %v",
					agent.Name, skillName, missing, agent.EffectiveTools)
			}
		}
	}
	// This repo ships no committed root-agent override: the root session runs
	// the compiled fallback, whose skill policy is unrestricted by
	// construction (no definition to restrict it). A committed "mivia"
	// definition, if one ever returns, must keep Skills nil for that to hold.
	rootUnrestricted := true
	if root, ok := reg.Get("mivia"); ok {
		rootUnrestricted = root.Skills == nil
	}
	for _, def := range skillReg.List() {
		if covered[def.Name] {
			continue
		}
		if rootUnrestricted {
			continue
		}
		t.Fatalf("committed skill %q is neither allowlisted by any committed agent nor owned by the unrestricted root", def.Name)
	}
}

// firstMissingSkillTool returns the first declared skill tool absent from the
// agent's effective tools, or "" when every requirement is covered.
func firstMissingSkillTool(agent *ResolvedAgent, skillTools []string) string {
	if agent == nil {
		return ""
	}
	have := make(map[string]struct{}, len(agent.EffectiveTools))
	for _, n := range agent.EffectiveTools {
		have[n] = struct{}{}
	}
	for _, n := range skillTools {
		if _, ok := have[n]; !ok {
			return n
		}
	}
	return ""
}
