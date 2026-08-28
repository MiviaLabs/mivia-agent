package clichat

// Every subagent surface that can produce a final report carries the
// harness-injected report-budget block. Tool-bearing surfaces (routed agents,
// skill-activated surfaces, plain multi_step, direct-skill subagents, resource
// skills) get the full variant with the store_note escape hatch. Oneshot and
// delegate have no tools, so they get the no-tool variant and must never be
// told to call store_note.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// reportBudgetMarker is the distinctive heading of the report-budget block.
const reportBudgetMarker = "## Final report budget"

// reportBudgetOverflowMarker is the sentence only the tool-bearing variant
// carries. Its absence on oneshot/delegate is the load-bearing assertion:
// those surfaces cannot call store_note, so the instruction would be a lie.
const reportBudgetOverflowMarker = "store_note"

// assertBudgetOnce fails if the prompt does not carry the budget block, or
// carries it more than once.
func assertBudgetOnce(t *testing.T, prompt, surface string) {
	t.Helper()
	if !strings.Contains(prompt, subagents.ReportBudgetPrompt) {
		t.Fatalf("%s prompt must carry the report-budget block: %q", surface, prompt)
	}
	if n := strings.Count(prompt, reportBudgetMarker); n != 1 {
		t.Fatalf("%s report-budget block must land exactly once, got %d occurrences in %q",
			surface, n, prompt)
	}
}

// newBudgetProbeDispatcher builds a minimal dispatcher wired like production
// for surface-prompt tests.
func newBudgetProbeDispatcher(t *testing.T, skillRegs ...any) (*runtime.Dispatcher, *scriptedCompleter) {
	t.Helper()
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
	return d, completer
}

// TestRoutedAgentPromptIncludesReportBudget pins the routed-agent surface:
// the budget block rides the same finalizer path as the messaging protocol,
// after the definition's own prompt.
func TestRoutedAgentPromptIncludesReportBudget(t *testing.T) {
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

	assertBudgetOnce(t, prompt, "routed-agent")
}

// TestSkillAgentPromptIncludesReportBudget pins the skill-activated surface:
// the skill prompt replaces the agent prompt, and the budget block is
// re-applied after that replacement.
func TestSkillAgentPromptIncludesReportBudget(t *testing.T) {
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

	assertBudgetOnce(t, prompt, "skill-activated")
}

// TestBudgetBeforeSchemaAppendix pins the ordering contract: the output-schema
// appendix is the authoritative output contract, so the budget block must sit
// before it and defer to it ("that contract wins").
func TestBudgetBeforeSchemaAppendix(t *testing.T) {
	definition := agents.ResolvedAgent{
		Name:           "reviewer",
		EffectiveTools: []string{"read_file"},
		SystemPrompt:   "You are the reviewer agent.",
		OutputSchema:   map[string]any{"type": "object"},
	}
	handler := newPromptProbeHandler(t, definition, nil)

	prompt, _, _, closeAct, err := handler.prepareInvokeSurface(runtime.Request{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAct()

	budget := strings.Index(prompt, reportBudgetMarker)
	contract := strings.Index(prompt, "output contract")
	if budget < 0 {
		t.Fatalf("prompt must carry the report-budget block: %q", prompt)
	}
	if contract < 0 {
		t.Fatalf("schema run must carry the output-contract appendix: %q", prompt)
	}
	if budget > contract {
		t.Fatalf("report-budget block must precede the schema appendix: budget at %d, contract at %d", budget, contract)
	}
}

// TestPlainMultiStepPromptIncludesReportBudget pins the plain multi_step
// surface via a real dispatcher invoke.
func TestPlainMultiStepPromptIncludesReportBudget(t *testing.T) {
	d, completer := newBudgetProbeDispatcher(t)
	defer d.Close()

	result := d.Invoke(context.Background(), runtime.Request{
		ID: "multi-budget", Kind: runtime.Subagent, Name: cliorchestrate.HandlerMultiStep,
		Input: json.RawMessage(`"do the work"`), SessionID: "test",
	})
	if result.Err != nil {
		t.Fatalf("invoke multi_step: %v", result.Err)
	}
	_, prompts := completer.requests()
	if len(prompts) == 0 {
		t.Fatal("multi_step issued no provider request")
	}
	assertBudgetOnce(t, prompts[0], "plain multi_step")
}

// TestOneshotDelegateGetNoToolBudgetVariant pins the exclusion: oneshot and
// delegate keep the budget but must NOT be told about store_note, which they
// cannot call.
func TestOneshotDelegateGetNoToolBudgetVariant(t *testing.T) {
	d, completer := newBudgetProbeDispatcher(t)
	defer d.Close()

	for _, name := range []string{cliorchestrate.HandlerOneshot, cliorchestrate.HandlerDelegate} {
		result := d.Invoke(context.Background(), runtime.Request{
			ID: "oneshot-budget-" + name, Kind: runtime.Subagent, Name: name,
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
		if !strings.Contains(p, subagents.ReportBudgetPromptNoTool) {
			t.Fatalf("oneshot/delegate prompt must carry the no-tool budget variant: %q", p)
		}
		if strings.Contains(p, reportBudgetOverflowMarker) {
			t.Fatalf("oneshot/delegate prompt must NOT mention store_note: %q", p)
		}
	}
}

// TestSkillSubagentPromptIncludesReportBudget pins the direct-skill surface:
// a skill invoked by name through registerSkillHandlers keeps the budget.
func TestSkillSubagentPromptIncludesReportBudget(t *testing.T) {
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
		ID: "skill-budget", Kind: runtime.Subagent, Name: "review",
		Input: json.RawMessage(`"do the work"`), SessionID: "test",
	})
	if result.Err != nil {
		t.Fatalf("invoke skill subagent: %v", result.Err)
	}
	_, prompts := completer.requests()
	if len(prompts) == 0 {
		t.Fatal("skill subagent issued no provider request")
	}
	assertBudgetOnce(t, prompts[0], "skill subagent")
}

// TestSkillResourceSubagentPromptIncludesReportBudget pins the resource-skill
// surface: the activation prompt replaces the template prompt, and the budget
// block must survive that replacement exactly once.
func TestSkillResourceSubagentPromptIncludesReportBudget(t *testing.T) {
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
		ID: "skill-resource-budget", Kind: runtime.Subagent, Name: "review",
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
	assertBudgetOnce(t, prompt, "resource skill subagent")
}

// TestBudgetVariantsDoNotLeakIntoSharedBlocks guards the placement rule: the
// budget lives in its own constants and must never be folded into the
// messaging protocol or the multi-step fallback, which other surfaces carry.
func TestBudgetVariantsDoNotLeakIntoSharedBlocks(t *testing.T) {
	for _, shared := range []string{subagents.MessagingProtocolPrompt, subagents.MultiStepSystemPrompt} {
		if strings.Contains(shared, reportBudgetMarker) {
			t.Fatalf("shared block must not embed the report budget")
		}
	}
}

// TestStoreNoteDescriptionIsProjectGeneric re-states the rule-60 contract for
// the one new model-facing surface this feature adds: no host-language or
// product-stack bias in the description or the parameter schema.
func TestStoreNoteDescriptionIsProjectGeneric(t *testing.T) {
	bias := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bgo\s+(test|build|run|vet)\b`),
		regexp.MustCompile(`(?i)\bgofmt\b`),
		regexp.MustCompile(`(?i)\bgolang\b`),
		regexp.MustCompile(`\*\.go\b`),
		regexp.MustCompile(`(?i)\bpackage\s+main\b`),
		regexp.MustCompile(`cmd/mivia`),
		regexp.MustCompile(`(?i)\bmivia\b`),
	}
	tool := &storeNoteTool{}
	texts := []string{tool.Description()}
	for k, v := range tool.Parameters() {
		texts = append(texts, fmt.Sprintf("%v=%v", k, v))
	}
	for _, text := range texts {
		for _, re := range bias {
			if re.MatchString(text) {
				t.Fatalf("store_note model-facing text matches bias pattern %q: %q", re, text)
			}
		}
	}
}
