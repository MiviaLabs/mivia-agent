package chat

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type blockingSessionStore struct {
	started chan struct{}
	release chan struct{}
	msgs    []provider.Message
}

func (s *blockingSessionStore) Save(string, []provider.Message, string, string) error { return nil }
func (s *blockingSessionStore) Load(name string) ([]provider.Message, error) {
	msgs, _, err := s.LoadWithInfo(name)
	return msgs, err
}
func (s *blockingSessionStore) LoadWithInfo(string) ([]provider.Message, SessionInfo, error) {
	close(s.started)
	<-s.release
	return append([]provider.Message(nil), s.msgs...), SessionInfo{Model: "saved"}, nil
}
func (s *blockingSessionStore) List() ([]SessionInfo, error) { return nil, nil }
func (s *blockingSessionStore) Delete(string) error          { return nil }

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
// The TUI now guards /clear with the same waiting check as /new and /load,
// so this session-layer fence is defense-in-depth for direct session users.
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

	_ = sess.Clear() // user purges history while that turn is still running

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

func TestLoadCannotResurrectAfterClear(t *testing.T) {
	store := &blockingSessionStore{started: make(chan struct{}), release: make(chan struct{}), msgs: []provider.Message{{Role: provider.RoleUser, Content: "saved-secret"}}}
	sess := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, nil)
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "current"}}
	sess.SetSessionStore(store, nil)
	result := make(chan error, 1)
	go func() { result <- sess.Load("saved") }()
	<-store.started
	_ = sess.Clear()
	close(store.release)
	if err := <-result; !errors.Is(err, ErrStaleOperation) {
		t.Fatalf("stale load error = %v, want ErrStaleOperation", err)
	}
	if got := historyBlob(sess); strings.Contains(got, "saved-secret") || strings.Contains(got, "current") {
		t.Fatalf("clear was overwritten by stale load: %s", got)
	}
}

var _ SessionStore = (*blockingSessionStore)(nil)
