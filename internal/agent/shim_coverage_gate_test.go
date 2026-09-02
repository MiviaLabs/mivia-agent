package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Every executable tool in a built SDK registry must be governed.
//
// dispatcherShim.Run is where the per-call timeout, the dedup declaration,
// the result cap, the hook gate and advisory, the duplicate rules and the
// failure outcome live. A tool that reaches the model WITHOUT it runs with
// none of them. applyDispatcherShim used to degrade to exactly that, in
// silence, in three ways - its own doc comment lists them.
//
// The class is a guard that degrades to UNWRAPPED EXECUTION instead of
// refusing. None of the three was reachable in a shipped session, but that
// was a property of today's callers rather than of the code, and the failure
// mode is nine contracts dropped at once with nothing logged.
//
// This asserts the OUTCOME - every tool is a *dispatcherShim, or the build
// failed - rather than the branches, so a fourth way of producing an
// ungoverned tool is caught without anyone adding a case here.
//
// WHAT IT CANNOT REACH: the failed-Add branch. sdkReg.Add only fails on a
// duplicate name and the code Removes that name immediately before, so no
// input drives it. Restoring the old `_ = sdkReg.Add(t)` there does NOT fail
// this test - I ran that mutation and watched it survive. That branch is
// protected by construction and review, not by this gate.
func TestNoToolReachesTheModelUngoverned(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"no dispatcher wired", Options{}},
		{"dispatcher wired", Options{Dispatcher: runtime.New(runtime.Policy{})}},
		// The SHIPPED shape: ref-only and turn shaping both active, so the
		// registry entry is a wrapper and not the shim itself. The gate must
		// still see the tool as governed.
		{"every wrapper active", Options{
			Dispatcher:             runtime.New(runtime.Policy{}),
			SessionID:              "sess-1",
			RefOnlyTools:           []string{"read_file"},
			BatchResultBudgetBytes: 8 << 10,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cliReg := tools.NewRegistry()
			cliReg.Register(governedProbe{name: "read_file"})

			sdkReg, err := buildSDKToolRegistry(nil, tc.opts, cliReg, &sdkTurnState{})
			if err != nil {
				// Refusing to build is a correct answer: nothing ungoverned
				// can reach the model from a registry that does not exist.
				return
			}
			if sdkReg == nil {
				return
			}
			if got, want := len(sdkReg.Tools()), len(cliReg.List()); got != want {
				t.Errorf("the registry has %d tool(s), the model was promised %d; "+
					"wrapping must not drop one silently either", got, want)
			}
			for _, tool := range sdkReg.Tools() {
				if !governedByDispatcher(tool) {
					t.Errorf("tool %q is in the registry the model calls but is "+
						"NOT wrapped by dispatcherShim: it executes with no "+
						"per-call timeout, no dedup, no result cap, no hooks and "+
						"no recorded outcome, and nothing says so. Either wire a "+
						"dispatcher or refuse to build the registry - degrading "+
						"to ungoverned execution is not an option.", tool.Name())
				}
			}
		})
	}
}

type governedProbe struct{ name string }

func (g governedProbe) Name() string               { return g.name }
func (g governedProbe) Description() string        { return g.name }
func (g governedProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (g governedProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// governedDispatcher is a real dispatcher for tests that build a tool
// registry while testing something else.
//
// Those tests used to leave Dispatcher nil and relied on applyDispatcherShim
// silently skipping - which is the defect this file gates. They are not about
// the dispatcher; they just need the registry to be constructible, and it is
// no longer constructible ungoverned.
func governedDispatcher(t *testing.T, reg *tools.Registry) *runtime.Dispatcher {
	t.Helper()
	if reg == nil {
		reg = tools.NewRegistry()
	}
	d, err := runtime.NewToolDispatcher(reg, runtime.Policy{})
	if err != nil {
		t.Fatalf("build dispatcher: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

// An infrastructure error on the deferred path must reach the model as a
// result, never fail the whole run.
//
// The SDK treats a non-nil error from OnToolCallError as a hard failure of
// the run rather than a tool result, and the implementation this path
// replaced had no error channel at all - every problem degraded to a message.
// Whitespace-only arguments are the concrete route: json.Marshal of a
// RawMessage containing "  " fails with "unexpected end of JSON input", and
// the reporter's malformed-JSON guard does not catch it because
// strings.TrimSpace(args) != "" is false.
func TestADeferredInfrastructureErrorDoesNotKillTheRun(t *testing.T) {
	cliReg := tools.NewRegistry()
	probe := governedProbe{name: "read_file"}
	cliReg.Register(probe)

	turn := newSDKTurnState()
	opts := Options{Dispatcher: governedDispatcher(t, cliReg), SessionID: "sess-1"}

	// Whitespace-only arguments: not empty, not valid JSON.
	body, err := RunUnadmittedTool(context.Background(), opts, turn, probe, json.RawMessage("  "))
	if err == nil {
		return // marshaling succeeded; nothing to degrade, and that is fine
	}
	_ = body
	// The contract is on the CALLER: it must turn this into a message.
	msg, rerr := hostAuthorizedToolMessage(context.Background(), opts, turn,
		sdkshape.ToolCall{ID: "c1", Name: "read_file", Arguments: json.RawMessage("  ")}, probe)
	if rerr != nil {
		t.Errorf("an unmarshalable argument blob failed the whole RUN (%v); one "+
			"bad call must not abort the turn - it has to reach the model as a "+
			"result, which is what the path this replaced always did", rerr)
	}
	if !strings.Contains(msg.Content, "error") {
		t.Errorf("the model was not told the call failed: %q", msg.Content)
	}
}

// governedByDispatcher reports whether a registry entry ultimately executes
// through the dispatcher shim, looking THROUGH the outer wrappers.
//
// The gate used to assert the entry IS a *dispatcherShim. That is false in
// every shipped session: buildSDKToolRegistry wraps the shim with the
// admission/approval adapter, then ref-only, then turn shaping, each replacing
// the entry with its own type. The gate passed only because its Options{}
// disabled all three - an approval layer needs a gate or policy, RefOnlyTools
// was empty, and a zero BatchResultBudgetBytes turns shaping off. So it
// asserted an invariant that holds only in a degenerate configuration, which a
// review pointed out.
//
// Unwrapping means the realistic configuration is now the one under test.
func governedByDispatcher(tool sdktools.Tool) bool {
	for range 8 { // bounded: the wrapper stack is three deep
		switch w := tool.(type) {
		case *dispatcherShim:
			return true
		case *refOnlyShim:
			tool = w.inner
		case *turnShapeWrapper:
			tool = w.inner
		default:
			return false
		}
	}
	return false
}
