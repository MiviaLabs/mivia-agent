package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	appruntime "github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Plan 01's M3 mutation proof, never written when 01 shipped.
//
// The dispatcher is the authorization boundary, but a loop must never gain
// reach from a wider dispatcher than the registry it advertised to the model:
// executeToolTask rejects a call for a tool absent from its own registry before
// dispatch. Deleting that guard makes this test fail.
func TestExecuteToolTaskRejectsToolMissingFromRegistry(t *testing.T) {
	visible := tools.NewRegistry()
	full := tools.NewRegistry()
	hidden := &dispatcherOnlyTestTool{}
	full.Register(hidden)

	dispatcher, err := appruntime.NewToolDispatcher(full, appruntime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)

	results := executeToolsParallel(
		context.Background(),
		[]provider.ToolCall{tc("call-1", hidden.Name(), `{}`)},
		visible, // the model-facing registry does NOT contain the tool
		Options{Dispatcher: dispatcher, MaxConcurrentTools: 1},
	)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].err == nil {
		t.Fatal("a tool absent from the loop registry must not be dispatched")
	}
	if !strings.Contains(results[0].err.Error(), "not available") {
		t.Fatalf("unexpected error: %v", results[0].err)
	}
	if n := hidden.executions.Load(); n != 0 {
		t.Fatalf("hidden tool executed %d times despite being absent from the registry", n)
	}
}
