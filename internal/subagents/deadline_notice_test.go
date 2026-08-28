package subagents

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
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

// TestDeadlineNoticeFiresExactlyAtQuarter pins the boundary: at exactly one
// quarter of the budget remaining the notice fires ("at or inside the
// threshold" in the contract). 0.25 is a power of two, so the comparison is
// exact and this case is deterministic; a strict-greater regression would
// pass every other test here and silently skip the notice at the boundary.
func TestDeadlineNoticeFiresExactlyAtQuarter(t *testing.T) {
	startedAt := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	total := 20 * time.Minute
	clk := &fixedNow{t: startedAt.Add(15 * time.Minute)} // exactly 5m = 25% left
	ctx, cancel := context.WithDeadline(context.Background(), startedAt.Add(total))
	defer cancel()

	hook := deadlineNoticeBeforeStep(ctx, startedAt, clk.Now)
	msgs := hook()
	if len(msgs) != 1 {
		t.Fatalf("at-quarter boundary produced %d messages, want exactly 1", len(msgs))
	}
	if want := (5 * time.Minute).String(); !strings.Contains(msgs[0].Content, want) {
		t.Errorf("notice %q missing the remaining budget %q", msgs[0].Content, want)
	}
	if again := hook(); again != nil {
		t.Fatalf("hook fired twice across the boundary: %+v", again)
	}
}

// slowTool blocks its one call for sleep, then finishes - the shape that
// pushes the next step boundary past the wrap-up threshold.
type slowTool struct {
	sleep time.Duration
	ran   atomic.Bool
}

func (s *slowTool) Name() string               { return "slow" }
func (s *slowTool) Description() string        { return "test tool that takes its time" }
func (s *slowTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (s *slowTool) Execute(context.Context, json.RawMessage) (string, error) {
	s.ran.Store(true)
	time.Sleep(s.sleep)
	return "slow tool finished", nil
}

// deadlineE2ECompleter plays the nested model: turn one asks for the slow
// tool, turn two finishes. It records every user-role message containing a
// deadline notice so the test can count what the MODEL saw, not what the
// hook produced.
type deadlineE2ECompleter struct {
	mu      sync.Mutex
	turn    int
	notices []string
}

func (c *deadlineE2ECompleter) Name() string { return "deadline-e2e" }

func (c *deadlineE2ECompleter) Chat(context.Context, provider.Request) (string, error) {
	return "done", nil
}

func (c *deadlineE2ECompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	if w != nil && resp != nil {
		_, _ = w.Write([]byte(resp.Content))
	}
	return "done", nil
}

func (c *deadlineE2ECompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	for _, m := range req.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "DEADLINE") {
			c.notices = append(c.notices, m.Content)
		}
	}
	turn := c.turn
	c.turn++
	c.mu.Unlock()
	if turn == 0 {
		call := provider.ToolCall{ID: "slow-1", Type: "function"}
		call.Function.Name = "slow"
		call.Function.Arguments = `{}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}}, nil
	}
	return &provider.Response{Content: "done"}, nil
}

// TestDeadlineNoticeReachesTheNestedModel is the wiring half: the notice
// must ride the real multi_step path (timeoutContext -> applyDeadlineNotice
// -> the loop's BeforeStep drain), not only the synthetic hook chain the
// other tests drive. A 1.6s first tool call inside a 2s total budget puts
// the second step boundary inside the quarter threshold, so the nested model
// must have seen exactly one DEADLINE notice - and the run must still finish
// inside the budget, because the notice is advisory and adds no steps.
func TestDeadlineNoticeReachesTheNestedModel(t *testing.T) {
	tool := &slowTool{sleep: 1600 * time.Millisecond}
	reg := tools.NewRegistry()
	reg.Register(tool)
	parent, err := runtime.NewToolDispatcher(reg, runtime.Policy{})
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}
	t.Cleanup(parent.Close)
	comp := &deadlineE2ECompleter{}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Dispatcher:   parent,
		Model:        "test-model",
		SystemPrompt: "Test sub-agent.",
		MaxSteps:     3,
		MaxTokens:    1024,
		TotalTimeout: 2 * time.Second,
	}
	if _, err := h.Invoke(context.Background(), runtime.Request{Name: "multi_step", Input: json.RawMessage(`"slow task"`)}); err != nil {
		t.Fatalf("multi_step: %v", err)
	}
	if !tool.ran.Load() {
		t.Fatal("the slow tool never ran")
	}
	comp.mu.Lock()
	defer comp.mu.Unlock()
	if len(comp.notices) != 1 {
		t.Fatalf("nested model saw %d DEADLINE notices, want exactly 1: %q", len(comp.notices), comp.notices)
	}
	if !strings.Contains(comp.notices[0], "final report") {
		t.Errorf("notice = %q, want the wrap-up contract", comp.notices[0])
	}
}
