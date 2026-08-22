package clichat

// E-B: every tool-bearing subagent surface carries the shared child-side
// messaging protocol block (subagents.MessagingProtocolPrompt). Routed agents,
// their skill-activated surfaces, and the plain multi_step handler all get it
// appended to the system prompt the model sees. Oneshot/delegate are
// deliberately excluded: they have no post_message tool to teach.

import (
	"context"
	"encoding/json"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// messagingProtocolMarker is the distinctive heading of the child-side
// messaging protocol block. Used for the negative assertions, where matching
// the whole block is both over- and under-constraining.
const messagingProtocolMarker = "## Agent messaging"

// newPromptProbeHandler builds an agentTaskHandler for surface-prompt tests.
// The full registry is a default tool registry so prepareInvokeSurface can
// scope and baseline-inject exactly as production does.
func newPromptProbeHandler(t *testing.T, definition agents.ResolvedAgent, skillReg *skills.Registry) *agentTaskHandler {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	return newAgentTaskHandler(definition, digest,
		tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}), runtime.New(runtime.Policy{}),
		SessionDispatcherOpts{
			Completer: &mockDelegateCompleter{name: "test", response: `{"ok":true}`},
			Model:     "model", Config: config.DefaultSubagentConfig, SkillReg: skillReg,
		})
}

// TestRoutedAgentPromptIncludesProtocol pins the routed-agent (plain) surface:
// prepareInvokeSurface must append the messaging protocol block to the
// definition's own system prompt, once, without losing the agent prompt.
func TestRoutedAgentPromptIncludesProtocol(t *testing.T) {
	definition := agents.ResolvedAgent{
		Name:           "reviewer",
		EffectiveTools: []string{"read_file"},
		SystemPrompt:   "You are the reviewer agent.\nBe strict and cite evidence.",
	}
	handler := newPromptProbeHandler(t, definition, nil)

	prompt, _, _, closeAct, err := handler.prepareInvokeSurface(runtime.Request{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAct()

	if !strings.Contains(prompt, "You are the reviewer agent.") {
		t.Fatalf("routed-agent prompt lost the definition's system prompt: %q", prompt)
	}
	if !strings.Contains(prompt, subagents.MessagingProtocolPrompt) {
		t.Fatalf("routed-agent prompt must carry the messaging protocol block: %q", prompt)
	}
	if strings.Count(prompt, messagingProtocolMarker) != 1 {
		t.Fatalf("messaging protocol block must land exactly once, got %d occurrences in %q",
			strings.Count(prompt, messagingProtocolMarker), prompt)
	}
}

// TestSkillAgentPromptIncludesProtocol pins the skill-activated surface: the
// skill's instructions replace the agent prompt, so the protocol block must be
// appended to the skill prompt (final prompt = skill instructions + block).
func TestSkillAgentPromptIncludesProtocol(t *testing.T) {
	skillReg := schemaSkillRegistry(t, "")
	definition := agents.ResolvedAgent{
		Name:           "reviewer",
		EffectiveTools: []string{},
		SystemPrompt:   "You are the reviewer agent.",
	}
	handler := newPromptProbeHandler(t, definition, skillReg)

	prompt, _, _, closeAct, err := handler.prepareInvokeSurface(runtime.Request{Skill: "review"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAct()

	if !strings.Contains(prompt, "Review the change.") {
		t.Fatalf("skill-activated prompt lost the skill instructions: %q", prompt)
	}
	if !strings.Contains(prompt, subagents.MessagingProtocolPrompt) {
		t.Fatalf("skill-activated prompt must carry the messaging protocol block: %q", prompt)
	}
	if strings.Count(prompt, messagingProtocolMarker) != 1 {
		t.Fatalf("messaging protocol block must land exactly once, got %d occurrences in %q",
			strings.Count(prompt, messagingProtocolMarker), prompt)
	}
}

// TestPlainMultiStepPromptIncludesProtocol pins the plain multi_step handler
// registered by registerMultiStepHandler: its system prompt must carry the
// protocol block (and keep the MultiStepSystemPrompt base).
func TestPlainMultiStepPromptIncludesProtocol(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	d, err := newSessionDispatcherMinimal(
		tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}), completer, "model",
		config.DefaultSubagentConfig, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	result := d.Invoke(context.Background(), runtime.Request{
		ID: "multi-prompt", Kind: runtime.Subagent, Name: cliorchestrate.HandlerMultiStep,
		Input: json.RawMessage(`"do the work"`), SessionID: "test",
	})
	if result.Err != nil {
		t.Fatalf("invoke multi_step: %v", result.Err)
	}
	_, prompts := completer.requests()
	if len(prompts) == 0 {
		t.Fatal("multi_step issued no provider request")
	}
	if !strings.Contains(prompts[0], "You are a focused sub-agent with access to tools") {
		t.Fatalf("plain multi_step lost its base system prompt: %q", prompts[0])
	}
	if !strings.Contains(prompts[0], subagents.MessagingProtocolPrompt) {
		t.Fatalf("plain multi_step system prompt must carry the messaging protocol block: %q", prompts[0])
	}
}

// TestOneshotDelegateDoNotIncludeProtocol pins the exclusion: oneshot and
// delegate have no post_message tool, so their system prompts must NOT carry
// the messaging protocol block.
func TestOneshotDelegateDoNotIncludeProtocol(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	d, err := newSessionDispatcherMinimal(
		tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}), completer, "model",
		config.DefaultSubagentConfig, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	for _, name := range []string{cliorchestrate.HandlerOneshot, cliorchestrate.HandlerDelegate} {
		result := d.Invoke(context.Background(), runtime.Request{
			ID: "oneshot-" + name, Kind: runtime.Subagent, Name: name,
			Input: json.RawMessage(`"do the work"`), SessionID: "test",
		})
		if result.Err != nil {
			t.Fatalf("invoke %s: %v", name, result.Err)
		}
	}
	_, prompts := completer.requests()
	if len(prompts) != 2 {
		t.Fatalf("expected 2 provider requests (oneshot, delegate), got %d", len(prompts))
	}
	for _, p := range prompts {
		if strings.Contains(p, messagingProtocolMarker) {
			t.Fatalf("oneshot/delegate prompt must NOT carry the messaging protocol block: %q", p)
		}
	}
}

// TestSkillSubagentPromptIncludesProtocol pins the direct-skill surface: a
// registered skill subagent invoked BY NAME (the registerSkillHandlers path,
// not the routed-agent prepareInvokeSurface path) must see the messaging
// protocol block appended to its system prompt exactly once, with the skill
// instructions intact.
func TestSkillSubagentPromptIncludesProtocol(t *testing.T) {
	skillReg := schemaSkillRegistry(t, "")
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	d, err := newSessionDispatcherMinimal(
		tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}), completer, "model",
		config.DefaultSubagentConfig, 0, skillReg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	result := d.Invoke(context.Background(), runtime.Request{
		ID: "skill-prompt", Kind: runtime.Subagent, Name: "review",
		Input: json.RawMessage(`"do the work"`), SessionID: "test",
	})
	if result.Err != nil {
		t.Fatalf("invoke skill subagent: %v", result.Err)
	}
	_, prompts := completer.requests()
	if len(prompts) == 0 {
		t.Fatal("skill subagent issued no provider request")
	}
	prompt := prompts[0]
	if !strings.Contains(prompt, "Review the change.") {
		t.Fatalf("skill subagent prompt lost the skill instructions: %q", prompt)
	}
	if !strings.Contains(prompt, subagents.MessagingProtocolPrompt) {
		t.Fatalf("skill subagent prompt must carry the messaging protocol block: %q", prompt)
	}
	if strings.Count(prompt, messagingProtocolMarker) != 1 {
		t.Fatalf("messaging protocol block must land exactly once, got %d occurrences in %q",
			strings.Count(prompt, messagingProtocolMarker), prompt)
	}
}

// TestSkillResourceSubagentPromptIncludesProtocol pins the resource-skill
// surface: a registered resource-declaring skill invoked BY NAME routes
// through activatedSkillHandler, which replaces the template prompt with the
// activation prompt. That replacement must still carry the messaging protocol
// block exactly once, alongside the bounded resource catalogue.
func TestSkillResourceSubagentPromptIncludesProtocol(t *testing.T) {
	skillReg := resourceSkillRegistry(t)
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	d, err := newSessionDispatcherMinimal(
		tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}), completer, "model",
		config.DefaultSubagentConfig, 0, skillReg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	result := d.Invoke(context.Background(), runtime.Request{
		ID: "skill-resource-prompt", Kind: runtime.Subagent, Name: "review",
		Input: json.RawMessage(`"do the work"`), SessionID: "test",
	})
	if result.Err != nil {
		t.Fatalf("invoke resource skill subagent: %v", result.Err)
	}
	_, prompts := completer.requests()
	if len(prompts) == 0 {
		t.Fatal("resource skill subagent issued no provider request")
	}
	prompt := prompts[0]
	if !strings.Contains(prompt, "<skill-resources>") {
		t.Fatalf("resource skill prompt lost the activation resource catalogue: %q", prompt)
	}
	if !strings.Contains(prompt, subagents.MessagingProtocolPrompt) {
		t.Fatalf("resource skill subagent prompt must carry the messaging protocol block: %q", prompt)
	}
	if strings.Count(prompt, messagingProtocolMarker) != 1 {
		t.Fatalf("messaging protocol block must land exactly once, got %d occurrences in %q",
			strings.Count(prompt, messagingProtocolMarker), prompt)
	}
}
