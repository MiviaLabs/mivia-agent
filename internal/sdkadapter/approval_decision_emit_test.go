package sdkadapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestDecideApprovalEmitsPendingOnlyWhenPrompting pins the emission
// contract the TUI's approval queue and the plain renderer's "? approve"
// line both stand on: EmitPending fires if and only if a live approver is
// about to be asked. Auto-approve and deny policies, standing decisions,
// a missing gate, and read-class bypasses all decide WITHOUT emitting.
//
// A pending event that fires with nobody prompted is not a cosmetic
// glitch: the transcript arms a live row from it, the approval queue
// arms a prompt from it, and the piped log prints "? approve <tool>" -
// every surface then claims a human is in the loop for a call that was
// never shown to one. The short-circuits above the EmitPending call are
// load-bearing, so they are pinned here at the one decision site.
func TestDecideApprovalEmitsPendingOnlyWhenPrompting(t *testing.T) {
	const (
		callID = "call-1"
		name   = "run_command"
		args   = `{"command":"go vet ./..."}`
	)
	writeClass := tools.ExecutionWrite
	approving := func(ctx context.Context, n string, a json.RawMessage) ApprovalResult {
		return ApprovalResult{Approved: true}
	}

	t.Run("decides without emitting", func(t *testing.T) {
		assertDecidesWithoutEmitting(t, callID, name, args, writeClass, approving)
	})

	t.Run("a prompted call emits exactly once, before the gate", func(t *testing.T) {
		assertPromptedEmitsOnceBeforeGate(t, callID, name, args, writeClass)
	})
}

// assertDecidesWithoutEmitting walks every short-circuit in
// DecideApproval and holds the no-emission side of the contract.
func assertDecidesWithoutEmitting(t *testing.T, callID, name, args string, writeClass tools.ExecutionClass, approving gateFunc) {
	t.Helper()
	standing := func(allow bool) *ApprovalStanding {
		st := &ApprovalStanding{}
		key := StandingKey{Name: name, Class: writeClass, ResourceKey: "k", Args: json.RawMessage(args)}
		if allow {
			st.Allow(key)
		} else {
			st.Deny(key)
		}
		return st
	}
	cases := []struct {
		verdict      string
		policy       string
		st           *ApprovalStanding
		class        tools.ExecutionClass
		tool         string
		gate         gateFunc
		wantApproved bool
		wantReason   string
	}{
		{"auto policy", "auto", nil, writeClass, name, approving, true, ""},
		{"deny policy", "deny", nil, writeClass, name, approving, false, "auto-denied"},
		{"standing allow", "", standing(true), writeClass, name, approving, true, ""},
		{"standing deny", "", standing(false), writeClass, name, approving, false, "standing decision"},
		{"no approver", "", nil, writeClass, name, nil, false, "no approver"},
		{"read class bypass", "", nil, tools.ExecutionRead, "read_file", approving, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.verdict, func(t *testing.T) {
			var r pendingRecord
			d := DecideApproval(context.Background(), ApprovalDeps{
				Policy: tc.policy, Standing: tc.st, Gate: tc.gate, EmitPending: r.emit(),
			}, ApprovalRequest{ToolCallID: callID, Name: tc.tool, Class: tc.class, ResourceKey: "k", Args: json.RawMessage(args)})
			if d.Approved != tc.wantApproved {
				t.Fatalf("decision = %+v, want approved=%v", d, tc.wantApproved)
			}
			if tc.wantReason != "" && !strings.Contains(d.Reason, tc.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", d.Reason, tc.wantReason)
			}
			if r.calls != 0 {
				t.Errorf("EmitPending fired %d times (%s); a pending event claims a human was asked", r.calls, tc.verdict)
			}
		})
	}
}

// assertPromptedEmitsOnceBeforeGate holds the emitting side: a call that
// needs a live decision announces itself exactly once, carrying the call
// id the UI resolves by, before the gate is consulted.
func assertPromptedEmitsOnceBeforeGate(t *testing.T, callID, name, args string, writeClass tools.ExecutionClass) {
	t.Helper()
	var r pendingRecord
	gateCalls := 0
	d := DecideApproval(context.Background(), ApprovalDeps{
		Gate: func(ctx context.Context, n string, a json.RawMessage) ApprovalResult {
			gateCalls++
			if r.calls != 1 {
				t.Errorf("gate invoked with %d pending emissions, want exactly 1 before it", r.calls)
			}
			return ApprovalResult{Approved: true}
		},
		EmitPending: r.emit(),
	}, ApprovalRequest{ToolCallID: callID, Name: name, Class: writeClass, Args: json.RawMessage(args)})
	if !d.Approved {
		t.Fatalf("decision = %+v, want the approving gate to approve", d)
	}
	if r.calls != 1 {
		t.Errorf("EmitPending fired %d times, want exactly once", r.calls)
	}
	if r.id != callID || r.n != name {
		t.Errorf("pending announced id=%q name=%q, want %q/%q (the UI resolves the decision by the call id)", r.id, r.n, callID, name)
	}
	if gateCalls != 1 {
		t.Errorf("gate invoked %d times, want once", gateCalls)
	}
}

// gateFunc is DecideApproval's Gate signature.
type gateFunc = func(context.Context, string, json.RawMessage) ApprovalResult

// pendingRecord counts EmitPending invocations and keeps the last
// announcement for field assertions.
type pendingRecord struct {
	calls int
	id, n string
}

func (r *pendingRecord) emit() func(toolCallID, name, detail, input string) {
	return func(id, name, detail, input string) {
		r.calls++
		r.id, r.n = id, name
	}
}
