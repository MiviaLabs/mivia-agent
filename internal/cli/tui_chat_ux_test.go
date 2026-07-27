package cli

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestEmptyContentToolsGetStatusLine — Phase A: no interim speech → system status + tools + final.
func TestEmptyContentToolsGetStatusLine(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "list files"})

	m.updateFromDrain(bridgeDrain{
		Tools: []bridgeToolEvt{
			{Start: true, ToolCallID: "t1", Name: "list_dir", Detail: `{"path":"."}`, At: time.Now()},
			{Start: false, ToolCallID: "t1", Name: "list_dir", Detail: "cmd/ internal/", At: time.Now()},
		},
	})
	// No ghost assistant for empty Content.
	for _, b := range m.blocks {
		if b.Kind == ChatBlockAssistant {
			t.Fatalf("unexpected assistant bubble on empty-content path: %q", b.Text)
		}
	}
	if !hasBlockKind(m.blocks, ChatBlockSystem) {
		t.Fatalf("expected status system line, kinds=%v", blockKinds(m.blocks))
	}
	foundStatus := false
	for _, b := range m.blocks {
		if b.Kind == ChatBlockSystem && strings.Contains(b.Text, "→") {
			foundStatus = true
			if !strings.Contains(b.Text, "Listing") && !strings.Contains(strings.ToLower(b.Text), "list") {
				// Verb map says Listing for list_dir.
				if !strings.Contains(b.Text, "Listing") {
					t.Fatalf("status line missing verb: %q", b.Text)
				}
			}
		}
	}
	if !foundStatus {
		t.Fatalf("no → status system block: %+v", m.blocks)
	}
	if !hasToolBlock(m.blocks, "list_dir") {
		t.Fatal("expected list_dir tool block")
	}
	// Composer stepDetail should reflect verb while tools ran; after end may still hold last verb.
	if m.stepDetail != "" && !strings.Contains(m.stepDetail, "Listing") {
		t.Fatalf("stepDetail=%q", m.stepDetail)
	}

	m.streamBuf.WriteString("Here are the files.")
	_ = m.finishStream(nil)
	if !hasAssistantText(m.blocks, "Here are the files") {
		t.Fatal("expected final assistant")
	}
	if !kindOrderContains(blockKinds(m.blocks),
		ChatBlockUser, ChatBlockSystem, ChatBlockTool, ChatBlockAssistant, ChatBlockDivider,
	) {
		t.Fatalf("unexpected order: %v", blockKinds(m.blocks))
	}
}

// TestShortInterimRejectedUsesStatus — Phase B + A together.
func TestShortInterimRejectedUsesStatus(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "go"})

	m.updateFromDrain(bridgeDrain{
		Interim: "OK.",
		Tools: []bridgeToolEvt{
			{Start: true, ToolCallID: "c1", Name: "grep", Detail: `{"pattern":"auth"}`, At: time.Now()},
			{Start: false, ToolCallID: "c1", Name: "grep", Detail: "matches", At: time.Now()},
		},
	})
	if hasAssistantText(m.blocks, "OK") {
		t.Fatal("short interim must not become assistant bubble")
	}
	if !hasBlockKind(m.blocks, ChatBlockSystem) {
		t.Fatal("expected status when interim rejected")
	}
	if !hasToolBlock(m.blocks, "grep") {
		t.Fatal("expected grep tool")
	}
}

// TestInterimAcceptedSkipsStatus — real prose + tools: speech, no status spam.
func TestInterimAcceptedSkipsStatus(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "find"})

	m.updateFromDrain(bridgeDrain{
		Interim: "I'll search the codebase first.",
		Tools: []bridgeToolEvt{
			{Start: true, ToolCallID: "c1", Name: "grep", Detail: `{"pattern":"bug"}`, At: time.Now()},
		},
	})
	if !hasAssistantText(m.blocks, "I'll search") {
		t.Fatal("expected interim speech bubble")
	}
	for _, b := range m.blocks {
		if b.Kind == ChatBlockSystem && strings.Contains(b.Text, "→") {
			t.Fatalf("status must not accompany real interim: %q", b.Text)
		}
	}
}

// TestAwaitingFirstActivityPlanning — Phase C.
func TestAwaitingFirstActivityPlanning(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.awaitingFirstActivity = true
	m.turnStart = time.Now().Add(-500 * time.Millisecond)
	m.width = 80
	m.height = 40
	m.layout()
	m.renderStreamVP()
	content := m.viewport.View()
	plain := stripANSI(content)
	if !strings.Contains(plain, "planning") {
		t.Fatalf("expected planning affordance, got %q", plain)
	}

	// Tool event clears awaiting.
	m.updateFromDrain(bridgeDrain{
		Tools: []bridgeToolEvt{
			{Start: true, ToolCallID: "x", Name: "read_file", Detail: `{"path":"a.go"}`, At: time.Now()},
		},
	})
	if m.awaitingFirstActivity {
		t.Fatal("awaiting should clear on tool start")
	}
	m.renderStreamVP()
	plain2 := stripANSI(m.viewport.View())
	if strings.Contains(plain2, "planning") {
		t.Fatalf("planning should be gone after tool: %q", plain2)
	}
}

// TestCancelKeepsInterimAndToolsInHistory — Phase E.
func TestCancelKeepsInterimAndToolsInHistory(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "explore"})
	m.updateFromDrain(bridgeDrain{
		Interim: "I'll inspect the project layout first.",
		Tools: []bridgeToolEvt{
			{Start: true, ToolCallID: "t1", Name: "list_dir", Detail: `{"path":"."}`, At: time.Now()},
			{Start: false, ToolCallID: "t1", Name: "list_dir", Detail: "ok", At: time.Now()},
			{Start: true, ToolCallID: "t2", Name: "read_file", Detail: `{"path":"main.go"}`, At: time.Now()},
		},
	})
	// Open tool still live; cancel should commit it.
	if len(m.toolRows) != 1 {
		t.Fatalf("expected 1 open tool, got %d", len(m.toolRows))
	}

	skipTA, _, _ := m.handleChatCancel()
	if !skipTA {
		t.Fatal("cancel should consume key")
	}
	if m.waiting {
		t.Fatal("waiting should be false after cancel")
	}
	if !hasAssistantText(m.blocks, "inspect the project") {
		t.Fatal("interim speech must remain")
	}
	if !hasToolBlock(m.blocks, "list_dir") {
		t.Fatal("completed tool must remain")
	}
	if !hasToolBlock(m.blocks, "read_file") {
		t.Fatal("open tool must be committed on cancel")
	}
	foundCancel := false
	for _, b := range m.blocks {
		if b.Kind == ChatBlockDivider && strings.Contains(b.Text, "cancelled") {
			foundCancel = true
		}
	}
	if !foundCancel {
		t.Fatalf("expected cancelled footer, kinds=%v texts=%v", blockKinds(m.blocks), blockTexts(m.blocks))
	}
}

// TestCancelBeforeFirstActivity — Phase E edge.
func TestCancelBeforeFirstActivity(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.awaitingFirstActivity = true
	m.turnStart = time.Now()
	m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "hi"})
	_, _, _ = m.handleChatCancel()
	if m.waiting {
		t.Fatal("must not wait after cancel")
	}
	// User + cancelled divider; no panic.
	if !hasBlockKind(m.blocks, ChatBlockUser) {
		t.Fatal("user block kept")
	}
	foundCancel := false
	for _, b := range m.blocks {
		if b.Kind == ChatBlockDivider && strings.Contains(b.Text, "cancelled") {
			foundCancel = true
		}
	}
	if !foundCancel {
		t.Fatalf("expected cancelled divider, got %v", blockKinds(m.blocks))
	}
}

// TestWorkHeaderInLivePanel — Phase F MVP.
func TestWorkHeaderInLivePanel(t *testing.T) {
	t.Parallel()
	now := time.Now()
	rows := []toolRow{
		{Name: "read_file", Detail: `{"path":"a"}`, Status: "running", Start: now},
		{Name: "grep", Detail: `{"pattern":"x"}`, Status: "running", Start: now},
	}
	st := toolPanelState{Selected: 0}
	out, _, _ := renderToolPanelWindow(rows, 80, now, st, 0, phaseTools, toolMaxVisibleRows, 0, 3*time.Second)
	plain := stripANSI(out)
	if !strings.Contains(plain, "Work ·") {
		t.Fatalf("expected Work header, got %q", plain)
	}
	if !strings.Contains(plain, "2 tools") {
		t.Fatalf("expected tool count, got %q", plain)
	}
}

// TestPushInterimGatesGhosts — bridge-level Phase B.
func TestPushInterimGatesGhosts(t *testing.T) {
	t.Parallel()
	b := newStreamBridge()
	b.PushInterim("OK.")
	b.PushInterim("I'll actually look into this carefully.")
	d := b.Drain()
	if strings.Contains(d.Interim, "OK") {
		t.Fatalf("ghost interim leaked: %q", d.Interim)
	}
	if !strings.Contains(d.Interim, "look into this") {
		t.Fatalf("real interim missing: %q", d.Interim)
	}
}

// TestFinishStreamCancelDoesNotAutoQueue — cancel stops the turn.
func TestFinishStreamCancelDoesNotAutoQueue(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.pendingQueue = []string{"next message"}
	// Avoid real session send: finishStream cancel path only.
	// sendNextQueued would call startAI which needs session; ensure we never get there.
	_ = m.finishStream(context.Canceled)
	if len(m.pendingQueue) != 1 {
		t.Fatalf("cancel must keep queue without auto-send, got %v", m.pendingQueue)
	}
	if m.waiting {
		t.Fatal("not waiting")
	}
}

func blockTexts(blocks []ChatBlock) []string {
	out := make([]string, len(blocks))
	for i, b := range blocks {
		out[i] = b.Text
	}
	return out
}
