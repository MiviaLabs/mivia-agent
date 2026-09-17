package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/context/manager"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// mockRunCommandSuccess is a run_command stand-in that always reports a
// successful exit (exit=0) regardless of the argv it was given - the body
// content is what toolResultBodyFailed/runCommandBodyFailed reads, the
// mutating-vs-exploratory decision is isMutatingCommand's job on the args.
type mockRunCommandSuccess struct{}

func (m *mockRunCommandSuccess) Name() string               { return tools.RunCommandToolName }
func (m *mockRunCommandSuccess) Description() string        { return "mock run_command" }
func (m *mockRunCommandSuccess) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (m *mockRunCommandSuccess) Execute(context.Context, json.RawMessage) (string, error) {
	return "command: x\ncwd: .\nexit=0\ndone", nil
}

// mockWriteFailTool is a write-class CLI tool whose Execute always errors -
// the "partial write, then failure" shape part 2 of the gate must still
// record.
type mockWriteFailTool struct{ name string }

func (m *mockWriteFailTool) Name() string               { return m.name }
func (m *mockWriteFailTool) Description() string        { return "mock failing write tool" }
func (m *mockWriteFailTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (m *mockWriteFailTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "workspace:mutation"}
}
func (m *mockWriteFailTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("disk full")
}

// mockReadFailTool is an unclassified (default ExecutionExternal, not
// run_command) tool whose Execute always errors - the negative case: a
// failed call that is neither write-class nor a mutating run_command must
// NOT be recorded as a changed surface.
type mockReadFailTool struct{ name string }

func (m *mockReadFailTool) Name() string               { return m.name }
func (m *mockReadFailTool) Description() string        { return "mock failing read tool" }
func (m *mockReadFailTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (m *mockReadFailTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("not found")
}

// newChangedSurfaceShim builds a dispatcherShim wired to a real dispatcher
// over cliTool, with a fresh turn state seeded with a fact tracker so the
// test can snapshot ChangedSurfaces after Run.
func newChangedSurfaceShim(t *testing.T, cliTool tools.Tool, innerName string) (*dispatcherShim, *manager.TurnState) {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(cliTool)
	dispatcher, err := runtime.NewToolDispatcher(reg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	turn := newSDKTurnState()
	facts := manager.NewTurnState()
	turn.seedTurnFacts(facts)
	shim := &dispatcherShim{
		inner: &sdkToolForName{name: innerName},
		cli:   cliTool,
		opts:  Options{Dispatcher: dispatcher, SessionID: "s"},
		turn:  turn,
	}
	return shim, facts
}

// TestDispatcherShimRecordsChangedSurfaceForSuccessfulMutatingRunCommand
// pins gate part 1: run_command declares Capability.Class ExecutionExternal
// (internal/tools/run.go), not ExecutionWrite, so a successful mutating
// call (e.g. `git commit`) must still be recorded via the
// toolName==RunCommandToolName && isMutatingCommand(args) branch - the
// same branch recordProgress already uses a few lines away.
func TestDispatcherShimRecordsChangedSurfaceForSuccessfulMutatingRunCommand(t *testing.T) {
	shim, facts := newChangedSurfaceShim(t, &mockRunCommandSuccess{}, tools.RunCommandToolName)
	in := sdktools.InOut{Value: map[string]any{"argv": []string{"git", "commit"}}}
	if _, err := shim.Run(context.Background(), in); err != nil {
		t.Fatalf("shim.Run: %v", err)
	}
	snap, err := facts.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(snap.ChangedSurfaces, tools.RunCommandToolName) {
		t.Fatalf("changed surfaces = %v, want %q recorded for a successful mutating run_command",
			snap.ChangedSurfaces, tools.RunCommandToolName)
	}
}

// TestDispatcherShimRecordsChangedSurfaceForFailedWriteClassTool pins gate
// part 2: a write-class tool that mutated state and then reported a
// failure (non-zero exit / error body) must still be recorded - the
// summary envelope's ChangedSurfaces exists to say what was touched, and a
// partial write is still a touch. The current `!failed` gate drops this.
func TestDispatcherShimRecordsChangedSurfaceForFailedWriteClassTool(t *testing.T) {
	shim, facts := newChangedSurfaceShim(t, &mockWriteFailTool{name: "write_thing"}, "write_thing")
	in := sdktools.InOut{Value: map[string]any{}}
	if _, err := shim.Run(context.Background(), in); err != nil {
		t.Fatalf("shim.Run: %v", err)
	}
	snap, err := facts.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(snap.ChangedSurfaces, "write_thing") {
		t.Fatalf("changed surfaces = %v, want %q recorded for a failed write-class call",
			snap.ChangedSurfaces, "write_thing")
	}
}

// TestDispatcherShimDoesNotRecordChangedSurfaceForNonMutatingRunCommand is
// the negative case for part 1: a genuinely exploratory run_command (`ls`,
// which isMutatingCommand does not recognize as mutating) must NOT be
// spuriously recorded.
func TestDispatcherShimDoesNotRecordChangedSurfaceForNonMutatingRunCommand(t *testing.T) {
	shim, facts := newChangedSurfaceShim(t, &mockRunCommandSuccess{}, tools.RunCommandToolName)
	in := sdktools.InOut{Value: map[string]any{"argv": []string{"ls"}}}
	if _, err := shim.Run(context.Background(), in); err != nil {
		t.Fatalf("shim.Run: %v", err)
	}
	snap, err := facts.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(snap.ChangedSurfaces, tools.RunCommandToolName) {
		t.Fatalf("changed surfaces = %v, non-mutating run_command must not be recorded", snap.ChangedSurfaces)
	}
}

// TestDispatcherShimDoesNotRecordChangedSurfaceForFailedNonWriteTool is the
// negative case for part 2: a failed call that is neither write-class nor
// a mutating run_command (default ExecutionExternal) must NOT be
// spuriously recorded just because the `!failed` gate was dropped.
func TestDispatcherShimDoesNotRecordChangedSurfaceForFailedNonWriteTool(t *testing.T) {
	shim, facts := newChangedSurfaceShim(t, &mockReadFailTool{name: "read_thing"}, "read_thing")
	in := sdktools.InOut{Value: map[string]any{}}
	if _, err := shim.Run(context.Background(), in); err != nil {
		t.Fatalf("shim.Run: %v", err)
	}
	snap, err := facts.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(snap.ChangedSurfaces, "read_thing") {
		t.Fatalf("changed surfaces = %v, a failed non-write non-run_command call must not be recorded", snap.ChangedSurfaces)
	}
}
