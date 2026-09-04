package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

type fakeMounter struct {
	convs   map[string]ports.Conversation
	mounted []string
	err     error
}

func (m *fakeMounter) Mount(id string) (ports.Conversation, error) {
	m.mounted = append(m.mounted, id)
	if m.err != nil {
		return nil, m.err
	}
	if c, ok := m.convs[id]; ok {
		return c, nil
	}
	return &fakeMountConv{id: id}, nil
}

type fakeMountConv struct {
	id    string
	sends []intent.Send
}

func (c *fakeMountConv) ID() string                           { return c.id }
func (c *fakeMountConv) Title() string                        { return c.id }
func (c *fakeMountConv) Model() ports.ModelInfo               { return ports.ModelInfo{Name: "test"} }
func (c *fakeMountConv) ContextUsage() ports.Usage            { return ports.Usage{} }
func (c *fakeMountConv) History() []ports.Message             { return nil }
func (c *fakeMountConv) ActiveTurn() (ports.TurnHandle, bool) { return nil, false }
func (c *fakeMountConv) Send(ctx context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.sends = append(c.sends, in)
	return &fakeTurnHandle{}, nil
}

type fakeTurnHandle struct {
	events chan uievent.Event
}

func (h *fakeTurnHandle) TurnID() string               { return "turn-1" }
func (h *fakeTurnHandle) ID() string                   { return "turn-1" }
func (h *fakeTurnHandle) Events() <-chan uievent.Event { return h.events }
func (h *fakeTurnHandle) Cancel()                      {}
func (h *fakeTurnHandle) CancelToolCall(string) bool   { return true }

func testTheme() theme.Theme {
	return theme.Theme{}
}

func TestRemoteInput_MountsUntrackedSession(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	mounter := &fakeMounter{}
	s.SetSessionMounter(mounter)

	ev := ports.RemoteInputEvent{
		ID:         "inp-1",
		SessionID:  "background-1",
		Kind:       "message",
		Body:       "hello from remote",
		ReceivedAt: time.Now(),
	}

	// 1. First event for untracked session dispatches mount cmd
	next, cmd := s.handleRemoteInput(ev)
	scr := next.(Screen)
	if cmd == nil {
		t.Fatal("expected non-nil cmd for mounting untracked session")
	}
	if len(scr.mounting["background-1"]) != 1 {
		t.Fatalf("expected 1 queued event in mounting map, got %d", len(scr.mounting["background-1"]))
	}

	// 2. Second event while mounting appends without dispatching another mount
	ev2 := ports.RemoteInputEvent{
		ID:         "inp-2",
		SessionID:  "background-1",
		Kind:       "message",
		Body:       "second remote message",
		ReceivedAt: time.Now(),
	}
	next2, _ := scr.handleRemoteInput(ev2)
	scr2 := next2.(Screen)
	if len(scr2.mounting["background-1"]) != 2 {
		t.Fatalf("expected 2 queued events in mounting map, got %d", len(scr2.mounting["background-1"]))
	}

	// 3. Resolve mount with sessionMountedMsg
	mountedConv := &fakeMountConv{id: "background-1"}
	msg := sessionMountedMsg{
		sessionID: "background-1",
		conv:      mountedConv,
	}
	next3, cmd3 := scr2.handleSessionMountedMsg(msg)
	if cmd3 == nil {
		t.Fatal("expected awaitSessionEvent cmd on mount success")
	}
	if len(mountedConv.sends) != 1 {
		t.Fatalf("expected 1 Send call on mounted conv, got %d", len(mountedConv.sends))
	}
	if mountedConv.sends[0].Text != "hello from remote" {
		t.Errorf("got send text %q, want %q", mountedConv.sends[0].Text, "hello from remote")
	}
	if mountedConv.sends[0].PersistedText != "(via web) hello from remote" {
		t.Errorf("got persisted text %q", mountedConv.sends[0].PersistedText)
	}

	// The second queued message must be in st.queue
	st := next3.sessions["background-1"]
	if st == nil {
		t.Fatal("expected sessionState in sessions map")
	}
	if len(st.queue) != 1 || st.queue[0] != "second remote message" {
		t.Errorf("expected queue ['second remote message'], got %v", st.queue)
	}
}

func TestRemoteInput_MountErrorNotices(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	mounter := &fakeMounter{err: errors.New("cannot load")}
	s.SetSessionMounter(mounter)

	ev := ports.RemoteInputEvent{
		ID:         "inp-1",
		SessionID:  "background-err",
		Kind:       "message",
		Body:       "hello",
		ReceivedAt: time.Now(),
	}

	next, _ := s.handleRemoteInput(ev)
	scr := next.(Screen)

	msg := sessionMountedMsg{
		sessionID: "background-err",
		err:       errors.New("cannot load"),
	}
	next2, cmd := scr.handleSessionMountedMsg(msg)
	if cmd != nil {
		t.Error("expected nil cmd on mount error")
	}
	if len(next2.mounting["background-err"]) != 0 {
		t.Error("mounting entry should be cleared")
	}
	if next2.sessions["background-err"] != nil {
		t.Error("no session state should be created on mount error")
	}
}

func TestRemoteInput_MountRaceUserSwitchedToForeground(t *testing.T) {
	th := testTheme()
	primary := &fakeMountConv{id: "primary"}
	s := New(th, theme.TierTrueColor, []theme.Theme{th}, primary, nil, 80, nil)

	mounter := &fakeMounter{}
	s.SetSessionMounter(mounter)

	ev := ports.RemoteInputEvent{
		ID:         "inp-1",
		SessionID:  "bg-race",
		Kind:       "message",
		Body:       "msg while mounting",
		ReceivedAt: time.Now(),
	}

	next, _ := s.handleRemoteInput(ev)
	scr := next.(Screen)

	// User switches to "bg-race" as foreground before mount message arrives!
	bgConv := &fakeMountConv{id: "bg-race"}
	scr.switchConversation(bgConv)

	// Now sessionMountedMsg arrives
	msg := sessionMountedMsg{
		sessionID: "bg-race",
		conv:      bgConv,
	}
	next2, _ := scr.handleSessionMountedMsg(msg)
	if next2.convID() != "bg-race" {
		t.Fatalf("expected foreground conv ID 'bg-race', got %q", next2.convID())
	}
	if len(bgConv.sends) != 1 {
		t.Fatalf("expected 1 send on foreground conv, got %d", len(bgConv.sends))
	}
	if bgConv.sends[0].Text != "msg while mounting" {
		t.Errorf("got send text %q", bgConv.sends[0].Text)
	}
}
