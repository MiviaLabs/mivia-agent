package uiadapter_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// collectCompactionEvents reads every event off h.Events() until the channel
// closes, with a hard deadline so a regression that stops closing the
// channel fails the test instead of hanging the suite.
func collectCompactionEvents(t *testing.T, h ports.CompactionHandle) []ports.CompactionEvent {
	t.Helper()
	var events []ports.CompactionEvent
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-h.Events():
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-deadline:
			t.Fatal("timed out waiting for compaction events channel to close")
			return events
		}
	}
}

// waitForCompactionDone drains h without asserting on the events themselves
// - used by tests whose interest is in the runner's overlap/cancel guards
// rather than the compaction outcome.
func waitForCompactionDone(t *testing.T, h ports.CompactionHandle) {
	t.Helper()
	collectCompactionEvents(t, h)
	// onDone (compaction.go:39-42) runs in a defer AFTER the events
	// channel closes, so give the scheduler a moment to run it before a
	// caller immediately re-checks compactionActive.
	time.Sleep(20 * time.Millisecond)
}

// TestCommandRunner_StartCompaction_NilSessionErrors exercises the guard at
// compaction.go:26-29: StartCompaction refuses to run without an active
// session rather than starting a goroutine against a nil *chat.Session.
func TestCommandRunner_StartCompaction_NilSessionErrors(t *testing.T) {
	runner := uiadapter.NewCommandRunner(nil, nil, nil)

	h, err := runner.StartCompaction(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on nil active session, got nil")
	}
	if h != nil {
		t.Fatalf("expected nil handle on error, got %+v", h)
	}
	if !strings.Contains(err.Error(), "no active session") {
		t.Errorf("error = %q, want it to mention 'no active session'", err.Error())
	}
}

// TestCommandRunner_StartCompaction_RejectsOverlapping exercises
// compaction.go:30-36: r.compactionActive is set synchronously (under
// r.compactionMu) before StartCompaction returns to its caller, so a second
// call issued right after the first must see it already true and refuse
// with "compaction already in progress" - proving real overlap protection,
// not just a check that happens to race the same way in this test.
func TestCommandRunner_StartCompaction_RejectsOverlapping(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "test", Model: "m1"}
	sess := chat.NewSession(res, comp)
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	first, err := runner.StartCompaction(context.Background(), "")
	if err != nil {
		t.Fatalf("first StartCompaction: unexpected error %v", err)
	}
	if first == nil {
		t.Fatal("expected a non-nil handle from the first call")
	}

	second, err := runner.StartCompaction(context.Background(), "")
	if err == nil {
		t.Fatal("expected the second, overlapping StartCompaction to error")
	}
	if second != nil {
		t.Fatalf("expected nil handle on overlap rejection, got %+v", second)
	}
	if !strings.Contains(err.Error(), "compaction already in progress") {
		t.Errorf("error = %q, want it to mention 'compaction already in progress'", err.Error())
	}

	// Drain the first operation to completion (it fails fast: the plain
	// session has no context manager configured) so compactionActive
	// resets and a fresh StartCompaction is accepted again - proving the
	// flag is a real mutex-guarded lock, not a one-shot latch.
	waitForCompactionDone(t, first)

	third, err := runner.StartCompaction(context.Background(), "")
	if err != nil {
		t.Fatalf("StartCompaction after the first completed: unexpected error %v", err)
	}
	waitForCompactionDone(t, third)
}

// TestCommandRunner_StartCompaction_SuccessEmitsPreparingSummarizingNotice
// drives StartCompaction through its real success path (compaction.go:37-56):
// a real *chat.Session wired to a real SQLite context store, with enough
// history that Compact actually reduces it, so the goroutine's two
// preparing/summarizing progress events and the final Notice - built from
// the session's real post-compaction ContextUsage() - are genuine, not
// hand-fed.
func TestCommandRunner_StartCompaction_SuccessEmitsPreparingSummarizingNotice(t *testing.T) {
	sess, res, _, cleanup := setupSessionStoreFixture(t)
	defer cleanup()
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	if _, err := sess.SendUser(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		sess.Messages = append(sess.Messages,
			provider.Message{Role: provider.RoleUser, Content: strings.Repeat("long question ", 20)},
			provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("long answer ", 20)},
		)
	}

	h, err := runner.StartCompaction(context.Background(), "focus text")
	if err != nil {
		t.Fatalf("StartCompaction: unexpected error %v", err)
	}

	events := collectCompactionEvents(t, h)
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events (preparing, summarizing, done), got %d: %+v", len(events), events)
	}
	if events[0].Phase != "compact" || events[0].Detail != "preparing" {
		t.Errorf("events[0] = %+v, want Phase=compact Detail=preparing", events[0])
	}
	if events[1].Phase != "compact" || events[1].Detail != "summarizing context" {
		t.Errorf("events[1] = %+v, want Phase=compact Detail='summarizing context'", events[1])
	}
	last := events[len(events)-1]
	if !last.Done {
		t.Fatalf("last event = %+v, want Done=true", last)
	}
	if last.Err != nil {
		t.Fatalf("last event Err = %v, want nil on the success path", last.Err)
	}
	if last.SessionID != sess.SessionID {
		t.Errorf("last event SessionID = %q, want %q", last.SessionID, sess.SessionID)
	}
	if !strings.Contains(last.Notice, "Context compacted") || !strings.Contains(last.Notice, "% used") {
		t.Errorf("last event Notice = %q, want it to report the post-compaction usage", last.Notice)
	}
}

// TestCommandRunner_StartCompaction_CompactErrorEmitsDoneWithErr drives the
// error branch at compaction.go:50-52: a session with no context manager or
// store configured makes the real sess.Compact call fail with "context
// compaction is not configured", and StartCompaction's goroutine must
// surface that as a single Done event carrying Err, with no success Notice.
func TestCommandRunner_StartCompaction_CompactErrorEmitsDoneWithErr(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "test", Model: "m1"}
	sess := chat.NewSession(res, comp)
	sess.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, Content: "answer"},
	}
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	h, err := runner.StartCompaction(context.Background(), "")
	if err != nil {
		t.Fatalf("StartCompaction: unexpected error %v", err)
	}

	events := collectCompactionEvents(t, h)
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	last := events[len(events)-1]
	if !last.Done {
		t.Fatalf("last event = %+v, want Done=true", last)
	}
	if last.Err == nil {
		t.Fatal("expected the last event to carry the Compact error, got nil")
	}
	if last.Notice != "" {
		t.Errorf("expected no success Notice on the error path, got %q", last.Notice)
	}
}

// TestCommandRunner_StartCompaction_CancelIsSafeAndUnblocksEvents exercises
// compactionHandle.Cancel (compaction.go:19): calling it must not panic and
// the handle's Events() channel must still close on its own, so a caller
// that cancels mid-flight (the TUI cancels its spinner's context on
// navigating away) never leaks the goroutine or blocks forever on a read.
func TestCommandRunner_StartCompaction_CancelIsSafeAndUnblocksEvents(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "test", Model: "m1"}
	sess := chat.NewSession(res, comp)
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	h, err := runner.StartCompaction(context.Background(), "")
	if err != nil {
		t.Fatalf("StartCompaction: unexpected error %v", err)
	}

	h.Cancel() // must not panic

	waitForCompactionDone(t, h)
}
