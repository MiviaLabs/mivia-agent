package subagents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// fixedNow is the controllable clock for the hook tests.
type fixedNow struct{ t time.Time }

func (f *fixedNow) Now() time.Time { return f.t }

func deadlineCtx(startedAt time.Time, total time.Duration, now *fixedNow) (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.Background(), startedAt.Add(total))
}

// TestDeadlineNoticeFiresOnceAtThreshold pins the core contract: the hook is
// silent while more than the threshold fraction of the budget remains, fires
// EXACTLY ONE user-role notice at the first boundary inside it, and stays
// silent afterwards - a second notice would only spend the child's remaining
// steps on acknowledgements.
func TestDeadlineNoticeFiresOnceAtThreshold(t *testing.T) {
	startedAt := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	total := 20 * time.Minute
	clk := &fixedNow{t: startedAt}
	ctx, cancel := deadlineCtx(startedAt, total, clk)
	defer cancel()

	hook := deadlineNoticeBeforeStep(ctx, startedAt, clk.Now)

	// Half the budget left: above the quarter threshold, silent.
	clk.t = startedAt.Add(10 * time.Minute)
	if msgs := hook(); msgs != nil {
		t.Fatalf("hook fired above the threshold: %+v", msgs)
	}

	// One second past the quarter threshold: fire exactly once.
	clk.t = startedAt.Add(15*time.Minute + time.Second)
	msgs := hook()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages at the threshold, want exactly 1", len(msgs))
	}
	if msgs[0].Role != provider.RoleUser {
		t.Fatalf("notice role = %q, want user", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "DEADLINE") || !strings.Contains(msgs[0].Content, "final report") {
		t.Errorf("notice text = %q, want the deadline + partial-results contract", msgs[0].Content)
	}
	if want := (5*time.Minute - time.Second).String(); !strings.Contains(msgs[0].Content, want) {
		t.Errorf("notice %q missing the remaining budget %q", msgs[0].Content, want)
	}
	if again := hook(); again != nil {
		t.Fatalf("hook fired twice: %+v", again)
	}
}

// TestDeadlineNoticeNoDeadline is the unchanged-behavior half: without a
// deadline on the context the hook never fires, and a plain
// context.Background (no deadline possible at all) stays inert.
func TestDeadlineNoticeNoDeadline(t *testing.T) {
	hook := deadlineNoticeBeforeStep(context.Background(), time.Now(), nil)
	if msgs := hook(); msgs != nil {
		t.Fatalf("deadline-less hook fired: %+v", msgs)
	}

	startedAt := time.Now().Add(-time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), startedAt.Add(20*time.Minute))
	defer cancel()
	hook = deadlineNoticeBeforeStep(ctx, startedAt, nil) // real clock: far from the threshold
	if msgs := hook(); msgs != nil {
		t.Fatalf("hook fired with most of the budget remaining: %+v", msgs)
	}
}

// TestDeadlineNoticeAlreadyExpired pins the boundary handoff: once the
// deadline has passed, the loop's own deadline checks own the expiry path
// (the notice must not claim remaining time that is gone).
func TestDeadlineNoticeAlreadyExpired(t *testing.T) {
	startedAt := time.Now().Add(-30 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), startedAt.Add(20*time.Minute))
	defer cancel()
	hook := deadlineNoticeBeforeStep(ctx, startedAt, nil)
	if msgs := hook(); msgs != nil {
		t.Fatalf("expired hook fired: %+v", msgs)
	}
}

// TestApplyDeadlineNoticeComposesWithMailbox pins the additive contract: the
// deadline hook must never displace the mailbox drain - a just-arrived steer
// is processed first, and the deadline notice rides the same boundary.
func TestApplyDeadlineNoticeComposesWithMailbox(t *testing.T) {
	startedAt := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clk := &fixedNow{t: startedAt.Add(19 * time.Minute)} // inside the threshold
	ctx, cancel := context.WithDeadline(context.Background(), startedAt.Add(20*time.Minute))
	defer cancel()

	var drained int
	mailbox := func() []provider.Message {
		drained++
		return []provider.Message{{Role: provider.RoleUser, Content: "steer body"}}
	}
	opts := &agent.Options{BeforeStep: mailbox}
	applyDeadlineNotice(ctx, startedAt, clk.Now, opts)

	msgs := opts.BeforeStep()
	if drained != 1 {
		t.Fatalf("mailbox drained %d times, want 1", drained)
	}
	if len(msgs) != 2 {
		t.Fatalf("composed boundary produced %d messages, want steer + deadline: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].Content, "steer body") {
		t.Errorf("first message = %+v, want the steer", msgs[0])
	}
	if !strings.Contains(msgs[1].Content, "DEADLINE") {
		t.Errorf("second message = %+v, want the deadline notice", msgs[1])
	}
}

// TestApplyDeadlineNoticeSetsHookWhenAbsent covers the no-mailbox surface: a
// plain dispatch (no coordinator bundle) still gets the deadline hook.
func TestApplyDeadlineNoticeSetsHookWhenAbsent(t *testing.T) {
	startedAt := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clk := &fixedNow{t: startedAt.Add(19 * time.Minute)}
	ctx, cancel := context.WithDeadline(context.Background(), startedAt.Add(20*time.Minute))
	defer cancel()

	opts := &agent.Options{}
	applyDeadlineNotice(ctx, startedAt, clk.Now, opts)
	if opts.BeforeStep == nil {
		t.Fatal("deadline hook missing on a mailbox-less surface")
	}
	if msgs := opts.BeforeStep(); len(msgs) != 1 || !strings.Contains(msgs[0].Content, "DEADLINE") {
		t.Fatalf("hook output = %+v, want the one deadline notice", msgs)
	}
}
