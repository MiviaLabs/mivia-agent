package controller

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

type panelProbeHandler struct{ called chan struct{} }

func (h panelProbeHandler) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	h.called <- struct{}{}
	return json.RawMessage(`{"ok":true}`), nil
}

type barrierProbeHandler struct {
	entered chan<- struct{}
	release <-chan struct{}
	active  atomic.Int32
}

type memberOutcomeHandler struct{ failed atomic.Bool }

func (h *memberOutcomeHandler) Invoke(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
	if h.failed.CompareAndSwap(false, true) {
		return nil, errors.New("member failed")
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (h *barrierProbeHandler) Invoke(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
	h.active.Add(1)
	defer h.active.Add(-1)
	h.entered <- struct{}{}
	select {
	case <-h.release:
		return json.RawMessage(`{"ok":true}`), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type probeMembers struct{ c coordinator.Coordinator }

func (p probeMembers) MemberNeedsActorPermit(context.Context, string, string) (bool, error) {
	return true, nil
}

type remoteJoinRaceMembers struct{ admitted chan<- struct{} }

func (remoteJoinRaceMembers) MemberNeedsActorPermit(context.Context, string, string) (bool, error) {
	return false, nil
}

func (remoteJoinRaceMembers) EnsureRemoteMember(context.Context, string, string) (*coordinator.RunHandle, error) {
	return nil, coordinator.ErrWaitOnlyJoinLost
}

func (m remoteJoinRaceMembers) EnsureMember(context.Context, string, string) (*coordinator.RunHandle, error) {
	m.admitted <- struct{}{}
	return nil, errors.New("admission stopped after permit check")
}

func (remoteJoinRaceMembers) JoinMember(context.Context, string, string, *coordinator.RunHandle) (*coordinator.RunResult, error) {
	return nil, nil
}

func TestRunPanelMembersAcquiresPermitWhenRemoteJoinIsLost(t *testing.T) {
	limiter := NewPanelActorLimiter()
	leases := make([]*panelActorLease, 4)
	for i := range leases {
		lease, err := limiter.Acquire(context.Background(), "occupied-"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		leases[i] = lease
	}
	admitted := make(chan struct{}, 1)
	type outcome struct {
		res PanelMembersResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := RunPanelMembers(context.Background(), limiter, PanelMembersRequest{
			AttemptID:   "attempt",
			Members:     []PanelMemberRequest{{MemberID: "member", RunID: "member-run"}},
			Coordinator: remoteJoinRaceMembers{admitted: admitted},
		})
		done <- outcome{res: res, err: err}
	}()
	select {
	case <-admitted:
		t.Fatal("local admission began without a panel permit")
	case <-time.After(50 * time.Millisecond):
	}
	leases[0].Release()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("local admission did not begin after a permit released")
	}
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("admission error should not fail the panel, got aggregate error: %v", out.err)
		}
		if len(out.res.Members) != 1 || out.res.Members[0].Err == nil {
			t.Fatal("admission error was not captured in the member's Err")
		}
	case <-time.After(time.Second):
		t.Fatal("panel member did not finish")
	}
	for _, lease := range leases[1:] {
		lease.Release()
	}
}

func TestRunPanelMembersReachesThreeMemberBarrier(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "agent", &barrierProbeHandler{entered: entered, release: release}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(ledger.NewMemoryLedgerRepository(), subagents.New(d, subagents.Policy{Workers: 3}))
	done := make(chan error, 1)
	go func() {
		_, err := RunPanelMembers(context.Background(), NewPanelActorLimiter(), PanelMembersRequest{
			AttemptID: "attempt", Members: []PanelMemberRequest{{MemberID: "a", RunID: "run-a"}, {MemberID: "b", RunID: "run-b"}, {MemberID: "c", RunID: "run-c"}}, Coordinator: probeMembers{c: c},
		})
		done <- err
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("member did not reach the barrier")
		}
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("members did not finish")
	}
}

func TestRunPanelMembersSharesFourSlotsAcrossPanels(t *testing.T) {
	entered := make(chan struct{}, 5)
	release := make(chan struct{})
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "agent", &barrierProbeHandler{entered: entered, release: release}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(ledger.NewMemoryLedgerRepository(), subagents.New(d, subagents.Policy{Workers: 5}))
	limiter := NewPanelActorLimiter()
	done := make(chan error, 2)
	for _, members := range [][]PanelMemberRequest{
		{{MemberID: "a", RunID: "run-a"}, {MemberID: "b", RunID: "run-b"}, {MemberID: "c", RunID: "run-c"}},
		{{MemberID: "d", RunID: "run-d"}, {MemberID: "e", RunID: "run-e"}},
	} {
		go func(members []PanelMemberRequest) {
			_, err := RunPanelMembers(context.Background(), limiter, PanelMembersRequest{AttemptID: "attempt", Members: members, Coordinator: probeMembers{c: c}})
			done <- err
		}(members)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("ready member did not enter")
		}
	}
	select {
	case <-entered:
		t.Fatal("fifth member entered before permit release")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("panel did not finish")
		}
	}
}

func TestRunPanelMembersRetainsSuccessfulSiblingsOnFailure(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "agent", &memberOutcomeHandler{}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(ledger.NewMemoryLedgerRepository(), subagents.New(d, subagents.Policy{Workers: 3}))
	result, err := RunPanelMembers(context.Background(), NewPanelActorLimiter(), PanelMembersRequest{
		AttemptID: "attempt", Members: []PanelMemberRequest{{MemberID: "a", RunID: "run-a"}, {MemberID: "b", RunID: "run-b"}, {MemberID: "c", RunID: "run-c"}}, Coordinator: probeMembers{c: c},
	})
	if err != nil {
		t.Fatalf("member failure should not fail the panel, got aggregate error: %v", err)
	}
	if len(result.Members) != 3 {
		t.Fatalf("member results = %d, want 3", len(result.Members))
	}
	var succeeded, failed int
	for _, member := range result.Members {
		if member.Err != nil {
			failed++
			continue
		}
		if member.Result == nil || len(member.Result.Results) != 1 || member.Result.Results[0].Err != nil {
			t.Fatalf("successful member %q result = %+v", member.MemberID, member.Result)
		}
		succeeded++
	}
	if succeeded != 2 || failed != 1 {
		t.Fatalf("member outcomes succeeded=%d failed=%d, want 2/1", succeeded, failed)
	}
}

func (p probeMembers) EnsureMember(ctx context.Context, _ string, member string) (*coordinator.RunHandle, error) {
	return p.c.EnsureSingleTaskRun(ctx, coordinator.EnsureRunRequest{
		RunID: coordinator.NewRunID(), IdempotencyKey: "probe-member-" + member,
		Policy: ledger.RunPolicy{NoRetry: true, FailInterrupted: true},
		Tasks:  []subagents.Task{{ID: "task-" + member, Name: "agent", AgentName: "agent", AgentDigest: "sha256:test", Scope: "workflow-panel:" + member, Budget: 1, Input: json.RawMessage(`"task"`)}},
	})
}

func (p probeMembers) JoinMember(ctx context.Context, _ string, _ string, h *coordinator.RunHandle) (*coordinator.RunResult, error) {
	return p.c.Join(ctx, h)
}

func TestPanelRunnerCoordinatorProbe(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	called := make(chan struct{}, 4)
	if err := d.Register(runtime.Subagent, "agent", panelProbeHandler{called: called}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(ledger.NewMemoryLedgerRepository(), subagents.New(d, subagents.Policy{Workers: 1}))
	h, err := c.EnsureSingleTaskRun(context.Background(), coordinator.EnsureRunRequest{
		RunID: coordinator.NewRunID(), IdempotencyKey: "panel-probe", Policy: ledger.RunPolicy{NoRetry: true, FailInterrupted: true},
		Tasks: []subagents.Task{{ID: "task", Name: "agent", AgentName: "agent", AgentDigest: "sha256:test", Scope: "workflow-panel", Budget: 1, Input: json.RawMessage(`"task"`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	default:
		t.Fatal("handler was not called")
	}
	_, err = RunPanelMembers(context.Background(), NewPanelActorLimiter(), PanelMembersRequest{
		AttemptID: "attempt", Members: []PanelMemberRequest{{MemberID: "a", RunID: "run-a"}, {MemberID: "b", RunID: "run-b"}}, Coordinator: probeMembers{c: c},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-called:
		default:
			t.Fatal("runner did not call handler")
		}
	}
}
