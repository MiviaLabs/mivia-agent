package uiadapter

// The pool records why an entry could not get its own tool surface in
// lastToolScopeNotice. Only the worktree route drained that slot, so a
// degraded /new or /resume was silent - the operator kept typing into a
// session whose tools had quietly narrowed - and the undrained string stayed
// in the slot until some LATER, unrelated worktree command appended it to its
// own outcome.

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// newRunnerWithNotice builds a runner over a pool holding a pending
// tool-scope notice, the state wireEntryLocked leaves behind when a session's
// surface could not be rebuilt.
func newRunnerWithNotice(t *testing.T, notice string) (*CommandRunner, *SessionPool) {
	t.Helper()
	stubWorkflowWiring(t)
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	sess := chat.NewSession(res, fallbackCompleter{providerName: "fake"})
	sess.SessionID = "session-main"
	state := &cliagents.AgentSessionState{WorkspaceRoot: t.TempDir()}
	pool := NewSessionPool(sess, res, state, true)
	t.Cleanup(pool.CloseAll)
	runner := NewCommandRunnerWithPool(sess, pool, res, state)
	pool.mu.Lock()
	pool.lastToolScopeNotice = notice
	pool.mu.Unlock()
	return runner, pool
}

func TestHandleNewReportsToolScopeNotice(t *testing.T) {
	const notice = "session tools: rebuild refused"
	runner, pool := newRunnerWithNotice(t, notice)

	out := runner.handleNew()
	if out.Err != "" {
		t.Fatalf("/new returned error: %s", out.Err)
	}
	if !strings.Contains(out.Notice, notice) {
		t.Errorf("/new notice = %q, want it to carry the tool-scope warning %q", out.Notice, notice)
	}
	if drained := pool.takeToolScopeNotice(); drained != "" {
		t.Errorf("/new left %q in the notice slot, where a later unrelated command would report it", drained)
	}
}

func TestSelectSessionReportsToolScopeNotice(t *testing.T) {
	const notice = "session tools: rebuild refused"
	runner, pool := newRunnerWithNotice(t, notice)

	// A resume of the pool's own live entry takes SelectSession's pooled
	// branch and returns the existing conversation, with no store needed.
	out := runner.SelectSession(t.Context(), "session-main")
	if out.Err != "" {
		t.Fatalf("/resume returned error: %s", out.Err)
	}
	if !strings.Contains(out.Notice, notice) {
		t.Errorf("/resume notice = %q, want it to carry the tool-scope warning %q", out.Notice, notice)
	}
	if drained := pool.takeToolScopeNotice(); drained != "" {
		t.Errorf("/resume left %q in the notice slot, where a later unrelated command would report it", drained)
	}
}
