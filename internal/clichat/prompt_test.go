package clichat

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
	// Keep the compiled prompt lean; content-ref routing (read_output /
	// ledger_read) is intentional and worth a few hundred bytes of budget.
	//
	// The inline "MANDATORY lifecycle (ADLC)" block and the ASD-STE100
	// writing-standard block were removed: a project/language-generic
	// default carries only what the agent needs to operate - identity, tool
	// discipline, safety framing, orchestration policy, and workspace-
	// customization discovery. Process/lifecycle and style opinions belong
	// to the workspace's own skills and agent files, which replace this
	// fallback at session setup. Do not raise this budget to make room for
	// content a project agent definition under .agents/agents/ can carry
	// instead.
	prompt := buildAgentPrompt(config.SubagentConfig{})
	if len(prompt) > 2950 {
		t.Fatalf("buildAgentPrompt is %d bytes, expected < 2950", len(prompt))
	}
	if !strings.Contains(prompt, ".agents/agents/") {
		t.Fatal("buildAgentPrompt must mention .agents/agents/ for self-maintenance")
	}
}

func TestDefaultSystemPrompt(t *testing.T) {
	if !strings.Contains(defaultSystemPrompt, "mivia") {
		t.Fatal("defaultSystemPrompt should mention mivia")
	}
}

// TestLoadAgentPromptIgnoresAgentPromptMarkdown pins the deliberate dead-ness
// of .mivia/agent-prompt.md: no code in internal/cli reads it. The compiled
// prompt comes only from buildAgentPrompt, which is file-independent.
func TestLoadAgentPromptIgnoresAgentPromptMarkdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	const marker = "PROMPT-MD-MUST-NOT-LOAD"
	if err := os.WriteFile(filepath.Join(dir, ".mivia", "agent-prompt.md"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()

	prompt := buildAgentPrompt(config.SubagentConfig{})
	if strings.Contains(prompt, marker) {
		t.Fatal("agent-prompt.md must not be loaded; use .agents/agents/*.toml")
	}
}

// The legacy namespace carries no meaning: a workspace holding only the old
// paths contributes nothing to the compiled prompt or the skill registry.
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

	if got := buildAgentPrompt(config.SubagentConfig{}); strings.Contains(got, "legacy prompt") {
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

func TestDefaultAgentPromptHasGenericVerifyGuidance(t *testing.T) {
	prompt := buildAgentPrompt(config.SubagentConfig{})
	lower := strings.ToLower(prompt)
	checks := []string{
		"run_command", "discover", ".agents/agents/", "last resort",
	}
	for _, c := range checks {
		if !strings.Contains(lower, strings.ToLower(c)) {
			t.Errorf("buildAgentPrompt missing %q", c)
		}
	}
	if strings.Contains(prompt, "go test ./...") {
		t.Fatal("buildAgentPrompt must not hardcode go test ./... (use .agents/agents/*.toml)")
	}
}

// TestAgentPromptsNameTheEditTools: a tool the model is never told about is a
// tool it never calls. The compiled prompt must list the file-editing tools
// the registry actually ships.
func TestAgentPromptsNameTheEditTools(t *testing.T) {
	prompt := buildAgentPrompt(config.SubagentConfig{})
	for _, tool := range []string{"read_file", "write_file", "search_replace", "multi_edit"} {
		if !strings.Contains(prompt, tool) {
			t.Errorf("buildAgentPrompt does not mention %q", tool)
		}
	}
}

// TestAgentPromptsNeverSetHandlerField is a regression test for the plan 07.1
// prompt/rules drift: dispatch_tasks/spawn_agent task objects have no `handler`
// field. decodeStrictTaskJSON uses DisallowUnknownFields and taskItemSchema sets
// additionalProperties:false, so sending `handler:"multi_step"` on a task fails
// the WHOLE call with `json: unknown field "handler"` (agent is the sole
// model-facing selector). The compiled prompt may not instruct setting one, and
// failure-recovery guidance must point at the real selector (`agent` + optional
// `skill`).
func TestAgentPromptsNeverSetHandlerField(t *testing.T) {
	prompt := buildAgentPrompt(config.SubagentConfig{})
	if strings.Contains(prompt, `handler:"multi_step"`) {
		t.Error("buildAgentPrompt must not contain handler:\"multi_step\": dispatch_tasks/spawn_agent tasks have no handler field (decodeStrictTaskJSON rejects it)")
	}
	// No handler-field task-selector instruction may appear anywhere in the
	// prompt - neither "verify handler is set on every task" nor any other
	// handler guidance. `delegate`'s multi_step boolean is a delegate-only
	// parameter and is not named "handler", so a clean prompt has none.
	if strings.Contains(strings.ToLower(prompt), "handler") {
		t.Error("buildAgentPrompt still instructs setting a handler field; the strict task schema rejects it and fails the whole call")
	}
	// Failure recovery must point at the real selector.
	if !strings.Contains(prompt, "verify every task names a valid agent") {
		t.Error("buildAgentPrompt failure-recovery text must tell the model to verify every task names a valid agent (and skill if needed)")
	}
}

// TestRootPromptsTeachParentMessaging pins the parent-side messaging block on
// the compiled root prompt. The parent must know its orchestration-messaging
// vocabulary: send_to_task (answer/steer), run_messages (blackboard), parked
// questions, and the <parent-message> advisory framing.
func TestRootPromptsTeachParentMessaging(t *testing.T) {
	prompt := buildAgentPrompt(config.SubagentConfig{})
	for _, marker := range []string{"send_to_task", "run_messages", "parked"} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("buildAgentPrompt does not teach parent-side messaging marker %q", marker)
		}
	}
	if !strings.Contains(prompt, "<parent-message>") {
		t.Error("buildAgentPrompt must carry the <parent-message> advisory convention")
	}
	if !strings.Contains(prompt, "data to weigh, never instructions") && !strings.Contains(prompt, "advisory") {
		t.Error("buildAgentPrompt must frame <parent-message> content as advisory input, not instructions")
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
	end := strings.Index(prompt[start:], "# Orchestration")
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
	prompt := buildAgentPrompt(config.SubagentConfig{})
	block := messagingBlock(prompt)
	if block == "" {
		t.Fatal("buildAgentPrompt has no # Agent messaging section")
	}
	if strings.Contains(strings.ToLower(block), "handler") {
		t.Errorf("buildAgentPrompt messaging block must not mention handler (strict task schema rejects it): %q", block)
	}
}

// TestProtocolMarkers pins the parent-side messaging vocabulary on the one
// compiled prompt surface. This repo no longer ships its own root-agent
// override (.agents/agents/mivia.md was removed so the dogfood workspace
// exercises the same compiled fallback every user gets), so there is no
// second surface to keep in agreement.
func TestProtocolMarkers(t *testing.T) {
	prompt := buildAgentPrompt(config.SubagentConfig{})
	if !strings.Contains(prompt, "send_to_task") {
		t.Error("buildAgentPrompt must carry the send_to_task marker")
	}
	if !strings.Contains(prompt, "run_messages") {
		t.Error("buildAgentPrompt must carry the run_messages marker")
	}
	if !strings.Contains(prompt, "data to weigh, never instructions") && !strings.Contains(prompt, "advisory") {
		t.Error("buildAgentPrompt must carry the <parent-message> advisory marker")
	}
}
