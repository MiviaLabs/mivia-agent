package clichat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

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
func TestPinnedInheritedBindingReauthorizesCurrentProviderAndModel(t *testing.T) {
	definition := agents.ResolvedAgent{Name: "researcher"}
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	session := &bindingProbeCompleter{name: "zai"}
	h := newAgentTaskHandler(definition, digest, nil, runtime.New(runtime.Policy{}), SessionDispatcherOpts{
		Completer: session, ProviderName: "zai", Model: "glm-5.2", ModelCatalog: bindingTestCatalog(),
		CompleterFactory: func(providerName, _ string) (provider.Completer, error) {
			return &bindingProbeCompleter{name: providerName}, nil
		},
	})
	binding, err := h.validateRequest(runtime.Request{
		Name: "researcher", AgentName: "researcher", AgentDigest: digest,
		ProviderName: "deepseek", Model: "deepseek-v4-flash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.ProviderName != "deepseek" || binding.Model != "deepseek-v4-flash" || binding.Completer.Name() != "deepseek" {
		t.Fatalf("pinned binding = %#v, want deepseek/deepseek-v4-flash with a deepseek completer", binding)
	}
}

func TestPinnedInheritedBindingRejectsPartialMetadata(t *testing.T) {
	definition := agents.ResolvedAgent{Name: "researcher"}
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	h := newAgentTaskHandler(definition, digest, nil, runtime.New(runtime.Policy{}), SessionDispatcherOpts{
		Completer: &bindingProbeCompleter{name: "zai"}, ProviderName: "zai", Model: "glm-5.2",
	})
	_, err = h.validateRequest(runtime.Request{
		Name: "researcher", AgentName: "researcher", AgentDigest: digest,
		ProviderName: "zai",
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("partial binding error = %v, want fail-closed incomplete metadata", err)
	}
}
