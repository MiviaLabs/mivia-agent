package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// originStub is a tool that reports a server, standing in for the MCP
// package's discovered tool without importing it (that would be a cycle).
type originStub struct {
	name   string
	server string
}

func (t originStub) Name() string               { return t.name }
func (t originStub) Description() string        { return "stub" }
func (t originStub) Parameters() map[string]any { return map[string]any{} }
func (t originStub) OriginServer() string       { return t.server }
func (originStub) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

// builtinStub is a tool with no origin, like every compiled-in tool.
type builtinStub struct{ name string }

func (t builtinStub) Name() string               { return t.name }
func (t builtinStub) Description() string        { return "stub" }
func (t builtinStub) Parameters() map[string]any { return map[string]any{} }
func (builtinStub) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

// TestExternalOriginsNamesOnlyServerSuppliedTools: the map is what lets a
// surface report the schema cost an operator can actually remove. A version
// that listed every tool, or none, would leave that number either wrong or
// permanently zero, and neither shows up as a failure anywhere else.
func TestExternalOriginsNamesOnlyServerSuppliedTools(t *testing.T) {
	r := NewRegistry()
	r.Register(builtinStub{name: "read_file"})
	r.Register(originStub{name: "linear_issue", server: "linear"})
	r.Register(originStub{name: "gh_pr", server: "github"})

	got := r.ExternalOrigins()
	if len(got) != 2 {
		t.Fatalf("ExternalOrigins listed %d tools, want the 2 server-supplied ones: %v", len(got), got)
	}
	if got["linear_issue"] != "linear" {
		t.Errorf("linear_issue maps to %q, want \"linear\"", got["linear_issue"])
	}
	if got["gh_pr"] != "github" {
		t.Errorf("gh_pr maps to %q, want \"github\"", got["gh_pr"])
	}
	if _, listed := got["read_file"]; listed {
		t.Error("a compiled-in tool was reported as server-supplied")
	}
}

// TestExternalOriginsIsEmptyWithoutServers: no servers connected must mean an
// empty map, not a nil-map panic and not a map of empty strings.
func TestExternalOriginsIsEmptyWithoutServers(t *testing.T) {
	r := NewRegistry()
	r.Register(builtinStub{name: "read_file"})
	if got := r.ExternalOrigins(); len(got) != 0 {
		t.Errorf("ExternalOrigins = %v, want empty when every tool is compiled in", got)
	}
}
