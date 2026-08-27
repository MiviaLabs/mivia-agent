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
// dispatch_tasks schema then offers it in the agent enum and roster prose.
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
	enum, ok := agent["enum"].([]string)
	if !ok {
		t.Fatalf("agent enum missing on a clean load: %#v", agent)
	}
	found := false
	for _, name := range enum {
		if name == "general-purpose" {
			found = true
		}
	}
	if !found {
		t.Fatalf("agent enum = %v, want it to offer general-purpose", enum)
	}
	description := agent["description"].(string)
	if !strings.Contains(description, "Optional") {
		t.Fatalf("agent description must state the field is optional: %q", description)
	}
	if !strings.Contains(description, "general-purpose") {
		t.Fatalf("agent roster prose must name the built-in: %q", description)
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

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[{"id":"t1","agent":"general-purpose","prompt":"work"}]}`))
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
