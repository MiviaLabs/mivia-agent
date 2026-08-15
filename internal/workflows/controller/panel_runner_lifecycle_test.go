package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

type failedPanelAdmission struct{}

func (failedPanelAdmission) EnsureMember(context.Context, string, string) (*coordinator.RunHandle, error) {
	return nil, errors.New("admission failed")
}

func (failedPanelAdmission) JoinMember(context.Context, string, string, *coordinator.RunHandle) (*coordinator.RunResult, error) {
	return nil, nil
}

func TestRunPanelMembersReleasesPermitAfterAdmissionFailure(t *testing.T) {
	limiter := NewPanelActorLimiter()
	result, err := RunPanelMembers(context.Background(), limiter, PanelMembersRequest{
		AttemptID: "attempt", Members: []PanelMemberRequest{{MemberID: "member", RunID: "run-member"}}, Coordinator: failedPanelAdmission{},
	})
	if err != nil {
		t.Fatalf("admission failure should not fail the panel, got aggregate error: %v", err)
	}
	if len(result.Members) != 1 || result.Members[0].Err == nil {
		t.Fatal("panel admission failure was not captured in the member's Err")
	}
	leases := make([]*panelActorLease, 4)
	for i := range leases {
		lease, acquireErr := limiter.Acquire(context.Background(), "after-failure-"+string(rune('a'+i)))
		if acquireErr != nil {
			t.Fatalf("permit %d after admission failure: %v", i, acquireErr)
		}
		leases[i] = lease
	}
	for _, lease := range leases {
		lease.Release()
	}
}

func TestRunPanelMembersCancelStopsPermitWait(t *testing.T) {
	limiter := NewPanelActorLimiter()
	leases := make([]*panelActorLease, 4)
	for i := range leases {
		lease, err := limiter.Acquire(context.Background(), "occupied-"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		leases[i] = lease
	}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		res PanelMembersResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := RunPanelMembers(ctx, limiter, PanelMembersRequest{
			AttemptID: "attempt", Members: []PanelMemberRequest{{MemberID: "member", RunID: "run-member"}}, Coordinator: failedPanelAdmission{},
		})
		done <- outcome{res: res, err: err}
	}()
	cancel()
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("canceled member should not fail the panel, got aggregate error: %v", out.err)
		}
		if len(out.res.Members) != 1 {
			t.Fatalf("member results = %d, want 1", len(out.res.Members))
		}
		if !errors.Is(out.res.Members[0].Err, context.Canceled) {
			t.Fatalf("canceled member Err = %v, want context canceled", out.res.Members[0].Err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop the permit wait")
	}
	for _, lease := range leases {
		lease.Release()
	}
}

type timeoutPanelHandler struct{}

func (timeoutPanelHandler) Invoke(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type timeoutPanelMember struct{ c coordinator.Coordinator }

func (p timeoutPanelMember) EnsureMember(ctx context.Context, _ string, member string) (*coordinator.RunHandle, error) {
	return p.c.EnsureSingleTaskRun(ctx, coordinator.EnsureRunRequest{
		RunID: coordinator.NewRunID(), IdempotencyKey: "timeout-panel-" + member,
		Policy: ledger.RunPolicy{NoRetry: true, FailInterrupted: true},
		Tasks:  []subagents.Task{{ID: "task-" + member, Name: "timeout", AgentName: "timeout", AgentDigest: "sha256:timeout", Scope: "panel-timeout:" + member, Timeout: 20 * time.Millisecond, Budget: 1, Input: json.RawMessage(`"task"`)}},
	})
}

func (p timeoutPanelMember) JoinMember(ctx context.Context, _ string, _ string, handle *coordinator.RunHandle) (*coordinator.RunResult, error) {
	return p.c.Join(ctx, handle)
}

func TestRunPanelMembersReportsMemberTimeout(t *testing.T) {
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "timeout", timeoutPanelHandler{}); err != nil {
		t.Fatal(err)
	}
	coord := coordinator.New(ledger.NewMemoryLedgerRepository(), subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	result, err := RunPanelMembers(context.Background(), NewPanelActorLimiter(), PanelMembersRequest{
		AttemptID: "attempt", Members: []PanelMemberRequest{{MemberID: "member", RunID: "run-member"}}, Coordinator: timeoutPanelMember{c: coord},
	})
	if err != nil {
		t.Fatalf("member timeout should not fail the panel, got aggregate error: %v", err)
	}
	if len(result.Members) != 1 {
		t.Fatalf("member results = %d, want 1", len(result.Members))
	}
	if result.Members[0].Err == nil {
		t.Fatal("member timeout was not captured in the member's Err")
	}
}

// Bug-audit regression: subagents.Result.Status is authoritative independent
// of Err (coordinator.mapStatus falls back to Err only when Status is
// unset). A non-completed status with a nil Err must still fail the panel,
// or Wave 5's synthesis envelope would silently decode a failed member's
// stale or partial Output into the host verdict.
func TestPanelMemberResultErrorRejectsNonCompletedStatusWithNilErr(t *testing.T) {
	for _, status := range []string{"failed", "timed_out", "canceled", "blocked"} {
		result := &coordinator.RunResult{Results: []subagents.Result{
			{TaskID: "task-1", Status: status, Err: nil, Output: json.RawMessage(`{"verdict":"approved","findings":[]}`)},
		}}
		if err := panelMemberResultError(result); err == nil {
			t.Fatalf("status %q with nil Err: panelMemberResultError() = nil, want an error", status)
		}
	}
	completed := &coordinator.RunResult{Results: []subagents.Result{
		{TaskID: "task-1", Status: "completed", Err: nil, Output: json.RawMessage(`{"verdict":"approved","findings":[]}`)},
	}}
	if err := panelMemberResultError(completed); err != nil {
		t.Fatalf("status completed: panelMemberResultError() = %v, want nil", err)
	}
}

// D14: a completed coordinator task with missing content is a panel
// failure, not something to synthesize from.
func TestPanelMemberResultErrorRejectsCompletedWithEmptyOutput(t *testing.T) {
	for name, output := range map[string]json.RawMessage{"nil": nil, "empty": json.RawMessage("")} {
		t.Run(name, func(t *testing.T) {
			result := &coordinator.RunResult{Results: []subagents.Result{
				{TaskID: "task-1", Status: "completed", Err: nil, Output: output},
			}}
			if err := panelMemberResultError(result); err == nil {
				t.Fatal("completed with no output content: panelMemberResultError() = nil, want an error")
			}
		})
	}
}
