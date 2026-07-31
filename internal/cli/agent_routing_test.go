package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func routingAgentRegistry(t *testing.T) *agents.AgentRegistry {
	t.Helper()
	reg := agents.NewRegistry()
	for _, agent := range []agents.ResolvedAgent{
		{Name: "researcher", Description: "Research evidence", EffectiveTools: []string{"read_file"}},
		{Name: "writer", Description: "Write reports", EffectiveTools: []string{"write_file"}},
	} {
		if err := reg.Publish(agent); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

func routingTools(t *testing.T) (*dispatchTasksTool, *spawnAgentTool) {
	t.Helper()
	d := runtime.New(runtime.Policy{})
	for _, name := range []string{"researcher", "writer"} {
		if err := d.Register(runtime.Subagent, name, handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		})); err != nil {
			t.Fatal(err)
		}
	}
	reg := routingAgentRegistry(t)
	repo := ledger.NewMemoryLedgerRepository()
	return &dispatchTasksTool{dispatcher: d, cfg: config.DefaultSubagentConfig, repo: repo, agentReg: reg},
		&spawnAgentTool{dispatcher: d, cfg: config.DefaultSubagentConfig, repo: repo, agentReg: reg}
}

func TestAgentFieldRequired(t *testing.T) {
	dispatch, spawn := routingTools(t)
	spawnCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "routing-test"})
	for name, invoke := range map[string]func() error{
		"dispatch": func() error {
			_, err := dispatch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"id":"x","prompt":"work"}]}`))
			return err
		},
		"spawn": func() error {
			_, err := spawn.Execute(spawnCtx, json.RawMessage(`{"tasks":[{"id":"x","prompt":"work"}]}`))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := invoke()
			if err == nil || !strings.Contains(err.Error(), "agent is required") {
				t.Fatalf("error = %v, want required agent", err)
			}
		})
	}
}

func TestHandlerAndNameSelectorsRejected(t *testing.T) {
	dispatch, spawn := routingTools(t)
	spawnCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "routing-test"})
	cases := []struct {
		name string
		tool func(json.RawMessage) error
		args string
	}{
		{"dispatch handler", func(a json.RawMessage) error { _, err := dispatch.Execute(context.Background(), a); return err }, `{"tasks":[{"id":"x","agent":"researcher","prompt":"work","handler":"multi_step"}]}`},
		{"spawn name", func(a json.RawMessage) error { _, err := spawn.Execute(spawnCtx, a); return err }, `{"tasks":[{"id":"x","agent":"researcher","prompt":"work","name":"oneshot"}]}`},
		{"dispatch role", func(a json.RawMessage) error { _, err := dispatch.Execute(context.Background(), a); return err }, `{"tasks":[{"id":"x","agent":"researcher","prompt":"work","role":"reviewer"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.tool(json.RawMessage(tc.args)); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("error = %v, want strict selector rejection", err)
			}
		})
	}
}

func TestBuiltInRunnerCannotSelectAgent(t *testing.T) {
	dispatch, _ := routingTools(t)
	_, err := dispatch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"id":"x","agent":"multi_step","prompt":"work"}]}`))
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("error = %v, want unknown agent", err)
	}
}

func TestAgentEnumInParameters(t *testing.T) {
	dispatch, spawn := routingTools(t)
	for name, parameters := range map[string]map[string]any{"dispatch": dispatch.Parameters(), "spawn": spawn.Parameters()} {
		t.Run(name, func(t *testing.T) {
			items := parameters["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
			props := items["properties"].(map[string]any)
			if _, found := props["handler"]; found {
				t.Fatal("handler leaked into model schema")
			}
			if _, found := props["name"]; found {
				t.Fatal("name leaked into model schema")
			}
			agent := props["agent"].(map[string]any)
			if got := agent["enum"].([]string); len(got) != 2 || got[0] != "researcher" || got[1] != "writer" {
				t.Fatalf("agent enum = %#v", got)
			}
			if !strings.Contains(agent["description"].(string), "researcher: Research evidence") {
				t.Fatalf("agent routing hint = %q", agent["description"])
			}
		})
	}
}

func TestSkillDoesNotReplaceAgent(t *testing.T) {
	reg := routingAgentRegistry(t)
	locked, _ := reg.Get("researcher")
	empty := []string{}
	locked.Skills = &empty
	reg = agents.NewRegistry()
	if err := reg.Publish(locked); err != nil {
		t.Fatal(err)
	}
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{Name: "audit", Tools: []string{"read_file"}})
	tool := &dispatchTasksTool{dispatcher: runtime.New(runtime.Policy{}), cfg: config.DefaultSubagentConfig, repo: ledger.NewMemoryLedgerRepository(), agentReg: reg, skillReg: skillReg}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[{"id":"x","agent":"researcher","skill":"audit","prompt":"work"}]}`))
	if err == nil || !strings.Contains(err.Error(), "may not invoke") {
		t.Fatalf("error = %v, want selected-agent skill denial", err)
	}
}

func TestResumeFailsWhenAgentDefinitionChangesBeforeLedgerMutation(t *testing.T) {
	definition := agents.ResolvedAgent{Name: "researcher", EffectiveTools: []string{"read_file"}}
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, definition.Name, &agentTaskHandler{definition: definition, digest: digest}); err != nil {
		t.Fatal(err)
	}
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "resume-routing", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: "resume-routing", TaskID: "task-1", Status: string(ledger.TaskStatusQueued), Version: 1,
		HandlerName: "forged-handler", AgentName: definition.Name, AgentDigest: "sha256:stale", Input: json.RawMessage(`"work"`),
	}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	if _, err := c.ResumeInterruptedRun(ctx, "resume-routing"); err == nil || !strings.Contains(err.Error(), "routing authorization") {
		t.Fatalf("resume error = %v, want stale routing rejection", err)
	}
	tasks, err := repo.ListTasks(ctx, "resume-routing")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != string(ledger.TaskStatusQueued) || tasks[0].HandlerName != "forged-handler" {
		t.Fatalf("resume mutated task before authorization: %#v", tasks)
	}
}
