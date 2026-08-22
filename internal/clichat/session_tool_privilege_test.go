package clichat

import (
	"context"
	"encoding/json"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// unmarkedControlTool stands in for a future session-control tool whose author
// forgot the Privileged() marker.
type unmarkedControlTool struct{}

func (unmarkedControlTool) Name() string               { return "future_control" }
func (unmarkedControlTool) Description() string        { return "session control" }
func (unmarkedControlTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (unmarkedControlTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "{}", nil
}

type markedControlTool struct{ unmarkedControlTool }

func (markedControlTool) Privileged() {}

func newPrivilegeTestDispatcher(t *testing.T) *runtime.Dispatcher {
	t.Helper()
	d, err := runtime.NewToolDispatcher(tools.NewRegistry(), runtime.Policy{})
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

// Nested agents are shielded by the PrivilegedTool marker, which is a runtime
// type assertion. Rejecting unmarked tools at the single registration choke
// point turns a silent capability leak into a startup failure.
func TestRegisterSessionToolRejectsUnmarkedTool(t *testing.T) {
	d := newPrivilegeTestDispatcher(t)
	reg := tools.NewRegistry()

	err := registerSessionTool(d, reg, unmarkedControlTool{})
	if err == nil {
		t.Fatal("registering an unmarked session tool must fail")
	}
	if !strings.Contains(err.Error(), "future_control") {
		t.Fatalf("error must name the offending tool, got %v", err)
	}
	if _, exists := reg.Get("future_control"); exists {
		t.Fatal("rejected tool must not reach the model-visible registry")
	}
}

func TestRegisterSessionToolAcceptsMarkedTool(t *testing.T) {
	d := newPrivilegeTestDispatcher(t)
	reg := tools.NewRegistry()

	if err := registerSessionTool(d, reg, markedControlTool{}); err != nil {
		t.Fatalf("marked session tool must register: %v", err)
	}
	if _, exists := reg.Get("future_control"); !exists {
		t.Fatal("marked tool missing from registry")
	}
}

// Every shipped orchestration tool must carry the marker, so the sub-agent
// registry filter excludes them without relying on the name denylist.
func TestOrchestrationToolsAreMarkedPrivileged(t *testing.T) {
	d := newPrivilegeTestDispatcher(t)
	shipped := []tools.Tool{
		&delegateTool{dispatcher: d},
		cliorchestrate.NewDispatchTasksToolConfigured(d, config.DefaultSubagentConfig, nil, nil),
		cliorchestrate.NewSpawnAgentToolConfigured(d, config.DefaultSubagentConfig, nil, nil),
		cliorchestrate.NewInspectAgentToolConfigured(d),
		cliorchestrate.NewJoinRunToolConfigured(d),
		cliorchestrate.NewCancelRunToolConfigured(d),
	}
	for _, tool := range shipped {
		if _, privileged := tool.(tools.PrivilegedTool); !privileged {
			t.Errorf("tool %q does not implement tools.PrivilegedTool", tool.Name())
		}
	}
}
