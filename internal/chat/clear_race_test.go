package chat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func historyBlob(s *Session) string {
	var parts []string
	for _, m := range s.Messages {
		parts = append(parts, m.Role+":"+m.Content)
	}
	return strings.Join(parts, "|")
}

// A turn already in flight must not resurrect history the user explicitly
// purged. The turnID guard only covers concurrent SendUser calls; resetSystem
// replaced Messages without touching turnID, so a stale turn's writeback still
// satisfied myTurn == s.turnID and won - restoring the whole prior
// conversation, which SaveAfterTurn then persisted to disk and to __last__.
//
// The TUI makes this reachable: slash commands are dispatched before the
// "waiting" check, so /clear runs while the agent goroutine is mid-turn.
func TestClearIsNotUndoneByInFlightTurn(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "reply"})
	sess.SystemPrompt = "SYS"
	sess.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "SYS"},
		{Role: provider.RoleUser, Content: "secret-1"},
		{Role: provider.RoleAssistant, Content: "answer-1"},
	}

	// A turn starts and captures its generation.
	sess.mu.Lock()
	sess.turnID++
	myTurn := sess.turnID
	sess.mu.Unlock()

	sess.Clear() // user purges history while that turn is still running

	// The in-flight turn completes and writes back the history it began with.
	sess.mu.Lock()
	if myTurn == sess.turnID {
		sess.Messages = []provider.Message{
			{Role: provider.RoleSystem, Content: "SYS"},
			{Role: provider.RoleUser, Content: "secret-1"},
			{Role: provider.RoleAssistant, Content: "answer-1"},
			{Role: provider.RoleUser, Content: "secret-2"},
			{Role: provider.RoleAssistant, Content: "reply"},
		}
	}
	sess.mu.Unlock()

	if got := historyBlob(sess); strings.Contains(got, "secret-1") {
		t.Fatalf("/clear was undone; purged content came back: %s", got)
	}
}

// Load has the same shape: it replaces Messages wholesale, so an in-flight turn
// must not overwrite the freshly loaded session with the pre-load history.
func TestLoadIsNotUndoneByInFlightTurn(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "reply"})
	sess.SessionDir = t.TempDir()
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "saved"}}
	if err := sess.Save("target"); err != nil {
		t.Fatal(err)
	}
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "current"}}

	sess.mu.Lock()
	sess.turnID++
	myTurn := sess.turnID
	sess.mu.Unlock()

	if err := sess.Load("target"); err != nil {
		t.Fatal(err)
	}

	sess.mu.Lock()
	if myTurn == sess.turnID {
		sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "current"}}
	}
	sess.mu.Unlock()

	if got := historyBlob(sess); strings.Contains(got, "current") {
		t.Fatalf("/load was undone by a stale turn: %s", got)
	}
}
