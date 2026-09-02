package sdkadapter

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// A refused call must report itself to the operator's surfaces.
//
// A denial returns from this wrapper WITHOUT entering the dispatcher shim, so
// nothing recorded an outcome for the call. The agent loop's no-outcome
// fallback then emitted a tool_end reading "completed (duplicate)", which both
// the NDJSON status mapping and the TUI's own OK computation read as success:
// an operator refused a command and every viewer said it ran.
//
// These tests drive the REAL wrapper through ConvertToolRegistryWithAdmission
// rather than calling the reporting helper directly. A test that called the
// helper would pass with the call site deleted, which is the exact shape that
// has shipped several features dead in this module.

type deniableTool struct {
	mu  sync.Mutex
	ran bool
}

func (*deniableTool) Name() string               { return "deny_tool" }
func (*deniableTool) Description() string        { return "deny tool" }
func (*deniableTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*deniableTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "deny_tool"}
}
func (t *deniableTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.mu.Lock()
	t.ran = true
	t.mu.Unlock()
	return "ran", nil
}

type deniedReport struct {
	id, name, reason string
	fired            bool
}

// runDeniedCase wires the real admission wrapper with the given predicates and
// returns what RecordDenied was told, plus whether the inner tool ran.
func runDeniedCase(t *testing.T, pred AdmissionPredicates) (deniedReport, bool) {
	t.Helper()
	var mu sync.Mutex
	var got deniedReport
	pred.RecordDenied = func(id, name, reason string) {
		mu.Lock()
		defer mu.Unlock()
		got = deniedReport{id: id, name: name, reason: reason, fired: true}
	}

	cli := &deniableTool{}
	reg := tools.NewRegistry()
	reg.Register(cli)
	sdkReg, err := ConvertToolRegistryWithAdmission(reg, pred)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := sdkReg.Get("deny_tool")
	if !ok {
		t.Fatal("deny_tool not in sdk reg")
	}

	ctx := toolcallctx.WithToolCall(context.Background(), sdkshape.ToolCall{
		ID: "call-DENY-1", Name: "deny_tool", Index: 0, Arguments: []byte(`{}`),
	})
	out, err := wrapped.Run(ctx, sdktools.InOut{Value: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s, _ := out.Value.(string); s == "ran" {
		t.Fatal("the inner tool ran despite the denial")
	}

	cli.mu.Lock()
	ran := cli.ran
	cli.mu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	return got, ran
}

// TestEveryDenialPathReportsItself covers all three ways this wrapper refuses
// a call. Each returned the same model-visible refusal and told the operator's
// surfaces nothing.
func TestEveryDenialPathReportsItself(t *testing.T) {
	cases := []struct {
		name string
		pred AdmissionPredicates
	}{
		{"the gate denies", AdmissionPredicates{
			ApprovalGate: func(context.Context, string, json.RawMessage) ApprovalResult {
				return ApprovalResult{Approved: false, Err: "denied"}
			},
		}},
		{"the policy is deny", AdmissionPredicates{
			ApprovalPolicy: "deny",
			ApprovalGate: func(context.Context, string, json.RawMessage) ApprovalResult {
				t.Error("the gate was consulted under a deny policy")
				return ApprovalResult{Approved: true}
			},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ran := runDeniedCase(t, tc.pred)

			if ran {
				t.Error("the inner tool ran despite the denial")
			}
			if !got.fired {
				t.Fatal("the denial reported nothing; the loop's no-outcome fallback " +
					"then emits \"completed (duplicate)\", and every surface reads a " +
					"refused call as one that ran and succeeded")
			}
			if got.id != "call-DENY-1" {
				t.Errorf("reported id %q, want call-DENY-1 - without the call id the "+
					"report cannot be matched to the row it corrects", got.id)
			}
			if got.name != "deny_tool" {
				t.Errorf("reported name %q, want deny_tool", got.name)
			}
			if got.reason == "" {
				t.Error("reported an empty reason; the operator is shown a refusal with " +
					"no cause")
			}
		})
	}
}

// TestAnApprovedCallReportsNoDenial is the other direction: the report must be
// specific to a refusal, or every ordinary call would be marked failed.
func TestAnApprovedCallReportsNoDenial(t *testing.T) {
	var mu sync.Mutex
	var fired bool

	cli := &deniableTool{}
	reg := tools.NewRegistry()
	reg.Register(cli)
	sdkReg, err := ConvertToolRegistryWithAdmission(reg, AdmissionPredicates{
		ApprovalGate: func(context.Context, string, json.RawMessage) ApprovalResult {
			return ApprovalResult{Approved: true}
		},
		RecordDenied: func(string, string, string) {
			mu.Lock()
			fired = true
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, _ := sdkReg.Get("deny_tool")
	ctx := toolcallctx.WithToolCall(context.Background(), sdkshape.ToolCall{
		ID: "call-OK-1", Name: "deny_tool", Index: 0, Arguments: []byte(`{}`),
	})
	if _, err := wrapped.Run(ctx, sdktools.InOut{Value: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if fired {
		t.Error("an approved call reported a denial; every ordinary tool call would " +
			"be marked failed")
	}
}

// TestADenyPolicyEnforcesItselfWithNoApprover is the fail-open the approval
// wiring shipped with.
//
// The adapter that holds the deny short-circuit was only built when an
// ApprovalGate was set, and a gate is set by the TUI and by nothing else. So
// on every headless surface - line mode, --json, a sidecar - configuring
// `[approvals] default_mode = "deny"` built no adapter, consulted no policy,
// and ran every write tool. The operator who chose the most restrictive
// setting got the least restrictive behaviour, with no error to notice.
func TestADenyPolicyEnforcesItselfWithNoApprover(t *testing.T) {
	cli := &deniableTool{}
	reg := tools.NewRegistry()
	reg.Register(cli)

	// Exactly the headless shape: a deny policy and NO gate.
	sdkReg, err := ConvertToolRegistryWithAdmission(reg, AdmissionPredicates{
		ApprovalPolicy: "deny",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := sdkReg.Get("deny_tool")
	if !ok {
		t.Fatal("deny_tool not in sdk reg")
	}

	ctx := toolcallctx.WithToolCall(context.Background(), sdkshape.ToolCall{
		ID: "call-HEADLESS-1", Name: "deny_tool", Index: 0, Arguments: []byte(`{}`),
	})
	out, err := wrapped.Run(ctx, sdktools.InOut{Value: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	cli.mu.Lock()
	ran := cli.ran
	cli.mu.Unlock()
	if ran {
		t.Fatal("a write tool RAN under an approval policy of \"deny\" because no " +
			"approver was attached; the policy is fail-open on every headless surface")
	}
	if s, _ := out.Value.(string); !strings.Contains(s, "denied") {
		t.Errorf("the model was told %q, want a refusal", s)
	}
}

// TestAnApprovalPolicyWithNoApproverDeniesRatherThanRuns holds the direction
// for the other policies. The absence of an approver must never read as
// approval.
func TestAnApprovalPolicyWithNoApproverDeniesRatherThanRuns(t *testing.T) {
	adapter := &approvalGatedToolAdapter{
		inner:           &denyingInner{},
		cliName:         "deny_tool",
		policy:          "always",
		getCapabilities: func(json.RawMessage) tools.Capability { return tools.Capability{Class: tools.ExecutionWrite} },
	}

	out, err := adapter.Run(context.Background(), sdktools.InOut{Value: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s, _ := out.Value.(string); !strings.Contains(s, "denied") {
		t.Errorf("a call needing approval with no approver returned %q; the absence "+
			"of an approver must not read as approval", s)
	}
}

// denyingInner fails the test if it is ever reached.
type denyingInner struct{}

func (*denyingInner) Name() string        { return "deny_tool" }
func (*denyingInner) Description() string { return "" }
func (*denyingInner) Schema() map[string]any {
	return map[string]any{"type": "object"}
}
func (*denyingInner) Run(context.Context, sdktools.InOut) (sdktools.Out, error) {
	return sdktools.Out{Value: "ran"}, nil
}
