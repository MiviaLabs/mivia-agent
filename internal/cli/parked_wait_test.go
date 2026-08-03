package cli

import (
	"context"
	"encoding/json"
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
