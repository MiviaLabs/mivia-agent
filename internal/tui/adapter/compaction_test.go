package adapter_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	contextmgr "github.com/MiviaLabs/mivia-agent/internal/context/manager"
	"github.com/MiviaLabs/mivia-agent/internal/context/state"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tui/adapter"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/ports"
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
	runner := adapter.NewCommandRunner(nil, nil, nil)

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
	// The first compaction must still be IN FLIGHT when the second call
	// lands, or there is no overlap to reject. A plain session's Compact
	// fails fast ("context compaction is not configured") BEFORE any
	// completer is reached, so a blocking completer never blocks anything:
	// the worker cleared compactionActive and raced the second call, which
	// is how this test failed under -race. Park the worker inside the
	// session's real compact path instead: a PreparationManager whose
	// Prepare call blocks until the overlap has been rejected. Receiving
	// on entered is a happens-after proof the worker is inside
	// sess.Compact with compactionActive still set.
	release := make(chan struct{})
	prep := newBlockingPreparation(release, contextmgr.StructuralPreparationManager{})
	sess, res, cleanup := setupBlockingContextSession(t, prep)
	defer cleanup()
	runner := adapter.NewCommandRunner(sess, res, nil)

	// Initialize the durable session (a first turn creates the snapshot
	// compactLoadSnapshot requires) and use a deadline on the entered
	// signal so a regression that never reaches Prepare fails instead of
	// hanging the suite.
	if _, err := sess.SendUser(context.Background(), "hello", nil); err != nil {
		t.Fatalf("SendUser: %v", err)
	}

	// Arm the blocker and start the compaction: only the manual compact
	// path from here on parks in Prepare.
	prep.arm()
	first, err := runner.StartCompaction(context.Background(), "")
	if err != nil {
		t.Fatalf("first StartCompaction: unexpected error %v", err)
	}
	if first == nil {
		t.Fatal("expected a non-nil handle from the first call")
	}

	// Deterministic in-flight proof: the worker is parked inside Prepare,
	// so the second call cannot miss the window.
	select {
	case <-prep.entered:
	case <-time.After(5 * time.Second):
		evs := collectCompactionEvents(t, first)
		t.Fatalf("worker never reached Prepare; overlap was not actually in flight; events: %+v", evs)
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

	// Release the parked compact and drain the first operation to
	// completion. With the blocker released and force=true, the compact
	// declines ("made no reduction"), so compactionActive resets and a
	// fresh StartCompaction is accepted again - proving the flag is a
	// real mutex-guarded lock, not a one-shot latch.
	close(release)
	waitForCompactionDone(t, first)

	third, err := runner.StartCompaction(context.Background(), "")
	if err != nil {
		t.Fatalf("StartCompaction after the first completed: unexpected error %v", err)
	}
	waitForCompactionDone(t, third)
}

// armedPreparation delegates to inner until armed, then parks so a test
// can hold the real compact path in flight. The warm-up turn (before arm)
// must keep the structural behavior, or turn persistence breaks.
type blockingPreparation struct {
	entered chan struct{}
	release chan struct{}
	armed   chan struct{}
	inner   contextmgr.PreparationManager
}

func newBlockingPreparation(release chan struct{}, inner contextmgr.PreparationManager) *blockingPreparation {
	return &blockingPreparation{entered: make(chan struct{}), release: release, armed: make(chan struct{}), inner: inner}
}

func (p *blockingPreparation) arm() { close(p.armed) }

func (p *blockingPreparation) Prepare(ctx context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	select {
	case <-p.armed:
		select {
		case <-p.entered:
		default:
			close(p.entered)
		}
		<-p.release
		return contextmgr.Preparation{}, nil
	default:
		return p.inner.Prepare(ctx, input)
	}
}

func (p *blockingPreparation) Discard(contextmgr.Preparation) {}

// setupBlockingContextSession wires a session with a real SQLite context
// store but a caller-supplied PreparationManager, so tests can park the
// compact path at a chosen point. Mirrors setupSessionStoreFixture.
func setupBlockingContextSession(t *testing.T, prep contextmgr.PreparationManager) (*chat.Session, *config.Resolved, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "adapter-compaction-test-*")
	if err != nil {
		t.Fatal(err)
	}
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess := chat.NewSession(res, &nullCompleter{})

	store, err := storage.OpenSQLite(tmpDir + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := state.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  prep,
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return sess, res, cleanup
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
	runner := adapter.NewCommandRunner(sess, res, nil)

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
	runner := adapter.NewCommandRunner(sess, res, nil)

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
	runner := adapter.NewCommandRunner(sess, res, nil)

	h, err := runner.StartCompaction(context.Background(), "")
	if err != nil {
		t.Fatalf("StartCompaction: unexpected error %v", err)
	}

	h.Cancel() // must not panic

	waitForCompactionDone(t, h)
}

// blockingCompleter parks every completion until release is closed, so a
// caller can hold an async operation open for as long as a test needs it
// in flight. Reads of release are safe from any goroutine: a closed channel
// is the only signal, and it is closed exactly once.
type blockingCompleter struct{ release chan struct{} }

func (c *blockingCompleter) Name() string { return "blocking" }

func (c *blockingCompleter) ChatStream(ctx context.Context, _ provider.Request, _ io.Writer) (string, error) {
	select {
	case <-c.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "", nil
}

func (c *blockingCompleter) Chat(ctx context.Context, _ provider.Request) (string, error) {
	select {
	case <-c.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "", nil
}

func (c *blockingCompleter) ChatTurn(ctx context.Context, _ provider.Request) (*provider.Response, error) {
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &provider.Response{FinishReason: "stop"}, nil
}
