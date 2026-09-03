package clichat

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// prepareInvokeSurface returns the system prompt, the rendered core-memory
// context frame (empty when there is none), the scoped registry, and an
// activation closer. The memory frame is returned separately - never composed
// into the prompt - so the subagent loop can deliver it as its own user-role
// message right after the system message. That keeps the system prompt
// byte-stable across memory promotions, so a memory change no longer
// invalidates the provider's cached prompt prefix (system + tools); it only
// changes the message at index 1, which is stable within one invocation
// anyway.
func (h *agentTaskHandler) prepareInvokeSurface(req runtime.Request) (string, string, *tools.Registry, func(), error) {
	if h.opts.EnsureMCPTools != nil {
		if err := h.opts.EnsureMCPTools(h.definition.EffectiveMCPServers); err != nil {
			return "", "", nil, func() {}, fmt.Errorf("MCP tools: %w", err)
		}
	}
	systemPrompt := h.definition.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = h.opts.Config.SystemPrompt
	}
	if systemPrompt == "" {
		systemPrompt = subagents.MultiStepSystemPrompt
	}
	// This surface passes no ExtraDenylist, so ScopeSpawned's own denial check
	// sees only the COMPILED list - an operator's additions are not in it. The
	// allowlist is therefore the only place their guardrail can apply here,
	// and AuthorizedAgentTools applies it from the agent's own
	// EffectiveDenylist. That matters most on this path: EnsureMCPTools above
	// has just merged THIS subagent's MCP server tools into the authority
	// registry, after root scope already ran, so nothing upstream has seen
	// them.
	registry := tools.ScopedRegistry(h.full, tools.ScopeOptions{
		Mode: tools.ScopeSpawned, Allowlist: agents.AllowlistSet(cliagents.AuthorizedAgentTools(&h.definition, h.full)),
	})
	// Baseline messaging: inject post_message after allowlist filter unless
	// the agent opted out via disallowed_tools = ["post_message"] (plan 53.02).
	// tools_remove alone does not opt out — resolve maps messaging opt-out
	// through DisallowedTools when agents list disallowed_tools.
	disallowed := messagingDisallowed(h.definition)
	injectBaselineMessaging(h.full, registry, h.opts.Config, disallowed)
	noop := func() {}
	// The resolved output schema must outrank skill report-shape text: skill
	// instructions replace the agent system prompt, and without the schema in
	// the system prompt a skill that demands its own report format wins over
	// the workflow step's output contract (observed as schema_violation runs).
	// The user-turn appendix stays too, but the system prompt is authoritative.
	schemaBlock := schemaSystemAppendix(h.resolveOutputSchema(req))
	// The core-memory block rides in its own message (D1c's ordering
	// concern - keeping the messaging-protocol/schema tail closest to the
	// prompt's end - is moot now that the block never enters the prompt).
	memoryContext := chat.MemoryContextContent(cliagents.CoreMemoryBlockForOpts(h.opts))
	if req.Skill == "" {
		return withReportBudget(withMessagingProtocol(systemPrompt), false) + schemaBlock, memoryContext, registry, noop, nil
	}
	scoped, prompt, closeActivation, err := h.activateSkill(req.Skill, registry)
	if err != nil {
		return "", "", nil, noop, err
	}
	injectBaselineMessaging(h.full, scoped, h.opts.Config, disallowed)
	// The skill's instructions replace the agent prompt, so the protocol block
	// is appended to the skill-activated prompt instead of the resolved one.
	// This keeps the child-side messaging contract in-context exactly once.
	return withReportBudget(withMessagingProtocol(prompt), false) + schemaBlock, memoryContext, scoped, closeActivation, nil
}

// resolveOutputSchema returns the output schema that will actually be enforced
// for this invocation: task-level overrides skill, skill overrides the agent
// definition. Only nil means "no schema": an empty object {} is a real schema
// and must be enforced as declared.
func (h *agentTaskHandler) resolveOutputSchema(req runtime.Request) map[string]any {
	out := req.OutputSchema
	if out == nil && req.Skill != "" && h.opts.SkillReg != nil {
		if sk, ok := h.opts.SkillReg.Get(req.Skill); ok && sk.OutputSchema != nil {
			out = sk.OutputSchema
		}
	}
	if out == nil {
		out = h.definition.OutputSchema
	}
	return out
}

// schemaSystemAppendix is the deterministic system-prompt block stating the
// output contract. It mirrors the user-turn PromptAppendix wording so both
// surfaces demand the same shape. Both renderers show the model-facing
// contract (meta-keywords stripped, never-echo instruction, compact example),
// never the raw schema document: a verbatim document invites the model to
// echo it back as its answer. A nil schema (no contract) emits nothing:
// json.Marshal of a nil map would otherwise produce a bogus "null" block.
func schemaSystemAppendix(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	contract := jschema.ModelSchemaContract(schema)
	if contract == "" {
		return ""
	}
	return jschema.EnvelopeAppendixBody(contract)
}

// messagingDisallowed is the opt-out set for the baseline messaging
// injection: names that must NOT be re-added to an already-scoped registry.
//
// It takes the AGENT rather than a name list on purpose. Both call sites used
// to pass agent.DisallowedTools - the agent file's own list - so an
// operator's mandatory_tool_denylist entry for post_message was stripped by
// applyToolPolicy, excluded by AuthorizedAgentTools, dropped by
// ScopedRegistry, and then put straight back by the injection. Taking the
// agent means there is no second list a caller can reach for:
// EffectiveDenylist carries the agent's own denials AND the operator's.
func messagingDisallowed(agent agents.ResolvedAgent) map[string]struct{} {
	out := map[string]struct{}{}
	for _, name := range agent.EffectiveDenylist {
		out[name] = struct{}{}
	}
	// DisallowedTools is a subset of EffectiveDenylist as resolve builds it;
	// included explicitly so a hand-built ResolvedAgent that sets only the
	// former still opts out rather than silently gaining post_message.
	for _, name := range agent.DisallowedTools {
		out[name] = struct{}{}
	}
	return out
}

// withMessagingProtocol appends the shared child-side messaging protocol block
// to a tool-bearing subagent's system prompt. Every surface that can call
// post_message must carry the kinds/question/answer semantics, the no_answer
// contract, and the <parent-message> anti-injection rule, so the block is
// appended exactly once per invocation after prompt resolution and before the
// prompt reaches the loop.
func withMessagingProtocol(prompt string) string {
	return prompt + "\n\n" + subagents.MessagingProtocolPrompt
}

// withReportBudget appends the harness-injected final-report budget block to a
// subagent system prompt. allowOverflow marks a surface with no store_note
// tool (oneshot/delegate): it cannot park overflow detail in the ledger, so it
// gets the budget variant without the store_note instruction. Tool-bearing
// surfaces pass false and carry the full variant. Callers must place the block
// before the output-schema appendix, so the schema contract stays last.
func withReportBudget(prompt string, allowOverflow bool) string {
	if allowOverflow {
		return prompt + "\n\n" + subagents.ReportBudgetPromptNoTool
	}
	return prompt + "\n\n" + subagents.ReportBudgetPrompt
}

// activateSkill checks that this agent may invoke the named skill and derives
// the skill's prompt and (when it declares resources) a registry carrying the
// scoped resource reader. The returned closer releases the activation and must
// be deferred by the caller for the lifetime of the run.
//
// When the invocation runs under a workflow admission (WorkflowSkillSnapshots
// is set), the EXECUTED content comes from the pinned admission bytes, never
// from the live skill source: the definition is hydrated from the pin and its
// resources are served from the pinned snapshots in memory (R1). The registry
// definition is still resolved, because admission (is this skill pinned for
// this run?) and authorization (may this agent invoke it?) are live host-side
// policy checks; only the executed bytes are pinned.
func (h *agentTaskHandler) activateSkill(name string, registry *tools.Registry) (*tools.Registry, string, func(), error) {
	noop := func() {}
	if h.opts.SkillReg == nil {
		if h.opts.WorkflowSkillSnapshots != nil {
			return nil, "", noop, workflowSkillResumeErrorf(name, fmt.Sprintf("is not authorized for agent %q", h.definition.Name))
		}
		return nil, "", noop, fmt.Errorf("agent %q may not invoke skill %q", h.definition.Name, name)
	}
	skill, ok := h.opts.SkillReg.Get(name)
	if !ok {
		if h.opts.WorkflowSkillSnapshots != nil {
			return nil, "", noop, workflowSkillResumeErrorf(name, "is not declared")
		}
		return nil, "", noop, fmt.Errorf("unknown skill %q", name)
	}
	exec := skill
	var pinnedResources []skills.ResourceSnapshot
	pinnedRun := false
	if snapshots := h.opts.WorkflowSkillSnapshots; snapshots != nil {
		pinned, ok := snapshots[name]
		if !ok {
			return nil, "", noop, workflowSkillResumeErrorf(name, "is not admitted")
		}
		hydrated, resources, err := cliworkflow.HydrateWorkflowSkillSnapshot(name, pinned)
		if err != nil {
			return nil, "", noop, err
		}
		exec, pinnedResources, pinnedRun = hydrated, resources, true
	}
	if err := cliagents.SkillScopeFromAgentAndRegistry(&h.definition, h.full).CheckSkillDefinition(skill); err != nil {
		return nil, "", noop, err
	}
	systemPrompt := exec.Instructions
	closeActivation := noop
	if len(exec.Resources) > 0 {
		var activation *skills.SkillActivation
		var err error
		if pinnedRun {
			activation, err = skills.ActivateSnapshot(exec, pinnedResources)
		} else {
			activation, err = skill.Activate()
		}
		if err != nil {
			return nil, "", noop, err
		}
		closeActivation = func() { activation.Close() }
		registry, err = InjectSkillResourceTool(registry, activation)
		if err != nil {
			closeActivation()
			return nil, "", noop, err
		}
		systemPrompt = activation.Prompt(true)
	}
	if strings.TrimSpace(exec.Description) != "" {
		systemPrompt = exec.Description + "\n\n" + systemPrompt
	}
	return registry, systemPrompt, closeActivation, nil
}
