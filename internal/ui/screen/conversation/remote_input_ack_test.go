package conversation

import (
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestAckCmd_NilAckReturnsNilCmd covers ackCmd's nil-safety contract: a nil
// ack must return a nil tea.Cmd so no goroutine is armed and tea.Batch skips
// it safely.
func TestAckCmd_NilAckReturnsNilCmd(t *testing.T) {
	if cmd := ackCmd(nil); cmd != nil {
		t.Fatal("ackCmd(nil) returned a non-nil Cmd")
	}
}

// TestAckCmd_NonNilAckRunsOnCmdInvocation proves the wrapped func runs only
// when the returned Cmd is actually invoked, not merely by constructing it.
func TestAckCmd_NonNilAckRunsOnCmdInvocation(t *testing.T) {
	called := false
	cmd := ackCmd(func() { called = true })
	if cmd == nil {
		t.Fatal("ackCmd(non-nil) returned a nil Cmd")
	}
	if called {
		t.Fatal("ack ran before the Cmd was invoked")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("ackCmd's Cmd returned %#v, want nil Msg", msg)
	}
	if !called {
		t.Fatal("ack did not run when the Cmd was invoked")
	}
}

// TestSendOrQueueRemote_AcksOnDispatchSuccess drives sendOrQueueRemote with
// s.active == nil and a Send that succeeds; the returned Cmd, once run, must
// invoke AckReceived exactly once.
func TestSendOrQueueRemote_AcksOnDispatchSuccess(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	ackCount := &ackCounter{}
	next, cmd := s.sendOrQueueRemote("hello", "(via web) hello", ackCount.inc)
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	runAllCmds(t, cmd)
	if got := ackCount.load(); got != 1 {
		t.Fatalf("ackCount = %d, want 1", got)
	}
	sc := next.(Screen)
	if len(primary.sends) != 1 {
		t.Fatalf("expected 1 Send call, got %d", len(primary.sends))
	}
	_ = sc
}

// TestSendOrQueueRemote_AcksOnDispatchFailure is the failure-outcome twin:
// Send errors, but AckReceived must still fire exactly once - custody was
// taken regardless of the dispatch's outcome.
func TestSendOrQueueRemote_AcksOnDispatchFailure(t *testing.T) {
	th := testTheme()
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, errConversation{err: errors.New("boom")}, nil, 80, fixedNow)

	ackCount := &ackCounter{}
	_, cmd := s.sendOrQueueRemote("hello", "(via web) hello", ackCount.inc)
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	runAllCmds(t, cmd)
	if got := ackCount.load(); got != 1 {
		t.Fatalf("ackCount = %d, want 1", got)
	}
}

// TestHandleRemoteInput_DirectSend_AcksOnSuccess covers handleRemoteInput's
// direct-Send branch (tracked background session) for a Send that succeeds.
func TestHandleRemoteInput_DirectSend_AcksOnSuccess(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	bgConv := &fakeMountConv{id: "bg-direct"}
	st := s.newSessionState(bgConv)
	s.sessions = map[string]*sessionState{"bg-direct": st}

	ackCount := &ackCounter{}
	ev := ports.RemoteInputEvent{
		SessionID: "bg-direct", Kind: "message", Body: "hi",
		AckReceived: ackCount.inc,
	}
	_, cmd := s.handleRemoteInput(ev)
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	runAllCmds(t, cmd)
	if got := ackCount.load(); got != 1 {
		t.Fatalf("ackCount = %d, want 1", got)
	}
	if len(bgConv.sends) != 1 {
		t.Fatalf("expected 1 Send call, got %d", len(bgConv.sends))
	}
}

// TestHandleRemoteInput_DirectSend_AcksOnError is the failure-outcome twin.
func TestHandleRemoteInput_DirectSend_AcksOnError(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	failConv := &failingSendConv{fakeMountConv: &fakeMountConv{id: "bg-direct-fail"}, err: errors.New("send exploded")}
	st := s.newSessionState(failConv)
	s.sessions = map[string]*sessionState{"bg-direct-fail": st}

	ackCount := &ackCounter{}
	ev := ports.RemoteInputEvent{
		SessionID: "bg-direct-fail", Kind: "message", Body: "hi",
		AckReceived: ackCount.inc,
	}
	_, cmd := s.handleRemoteInput(ev)
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	runAllCmds(t, cmd)
	if got := ackCount.load(); got != 1 {
		t.Fatalf("ackCount = %d, want 1", got)
	}
}

// runAllCmds recursively invokes cmd, flattening any tea.BatchMsg it
// returns and invoking every sub-Cmd too, so a batched ackCmd buried inside
// nested Cmds still actually runs. Each Cmd runs on its own goroutine and
// this only waits a bounded window for observable side effects (the ack
// funcs under test), matching cmdPump's flattening approach in
// remote_input_smoke_test.go: a long-lived read Cmd (e.g.
// awaitSessionEvent, which blocks forever on the fixture's nil events
// channel) is expected to still be blocked when this returns, and that is
// fine - nothing here waits on it to finish.
func runAllCmds(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	var wg sync.WaitGroup
	runAllCmdsWG(t, cmd, &wg)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		// Expected for a Cmd that blocks on a channel read with nothing
		// ever sent (e.g. awaitSessionEvent on a fixture's nil channel);
		// the goroutines above are intentionally leaked for the test's
		// lifetime. Sibling Cmds in the same batch are spawned
		// concurrently, so one blocking Cmd never starves another.
	}
}

func runAllCmdsWG(t *testing.T, cmd tea.Cmd, wg *sync.WaitGroup) {
	t.Helper()
	if cmd == nil {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				runAllCmdsWG(t, c, wg)
			}
		}
	}()
}
