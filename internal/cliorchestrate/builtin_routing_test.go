package cliorchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// TestCleanLoadShipsBuiltInInRoutingSchema pins the production load path: a
// clean workspace resolves the compiled general-purpose agent, and the
// dispatch_tasks schema then offers it in the agent roster prose.
func TestCleanLoadShipsBuiltInInRoutingSchema(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	reg, _, warnings, err := agents.LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	tool := &dispatchTasksTool{agentReg: reg, cfg: config.DefaultSubagentConfig, repo: ledger.NewMemoryLedgerRepository()}
	items := tool.Parameters()["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	agent := items["properties"].(map[string]any)["agent"].(map[string]any)
	// The roster is prose, not an enum: see taskItemSchema.
	if enum, found := agent["enum"]; found {
		t.Fatalf("agent enum = %#v; the roster travels in the description", enum)
	}
	description := agent["description"].(string)
	if !strings.Contains(description, "Optional") {
		t.Fatalf("agent description must state the field is optional: %q", description)
	}
	// The always-available clause, NOT the bare name: agentRoutingBaseDescription
	// already contains "general-purpose" ("when general-purpose is available"),
	// and it ships even for a nil registry - so asserting the name alone passes
	// with the roster entirely gone. agentRoutingDescription emits this clause
	// only when reg.Get(BuiltInGeneralPurposeName) succeeds, which is the
	// resolution this test exists to prove.
	if !strings.Contains(description, "Built-in general-purpose is always available.") {
		t.Fatalf("agent roster prose must record that the built-in resolved: %q", description)
	}
}

// TestCleanRegistryDispatchesBuiltInAgent pins the first-run fan-out
// end to end: dispatch_tasks naming the built-in general-purpose agent
// resolves and completes on a registry loaded from a clean workspace.
func TestCleanRegistryDispatchesBuiltInAgent(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	reg, _, _, err := agents.LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "general-purpose", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(runtime.Subagent, HandlerOneshot, handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"output":"oneshot-ok"}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	tool := &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig, repo: ledger.NewMemoryLedgerRepository(), agentReg: reg}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[{"id":"t1","agent":"general-purpose","prompt":"work"}],"wait":"run"}`))
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Fatalf("Execute output = %q, want the built-in agent's result", out)
	}
	if strings.Contains(out, "failed") {
		t.Fatalf("Execute output reports a failure: %q", out)
	}
}

func TestDispatchTasksDefaultsBlankAgentToGeneralPurpose(t *testing.T) {
	reg := agents.NewRegistry()
	if err := reg.Publish(agents.ResolvedAgent{Name: agents.BuiltInGeneralPurposeName}); err != nil {
		t.Fatal(err)
	}
	tool := &dispatchTasksTool{agentReg: reg, cfg: config.DefaultSubagentConfig}
	tasks, err := tool.buildTasks("call", []dispatchTaskParam{{ID: "t1", Prompt: "work"}, {ID: "t2", Agent: " ", Prompt: "more"}}, 60)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.AgentName != agents.BuiltInGeneralPurposeName || task.Name != agents.BuiltInGeneralPurposeName {
			t.Fatalf("task route = name %q agent %q, want general-purpose", task.Name, task.AgentName)
		}
	}
}

func TestDispatchTasksUsesOneshotOnlyWhenGeneralPurposeUnavailable(t *testing.T) {
	tool := &dispatchTasksTool{agentReg: agents.NewRegistry(), cfg: config.DefaultSubagentConfig}
	tasks, err := tool.buildTasks("call", []dispatchTaskParam{{ID: "t1", Prompt: "work"}}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].Name != HandlerOneshot || tasks[0].AgentName != "" {
		t.Fatalf("task = %+v, want one-shot route", tasks[0])
	}
}

func TestDispatchTasksRejectsDuplicateCanonicalTaskIDsBeforeSpawn(t *testing.T) {
	tool := &dispatchTasksTool{agentReg: agents.NewRegistry(), cfg: config.DefaultSubagentConfig}
	_, err := tool.buildTasks("call", []dispatchTaskParam{{ID: "same", Prompt: "one"}, {ID: " same ", Prompt: "two"}}, 60)
	if err == nil || !strings.Contains(err.Error(), "duplicate task id") {
		t.Fatalf("err = %v, want duplicate canonical task id rejection", err)
	}
}

// TestRoutingProseDropsAlwaysAvailableClaimWhenBuiltInSkipped pins that the
// schema prose never promises the built-in when it did not resolve (e.g. a
// same-name skill collision skips it with a warning).
func TestRoutingProseDropsAlwaysAvailableClaimWhenBuiltInSkipped(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	reg, _, _, err := agents.LoadAndResolve(ws, map[string]struct{}{"general-purpose": {}})
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	if _, ok := reg.Get("general-purpose"); ok {
		t.Fatalf("precondition failed: built-in unexpectedly present in %v", reg.Names())
	}
	tool := &dispatchTasksTool{agentReg: reg, cfg: config.DefaultSubagentConfig, repo: ledger.NewMemoryLedgerRepository()}
	items := tool.Parameters()["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	description := items["properties"].(map[string]any)["agent"].(map[string]any)["description"].(string)
	if strings.Contains(description, "always available") {
		t.Fatalf("description claims the built-in is always available after a tolerant skip: %q", description)
	}
}

// TestRoutingProseSinglePeriod pins the roster join punctuation: the claim
// clause and the roster must not produce a doubled period.
func TestRoutingProseSinglePeriod(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	reg, _, _, err := agents.LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	tool := &dispatchTasksTool{agentReg: reg, cfg: config.DefaultSubagentConfig, repo: ledger.NewMemoryLedgerRepository()}
	items := tool.Parameters()["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	description := items["properties"].(map[string]any)["agent"].(map[string]any)["description"].(string)
	if strings.Contains(description, "..") {
		t.Fatalf("doubled period in schema prose: %q", description)
	}
}

// TestDispatchDescriptionStopsDemandingAnAgent pins D1 of the roster plan:
// the primary router text must not order "each task must explicitly select
// one authorized agent" (that sentence sent models hunting for agents that
// were never visible). Kill mutation: restore the old opening sentences.
func TestDispatchDescriptionStopsDemandingAnAgent(t *testing.T) {
	desc := (&dispatchTasksTool{}).Description()
	if strings.Contains(desc, "must explicitly select one authorized agent") {
		t.Fatalf("description still demands a named agent: %q", desc)
	}
	lower := strings.ToLower(desc)
	for _, want := range []string{"optional"} {
		if !strings.Contains(lower, want) {
			t.Fatalf("description missing %q: %q", want, desc)
		}
	}
}

// TestDispatchDescriptionGatesBuiltInClaim pins the prose/enum invariant on
// the TOP-LEVEL description: the always-available claim appears only when the
// built-in actually resolved into the registry. Kill mutation: hardcode the
// claim regardless of t.agentReg.
func TestDispatchDescriptionGatesBuiltInClaim(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	with, _, _, err := agents.LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	without, _, _, err := agents.LoadAndResolve(ws, map[string]struct{}{"general-purpose": {}})
	if err != nil {
		t.Fatalf("LoadAndResolve (collision) error = %v", err)
	}
	if _, ok := without.Get("general-purpose"); ok {
		t.Fatal("precondition failed: collision load still has the built-in")
	}

	if got := (&dispatchTasksTool{agentReg: with}).Description(); !strings.Contains(got, "always available") {
		t.Fatalf("description must claim availability when the built-in resolved: %q", got)
	}
	got := (&dispatchTasksTool{agentReg: without}).Description()
	if strings.Contains(got, "always available") || strings.Contains(got, "general-purpose") {
		t.Fatalf("description must not promise the skipped built-in: %q", got)
	}
}
