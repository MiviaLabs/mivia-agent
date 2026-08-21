package cli

import (
	"strings"
	"testing"
	"time"
)

// TestTUISmoke_FullJourney exercises the TUI model state machine end-to-end
// without a real terminal (scripted TTY).
func TestTUISmoke_FullJourney(t *testing.T) {
	m := newSmokeModel(t)
	m.beginNewSession()
	m.enterChatMode()
	assertFreshChat(t, m)
	simulateSmokeTurn(m)
	assertSmokeFinished(t, m)
	assertSmokeView(t, m)
}

func assertFreshChat(t *testing.T, m *tuiModel) {
	t.Helper()
	if m.mode != modeChat {
		t.Fatalf("expected modeChat, got %v", m.mode)
	}
	if m.waiting {
		t.Fatal("fresh chat should not be waiting")
	}
	if len(m.toolRows) != 0 || len(m.pendingQueue) != 0 {
		t.Fatalf("fresh chat must clear tools/queue: tools=%d q=%d", len(m.toolRows), len(m.pendingQueue))
	}
	if len(m.blocks) != 0 {
		t.Fatalf("fresh chat should have 0 blocks, got %d", len(m.blocks))
	}
}

func simulateSmokeTurn(m *tuiModel) {
	m.appendBlock(ChatBlock{
		TurnID: uint64(m.session.UserTurns() + 1),
		Kind:   ChatBlockUser,
		Text:   "hello, what files are in my project?",
	})
	m.waiting = true
	m.turnStart = time.Now()
	m.toolRows = []toolRow{
		{Name: "read_file", Detail: `{"path":"main.go"}`, Start: time.Now(), Status: "running"},
		{Name: "grep", Detail: `{"pattern":"func"}`, Start: time.Now(), Status: "running"},
	}
	m.toolPanel.Selected = 0
	m.toolPanel.ordered = orderToolIndices(m.toolRows)
	m.streamBuf.WriteString("I found the following files in your project:\n\n- `main.go` contains the entry point\n- `internal/` has the core logic")
	m.thinkingBuf.WriteString("Analyzing project structure...\nChecking file contents...")
	m.toolRows[0].Done = true
	m.toolRows[0].Status = "completed"
	m.toolRows[0].Result = `package main\n\nfunc main() {\n\tprintln("hello")\n}`
	m.pendingQueue = nil
	_ = m.finishStream(nil)
}

func assertSmokeFinished(t *testing.T, m *tuiModel) {
	t.Helper()
	if m.waiting {
		t.Fatal("finishStream must clear waiting")
	}
	if len(m.toolRows) != 0 {
		t.Fatalf("finishStream must clear toolRows, got %d", len(m.toolRows))
	}
	if m.toolPanel.Selected != -1 {
		t.Fatalf("finishStream must reset toolPanel.Selected, got %d", m.toolPanel.Selected)
	}
	if m.streamBuf.Len() != 0 {
		t.Fatal("finishStream must reset streamBuf")
	}
	if len(m.blocks) == 0 {
		t.Fatal("expected at least 1 block after finishStream, got 0")
	}
	var kinds []string
	var texts []string
	for _, b := range m.blocks {
		kinds = append(kinds, string(b.Kind))
		texts = append(texts, b.Text)
	}
	assertKindsPresent(t, kinds, "user", "assistant", "tool", "turn_divider")
	if !strings.Contains(strings.Join(texts, "\n"), "main.go") {
		t.Fatalf("expected assistant text mentioning main.go, got %q", texts)
	}
}

func assertKindsPresent(t *testing.T, kinds []string, want ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, k := range kinds {
		set[k] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Fatalf("expected kind %q, got kinds=%v", w, kinds)
		}
	}
}

func assertSmokeView(t *testing.T, m *tuiModel) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked: %v", r)
		}
	}()
	view := m.View()
	if view == "" {
		t.Fatal("View() returned empty string")
	}
	if lines := strings.Split(view, "\n"); len(lines) > m.height {
		t.Fatalf("View() line count %d exceeds height %d", len(lines), m.height)
	}
	if plain := stripANSI(view); !strings.Contains(plain, "main.go") {
		t.Logf("warning: assistant text not visible in rendered view; plain=%q", plain)
	}
}

// TestTUISmoke_HelpCommand renders /help without panic.
func TestTUISmoke_HelpCommand(t *testing.T) {
	m := newSmokeModel(t)
	m.beginNewSession()
	m.enterChatMode()

	// Simulate user typing /help and handleSlash being called.
	ok := m.handleSlash("/help")
	if !ok {
		t.Fatal("handleSlash returned false for /help")
	}

	// Verify a help block was appended.
	if len(m.blocks) == 0 {
		t.Fatal("expected at least one block after /help")
	}
	foundHelp := false
	for _, b := range m.blocks {
		if strings.Contains(b.Text, "Commands") || strings.Contains(b.Text, "/help") {
			foundHelp = true
			break
		}
	}
	if !foundHelp {
		t.Fatalf("expected help text in blocks, got %v", m.blocks)
	}

	// View() must render without panic and not exceed height.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked after /help: %v", r)
		}
	}()
	view := m.View()
	if view == "" {
		t.Fatal("View() returned empty after /help")
	}
	viewLines := strings.Split(view, "\n")
	if len(viewLines) > m.height {
		t.Fatalf("View() line count %d exceeds height %d", len(viewLines), m.height)
	}

	// Verify help content is visible.
	plain := stripANSI(view)
	if !strings.Contains(plain, "help") && !strings.Contains(plain, "Commands") {
		t.Logf("warning: help text not visible in rendered view; plain=%q", plain)
	}
}

// TestTUISmoke_StreamDrainEvents simulates the bridge drain event loop
// that updateMessage performs, without tea.Program.
func TestTUISmoke_StreamDrainEvents(t *testing.T) {
	m := newSmokeModel(t)
	m.beginNewSession()
	m.enterChatMode()
	m.waiting = true
	m.turnStart = time.Now()
	m.appendBlock(ChatBlock{TurnID: 1, Kind: ChatBlockUser, Text: "list files"})

	seedBridgeToolsAndStream(m.bridge)
	stream, tools := drainAndAssertLive(t, m.bridge)
	m.applyToolEvents(tools)
	// Both tools ended in the same batch → progressive commit to history.
	if len(m.toolRows) != 0 {
		t.Fatalf("expected 0 open live tools after batch ends, got %d", len(m.toolRows))
	}
	toolBlocks := 0
	for _, b := range m.blocks {
		if b.Kind == ChatBlockTool {
			toolBlocks++
		}
	}
	if toolBlocks < 2 {
		t.Fatalf("expected ≥2 tool ChatBlocks in history, got %d", toolBlocks)
	}
	m.streamBuf.WriteString(stream)

	m.bridge.Finish(nil)
	d := m.bridge.Drain()
	done2 := d.Done
	doneErr2 := d.DoneErr
	if !done2 {
		t.Fatal("expected bridge done after Finish")
	}
	if doneErr2 != nil {
		t.Fatalf("expected nil doneErr2, got %v", doneErr2)
	}
	m.pendingQueue = nil
	if cmds := m.finishStream(nil); cmds != nil {
		t.Fatalf("finishStream should return nil cmds, got %v", cmds)
	}
	if len(m.blocks) == 0 {
		t.Fatal("expected blocks after finishStream")
	}
	assertSmokeView(t, m)
}

func seedBridgeToolsAndStream(b *StreamBridge) {
	b.PushTool(true, "read_file", `{"path":"."}`)
	b.PushTool(true, "grep", `{"pattern":"test"}`)
	_, _ = b.Write([]byte("Here are the files I found:\n\n- README.md\n- main.go\n"))
	b.PushTool(false, "read_file", `# README\n\nProject documentation`)
	b.PushTool(false, "grep", "test_file.go:10")
}

func drainAndAssertLive(t *testing.T, b *StreamBridge) (stream string, tools []bridgeToolEvt) {
	t.Helper()
	d := b.Drain()
	if d.Stream == "" {
		t.Fatal("expected stream text from drain")
	}
	if len(d.Tools) != 4 {
		t.Fatalf("expected 4 tool events, got %d", len(d.Tools))
	}
	if d.Done {
		t.Fatal("bridge should not be done yet (no Finish called)")
	}
	if d.DoneErr != nil {
		t.Fatalf("expected nil doneErr, got %v", d.DoneErr)
	}
	return d.Stream, d.Tools
}

// TestTUISmoke_ViewRenderAtVariousHeights verifies View() at different terminal sizes.
func TestTUISmoke_ViewRenderAtVariousHeights(t *testing.T) {
	for _, h := range []int{10, 24, 40} {
		m := newSmokeModel(t)
		m.height = h
		m.beginNewSession()
		m.enterChatMode()

		// Add some blocks so there's content to render.
		m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "hello", TurnID: 1})
		m.appendBlock(ChatBlock{Kind: ChatBlockAssistant, Text: "world", TurnID: 1})
		m.renderVP()

		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("height=%d: View() panicked: %v", h, r)
				}
			}()
			view := m.View()
			if view == "" {
				t.Fatalf("height=%d: View() returned empty", h)
			}
			viewLines := strings.Split(view, "\n")
			if len(viewLines) > m.height {
				t.Fatalf("height=%d: View() line count %d exceeds height %d", h, len(viewLines), m.height)
			}
		}()
	}
}

// newSmokeModel builds a minimal tuiModel for smoke tests (same as journeyModel
// but with a default chat mode setup convenience).
func newSmokeModel(t *testing.T) *tuiModel {
	t.Helper()
	ti := newComposerTextarea()
	ti.SetWidth(80)
	ti.SetHeight(3)
	m := &tuiModel{
		session:               newTestSessionForModel("test-model"),
		modelName:             "test-model",
		viewport:              newTranscriptViewport(80, 20),
		textarea:              ti,
		messages:              []string{},
		bridge:                NewStreamBridge(),
		toolPanel:             toolPanelState{Selected: -1},
		pendingQueue:          []string{},
		mode:                  modeWelcome,
		width:                 80,
		height:                40,
		ready:                 true,
		thinkingExpandDefault: false,
		followOutput:          true,
		workGroupCollapsed:    map[string]bool{},
		hitMap:                tuiHitMap{version: 1},
	}
	return m
}
