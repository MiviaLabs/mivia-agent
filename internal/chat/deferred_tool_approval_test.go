package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// The deferred-tool path invokes the runtime dispatcher DIRECTLY, underneath
// the SDK registry where the approval wrapper lives. It therefore carried no
// approval at all: a threat model drove a write tool through it and watched
// the file appear under a "deny" policy with a live approver attached.

// writingTool is Write-class, so any policy but "auto" must decide about it.
type writingTool struct{ ran bool }

func (*writingTool) Name() string               { return "write_file" }
func (*writingTool) Description() string        { return "writes" }
func (*writingTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*writingTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "write_file"}
}
func (t *writingTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.ran = true
	return "WROTE THE FILE", nil
}

// TestTheDeferredPathRefusesUnderADenyPolicy is the reproduction.
func TestTheDeferredPathRefusesUnderADenyPolicy(t *testing.T) {
	var gateCalls int
	s := &Session{
		ApprovalPolicy: "deny",
		ApprovalGate: func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
			gateCalls++
			return sdkadapter.ApprovalResult{Approved: true}
		},
	}

	got := s.decideDeferredApproval(context.Background(), &writingTool{}, "write_file", json.RawMessage(`{}`))

	if got.Approved {
		t.Fatal("a write tool was approved on the deferred path under a \"deny\" " +
			"policy; the operator's most restrictive setting was bypassed by the " +
			"one route that does not go through the approval wrapper")
	}
	if gateCalls != 0 {
		t.Errorf("the gate was consulted %d times under a deny policy", gateCalls)
	}
	if !strings.Contains(got.Reason, "deny") {
		t.Errorf("reason = %q, want it to name the policy", got.Reason)
	}
}

// TestTheDeferredPathDeniesWithNoApprover holds the fail-closed direction on
// this path too: a policy that needs a decision, and nobody to ask.
func TestTheDeferredPathDeniesWithNoApprover(t *testing.T) {
	s := &Session{ApprovalPolicy: "write-only"}

	got := s.decideDeferredApproval(context.Background(), &writingTool{}, "write_file", json.RawMessage(`{}`))

	if got.Approved {
		t.Error("a write tool ran on the deferred path with no approver attached; " +
			"the absence of an approver must never read as approval")
	}
}

// TestTheDeferredPathAsksTheGate proves it does not simply refuse everything -
// an operator who approves must get their tool.
func TestTheDeferredPathAsksTheGate(t *testing.T) {
	var asked string
	s := &Session{
		ApprovalPolicy: "write-only",
		ApprovalGate: func(_ context.Context, name string, _ json.RawMessage) sdkadapter.ApprovalResult {
			asked = name
			return sdkadapter.ApprovalResult{Approved: true}
		},
	}

	got := s.decideDeferredApproval(context.Background(), &writingTool{}, "write_file", json.RawMessage(`{}`))

	if !got.Approved {
		t.Errorf("an approved call was refused: %q", got.Reason)
	}
	if asked != "write_file" {
		t.Errorf("the gate was asked about %q, want write_file", asked)
	}
}

// TestTheDeferredPathRunsUnderAutoWithoutAsking keeps the shipped default
// unchanged: auto means run, and adding a prompt there would be a regression
// for every user who never configured approvals.
func TestTheDeferredPathRunsUnderAutoWithoutAsking(t *testing.T) {
	var gateCalls int
	s := &Session{
		ApprovalPolicy: "auto",
		ApprovalGate: func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
			gateCalls++
			return sdkadapter.ApprovalResult{Approved: false}
		},
	}

	got := s.decideDeferredApproval(context.Background(), &writingTool{}, "write_file", json.RawMessage(`{}`))

	if !got.Approved {
		t.Error("the auto policy refused a call; the shipped default must be unchanged")
	}
	if gateCalls != 0 {
		t.Errorf("the gate was consulted %d times under auto", gateCalls)
	}
}

// TestAnUnsetPolicyOnTheDeferredPathStillDecides covers the session that
// carries no policy at all - which a threat model found /new and /resume
// produce. On this path an unset policy must not mean "run".
func TestAnUnsetPolicyOnTheDeferredPathStillDecides(t *testing.T) {
	s := &Session{}

	got := s.decideDeferredApproval(context.Background(), &writingTool{}, "write_file", json.RawMessage(`{}`))

	if got.Approved {
		t.Error("a session with no approval policy ran a write tool on the deferred " +
			"path; an unset policy is not a licence to execute")
	}
}

// TestRunDeferredToolNowItselfRefuses drives the REAL deferred path, not the
// decision helper.
//
// Deleting the guard from runDeferredToolNow leaves every test above green,
// because they all call decideDeferredApproval directly. That is the shape
// that has shipped several things dead in this repo, so the call site gets its
// own test that executes it.
func TestRunDeferredToolNowItselfRefuses(t *testing.T) {
	tool := &writingTool{}
	reg := tools.NewRegistry()
	reg.Register(tool)
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)

	s := &Session{ApprovalPolicy: "deny"}
	content, _, _, ok := s.runDeferredToolNow(
		context.Background(), d, func() *tools.Registry { return reg },
		"sess-1", 1, "write_file", json.RawMessage(`{}`),
	)

	if tool.ran {
		t.Fatal("the deferred path EXECUTED a write tool under a \"deny\" policy; " +
			"this is the route that bypasses the approval wrapper entirely")
	}
	if !ok {
		t.Fatal("the refusal was not reported back to the loop, so the model gets " +
			"no result for a call it made")
	}
	if !strings.Contains(content, "denied") {
		t.Errorf("the model was told %q, want a refusal", content)
	}
}

// TestRunDeferredToolNowRunsWhenApproved is the other direction on the real
// path: an approved call must still execute and return its output.
func TestRunDeferredToolNowRunsWhenApproved(t *testing.T) {
	tool := &writingTool{}
	reg := tools.NewRegistry()
	reg.Register(tool)
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)

	s := &Session{ApprovalPolicy: "auto"}
	content, _, _, ok := s.runDeferredToolNow(
		context.Background(), d, func() *tools.Registry { return reg },
		"sess-1", 1, "write_file", json.RawMessage(`{}`),
	)

	if !ok {
		t.Fatal("the deferred path reported no result for an approved call")
	}
	if !tool.ran {
		t.Error("an approved call did not run")
	}
	if !strings.Contains(content, "WROTE") {
		t.Errorf("content = %q, want the tool's own output", content)
	}
}
