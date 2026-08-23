package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// gateSeq is a shared ordering counter so tests can prove EventToolPending
// fires BEFORE the gate invocation.
type gateSeq struct{ n int }

func (s *gateSeq) next() int { s.n++; return s.n }

// recordingGate is a scriptable Options.ApprovalGate that records each
// call (with its sequence number) and returns the scripted verdict.
type recordingGate struct {
	mu      sync.Mutex
	calls   []string
	seqs    []int
	verdict sdkadapter.ApprovalResult
	onCall  func()
	seq     *gateSeq
}

func (g *recordingGate) gate(_ context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
	g.mu.Lock()
	g.calls = append(g.calls, name+"("+string(args)+")")
	n := 0
	if g.seq != nil {
		n = g.seq.next()
	}
	g.seqs = append(g.seqs, n)
	cb := g.onCall
	g.mu.Unlock()
	if cb != nil {
		cb()
	}
	return g.verdict
}

func (g *recordingGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *recordingGate) lastSeq() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.seqs) == 0 {
		return 0
	}
	return g.seqs[len(g.seqs)-1]
}

// approvalEnv bundles the shared fixtures of the legacy-gate tests.
type approvalEnv struct {
	reg        *tools.Registry
	started    *atomic.Int32
	disp       interface{ Close() }
	gate       *recordingGate
	pendingSeq map[string]int // ToolCallID -> seq of its EventToolPending
	seq        *gateSeq
}

func newApprovalEnv(t *testing.T, class tools.ExecutionClass) *approvalEnv {
	t.Helper()
	started := &atomic.Int32{}
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{
		name: "gated", class: class, key: "path:gated",
		delay: time.Millisecond, started: started,
	})
	env := &approvalEnv{reg: reg, started: started, seq: &gateSeq{}, pendingSeq: map[string]int{}}
	env.gate = &recordingGate{verdict: sdkadapter.ApprovalResult{Approved: true}, seq: env.seq}
	return env
}

// runOneGatedCall drives one write-shaped call through the batch.
func (e *approvalEnv) runOneGatedCall(t *testing.T, opts Options) []toolExecResult {
	t.Helper()
	results := executeToolsParallel(context.Background(), []provider.ToolCall{
		tc("call-1", "gated", `{"path":"a.txt"}`),
	}, e.reg, opts)
	return results
}

// approvalOptions builds Options with the gate and the pending-event
// recorder installed.
func (e *approvalEnv) approvalOptions(standing *sdkadapter.ApprovalStanding) Options {
	return Options{
		TurnID: "turn:1", ParentID: "session", Step: 1,
		ApprovalGate:     e.gate.gate,
		ApprovalStanding: standing,
		OnEvent: func(ev Event) {
			if ev.Kind == EventToolPending {
				e.pendingSeq[ev.ToolCallID] = e.seq.next()
			}
		},
	}
}

// TestGateToolApprovalWriteClassPromptsAndApprovedCallRuns pins the
// happy path: the gate fires once for a write-class call with the
// exact name and raw args, EventToolPending precedes the gate, and an
// approved call executes.
func TestGateToolApprovalWriteClassPromptsAndApprovedCallRuns(t *testing.T) {
	env := newApprovalEnv(t, tools.ExecutionWrite)
	results := env.runOneGatedCall(t, env.approvalOptions(nil))
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("results=%+v err=%v, want one clean result", results, results[0].err)
	}
	if got := env.gate.count(); got != 1 {
		t.Fatalf("gate calls = %d, want 1", got)
	}
	if got := env.started.Load(); got != 1 {
		t.Fatalf("handler runs = %d, want 1 after approval", got)
	}
	if seq, ok := env.pendingSeq["call-1"]; !ok || seq == 0 {
		t.Fatalf("EventToolPending never fired for call-1")
	} else if seq >= env.gate.lastSeq() {
		t.Fatalf("pending seq %d not before gate seq %d", seq, env.gate.lastSeq())
	}
}

// TestGateToolApprovalDenialFailsToolTaskWithDenialText pins the
// missing-failure-recording class of bug: a denied call must leave a
// failed result carrying the denial text and must not run the tool.
func TestGateToolApprovalDenialFailsToolTaskWithDenialText(t *testing.T) {
	env := newApprovalEnv(t, tools.ExecutionWrite)
	env.gate.verdict = sdkadapter.ApprovalResult{Err: "not this file"}
	results := env.runOneGatedCall(t, env.approvalOptions(nil))
	if results[0].err == nil {
		t.Fatalf("denied call err = nil, want a failed result")
	}
	if !strings.Contains(results[0].result, "tool call denied by user: not this file") {
		t.Fatalf("result = %q, want the denial text", results[0].result)
	}
	if got := env.started.Load(); got != 0 {
		t.Fatalf("handler ran %d times after denial, want 0", got)
	}
}

// TestGateToolApprovalDenialWithoutErrUsesDefaultText pins the empty-Err
// fallback: the denial text uses "denied".
func TestGateToolApprovalDenialWithoutErrUsesDefaultText(t *testing.T) {
	env := newApprovalEnv(t, tools.ExecutionWrite)
	env.gate.verdict = sdkadapter.ApprovalResult{}
	results := env.runOneGatedCall(t, env.approvalOptions(nil))
	if !strings.Contains(results[0].result, "tool call denied by user: denied") {
		t.Fatalf("result = %q, want the default denial text", results[0].result)
	}
}

// TestGateToolApprovalReadClassSkipsGate pins the threshold:
// read-class tools never prompt.
func TestGateToolApprovalReadAndUnclassifiedSkipGate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class tools.ExecutionClass
	}{
		{"read", tools.ExecutionRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newApprovalEnv(t, tc.class)
			results := env.runOneGatedCall(t, env.approvalOptions(nil))
			if results[0].err != nil {
				t.Fatalf("err = %v, want clean run", results[0].err)
			}
			if got := env.gate.count(); got != 0 {
				t.Fatalf("gate calls = %d, want 0 for %s class", got, tc.name)
			}
			if len(env.pendingSeq) != 0 {
				t.Fatalf("EventToolPending fired %d times for %s class, want 0", len(env.pendingSeq), tc.name)
			}
			if got := env.started.Load(); got != 1 {
				t.Fatalf("handler runs = %d, want 1", got)
			}
		})
	}
}

// TestGateToolApprovalNilGateApprovesAll pins pre-gate behavior: a nil
// ApprovalGate (and nil standing) runs every tool without prompting.
func TestGateToolApprovalNilGateApprovesAll(t *testing.T) {
	env := newApprovalEnv(t, tools.ExecutionWrite)
	results := env.runOneGatedCall(t, Options{TurnID: "t", ParentID: "s", Step: 1})
	if results[0].err != nil {
		t.Fatalf("err = %v, want clean run under nil gate", results[0].err)
	}
	if got := env.started.Load(); got != 1 {
		t.Fatalf("handler runs = %d, want 1 under nil gate", got)
	}
}

// TestGateToolApprovalStandingAllowShortCircuitsGate pins the "always
// approve" contract: ApprovedForClass persists, and a second identical
// call runs without consulting the gate again.
func TestGateToolApprovalStandingAllowShortCircuitsGate(t *testing.T) {
	env := newApprovalEnv(t, tools.ExecutionWrite)
	env.gate.verdict = sdkadapter.ApprovalResult{Approved: true, ApprovedForClass: true}
	standing := sdkadapter.NewApprovalStanding()
	opts := env.approvalOptions(standing)
	for i := 0; i < 2; i++ {
		results := env.runOneGatedCall(t, opts)
		if results[0].err != nil {
			t.Fatalf("run %d err = %v", i, results[0].err)
		}
	}
	if got := env.gate.count(); got != 1 {
		t.Fatalf("gate calls = %d, want 1 (second call short-circuits)", got)
	}
	if approved, ok := standing.Lookup("gated"); !ok || !approved {
		t.Fatalf("standing lookup = (%v,%v), want (true,true)", approved, ok)
	}
}

// TestGateToolApprovalStandingDenyFailsWithoutInvokingGate pins the
// "always deny" contract: a pre-seeded deny fails the call without a
// gate invocation or a pending event.
func TestGateToolApprovalStandingDenyFailsWithoutInvokingGate(t *testing.T) {
	env := newApprovalEnv(t, tools.ExecutionWrite)
	standing := sdkadapter.NewApprovalStanding()
	standing.Deny("gated", tools.ExecutionWrite)
	results := env.runOneGatedCall(t, env.approvalOptions(standing))
	if results[0].err == nil {
		t.Fatalf("standing-deny err = nil, want failed result")
	}
	if !strings.Contains(results[0].result, "standing decision") {
		t.Fatalf("result = %q, want the standing-decision denial text", results[0].result)
	}
	if got := env.gate.count(); got != 0 {
		t.Fatalf("gate calls = %d, want 0 under standing deny", got)
	}
	if len(env.pendingSeq) != 0 {
		t.Fatalf("EventToolPending fired under standing deny, want none")
	}
}

// TestGateToolApprovalPostApprovalContextCancelFailsTask pins the
// cancel branch: an approval racing a canceled context fails the task
// with context.Canceled instead of proceeding.
func TestGateToolApprovalPostApprovalContextCancelFailsTask(t *testing.T) {
	env := newApprovalEnv(t, tools.ExecutionWrite)
	ctx, cancel := context.WithCancel(context.Background())
	env.gate.onCall = cancel
	results := executeToolsParallel(ctx, []provider.ToolCall{
		tc("call-1", "gated", `{"path":"a.txt"}`),
	}, env.reg, env.approvalOptions(nil))
	if !errors.Is(results[0].err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", results[0].err)
	}
	if got := env.started.Load(); got != 0 {
		t.Fatalf("handler ran %d times after cancel, want 0", got)
	}
}
