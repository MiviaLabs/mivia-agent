package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
)

// PanelMemberCoordinator performs persisted panel child operations.
type PanelMemberCoordinator interface {
	EnsureMember(context.Context, string, string) (*coordinator.RunHandle, error)
	JoinMember(context.Context, string, string, *coordinator.RunHandle) (*coordinator.RunResult, error)
}

// PanelMemberPermitProbe avoids blocking a remote wait-only join behind local
// actors that hold the process-wide cap.
type PanelMemberPermitProbe interface {
	MemberNeedsActorPermit(context.Context, string, string) (bool, error)
}

type PanelMemberRemoteJoiner interface {
	EnsureRemoteMember(context.Context, string, string) (*coordinator.RunHandle, error)
}

// PanelMembersRequest names the exact already-admitted member work.
type PanelMembersRequest struct {
	AttemptID   string
	Members     []PanelMemberRequest
	Coordinator PanelMemberCoordinator
}

// PanelMemberRequest names one deterministic persisted child run.
type PanelMemberRequest struct {
	MemberID string
	RunID    string
}

// PanelMemberResult keeps one child outcome separate from all siblings.
type PanelMemberResult struct {
	MemberID string
	Result   *coordinator.RunResult
	Err      error
}

// PanelMembersResult contains every outcome, including successful siblings
// when another required member fails.
type PanelMembersResult struct {
	Members []PanelMemberResult
}

// RunPanelMembers starts every member concurrently and waits for each result.
// It never starts synthesis.
//
// RunPanelMembers is policy-agnostic about member outcomes: it returns every
// member result (success and failure alike) and never fails the whole panel on
// a member failure. Each member's failure is preserved in PanelMemberResult.Err
// for the caller to act on; the caller applies the failure policy. RunPanelMembers
// only returns a non-nil error for request-level problems (nil limiter or
// coordinator, empty attempt ID, or no members), which are rejected up front.
func RunPanelMembers(ctx context.Context, limiter *PanelActorLimiter, req PanelMembersRequest) (PanelMembersResult, error) {
	if limiter == nil || req.Coordinator == nil || req.AttemptID == "" || len(req.Members) == 0 {
		return PanelMembersResult{}, fmt.Errorf("panel member request is incomplete")
	}
	results := make([]PanelMemberResult, len(req.Members))
	var wg sync.WaitGroup
	for index, member := range req.Members {
		wg.Add(1)
		go func(index int, member PanelMemberRequest) {
			defer wg.Done()
			results[index] = runOnePanelMember(ctx, limiter, req, member)
		}(index, member)
	}
	wg.Wait()
	return PanelMembersResult{Members: results}, nil
}

func runOnePanelMember(ctx context.Context, limiter *PanelActorLimiter, req PanelMembersRequest, member PanelMemberRequest) PanelMemberResult {
	result := PanelMemberResult{MemberID: member.MemberID}
	if member.MemberID == "" || member.RunID == "" {
		result.Err = fmt.Errorf("panel member identity is incomplete")
		return result
	}
	lease, err := acquirePanelMemberPermit(ctx, limiter, req, member)
	if err != nil {
		result.Err = err
		return result
	}
	handle, lease, err := ensurePanelMember(ctx, limiter, req, member, lease)
	if err != nil {
		if lease != nil {
			lease.ReleaseBeforeActor()
		}
		result.Err = err
		return result
	}
	if err := attachPanelMemberLease(member, lease, handle); err != nil {
		result.Err = err
		return result
	}
	result.Result, result.Err = req.Coordinator.JoinMember(ctx, req.AttemptID, member.MemberID, handle)
	if result.Err == nil {
		result.Err = panelMemberResultError(result.Result)
	}
	return result
}

func acquirePanelMemberPermit(ctx context.Context, limiter *PanelActorLimiter, req PanelMembersRequest, member PanelMemberRequest) (*panelActorLease, error) {
	probe, ok := req.Coordinator.(PanelMemberPermitProbe)
	if !ok {
		return acquireGuardedPanelMemberPermit(ctx, limiter, member.RunID)
	}
	needsPermit, err := probe.MemberNeedsActorPermit(ctx, req.AttemptID, member.MemberID)
	if err != nil || !needsPermit {
		return nil, err
	}
	return acquireGuardedPanelMemberPermit(ctx, limiter, member.RunID)
}

// acquireGuardedPanelMemberPermit wraps limiter.Acquire with an explicit,
// deterministic ctx-cancellation check immediately after a lease is granted.
// Go's select does not prioritize an already-closed ctx.Done() case over a
// simultaneously ready send (limiter.Acquire's internal select races
// l.slots<-struct{}{} against ctx.Done()), so a permit wait whose ctx was
// canceled at the exact moment a slot freed up can still win the select and
// return a valid lease. Without this guard, every caller here would go on to
// call runnable admission (EnsureMember) on a canceled ctx, violating "a
// canceled permit waiter never calls runnable admission" (required test
// matrix item). ReleaseBeforeActor safely returns the slot without ever
// attaching a local actor to it.
func acquireGuardedPanelMemberPermit(ctx context.Context, limiter *PanelActorLimiter, runID string) (*panelActorLease, error) {
	lease, err := limiter.Acquire(ctx, runID)
	if err != nil {
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		lease.ReleaseBeforeActor()
		return nil, ctxErr
	}
	return lease, nil
}

func ensurePanelMember(ctx context.Context, limiter *PanelActorLimiter, req PanelMembersRequest, member PanelMemberRequest, lease *panelActorLease) (*coordinator.RunHandle, *panelActorLease, error) {
	if lease != nil {
		handle, err := req.Coordinator.EnsureMember(ctx, req.AttemptID, member.MemberID)
		return handle, lease, err
	}
	remote, ok := req.Coordinator.(PanelMemberRemoteJoiner)
	if !ok {
		handle, err := req.Coordinator.EnsureMember(ctx, req.AttemptID, member.MemberID)
		return handle, nil, err
	}
	handle, err := remote.EnsureRemoteMember(ctx, req.AttemptID, member.MemberID)
	if !errors.Is(err, coordinator.ErrWaitOnlyJoinLost) {
		return handle, nil, err
	}
	lease, err = acquireGuardedPanelMemberPermit(ctx, limiter, member.RunID)
	if err != nil {
		return nil, nil, err
	}
	handle, err = req.Coordinator.EnsureMember(ctx, req.AttemptID, member.MemberID)
	return handle, lease, err
}

func attachPanelMemberLease(member PanelMemberRequest, lease *panelActorLease, handle *coordinator.RunHandle) error {
	if handle == nil {
		if lease != nil {
			lease.ReleaseBeforeActor()
		}
		return fmt.Errorf("panel member %q has no run handle", member.MemberID)
	}
	if !handle.LocalActor() {
		if lease != nil {
			lease.ReleaseBeforeActor()
		}
		return nil
	}
	if lease == nil {
		return fmt.Errorf("panel member %q became local without a permit", member.MemberID)
	}
	lease.AttachLocal()
	go func() {
		<-handle.Done()
		lease.Release()
	}()
	return nil
}

func panelMemberResultError(result *coordinator.RunResult) error {
	if result == nil {
		return nil
	}
	if result.Err != nil {
		return result.Err
	}
	for _, child := range result.Results {
		if child.Err != nil {
			return child.Err
		}
		// A task can report a non-completed terminal status (failed,
		// timed_out, canceled, blocked) with Err == nil (mapStatus treats
		// Status as authoritative independent of Err). Wave 5's synthesis
		// envelope trusts every member result RunPanelMembers lets through,
		// so a non-completed status must fail here too, not only a non-nil
		// Err: otherwise a failed member's stale or partial Output could be
		// silently decoded into the host verdict.
		if child.Status != "completed" {
			return fmt.Errorf("panel member task %q ended with status %q, not completed", child.TaskID, child.Status)
		}
		// D14: a completed task with missing content is a panel failure, not
		// something to synthesize from. len(nil json.RawMessage) == 0, so this
		// also catches an unset Output field, not only an explicit "".
		if len(child.Output) == 0 {
			return fmt.Errorf("panel member task %q completed with no output content", child.TaskID)
		}
	}
	return nil
}
