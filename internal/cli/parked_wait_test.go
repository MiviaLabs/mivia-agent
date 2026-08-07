package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

func TestParkedWaitDuration(t *testing.T) {
	// No deadline → full waitSec.
	if got := parkedWaitDuration(context.Background(), 7); got != 7*time.Second {
		t.Fatalf("no deadline: got %v, want %v", got, 7*time.Second)
	}
	// Deadline beyond waitSec → waitSec (clamp must not shorten).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if got := parkedWaitDuration(ctx, 7); got != 7*time.Second {
		t.Fatalf("deadline beyond wait: got %v, want %v", got, 7*time.Second)
	}
	// Sub-second deadline → returned duration equals the remaining deadline
	// (not the full waitSec): duration comparison must not floor to 0 seconds.
	dl := time.Now().Add(250 * time.Millisecond)
	dctx, dcancel := context.WithDeadline(context.Background(), dl)
	defer dcancel()
	got := parkedWaitDuration(dctx, 60)
	rem := time.Until(dl)
	if got <= 0 || got > rem+10*time.Millisecond {
		t.Fatalf("sub-second deadline: got %v, remaining %v", got, rem)
	}
	if got >= time.Second {
		t.Fatalf("sub-second deadline must clamp below one second, got %v", got)
	}
}

func TestWaitOnParkedAnswerDeclineSentinel(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, _ := setupPostMessageEnv(t, cfg)
	askID := "ask-decline-sentinel"
	c.RegisterAsk(runID, taskID, "worker", askID, nil)
	ch, unpark, err := c.ParkQuestion(runID, taskID, askID)
	if err != nil {
		t.Fatal(err)
	}
	// Coordinator wave 1 delivers AskDeclinePrefix+reason when the responder
	// finalizes without answering; the asker must surface it as no_answer.
	if !c.DeliverAnswer(runID, taskID, askID, agentmsg.AskDeclinePrefix+agentmsg.DeclineReasonResponderTerminal) {
		unpark()
		t.Fatal("deliver decline sentinel")
	}
	parked := true
	msg := newAskForWait(t, runID, taskID, askID)
	out, err := tool.waitOnParkedAnswer(context.Background(), c,
		runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 1, ch, &parked, unpark)
	unpark()
	if err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res["status"] != "no_answer" || res["reason"] != agentmsg.DeclineReasonResponderTerminal {
		t.Fatalf("out=%s", out)
	}
	if res["message_id"] != askID {
		t.Fatalf("out=%s", out)
	}
	if parked {
		t.Fatal("park must be retired after decline")
	}
}

func TestWaitOnParkedAnswerNormalAnswerStillAnswered(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, _ := setupPostMessageEnv(t, cfg)
	askID := "ask-normal-answer"
	c.RegisterAsk(runID, taskID, "worker", askID, nil)
	ch, unpark, err := c.ParkQuestion(runID, taskID, askID)
	if err != nil {
		t.Fatal(err)
	}
	if !c.DeliverAnswer(runID, taskID, askID, "yes") {
		unpark()
		t.Fatal("deliver answer")
	}
	parked := true
	msg := newAskForWait(t, runID, taskID, askID)
	out, err := tool.waitOnParkedAnswer(context.Background(), c,
		runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 1, ch, &parked, unpark)
	unpark()
	if err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res["status"] != "answered" || res["answer"] != "yes" {
		t.Fatalf("out=%s", out)
	}
}

// TestWaitOnParkedAnswerDeclinePreferredOverTimeout: with a zero-duration timer
// and a ready sentinel, whichever select case wins, the decline must be
// reported — never timed_out.
func TestWaitOnParkedAnswerDeclinePreferredOverTimeout(t *testing.T) {
	prev := askWaitUnit
	askWaitUnit = 0 // zero-duration timer is immediately ready → races the channel
	t.Cleanup(func() { askWaitUnit = prev })

	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, _ := setupPostMessageEnv(t, cfg)
	for i := 0; i < 40; i++ {
		askID := fmt.Sprintf("ask-decline-race-%d", i)
		c.RegisterAsk(runID, taskID, "worker", askID, nil)
		ch, unpark, err := c.ParkQuestion(runID, taskID, askID)
		if err != nil {
			t.Fatal(err)
		}
		if !c.DeliverAnswer(runID, taskID, askID, agentmsg.AskDeclinePrefix+agentmsg.DeclineReasonResponderTerminal) {
			unpark()
			t.Fatal("deliver sentinel")
		}
		parked := true
		msg := newAskForWait(t, runID, taskID, askID)
		out, err := tool.waitOnParkedAnswer(context.Background(), c,
			runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 1, ch, &parked, unpark)
		unpark()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `"status":"no_answer"`) ||
			!strings.Contains(out, agentmsg.DeclineReasonResponderTerminal) ||
			strings.Contains(out, "timed_out") {
			t.Fatalf("iter %d: out=%s", i, out)
		}
	}
}

// TestWaitOnParkedAnswerDeclinePreferredOverCancel: a cancelled ctx plus a
// ready sentinel must surface the decline, not the raw cancel error.
func TestWaitOnParkedAnswerDeclinePreferredOverCancel(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, _ := setupPostMessageEnv(t, cfg)
	for i := 0; i < 40; i++ {
		askID := fmt.Sprintf("ask-decline-cancel-%d", i)
		c.RegisterAsk(runID, taskID, "worker", askID, nil)
		ch, unpark, err := c.ParkQuestion(runID, taskID, askID)
		if err != nil {
			t.Fatal(err)
		}
		if !c.DeliverAnswer(runID, taskID, askID, agentmsg.AskDeclinePrefix+agentmsg.DeclineReasonResponderTerminal) {
			unpark()
			t.Fatal("deliver sentinel")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		parked := true
		msg := newAskForWait(t, runID, taskID, askID)
		out, err := tool.waitOnParkedAnswer(ctx, c,
			runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 5, ch, &parked, unpark)
		unpark()
		if err != nil {
			t.Fatalf("iter %d: err=%v out=%s", i, err, out)
		}
		if !strings.Contains(out, agentmsg.DeclineReasonResponderTerminal) || strings.Contains(out, "timed_out") {
			t.Fatalf("iter %d: out=%s", i, out)
		}
	}
}

func TestWaitForAnswerDeclineSentinel(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, _ := setupPostMessageEnv(t, cfg)
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindQuestion,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{Role: agentmsg.ParentSentinel},
		"q", nil, agentmsg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		deadline := time.After(2 * time.Second)
		for {
			if c.CountPendingQuestions(runID, taskID) == 1 {
				_ = c.DeliverAnswer(runID, taskID, msg.ID, agentmsg.AskDeclinePrefix+agentmsg.DeclineReasonResponderTerminal)
				return
			}
			select {
			case <-deadline:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	out, err := tool.waitForAnswer(context.Background(), c,
		runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 2)
	if err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res["status"] != "no_answer" || res["reason"] != agentmsg.DeclineReasonResponderTerminal {
		t.Fatalf("out=%s", out)
	}
}

// TestWaitForAnswerDeadlineExceededReturnsNoAnswer: a question whose
// wait_seconds exceeds the enclosing tool/step budget must exit as the
// documented no_answer result (reason timed_out) with nil error — never a raw
// ctx.Err(). The budget is already spent when the question parks (rem <= 0 at
// clamp time, so the timer cannot be clamped and ctx.Done() fires immediately
// inside the select); park/post still succeed because the ledger is
// ctx-agnostic, leaving the select as the only place the deadline error could
// surface.
func TestWaitForAnswerDeadlineExceededReturnsNoAnswer(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	expiredCtx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindQuestion,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{Role: agentmsg.ParentSentinel},
		"q", nil, agentmsg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.waitForAnswer(expiredCtx, c,
		runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 10000)
	if err != nil {
		t.Fatalf("deadline-exceeded park surfaced a raw error: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res["status"] != "no_answer" || res["reason"] != "timed_out" {
		t.Fatalf("out=%s", out)
	}
}

// TestWaitForAnswerParkDurationClampedToDeadline: wait_seconds above a clamped
// budget is clamped — the park is registered for the remaining deadline time,
// not the full requested wait, and the wait ends as no_answer with nil error.
func TestWaitForAnswerParkDurationClampedToDeadline(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindQuestion,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{Role: agentmsg.ParentSentinel},
		"q", nil, agentmsg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		out string
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		out, err := tool.waitForAnswer(dctx, c,
			runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 10000)
		resCh <- result{out, err}
	}()
	// Capture the registered park's expiry while the wait is live: a clamped
	// park expires at the parkTTL floor (~30m), never at the 10000s request
	// (now + 10000s + slack).
	var expiresAt time.Time
	deadline := time.After(3 * time.Second)
	for {
		parks := c.ParkedQuestions(runID)
		if len(parks) > 0 {
			expiresAt = parks[0].ExpiresAt
			break
		}
		select {
		case <-deadline:
			t.Fatal("question never parked")
		case <-time.After(5 * time.Millisecond):
		}
	}
	now := time.Now()
	if !expiresAt.After(now) || !expiresAt.Before(now.Add(10000*time.Second)) {
		t.Fatalf("park expiry %v not clamped below the 10000s request (now=%v)", expiresAt, now)
	}
	res := <-resCh
	if res.err != nil {
		t.Fatalf("clamped park surfaced a raw error: %v", res.err)
	}
	if !strings.Contains(res.out, `"status":"no_answer"`) || !strings.Contains(res.out, "timed_out") {
		t.Fatalf("out=%s", res.out)
	}
}

// TestWaitForAnswerCancelPropagatesError: a canceled park must surface
// context.Canceled as a raw error — a canceled task must never be mistaken for
// a no_answer park timeout.
func TestWaitForAnswerCancelPropagatesError(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindQuestion,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{Role: agentmsg.ParentSentinel},
		"q", nil, agentmsg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resCh := make(chan error, 1)
	go func() {
		_, err := tool.waitForAnswer(cctx, c,
			runtime.TaskIdentity{RunID: runID, TaskID: taskID}, msg, 30)
		resCh <- err
	}()
	deadline := time.After(3 * time.Second)
	for c.CountPendingQuestions(runID, taskID) != 1 {
		select {
		case <-deadline:
			t.Fatal("question never parked")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-resCh:
		if err == nil {
			t.Fatal("cancel must propagate an error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waitForAnswer did not return after cancel")
	}
}

// newAskForWait builds a KindAsk message whose ID matches a registered ask.
func newAskForWait(t *testing.T, runID, taskID, askID string) agentmsg.Message {
	t.Helper()
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{Role: "p"},
		"q", nil, agentmsg.Options{ID: askID})
	if err != nil {
		t.Fatal(err)
	}
	msg.ID = askID
	return msg
}
