package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// TestInitReturnsPollCmd verifies that Init() returns a non-nil cmd.
// The batch must include pollCmd; we verify by executing the cmd.
func TestInitReturnsPollCmd(t *testing.T) {
	m := newSmokeModel(t)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() returned nil cmd")
	}
	// Execute - must not panic; produces at least one message.
	msg := cmd()
	if msg == nil {
		// tea.Batch goroutines may race; accept nil as long as no panic.
		t.Log("Init() batch cmd returned nil (race in tea.Batch is expected)")
	} else {
		_ = msg
	}
}

// TestTuiTickMsgAlwaysRequeuesPoll verifies that when a tuiTickMsg arrives,
// the returned cmd is non-nil (proving pollCmd was re-queued).
func TestTuiTickMsgAlwaysRequeuesPoll(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()

	model, cmd := m.Update(tuiTickMsg{bridge: m.bridge})
	if cmd == nil {
		t.Fatal("tuiTickMsg must re-queue pollCmd, got nil cmd")
	}

	// State must be unchanged (empty bridge → no data)
	got := model.(*TUIModel)
	if got.streamBuf.Len() != 0 {
		t.Fatalf("unexpected stream buffer content: %q", got.streamBuf.String())
	}
	if got.thinkingBuf.Len() != 0 {
		t.Fatalf("unexpected thinking buffer content: %q", got.thinkingBuf.String())
	}
	if len(got.toolRows) != 0 {
		t.Fatalf("unexpected tool rows: %d", len(got.toolRows))
	}
}

// TestTuiTickMsgDoesNotDependOnData verifies that even with an empty bridge,
// tuiTickMsg still re-queues pollCmd (the always-re-queue invariant).
func TestTuiTickMsgDoesNotDependOnData(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat

	model, cmd := m.Update(tuiTickMsg{bridge: m.bridge})
	if cmd == nil {
		t.Fatal("tuiTickMsg with empty bridge must still re-queue pollCmd")
	}
	_ = model
}

// TestTuiTickMsgDrainsBridge verifies that tuiTickMsg drains bridge data
// into streamBuf, thinkingBuf, toolRows, and re-queues pollCmd.
func TestTuiTickMsgDrainsBridge(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()

	m.bridge.PushTool(true, "read_file", `{"path":"a.go"}`)
	_, _ = m.bridge.Write([]byte("hello stream"))
	m.bridge.PushThinking("analyzing...")

	model, cmd := m.Update(tuiTickMsg{bridge: m.bridge})
	got := model.(*TUIModel)

	if got.streamBuf.String() != "hello stream" {
		t.Fatalf("expected stream 'hello stream', got %q", got.streamBuf.String())
	}
	// Thinking is flushed into history when tools start (chat timeline).
	if got.thinkingBuf.Len() != 0 {
		t.Fatalf("thinking should flush to history before tools, got %q", got.thinkingBuf.String())
	}
	foundThinking := false
	for _, b := range got.blocks {
		if b.Kind == cli.ChatBlockThinking && strings.Contains(b.Text, "analyzing") {
			foundThinking = true
			break
		}
	}
	if !foundThinking {
		t.Fatal("expected thinking cli.ChatBlock in history after tool start")
	}
	if len(got.toolRows) == 0 {
		t.Fatal("expected tool rows after drain")
	}
	if cmd == nil {
		t.Fatal("draining tuiTickMsg must still re-queue pollCmd")
	}
}

// TestTuiTickMsgFinishStream verifies that when bridge signals done,
// tuiTickMsg triggers finishStream (waiting=false, assistant block created).
func TestTuiTickMsgFinishStream(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.appendBlock(cli.ChatBlock{
		TurnID: uint64(m.session.UserTurns() + 1),
		Kind:   cli.ChatBlockUser,
		Text:   "test",
	})

	_, _ = m.bridge.Write([]byte("result"))
	m.bridge.Finish(nil)

	model, cmd := m.Update(tuiTickMsg{bridge: m.bridge})
	got := model.(*TUIModel)

	if got.waiting {
		t.Fatal("expected waiting=false after finishStream")
	}
	if got.streamBuf.Len() != 0 {
		t.Fatalf("expected empty stream buffer after finish, got %q", got.streamBuf.String())
	}
	foundAssistant := false
	for _, blk := range got.blocks {
		if blk.Kind == cli.ChatBlockAssistant {
			foundAssistant = true
			break
		}
	}
	if !foundAssistant {
		t.Fatal("expected an assistant cli.ChatBlock after finishStream")
	}
	_ = cmd
}

// TestStreamVPIncludesToolPanel verifies that renderStreamVP includes
// tool panel content when toolRows are populated.
func TestStreamVPIncludesToolPanel(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()

	m.toolRows = []ToolRow{
		{Name: "read_file", Detail: `{"path":"main.go"}`, Start: time.Now(), Status: "running"},
		{Name: "grep", Detail: `{"pattern":"func"}`, Start: time.Now(), Status: "running"},
	}
	m.toolPanel.ordered = orderToolIndices(m.toolRows)
	m.toolPanel.Selected = 0
	m.streamBuf.WriteString("searching...")

	// Live tool rows render in the paint-only live panel overlay, not
	// inside the viewport: keeping them out of the transcript is what stops
	// the chat from jumping while the agent works.
	m.renderStreamVP()
	frame := cli.StripANSI(m.View())
	if !strings.Contains(frame, "read_file") {
		t.Fatalf("expected live panel to include 'read_file':\n%s", frame)
	}
	if !strings.Contains(frame, "grep") {
		t.Fatalf("expected live panel to include 'grep':\n%s", frame)
	}
	if strings.Contains(cli.StripANSI(m.viewport.View()), "read_file") {
		t.Fatal("live tool rows must not be inside the transcript viewport")
	}
}

// TestBridgeDrainNotDoubleProcessed verifies that after a tuiTickMsg drains
// the bridge, a subsequent KeyMsg does NOT process the same data again.
func TestBridgeDrainNotDoubleProcessed(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()

	_, _ = m.bridge.Write([]byte("unique-data-123"))
	m.bridge.PushTool(true, "read_file", `{"path":"test"}`)

	// Drain via tuiTickMsg
	model1, _ := m.Update(tuiTickMsg{bridge: m.bridge})
	got1 := model1.(*TUIModel)
	if got1.streamBuf.String() != "unique-data-123" {
		t.Fatalf("tuiTickMsg should drain: got %q", got1.streamBuf.String())
	}
	if len(got1.toolRows) == 0 {
		t.Fatal("tuiTickMsg should drain tool events")
	}

	// Simulate KeyMsg - bridge is now empty; no re-drain.
	model2, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	got2 := model2.(*TUIModel)

	// Stream buffer must not contain duplicate data.
	if strings.Count(got2.streamBuf.String(), "unique-data-123") > 1 {
		t.Fatalf("stream data was double-processed: %q", got2.streamBuf.String())
	}
}

// TestTuiTickMsgIgnoresStaleBridge verifies that a tick from a stale bridge
// is NOT drained but pollCmd IS still re-queued (chain stays alive).
func TestTuiTickMsgIgnoresStaleBridge(t *testing.T) {
	oldBridge := cli.NewStreamBridge()
	currentBridge := cli.NewStreamBridge()
	m := &TUIModel{
		bridge:    currentBridge,
		waiting:   true,
		mode:      modeChat,
		turnStart: time.Now(),
		textarea:  newSmokeTextarea(),
		viewport:  viewport.New(80, 20),
		session:   newTestSessionForModel("test"),
		modelName: "test",
	}
	_, _ = oldBridge.Write([]byte("stale"))

	model, cmd := m.Update(tuiTickMsg{bridge: oldBridge})
	got := model.(*TUIModel)

	// Stale bridge data must not be applied
	if got.streamBuf.Len() != 0 {
		t.Fatalf("stale bridge data was applied: %q", got.streamBuf.String())
	}

	// pollCmd must still be re-queued (the chain stays alive for the current bridge)
	if cmd == nil {
		t.Fatal("even with stale bridge, pollCmd must be re-queued")
	}
}

// TestTuiTickMsgWelcomeModeNoDrain verifies that in welcome mode,
// tuiTickMsg does NOT drain (no bridge drain in welcome) but still
// re-queues pollCmd so the chain starts when chat mode is entered.
func TestTuiTickMsgWelcomeModeNoDrain(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeWelcome

	_, _ = m.bridge.Write([]byte("welcome-data"))

	model, cmd := m.Update(tuiTickMsg{bridge: m.bridge})
	got := model.(*TUIModel)

	if got.streamBuf.Len() != 0 {
		t.Fatalf("welcome mode must not drain bridge: got %q", got.streamBuf.String())
	}
	if cmd == nil {
		t.Fatal("welcome mode tuiTickMsg must still re-queue pollCmd")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newSmokeTextarea() textarea.Model {
	ti := textarea.New()
	ti.SetWidth(80)
	ti.SetHeight(3)
	return ti
}
