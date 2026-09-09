package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
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
		"agent-creator", "architecture-review", "bug-audit", "capture", "concurrency-review",
		"delivery",
		"docs-maintenance", "docs-update", "fast-bug-audit", "feature-delivery",
		"gate-authoring",
		"memories-housekeeping",
		"logic-review",
		"panel-architecture-review",
		"panel-bug-audit", "panel-secure-change", "performance-review",
		"review",
		"review-synthesis",
		"secure-change", "simplification-review", "skill-creator",
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
		requireThisRepo(t)
		t.Skip("committed skills not present at", dir)
	}
	return dir
}

// requireThisRepo fails (not skips) when the working tree IS the mivia-agent
// module but .agents/skills or .agents/agents has gone missing. A deleted
// tree the loader glob then walks is empty, which reads as zero warnings and
// zero violations - the same "absent directory reads as a pass" defect
// verify_skill_tree.check_skill_dir documents and guards against. Skipping
// here would let the same class through on the Go side.
func requireThisRepo(t *testing.T) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	goMod := filepath.Join(cwd, "..", "..", "go.mod")
	body, err := os.ReadFile(goMod)
	if err != nil {
		return // no go.mod two levels up: genuinely a foreign checkout
	}
	if strings.Contains(string(body), "module github.com/MiviaLabs/mivia-agent") {
		t.Fatalf(
			"this IS the mivia-agent module (%s), but the committed .agents "+
				"tree is missing. That is not a foreign checkout to skip past.",
			goMod,
		)
	}
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
		requireThisRepo(t)
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
	// A nil Skills key admits EVERY skill (SkillAllowed returns true), so
	// skipping those agents left five of sixteen committed roles and 52
	// admitted pairs unverified. Pin the refusals instead: each one is a
	// dispatch that fails closed at CheckSkillInvocation, and the set must
	// change only on purpose. A role that deliberately cannot write files or
	// run commands is expected here; a NEW entry means a skill just became
	// unreachable for that role.
	assertNilAllowlistRefusals(t, reg, skillReg)

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

// wantNilAllowlistRefusals maps each committed role with no skills: key to the
// skills its effective tools cannot cover, as "<skill>(<first missing tool>)".
// These roles admit every skill by policy, so the tool superset is the only
// thing standing between them and a runtime refusal.
var wantNilAllowlistRefusals = map[string][]string{
	"builder": {
		"memories-housekeeping(delete_file)",
		"workflow-feature-delivery(multi_edit)",
		"workflow-runs-analysis(workflow_list_runs)",
	},
	"e2e-engineer": {
		"agent-creator(run_command)",
		"feature-delivery(run_command)",
		"gate-authoring(run_command)",
		"performance-review(run_command)",
		"session-analysis(run_command)",
		"skill-creator(run_command)",
		"verify-change(run_command)",
		"verify-code-change(run_command)",
		"workflow-runs-analysis(workflow_list_runs)",
	},
	"panel-reviewer": reviewerRefusals,
	"plan-reviewer":  reviewerRefusals,
	"planner":        reviewerRefusals,
}

// The three read-only review roles hold no write or exec tools on purpose, so
// they share one refusal set.
var reviewerRefusals = []string{
	"agent-creator(write_file)",
	"capture(write_file)",
	"docs-maintenance(write_file)",
	"docs-update(write_file)",
	"feature-delivery(write_file)",
	"gate-authoring(run_command)",
	"memories-housekeeping(write_file)",
	"performance-review(run_command)",
	"session-analysis(run_command)",
	"skill-creator(write_file)",
	"verify-change(run_command)",
	"verify-code-change(run_command)",
	"workflow-feature-delivery(write_file)",
	"workflow-runs-analysis(workflow_list_runs)",
}

func assertNilAllowlistRefusals(t *testing.T, reg *AgentRegistry, skillReg *skills.Registry) {
	t.Helper()
	seen := make(map[string]bool)
	for _, agent := range reg.List() {
		if agent.Skills != nil {
			continue
		}
		seen[agent.Name] = true
		var refused []string
		for _, def := range skillReg.List() {
			if missing := firstMissingSkillTool(&agent, def.Tools); missing != "" {
				refused = append(refused, fmt.Sprintf("%s(%s)", def.Name, missing))
			}
		}
		sort.Strings(refused)
		want, ok := wantNilAllowlistRefusals[agent.Name]
		if !ok {
			t.Fatalf("agent %q has no skills: key and is not pinned in wantNilAllowlistRefusals; it admits every skill, so its refusals must be recorded. Observed: %v", agent.Name, refused)
		}
		sorted := append([]string(nil), want...)
		sort.Strings(sorted)
		if !slices.Equal(refused, sorted) {
			t.Fatalf("agent %q nil-allowlist refusals drifted.\n got: %v\nwant: %v\nA new entry means a skill became unreachable for this role; a removed one means it gained a tool. Update the pin only when the change is intended.", agent.Name, refused, sorted)
		}
	}
	for name := range wantNilAllowlistRefusals {
		if !seen[name] {
			t.Fatalf("wantNilAllowlistRefusals pins %q, which is not a committed role with a nil skills: key. A stale pin covers nothing.", name)
		}
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
