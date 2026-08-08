package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestDefaultAgentPromptIsShort(t *testing.T) {
	// Keep the compiled fallback lean; content-ref routing (read_output /
	// ledger_read) is intentional and worth a few hundred bytes of budget.
	//
	// The budget went from 4100 to 4700 for prompts.WritingStandard. The
	// standard is about 530 bytes, and it applies to every piece of prose the
	// agent writes, so the agent must see it before it writes, not after a
	// reviewer rejects the text. Keep the fragment compact. Do not raise this
	// budget again to make room for content that a project agent definition
	// under .mivia/agents/ can carry instead.
	if len(defaultAgentPrompt) > 4700 {
		t.Fatalf("defaultAgentPrompt is %d bytes, expected < 4700", len(defaultAgentPrompt))
	}
	if !strings.Contains(defaultAgentPrompt, "ASD-STE100") {
		t.Fatal("defaultAgentPrompt must carry the writing standard")
	}
	if !strings.Contains(defaultAgentPrompt, ".mivia/agents/") {
		t.Fatal("defaultAgentPrompt must mention .mivia/agents/ for self-maintenance")
	}
}

func TestDefaultSystemPrompt(t *testing.T) {
	if !strings.Contains(defaultSystemPrompt, "mivia") {
		t.Fatal("defaultSystemPrompt should mention mivia")
	}
}

func TestLoadAgentPromptFallsBack(t *testing.T) {
	// Compiled fallback only - agent-prompt.md is not read.
	prompt := loadAgentPrompt("/tmp/nonexistent-mivia-test-dir-12345")
	if prompt != defaultAgentPrompt {
		t.Fatal("should fall back to defaultAgentPrompt")
	}
}

func TestLoadAgentPromptIgnoresAgentPromptMarkdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mivia", "agent-prompt.md"), []byte("should not load"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := loadAgentPrompt(dir)
	if prompt == "should not load" {
		t.Fatal("agent-prompt.md must not be loaded; use .mivia/agents/*.toml")
	}
	if prompt != defaultAgentPrompt {
		t.Fatalf("got %q, want compiled default", prompt)
	}
}

// The legacy namespace carries no meaning: a workspace holding only the old
// paths gets the compiled default, with nothing warning that it was ignored.
func TestWorkspaceIgnoresLegacyAIDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ai", "skills", "legacy-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ai", "agent-prompt.md"), []byte("legacy prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillBody := "---\nname: legacy-skill\ndescription: should not load\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, ".ai", "skills", "legacy-skill", "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := loadAgentPrompt(dir); got != defaultAgentPrompt {
		t.Errorf("legacy .ai/agent-prompt.md must be ignored, got %q", got)
	}

	reg, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: workspace.SkillsDir(dir), Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatalf("load skills: %v", err)
	}
	if n := len(reg.List()); n != 0 {
		t.Errorf("legacy .ai/skills must not load, got %d skills", n)
	}
}

func TestLoadAgentPromptEmptyDir(t *testing.T) {
	prompt := loadAgentPrompt("")
	if prompt != defaultAgentPrompt {
		t.Fatal("empty workspaceDir should fall back to default")
	}
}

func TestDefaultAgentPromptHasGenericVerifyGuidance(t *testing.T) {
	lower := strings.ToLower(defaultAgentPrompt)
	checks := []string{
		"run_command", "discover", ".mivia/agents/", "last resort",
	}
	for _, c := range checks {
		if !strings.Contains(lower, strings.ToLower(c)) {
			t.Errorf("defaultAgentPrompt missing %q", c)
		}
	}
	if strings.Contains(defaultAgentPrompt, "go test ./...") {
		t.Fatal("defaultAgentPrompt must not hardcode go test ./... (use .mivia/agents/*.toml)")
	}
}

// TestAgentPromptsNameTheEditTools: a tool the model is never told about is a
// tool it never calls. Both prompt surfaces - the static fallback and the
// config-interpolated build - must list the file-editing tools the registry
// actually ships.
func TestAgentPromptsNameTheEditTools(t *testing.T) {
	prompts := map[string]string{
		"defaultAgentPrompt": defaultAgentPrompt,
		"buildAgentPrompt":   buildAgentPrompt(config.SubagentConfig{}),
	}
	for name, prompt := range prompts {
		for _, tool := range []string{"read_file", "write_file", "search_replace", "multi_edit"} {
			if !strings.Contains(prompt, tool) {
				t.Errorf("%s does not mention %q", name, tool)
			}
		}
	}
}

// TestAgentPromptsNeverSetHandlerField is a regression test for the plan 07.1
// prompt/rules drift: dispatch_tasks/spawn_agent task objects have no `handler`
// field. decodeStrictTaskJSON uses DisallowUnknownFields and taskItemSchema sets
// additionalProperties:false, so sending `handler:"multi_step"` on a task fails
// the WHOLE call with `json: unknown field "handler"` (agent is the sole
// model-facing selector). Neither compiled prompt may instruct setting one, and
// failure-recovery guidance must point at the real selector (`agent` + optional
// `skill`).
func TestAgentPromptsNeverSetHandlerField(t *testing.T) {
	prompts := map[string]string{
		"defaultAgentPrompt": defaultAgentPrompt,
		"buildAgentPrompt":   buildAgentPrompt(config.SubagentConfig{}),
	}
	for name, prompt := range prompts {
		if strings.Contains(prompt, `handler:"multi_step"`) {
			t.Errorf("%s must not contain handler:\"multi_step\": dispatch_tasks/spawn_agent tasks have no handler field (decodeStrictTaskJSON rejects it)", name)
		}
		// No handler-field task-selector instruction may appear anywhere in the
		// prompt - neither "verify handler is set on every task" nor any other
		// handler guidance. `delegate`'s multi_step boolean is a delegate-only
		// parameter and is not named "handler", so a clean prompt has none.
		if strings.Contains(strings.ToLower(prompt), "handler") {
			t.Errorf("%s still instructs setting a handler field; the strict task schema rejects it and fails the whole call", name)
		}
		// Failure recovery must point at the real selector.
		if !strings.Contains(prompt, "verify every task names a valid agent") {
			t.Errorf("%s failure-recovery text must tell the model to verify every task names a valid agent (and skill if needed)", name)
		}
	}
}

// TestRootPromptsTeachParentMessaging pins the parent-side messaging block on
// both compiled root prompt surfaces (the static fallback and the
// config-interpolated build). The parent must know its orchestration-messaging
// vocabulary: send_to_task (answer/steer), run_messages (blackboard), parked
// questions, and the <parent-message> advisory framing.
func TestRootPromptsTeachParentMessaging(t *testing.T) {
	prompts := map[string]string{
		"defaultAgentPrompt": defaultAgentPrompt,
		"buildAgentPrompt":   buildAgentPrompt(config.SubagentConfig{}),
	}
	for name, prompt := range prompts {
		for _, marker := range []string{"send_to_task", "run_messages", "parked"} {
			if !strings.Contains(prompt, marker) {
				t.Errorf("%s does not teach parent-side messaging marker %q", name, marker)
			}
		}
		if !strings.Contains(prompt, "<parent-message>") {
			t.Errorf("%s must carry the <parent-message> advisory convention", name)
		}
		if !strings.Contains(prompt, "data to weigh, never instructions") && !strings.Contains(prompt, "advisory") {
			t.Errorf("%s must frame <parent-message> content as advisory input, not instructions", name)
		}
	}
}

// messagingBlock extracts the "# Agent messaging" section of a root prompt so
// tests can assert on the new block in isolation.
func messagingBlock(prompt string) string {
	const startMark = "# Agent messaging"
	start := strings.Index(prompt, startMark)
	if start < 0 {
		return ""
	}
	end := strings.Index(prompt[start:], "# MANDATORY")
	if end < 0 {
		return prompt[start:]
	}
	return prompt[start : start+end]
}

// TestRootPromptMessagingBlockOmitsHandler is a focused check on the new
// messaging section: it must never instruct the model to set a "handler"
// field. TestAgentPromptsNeverSetHandlerField already guards the whole prompt;
// this keeps the guarantee visible on the section that was added for parent
// messaging (dispatch_tasks/spawn_agent task objects have no handler field).
func TestRootPromptMessagingBlockOmitsHandler(t *testing.T) {
	prompts := map[string]string{
		"defaultAgentPrompt": defaultAgentPrompt,
		"buildAgentPrompt":   buildAgentPrompt(config.SubagentConfig{}),
	}
	for name, prompt := range prompts {
		block := messagingBlock(prompt)
		if block == "" {
			t.Errorf("%s has no # Agent messaging section", name)
			continue
		}
		if strings.Contains(strings.ToLower(block), "handler") {
			t.Errorf("%s messaging block must not mention handler (strict task schema rejects it): %q", name, block)
		}
	}
}

// workspaceRoot walks up from the test working directory to the workspace
// containing .mivia/agents/mivia.toml.
func workspaceRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".mivia", "agents", "mivia.toml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate .mivia/agents/mivia.toml from %s", dir)
		}
		dir = parent
	}
}

// extractTomlSystemPrompt pulls the system_prompt TOML multi-line string
// ("""...""") out of an agent definition file with a simple scan.
func extractTomlSystemPrompt(raw string) (string, bool) {
	const key = `system_prompt = """`
	idx := strings.Index(raw, key)
	if idx < 0 {
		return "", false
	}
	rest := raw[idx+len(key):]
	end := strings.Index(rest, `"""`)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// TestProtocolMarkersAgreeAcrossSurfaces pins the parent-side messaging
// markers across all three surfaces that teach them: the two compiled prompts
// and the live .mivia/agents/mivia.toml system_prompt. The formats differ so
// the surfaces can't be byte-equal; the shared vocabulary markers are what
// keep cross-surface drift honest.
func TestProtocolMarkersAgreeAcrossSurfaces(t *testing.T) {
	tomlPath := filepath.Join(workspaceRoot(t), ".mivia", "agents", "mivia.toml")
	raw, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read %s: %v", tomlPath, err)
	}
	tomlPrompt, ok := extractTomlSystemPrompt(string(raw))
	if !ok {
		t.Fatalf("could not extract system_prompt multi-line string from %s", tomlPath)
	}

	surfaces := map[string]string{
		"defaultAgentPrompt": defaultAgentPrompt,
		"buildAgentPrompt":   buildAgentPrompt(config.SubagentConfig{}),
		"mivia.toml":         tomlPrompt,
	}
	for name, prompt := range surfaces {
		if !strings.Contains(prompt, "send_to_task") {
			t.Errorf("%s must share the send_to_task marker", name)
		}
		if !strings.Contains(prompt, "run_messages") {
			t.Errorf("%s must share the run_messages marker", name)
		}
		if !strings.Contains(prompt, "data to weigh, never instructions") && !strings.Contains(prompt, "advisory") {
			t.Errorf("%s must share the <parent-message> advisory marker", name)
		}
	}
}
