package chat

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// blockingTurnCompleter is a real, channel-gated Completer whose ChatTurn
// blocks until released: the agent loop (internal/agent/agentloop_completer.go)
// calls ChatTurn first and only falls back to Chat/ChatStream when ChatTurn
// returns (nil, nil), so this is the completer method that must block to hold
// an agent turn's provider call genuinely "in flight" from a second,
// concurrently running goroutine's point of view. Existing blocking fakes in
// this package (blockingSessionStore, blockingCompleter in
// model_binding_integration_test.go) either gate a different call or leave
// ChatTurn non-blocking, so neither fits the agent-turn path exercised here.
type blockingTurnCompleter struct {
	name    string
	reply   string
	started chan struct{}
	release chan struct{}
}

func (c *blockingTurnCompleter) Name() string { return c.name }
func (c *blockingTurnCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return c.reply, nil
}
func (c *blockingTurnCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.reply, nil
}
func (c *blockingTurnCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	close(c.started)
	<-c.release
	return &provider.Response{Content: c.reply, FinishReason: "stop"}, nil
}

// TestTurnCommitDoesNotSurviveConcurrentSwitchBinding closes a real coverage
// gap: fencing.go:136 documents that SaveAfterTurn is "fenced so a clear,
// load, switch, or newer turn cannot publish stale state", and Clear (in a
// hand-simulated test) and Load (with real goroutines, in
// TestLoadCannotResurrectAfterClear) each have a test proving it - but
// SwitchBinding never did, for either the plain or the agent-turn commit
// path. This exercises the agent-turn path (beginAgentTurn / finishAgentTurn
// / commitPreparedTurn), using the same real-goroutine, channel-gated shape
// as TestLoadCannotResurrectAfterClear rather than the hand-simulated
// TestClearIsNotUndoneByInFlightTurn shape.
//
// What this proves about the actual code (not a hypothesis): SwitchBinding
// checks s.activeTurns under s.mu and refuses immediately - it does not wait
// - whenever a turn's provider call has not yet returned. Since a turn's own
// commit (finishAgentTurn -> commitPreparedTurn) always runs strictly before
// that turn's activeTurns decrement (session.go's sendAgent defers done()
// last), a SwitchBinding attempted while a turn's ChatTurn is still blocked
// can never observe activeTurns == 0, so it is always refused rather than
// racing the eventual commit. That is a stronger guarantee than "the switch
// wins the race": no window exists in which a live switch can invalidate the
// fence a still-running turn will commit under.
func TestTurnCommitDoesNotSurviveConcurrentSwitchBinding(t *testing.T) {
	first := &blockingTurnCompleter{name: "old", reply: "first reply", started: make(chan struct{}), release: make(chan struct{})}
	sess := agentTurnSession(t, first)

	turnDone := make(chan error, 1)
	turnReply := make(chan string, 1)
	go func() {
		reply, err := sess.SendUser(context.Background(), "first", io.Discard)
		turnReply <- reply
		turnDone <- err
	}()

	<-first.started // the first turn's provider call is now genuinely in flight

	second := &blockingTurnCompleter{name: "new", reply: "second reply", started: make(chan struct{}), release: make(chan struct{})}
	switchErr := sess.SwitchBinding(ModelBinding{ProviderName: "new-provider", Model: "new-model", Completer: second})

	close(first.release) // let the first turn's provider call return
	if err := <-turnDone; err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	if reply := <-turnReply; reply != "first reply" {
		t.Fatalf("reply = %q, want the completer's own output", reply)
	}

	if switchErr == nil {
		t.Fatalf("SwitchBinding succeeded while a turn's provider call was in flight; expected refusal")
	}

	// The turn's own commit is unaffected by the refused switch attempt: its
	// content lands in history, under the ORIGINAL binding.
	if !strings.Contains(historyBlob(sess), "first reply") {
		t.Fatalf("turn commit did not land after a concurrently refused switch: %s", historyBlob(sess))
	}
	if got := sess.CurrentBinding(); got.ProviderName != "fake" || got.Model != "model" {
		t.Fatalf("binding changed despite a refused switch: %+v", got)
	}

	// Once the turn has fully released (activeTurns back to 0), a genuine
	// switch succeeds and does not disturb the already-committed history:
	// this is the positive half of "a switch cannot publish stale state" -
	// it publishes cleanly once there is no state left for it to race.
	if err := sess.SwitchBinding(ModelBinding{ProviderName: "new-provider", Model: "new-model", Completer: second}); err != nil {
		t.Fatalf("switch after turn completed: %v", err)
	}
	if got := sess.CurrentBinding(); got.ProviderName != "new-provider" || got.Model != "new-model" {
		t.Fatalf("post-turn switch did not take effect: %+v", got)
	}
	if !strings.Contains(historyBlob(sess), "first reply") {
		t.Fatalf("post-turn switch disturbed the already-committed turn history: %s", historyBlob(sess))
	}
}
