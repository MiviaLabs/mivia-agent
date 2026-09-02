package clichat

// Coverage for two delivery-2 behaviors: adoptSessionLedgerRepo's
// idempotence (a second adopt must keep the first instance - surface
// rebuilds and workflow child-run stamps depend on it), and
// registerSkillHandlers' scope gate (a skill the selected agent may not
// invoke must not reach the dispatcher).

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestAdoptSessionLedgerRepoKeepsFirstInstance(t *testing.T) {
	dir := t.TempDir()
	sess := chat.NewSession(&config.Resolved{}, nullCompleter{})
	state := &AgentSessionState{}

	cfgFirst := config.SubagentConfig{StoreBackend: "sqlite", StorePath: filepath.Join(dir, "a.db")}
	adoptSessionLedgerRepo(sess, cfgFirst, state, sessionRouting{})
	first := state.LedgerRepo
	if first == nil {
		t.Fatal("first adopt must set the session ledger repo")
	}

	cfgSecond := config.SubagentConfig{StoreBackend: "sqlite", StorePath: filepath.Join(dir, "b.db")}
	adoptSessionLedgerRepo(sess, cfgSecond, state, sessionRouting{})
	if state.LedgerRepo != first {
		t.Fatal("second adopt must keep the first instance; rebuilds and child-run stamps pin it")
	}

	releaseSessionLedgerRepo(state)
	if state.LedgerRepo != nil {
		t.Fatal("release must close and forget the adopted store")
	}
}

func TestRegisterSkillHandlersSkipsDeniedSkills(t *testing.T) {
	ws, err := workspace.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	d, err := runtime.NewToolDispatcher(tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	skillReg := skills.NewRegistry()
	for _, name := range []string{"allowed-skill", "denied-skill"} {
		if err := skillReg.Register(skills.Definition{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	skillsAllowed := []string{"allowed-skill"}
	selected := &agents.ResolvedAgent{Name: "tester", Skills: &skillsAllowed}
	scope := cliagents.SkillScopeFromAgent(selected)

	err = registerSkillHandlers(d, tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}),
		nullCompleter{}, "test-model", sessionDial{}, config.SubagentConfig{}, resultBudgets{},
		0, nil, nil, skillReg, scope, nil, contextmgr.PrepareInput{}, nil, nil)
	if err != nil {
		t.Fatalf("registerSkillHandlers: %v", err)
	}

	denied := d.Invoke(context.Background(), runtime.Request{
		Kind: runtime.Subagent, Name: "denied-skill", Input: json.RawMessage(`"x"`),
	})
	if denied.Err == nil || !strings.Contains(denied.Err.Error(), `unknown subagent "denied-skill"`) {
		t.Fatalf("denied skill invoke err = %v, want the unknown-handler error", denied.Err)
	}
	allowed := d.Invoke(context.Background(), runtime.Request{
		Kind: runtime.Subagent, Name: "allowed-skill", Input: json.RawMessage(`"x"`),
	})
	if allowed.Err != nil && strings.Contains(allowed.Err.Error(), "unknown subagent") {
		t.Fatalf("allowed skill invoke err = %v; the permitted skill must be registered", allowed.Err)
	}
}
