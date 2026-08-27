package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	appruntime "github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// blockingNoProfileTool is a CLI tool with NO Capability method, so it
// declares no per-tool timeout. Execute blocks for the configured
// duration and ignores ctx, so only the SDK registry's run-timeout
// backstop can cut the call short.
type blockingNoProfileTool struct {
	name  string
	block time.Duration
}

func (b *blockingNoProfileTool) Name() string               { return b.name }
func (b *blockingNoProfileTool) Description() string        { return "blocking" }
func (b *blockingNoProfileTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (b *blockingNoProfileTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	time.Sleep(b.block)
	return "done", nil
}

// TestBuildSDKToolRegistryHonorsToolRunTimeout pins the config wiring:
// a positive Options.ToolRunTimeout must become the SDK registry's
// default run timeout, so a no-profile tool that blocks past it fails
// with the SDK's ErrRunTimeout. This is the observable proof that the
// CLI config value reaches sdktools.New (a cheap stand-in for the
// 10-minute default, which is too slow to exercise directly).
func TestBuildSDKToolRegistryHonorsToolRunTimeout(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&blockingNoProfileTool{name: "slow_tool", block: 2 * time.Second})
	loop := &Loop{Tools: reg}
	opts := Options{Model: "test-model", ToolRunTimeout: 150 * time.Millisecond}
	sdkReg, err := buildSDKToolRegistry(loop, opts, reg, newSDKTurnState())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = sdkReg.Run(context.Background(), "slow_tool", sdktools.InOut{Value: map[string]any{}})
	if !errors.Is(err, sdktools.ErrRunTimeout) {
		t.Fatalf("Run err = %v, want ErrRunTimeout (Options.ToolRunTimeout must reach the SDK registry default)", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Run took %v; the 150ms bound did not fire", elapsed)
	}
}

// TestBuildSDKToolRegistryDefaultsToUncapped pins the default mapping:
// Options.ToolRunTimeout <= 0 must map to the SDK's TimeoutNone, NOT
// fall through to the SDK's hardcoded 10-minute DefaultRunTimeout. The
// CLI dispatcher shim already arms every call's Capability.Timeout /
// Options.ToolTimeout as a real deadline, so by default the SDK
// backstop must never be tighter than those declared budgets. The
// blocking tool completing here proves no small registry bound was
// installed; the TimeoutNone mapping itself is pinned at the
// sdkadapter layer (TestConvertToolRegistryTimeoutNoneUncaps).
func TestBuildSDKToolRegistryDefaultsToUncapped(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&blockingNoProfileTool{name: "slow_tool", block: 300 * time.Millisecond})
	loop := &Loop{Tools: reg}
	opts := Options{Model: "test-model"}
	sdkReg, err := buildSDKToolRegistry(loop, opts, reg, newSDKTurnState())
	if err != nil {
		t.Fatal(err)
	}
	out, err := sdkReg.Run(context.Background(), "slow_tool", sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run err = %v, want success with the default (uncapped) run timeout", err)
	}
	if s, _ := out.Value.(string); s == "" {
		t.Fatal("empty tool result; expected the blocking tool's output")
	}
}

// TestBuildSDKToolRegistryDeclaredTimeoutBeatsDefault proves the
// precedence end to end through the full shim chain: a CLI CapableTool
// with a declared Capability.Timeout is never bounded by a TIGHTER
// registry default (the dispatch_tasks-at-10-minutes bug class). The
// outermost registered wrapper must publish a profile whose declared
// timeout wins over the configured default - the dispatcher shim maps
// declared > 0 to TimeoutNone because it arms the declared budget
// (plus per-call raises) as a real deadline itself, and a verbatim SDK
// bound would race its graceful timeout envelope at the same instant.
func TestBuildSDKToolRegistryDeclaredTimeoutBeatsDefault(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&declaredTimeoutBlockingTool{name: "budgeted_tool", block: 400 * time.Millisecond, timeout: time.Hour})
	disp, err := appruntime.NewToolDispatcher(reg, appruntime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disp.Close)
	loop := &Loop{Tools: reg}
	// Dispatcher wired: production always wires one, and the shim's
	// declared-timeout mapping only exists on that path (a bare
	// converter product keeps the verbatim profile so the SDK is the
	// sole enforcement layer).
	opts := Options{Model: "test-model", ToolRunTimeout: 150 * time.Millisecond, Dispatcher: disp}
	sdkReg, err := buildSDKToolRegistry(loop, opts, reg, newSDKTurnState())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := sdkReg.Get("budgeted_tool")
	if !ok {
		t.Fatal("budgeted_tool missing from converted registry")
	}
	pt, ok := registered.(sdktools.ProfiledTool)
	if !ok {
		t.Fatalf("registered tool %T does not implement sdktools.ProfiledTool", registered)
	}
	if got := pt.ExecutionProfile().Timeout; got >= 0 {
		t.Fatalf("declared-timeout tool publishes SDK Timeout %v, want negative (TimeoutNone: the dispatcher shim owns the declared deadline)", got)
	}
	out, err := sdkReg.Run(context.Background(), "budgeted_tool", sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run err = %v, want success (declared 1h Capability.Timeout must beat the 150ms registry default)", err)
	}
	if s, _ := out.Value.(string); s == "" {
		t.Fatal("empty tool result; expected the blocking tool's output")
	}
}

// declaredTimeoutBlockingTool blocks like blockingNoProfileTool but
// declares a Capability.Timeout, exercising the ProfiledTool bridge
// through every registered wrapper layer.
type declaredTimeoutBlockingTool struct {
	name    string
	block   time.Duration
	timeout time.Duration
}

func (d *declaredTimeoutBlockingTool) Name() string        { return d.name }
func (d *declaredTimeoutBlockingTool) Description() string { return "blocking with declared budget" }
func (d *declaredTimeoutBlockingTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (d *declaredTimeoutBlockingTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	time.Sleep(d.block)
	return "done", nil
}
func (d *declaredTimeoutBlockingTool) Capability(_ json.RawMessage) tools.Capability {
	return tools.Capability{Timeout: d.timeout}
}
