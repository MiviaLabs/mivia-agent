package agent

// Reproducing test for the mid-turn surface-rotation seam: after a
// deferred-tool admission the chat session's Surface hook hands the
// SDK bridge a NEW CLI registry (bridgeSDKBridgeSurface ->
// buildSDKToolRegistry). The run-timeout contract must survive that
// rebuild exactly as it holds at turn start:
//
//  1. a declared-Capability.Timeout tool's OUTERMOST registered value
//     must publish a negative SDK ExecutionProfile.Timeout
//     (TimeoutNone: the dispatcher shim owns the declared deadline) -
//     never 0-undeclared, which would hand the call to the registry
//     default resolution;
//  2. the rotated registry's configured default must be TimeoutNone
//     when Options.ToolRunTimeout <= 0, so the SDK's hardcoded
//     10-minute DefaultRunTimeout can never arm against the rotated
//     surface (the dispatch_tasks-killed-at-10:00.7 incident class);
//  3. a positive Options.ToolRunTimeout must still reach the rotated
//     registry (the option is not lost mid-run).
//
// The rotated CLI registry is shaped like the real post-admission
// product: a ScopedRegistryWithTail(base, root scope, admitted tail)
// core with the privileged orchestration tool registered afterwards
// (the dispatcher registration order), plus the real bridged hook and
// both rotation shapes the session can produce (with and without a
// rotated Dispatcher).

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	appruntime "github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// registryDefaultRunTimeout reads the SDK registry's unexported
// configured default via reflection. The SDK exports no accessor; the
// numeric read (Value.Int) is legal on an unexported field, unlike
// Interface().
func registryDefaultRunTimeout(t *testing.T, reg *sdktools.Registry) time.Duration {
	t.Helper()
	rv := reflect.ValueOf(reg).Elem().FieldByName("defaultRunTimeout")
	if !rv.IsValid() {
		t.Fatal("sdktools.Registry has no defaultRunTimeout field; update this test to the SDK's current shape")
	}
	return time.Duration(rv.Int())
}

// rotatedSurfaceFixture builds the real seam: turn-start options via
// buildAgentLoopOptions (installing the bridged Surface hook), then
// one rotation carrying the post-admission CLI registry shape.
type rotatedSurfaceFixture struct {
	rotatedCLI *tools.Registry
	specs      []provider.ToolSpec
}

func newRotatedCLIRegistry(t *testing.T) *rotatedSurfaceFixture {
	t.Helper()
	base := tools.NewRegistry()
	base.Register(&blockingNoProfileTool{name: "read_file", block: 0})
	base.Register(&blockingNoProfileTool{name: "slow_tool", block: 2 * time.Second})
	base.Register(&blockingNoProfileTool{name: "ledger_read", block: 0})
	rotated := tools.ScopedRegistryWithTail(base, tools.ScopeOptions{
		Mode:      tools.ScopeRoot,
		Allowlist: map[string]struct{}{"read_file": {}, "slow_tool": {}},
	}, []string{"ledger_read"})
	// Privileged orchestration tool lands after the core block, the
	// dispatcher registration order. 12h declared budget, the
	// dispatch_tasks shape.
	rotated.Register(&declaredTimeoutBlockingTool{name: "dispatch_tasks", block: 200 * time.Millisecond, timeout: 12 * time.Hour})
	return &rotatedSurfaceFixture{rotatedCLI: rotated, specs: rotated.OpenAITools()}
}

// buildAndRotate constructs the turn-start SDK options with the given
// CLI Options fields, then invokes the REAL bridged Surface hook once
// (the SDK's step-2+ consultation) and returns the rotated registry.
func buildAndRotate(t *testing.T, opts Options, fix *rotatedSurfaceFixture, rotateDispatcher bool) *sdktools.Registry {
	t.Helper()
	initial := tools.NewRegistry()
	initial.Register(&declaredTimeoutBlockingTool{name: "dispatch_tasks", block: 200 * time.Millisecond, timeout: 12 * time.Hour})
	loop := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: initial}
	var rotDisp *appruntime.Dispatcher
	if rotateDispatcher {
		d, err := appruntime.NewToolDispatcher(fix.rotatedCLI, appruntime.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		rotDisp = d
	}
	opts.Surface = func() Surface {
		return Surface{Registry: fix.rotatedCLI, Dispatcher: rotDisp, ToolSpecs: fix.specs}
	}
	sdkOpts, turn, err := buildAgentLoopOptions(loop, opts, "hi")
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if sdkOpts.Surface == nil {
		t.Fatal("bridged Surface hook not installed")
	}
	rotated := sdkOpts.Surface()
	if err := turn.bridgeError(); err != nil {
		t.Fatalf("surface rotation recorded bridge error: %v", err)
	}
	if rotated == nil || rotated.Registry == nil {
		t.Fatal("rotation returned no registry; the session's Surface hook always carries one")
	}
	return rotated.Registry
}

// TestSurfaceRotationKeepsDeclaredTimeoutContract is the incident
// reproduction attempt: post-rotation, dispatch_tasks' outermost
// registered value must NOT resolve to 0-undeclared while the
// registry default is not TimeoutNone - the only combination that
// arms the SDK's hardcoded 10-minute DefaultRunTimeout against a
// 12h-budget tool.
func TestSurfaceRotationKeepsDeclaredTimeoutContract(t *testing.T) {
	for _, tc := range []struct {
		name             string
		rotateDispatcher bool
	}{
		{"rotation with new dispatcher", true},
		{"rotation with nil dispatcher (keep prior)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fix := newRotatedCLIRegistry(t)
			disp, err := appruntime.NewToolDispatcher(fix.rotatedCLI, appruntime.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(disp.Close)
			// The real chat turn: a wired dispatcher, ToolRunTimeout unset,
			// and an approval gate so the admission wrapper layer
			// (WrapRegistryWithAdmission) is part of the chain like in the
			// interactive session.
			opts := Options{Model: "test-model", Dispatcher: disp,
				ApprovalPolicy: "write-only",
				ApprovalGate: func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
					return sdkadapter.ApprovalResult{Approved: true}
				},
			}
			rotatedReg := buildAndRotate(t, opts, fix, tc.rotateDispatcher)

			// Link (a): the outermost dispatch_tasks value must publish a
			// non-zero profile timeout (negative = shim owns the deadline).
			registered, ok := rotatedReg.Get("dispatch_tasks")
			if !ok {
				t.Fatal("dispatch_tasks missing from the rotated SDK registry")
			}
			if got := sdktools.ExecutionProfileOf(registered).Timeout; got == 0 {
				t.Fatalf("post-rotation dispatch_tasks profile Timeout = 0 (undeclared): the shim chain lost the declared 12h budget, registry default resolution applies")
			} else if got > 0 {
				t.Fatalf("post-rotation dispatch_tasks profile Timeout = %v (verbatim positive): the dispatcher shim's TimeoutNone mapping was lost", got)
			}

			// Link (b): the rotated registry's configured default must be
			// TimeoutNone (negative), never 0 - 0 resolves to the SDK's
			// 10-minute DefaultRunTimeout for every no-profile tool.
			if d := registryDefaultRunTimeout(t, rotatedReg); d >= 0 {
				t.Fatalf("rotated registry defaultRunTimeout = %v, want negative (TimeoutNone); 0 arms the SDK 10-minute backstop", d)
			}
		})
	}
}

// TestSurfaceRotationCarriesConfiguredRunTimeout proves behaviorally
// that a configured Options.ToolRunTimeout survives rotation: the
// rotated registry must bound a no-profile blocking tool at the small
// configured value, exactly as the turn-start registry does
// (TestBuildSDKToolRegistryHonorsToolRunTimeout).
func TestSurfaceRotationCarriesConfiguredRunTimeout(t *testing.T) {
	fix := newRotatedCLIRegistry(t)
	disp, err := appruntime.NewToolDispatcher(fix.rotatedCLI, appruntime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disp.Close)
	opts := Options{Model: "test-model", ToolRunTimeout: 150 * time.Millisecond}
	rotatedReg := buildAndRotate(t, opts, fix, true)
	start := time.Now()
	_, err = rotatedReg.Run(context.Background(), "slow_tool", sdktools.InOut{Value: map[string]any{}})
	if !errors.Is(err, sdktools.ErrRunTimeout) {
		t.Fatalf("rotated Run err = %v, want ErrRunTimeout (configured ToolRunTimeout lost across rotation)", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("rotated Run took %v; the 150ms bound did not fire", elapsed)
	}
}
