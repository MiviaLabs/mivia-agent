package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
)

// TestTUISmoke_FullJourney exercises the TUI model state machine end-to-end
// without a real terminal (scripted TTY). It simulates:
//   - welcome → chat mode transition
//   - startAI lifecycle (waiting, user block, tool rows, stream buffer)
//   - finishStream (assistant + tool blocks verification)
//   - View() rendering without panic
//   - View line count ≤ height
func TestTUISmoke_FullJourney(t *testing.T) {
	m := newSmokeModel(t)
	m.beginNewSession()
	m.enterChatMode()

	// Verify we're in chat mode, not waiting, with clean state.
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

	// ---- Simulate startAI ----
	// Insert a user block as startAI would (including turn divider if blocks exist).
	if len(m.blocks) > 0 {
		m.appendBlock(ChatBlock{
			TurnID: uint64(m.session.UserTurns() + 1),
			Kind:   ChatBlockDivider,
		})
	}
	m.appendBlock(ChatBlock{
		TurnID: uint64(m.session.UserTurns() + 1),
		Kind:   ChatBlockUser,
		Text:   "hello, what files are in my project?",
	})
	m.waiting = true
	m.turnStart = time.Now()

	// Add tool rows (simulating tool events during streaming).
	m.toolRows = []toolRow{
		{Name: "read_file", Detail: `{"path":"main.go"}`, Start: time.Now(), Status: "running"},
		{Name: "grep", Detail: `{"pattern":"func"}`, Start: time.Now(), Status: "running"},
	}
	m.toolPanel.Selected = 0
	m.toolPanel.ordered = orderToolIndices(m.toolRows)

	// Simulate stream buffer having text.
	m.streamBuf.WriteString("I found the following files in your project:\n\n- `main.go` contains the entry point\n- `internal/` has the core logic")

	// Simulate thinking buffer.
	m.thinkingBuf.WriteString("Analyzing project structure...\nChecking file contents...")

	// Mark first tool done, second one still running.
	m.toolRows[0].Done = true
	m.toolRows[0].Status = "completed"
	m.toolRows[0].Result = `package main\n\nfunc main() {\n\tprintln("hello")\n}`

	// ---- Call finishStream (no pending queue, so no auto-send) ----
	m.pendingQueue = nil
	cmds := m.finishStream(nil)
	if cmds != nil {
		t.Fatalf("finishStream with empty queue should return nil cmds, got %v", cmds)
	}

	// ---- Verify post-finish state ----
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

	// ---- Verify blocks contain expected kinds ----
	if len(m.blocks) == 0 {
		t.Fatal("expected at least 1 block after finishStream, got 0")
	}

	// Collect block kinds for verification.
	var kinds []string
	var texts []string
	for _, b := range m.blocks {
		kinds = append(kinds, string(b.Kind))
		texts = append(texts, b.Text)
	}

	// Should have: user block, assistant block, thinking block, tool blocks, done divider.
	hasUser := false
	hasAssistant := false
	hasTool := false
	hasDivider := false
	for _, k := range kinds {
		switch k {
		case "user":
			hasUser = true
		case "assistant":
			hasAssistant = true
		case "tool":
			hasTool = true
		case "turn_divider":
			hasDivider = true
		}
	}
	if !hasUser {
		t.Fatalf("expected a user block, got kinds=%v", kinds)
	}
	if !hasAssistant {
		t.Fatalf("expected an assistant block, got kinds=%v", kinds)
	}
	if !hasTool {
		t.Fatalf("expected tool block(s), got kinds=%v", kinds)
	}
	// There should be at least the final done divider.
	if !hasDivider {
		t.Fatalf("expected a turn_divider block (done), got kinds=%v", kinds)
	}

	// Verify assistant text is present.
	allText := strings.Join(texts, "\n")
	if !strings.Contains(allText, "main.go") {
		t.Fatalf("expected assistant text mentioning main.go, got %q", allText)
	}

	// ---- Verify View() does not panic and line count ≤ height ----
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked: %v", r)
		}
	}()
	view := m.View()
	if view == "" {
		t.Fatal("View() returned empty string")
	}
	viewLines := strings.Split(view, "\n")
	if len(viewLines) > m.height {
		t.Fatalf("View() line count %d exceeds height %d", len(viewLines), m.height)
	}
	// View should contain some of the key text.
	plain := stripANSI(view)
	if !strings.Contains(plain, "main.go") {
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

	// Start waiting (as startAI would).
	m.waiting = true
	m.turnStart = time.Now()

	// Simulate a user block already present.
	m.appendBlock(ChatBlock{
		TurnID: 1,
		Kind:   ChatBlockUser,
		Text:   "list files",
	})

	// Push tool events and stream text onto the bridge.
	m.bridge.PushTool(true, "read_file", `{"path":"."}`)
	m.bridge.PushTool(true, "grep", `{"pattern":"test"}`)
	_, _ = m.bridge.Write([]byte("Here are the files I found:\n\n- README.md\n- main.go\n"))

	// Mark tools done.
	m.bridge.PushTool(false, "read_file", `# README\n\nProject documentation`)
	m.bridge.PushTool(false, "grep", "test_file.go:10")

	// Drain the bridge as updateMessage would.
	stream, tools, done, doneErr, thinking, stepDetail, stepDetailAt := m.bridge.Drain()

	if stream == "" {
		t.Fatal("expected stream text from drain")
	}
	if len(tools) != 4 { // 4 events: 2 starts + 2 ends
		t.Fatalf("expected 4 tool events, got %d", len(tools))
	}
	if done {
		t.Fatal("bridge should not be done yet (no Finish called)")
	}
	if doneErr != nil {
		t.Fatalf("expected nil doneErr, got %v", doneErr)
	}
	if thinking != "" {
		t.Logf("thinking text from drain: %q", thinking)
	}
	if stepDetail != "" {
		t.Logf("step detail from drain: %q at %v", stepDetail, stepDetailAt)
	}

	// Apply tool events to model as updateMessage would.
	m.applyToolEvents(tools)

	// Should have tool rows populated.
	if len(m.toolRows) != 2 {
		t.Fatalf("expected 2 tool rows after applyToolEvents, got %d", len(m.toolRows))
	}

	// Write stream text to buffer.
	m.streamBuf.WriteString(stream)

	// Now mark the bridge finished (simulating the goroutine's final call).
	m.bridge.Finish(nil)

	// Drain again to get done=true.
	_, _, done2, doneErr2, _, _, _ := m.bridge.Drain()
	if !done2 {
		t.Fatal("expected bridge done after Finish")
	}
	if doneErr2 != nil {
		t.Fatalf("expected nil doneErr2, got %v", doneErr2)
	}

	// finishStream cleans everything up.
	m.pendingQueue = nil
	cmds := m.finishStream(nil)
	if cmds != nil {
		t.Fatalf("finishStream should return nil cmds, got %v", cmds)
	}

	// Verify blocks after full cycle.
	if len(m.blocks) == 0 {
		t.Fatal("expected blocks after finishStream")
	}

	// View rendering.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked: %v", r)
		}
	}()
	view := m.View()
	if view == "" {
		t.Fatal("View() returned empty")
	}
	viewLines := strings.Split(view, "\n")
	if len(viewLines) > m.height {
		t.Fatalf("View() line count %d exceeds height %d", len(viewLines), m.height)
	}
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
	ti := textarea.New()
	ti.SetWidth(80)
	ti.SetHeight(3)
	m := &tuiModel{
		session:      &chat.Session{Model: "test-model"},
		modelName:    "test-model",
		viewport:     viewport.New(80, 20),
		textarea:     ti,
		messages:     []string{},
		bridge:       newStreamBridge(),
		toolPanel:    toolPanelState{Selected: -1},
		pendingQueue: []string{},
		mode:         modeWelcome,
		width:        80,
		height:       40,
		ready:        true,
	}
	return m
}
