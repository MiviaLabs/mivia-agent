package cli

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	tea "github.com/charmbracelet/bubbletea"
)

// queueDrainCompleter holds turn 1 until released, so the drain test can open
// the queue manager deterministically while the first turn is in flight.
type queueDrainCompleter struct {
	mu        sync.Mutex
	requests  []provider.Request
	release   chan struct{}
	firstDone chan struct{}
}

func (c *queueDrainCompleter) Name() string { return "queue-drain" }
func (c *queueDrainCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (c *queueDrainCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (c *queueDrainCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	n := len(c.requests)
	c.mu.Unlock()
	if n == 1 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.release:
		}
		close(c.firstDone)
		return &provider.Response{Content: "first answer", FinishReason: "stop"}, nil
	}
	return &provider.Response{Content: "next answer", FinishReason: "stop"}, nil
}

func (c *queueDrainCompleter) Requests() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.requests...)
}

// sessionHarness builds the session fixture the force-send integration tests
// use, ready to be dropped into startScrollProgram.
func queueSessionHarness(t *testing.T, completer provider.Completer) *chat.Session {
	t.Helper()
	root := t.TempDir()
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, completer)
	session.UseTools = true
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	session.Tools = tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	store, err := setupSessionContext(session, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return session
}

// TestIntegrationQueueSteerCancelsTurnAndSendsSelected drives the headline
// flow end to end: while a turn is blocked, the user queues a message, opens
// the queue manager, and steers it - the blocked turn is cancelled and the
// steered message is the next provider request.
func TestIntegrationQueueSteerCancelsTurnAndSendsSelected(t *testing.T) {
	completer := &forceSendIntegrationCompleter{firstStarted: make(chan struct{})}
	session := queueSessionHarness(t, completer)
	sp := startScrollProgram(t, func(m *tuiModel) {
		m.session = session
		m.toolsOn = true
		m.waiting = false
	})
	sp.send(keyRunes("first question"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case <-completer.firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not reach the provider")
	}

	sp.send(keyRunes("steer me"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(2*time.Second, func(m *tuiModel) bool {
		return m.waiting && len(m.pendingQueue) == 1 && m.pendingQueue[0] == "steer me"
	}) {
		t.Fatal("message was not queued")
	}

	sp.send(tea.KeyMsg{Type: tea.KeyCtrlUp})
	if !sp.waitUntil(2*time.Second, func(m *tuiModel) bool { return m.queueMgr.open }) {
		t.Fatal("ctrl+up did not open the queue manager")
	}

	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(3*time.Second, func(m *tuiModel) bool {
		return !m.waiting && len(m.pendingQueue) == 0 && !m.queueMgr.open && len(completer.Requests()) == 2
	}) {
		reqs := completer.Requests()
		t.Fatalf("steer did not cancel the blocked turn and send the queued message: requests=%d, waiting=%v", len(reqs), nil)
	}
}

// TestIntegrationQueueDrainClosesManager pins the drain path: with the
// manager open, the turn ending auto-drains the queue and the manager closes
// itself at zero instead of leaving an invisible modal consuming keys.
func TestIntegrationQueueDrainClosesManager(t *testing.T) {
	completer := &queueDrainCompleter{release: make(chan struct{}), firstDone: make(chan struct{})}
	session := queueSessionHarness(t, completer)
	sp := startScrollProgram(t, func(m *tuiModel) {
		m.session = session
		m.toolsOn = true
		m.waiting = false
	})
	sp.send(keyRunes("first question"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(2*time.Second, func(m *tuiModel) bool { return m.waiting }) {
		t.Fatal("first turn never started")
	}

	sp.send(keyRunes("second question"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(2*time.Second, func(m *tuiModel) bool { return m.waiting && len(m.pendingQueue) == 1 }) {
		t.Fatal("second question was not queued")
	}
	sp.send(tea.KeyMsg{Type: tea.KeyCtrlUp})
	if !sp.waitUntil(2*time.Second, func(m *tuiModel) bool { return m.queueMgr.open }) {
		t.Fatal("queue manager did not open")
	}

	close(completer.release)
	if !sp.waitUntil(4*time.Second, func(m *tuiModel) bool {
		return !m.waiting && len(m.pendingQueue) == 0 && !m.queueMgr.open && len(completer.Requests()) == 2
	}) {
		t.Fatalf("drain did not empty the queue and close the manager: requests=%d", len(completer.Requests()))
	}
}

// TestQueueManagerEditFlowIntegration drives edit → re-queue → esc-restore
// through the real key path on a model with a live session.
func TestQueueManagerEditFlowIntegration(t *testing.T) {
	m := sendQueueModel(t)
	m.waiting = true
	m.pendingQueue = []string{"original text"}
	m.pendingQueueLabels = []string{"original text"}
	m.pendingSkillTurns = []*skillSlashSpec{nil}
	m.queueMgr = queueMgrState{open: true, selected: 0}

	// e loads the item into the composer and starts the edit.
	if _, _, _ = m.handleQueueManagerKey("e"); !m.editingQueued {
		t.Fatalf("e must start the edit")
	}
	if m.textarea.Value() != "original text" {
		t.Fatalf("composer must hold the queued text, got %q", m.textarea.Value())
	}

	// Type the updated version and Enter: it re-queues at the tail.
	m.textarea.SetValue("updated text")
	_, _, _ = m.handleChatEnter(false)
	if m.editingQueued {
		t.Fatalf("enter must end the edit")
	}
	if len(m.pendingQueue) != 1 || m.pendingQueue[0] != "updated text" {
		t.Fatalf("updated text must be queued: %v", m.pendingQueue)
	}

	// Edit again and Esc: the ORIGINAL item returns at its original index.
	m.queueMgr = queueMgrState{open: true, selected: 0}
	_, _, _ = m.handleQueueManagerKey("e")
	_, _, _ = m.handleChatControlKey("esc", false, false)
	if m.editingQueued {
		t.Fatalf("esc must end the edit")
	}
	if len(m.pendingQueue) != 1 || m.pendingQueue[0] != "updated text" {
		t.Fatalf("esc must restore the item unchanged: %v", m.pendingQueue)
	}
}

// TestQueueManagerNoQueuedInfoLine pins the UX fix: queueing a message no
// longer throws a "(queued: ...)" line into the transcript.
func TestQueueManagerNoQueuedInfoLine(t *testing.T) {
	m := sendQueueModel(t)
	m.waiting = true
	m.textarea.SetValue("queued message")
	_, _, _ = m.handleChatEnter(false)
	if len(m.pendingQueue) != 1 {
		t.Fatalf("message was not queued: %v", m.pendingQueue)
	}
	for _, b := range m.blocks {
		if strings.Contains(b.Text, "(queued:") {
			t.Fatalf("transcript must not contain queued-info lines, found %q", b.Text)
		}
	}
}

// TestQueueManagerPasteSwallowedWhileOpen pins INV-TUI-29 for the manager: a
// paste cannot land in the composer behind the open popup.
func TestQueueManagerPasteSwallowedWhileOpen(t *testing.T) {
	m := queueManagerKeysModel(t)
	before := m.textarea.Value()
	_, _ = updateMessageImpl(m, pasteTextMsg{text: "pasted"})
	if m.textarea.Value() != before {
		t.Fatalf("paste must be swallowed while the manager is open, draft now %q", m.textarea.Value())
	}
}

// TestQueueManagerCtrlCAndQCloseManager pins INV-TUI-26: ctrl+c closes the
// manager first (close-then-act); ctrl+q quits outright, never swallowed by
// the open manager.
func TestQueueManagerCtrlCAndQCloseManager(t *testing.T) {
	m := queueManagerKeysModel(t)
	_, _, cmds := m.handleChatKey("ctrl+q", false)
	if len(cmds) == 0 {
		t.Fatalf("ctrl+q over the open manager must return the quit command")
	}
	m = queueManagerKeysModel(t)
	_, _, _ = m.handleChatKey("ctrl+c", false)
	if m.queueMgr.open {
		t.Fatalf("ctrl+c must close the manager")
	}
}

// TestQueueSlashOpensAndRefuses tests the /queue command surface.
func TestQueueSlashOpensAndRefuses(t *testing.T) {
	m := sendQueueModel(t)
	if !m.handleSlash("/queue") || m.queueMgr.open {
		t.Fatalf("/queue with an empty queue must be a consumed no-op")
	}
	m.pendingQueue = []string{"x"}
	m.pendingQueueLabels = []string{"x"}
	m.pendingSkillTurns = []*skillSlashSpec{nil}
	if !m.handleSlash("/queue") || !m.queueMgr.open {
		t.Fatalf("/queue with a non-empty queue must open the manager")
	}
}

// TestBeginNewSessionResetsQueue pins the lifecycle reset on /new.
func TestBeginNewSessionResetsQueue(t *testing.T) {
	m := sendQueueModel(t)
	m.pendingQueue = []string{"x"}
	m.pendingQueueLabels = []string{"x"}
	m.pendingSkillTurns = []*skillSlashSpec{nil}
	m.queueMgr = queueMgrState{open: true}
	m.editingQueued = true
	m.beginNewSession()
	if len(m.pendingQueue) != 0 || m.queueMgr.open || m.editingQueued {
		t.Fatalf("beginNewSession must reset queue+manager+edit state")
	}
}
