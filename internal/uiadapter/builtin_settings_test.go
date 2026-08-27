package uiadapter

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestAgentSourceToScopeBuiltin pins the origin mapping: a compiled built-in
// lands in ScopeBuiltin (read-only), never in the mutable user/project
// buckets. Kill mutation: delete the AgentSourceBuiltIn case - the row would
// fall into ScopeUser's file-writing semantics.
func TestAgentSourceToScopeBuiltin(t *testing.T) {
	if got := agentSourceToScope(config.AgentSourceBuiltIn); got != ports.ScopeBuiltin {
		t.Fatalf("agentSourceToScope(builtin) = %v, want ScopeBuiltin", got)
	}
	if got := agentSourceToScope(config.AgentSourceUser); got != ports.ScopeUser {
		t.Fatalf("agentSourceToScope(user) = %v, want ScopeUser", got)
	}
	if got := agentSourceToScope(config.AgentSourceWorkspace); got != ports.ScopeProject {
		t.Fatalf("agentSourceToScope(workspace) = %v, want ScopeProject", got)
	}
}

// TestAgentsDirForScopeBuiltinHasNoDirectory pins that the builtin scope
// never resolves to an on-disk directory: compiled content is never written
// or deleted. Kill mutation: let ScopeBuiltin fall through to the workspace
// directory branch.
func TestAgentsDirForScopeBuiltinHasNoDirectory(t *testing.T) {
	if dir := agentsDirForScope(ports.ScopeBuiltin); dir != "" {
		t.Fatalf("builtin scope directory = %q, want empty", dir)
	}
}

// builtinSettingsStore builds a store whose registry seeds one ScopeBuiltin
// row and one ScopeUser row, through the production seeding path.
func builtinSettingsStore(t *testing.T) ports.Settings {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	res := &config.Resolved{ProviderName: "zai", Model: "glm-5.2"}
	reg := agents.NewRegistry()
	if err := reg.Publish(agents.ResolvedAgent{
		Name:        "general-purpose",
		Description: "built-in",
		Provenance:  agents.Provenance{Source: config.AgentSourceBuiltIn},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Publish(agents.ResolvedAgent{
		Name:        "reviewer",
		Description: "code reviewer",
		Provenance:  agents.Provenance{Source: config.AgentSourceUser},
	}); err != nil {
		t.Fatal(err)
	}
	state := &cliagents.AgentSessionState{Registry: reg, ToolBase: nil}
	return NewSettingsStore(nil, res, state).Settings()
}

// drainFailureMessage collects the final failure message from a save handle.
func drainFailureMessage(h ports.SaveHandle) (message string, failed bool) {
	for ev := range h.Events() {
		if ev.State == ports.SaveFailed {
			return ev.Message, true
		}
	}
	return "", false
}

// TestSettingsBuiltinRowRemoveRefused pins the store-level guard: removing a
// ScopeBuiltin row is refused and leaves the row intact. Kill mutation:
// delete the ScopeBuiltin check in applyAgent's RemoveAgent arm.
func TestSettingsBuiltinRowRemoveRefused(t *testing.T) {
	settings := builtinSettingsStore(t)

	h, err := settings.Agents.Apply(context.Background(), ports.ScopeUser, ports.RemoveAgent{Name: "general-purpose"})
	if err != nil {
		t.Fatal(err)
	}
	message, failed := drainFailureMessage(h)
	if !failed {
		t.Fatal("removing a built-in row must fail")
	}
	if !strings.Contains(message, "built-in") {
		t.Fatalf("failure message must name the builtin guard: %q", message)
	}
	found := false
	for _, a := range settings.Agents.Agents() {
		if a.Name == "general-purpose" && a.Scope == ports.ScopeBuiltin {
			found = true
		}
	}
	if !found {
		t.Fatal("the built-in row was removed")
	}
}

// TestSettingsReservedRootNameUpsertRefused pins the store-level guard: no
// file-backed agent may take the reserved root identity name. Kill mutation:
// delete the config.RootAgentName check in applyAgent's UpsertAgent arm.
func TestSettingsReservedRootNameUpsertRefused(t *testing.T) {
	settings := builtinSettingsStore(t)

	h, err := settings.Agents.Apply(context.Background(), ports.ScopeUser, ports.UpsertAgent{
		Agent: ports.AgentView{Name: config.RootAgentName, Description: "imposter"},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, failed := drainFailureMessage(h)
	if !failed {
		t.Fatal("upserting the reserved root name must fail")
	}
	if !strings.Contains(message, "reserved") {
		t.Fatalf("failure message must name the reservation: %q", message)
	}
}

// TestSettingsBuiltinScopeUpsertRefused pins the scope-parameter guard: an
// Upsert addressed AT ScopeBuiltin is refused regardless of content. Kill
// mutation: delete the scope check at the top of applyAgent's UpsertAgent arm.
func TestSettingsBuiltinScopeUpsertRefused(t *testing.T) {
	settings := builtinSettingsStore(t)

	h, err := settings.Agents.Apply(context.Background(), ports.ScopeBuiltin, ports.UpsertAgent{
		Agent: ports.AgentView{Name: "sneaky", Description: "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, failed := drainFailureMessage(h)
	if !failed {
		t.Fatal("upsert addressed at the builtin scope must fail")
	}
	if !strings.Contains(message, "read-only") {
		t.Fatalf("failure message must name the read-only guard: %q", message)
	}
}
