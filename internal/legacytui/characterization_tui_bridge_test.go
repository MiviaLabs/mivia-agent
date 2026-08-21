package legacytui

// TestCharacterization_TUIEventBridge pins the one flow the original
// characterization suite in internal/cli/characterization_test.go could not
// reach: a tool-call turn driven through the real interactive TUI, not the
// --json line-mode path every other test in that file uses. Split into this
// package (see internal/cli/characterization_test.go's package comment for
// the suite's origin and stability contract) because TUIModel, cli.ChatBlock,
// and the bubbletea test harness it drives now live in internal/legacytui.

import (
	"context"
	"encoding/json"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// charTUIBridgeTool is a minimal tool for TestCharacterization_TUIEventBridge,
// shaped like tui_force_send_integration_test.go's forceSendProbeTool: a
// fixed-string tool with no workspace dependency, so the turn's tool_start /
// tool_end pair is deterministic and platform-independent (unlike run_command
// against "echo", which the JSON-mode flows in characterization_test.go use).
type charTUIBridgeTool struct{}

func (charTUIBridgeTool) Name() string        { return "tui_bridge_probe" }
func (charTUIBridgeTool) Description() string { return "Returns a fixed probe result." }
func (charTUIBridgeTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (charTUIBridgeTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "tui-bridge-probe"}
}
func (charTUIBridgeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "probe result", nil
}

// charTUIBridgeCompleter is a scripted provider.Completer implementing
// ChatTurn (the tool-calling entrypoint internal/agent.Loop drives), same
// shape as tui_force_send_integration_test.go's forceSendToolStepCompleter:
// one tool call, then a final text answer.
type charTUIBridgeCompleter struct {
	mu    sync.Mutex
	calls int
}

func (c *charTUIBridgeCompleter) Name() string { return "char-tui-bridge-completer" }
func (c *charTUIBridgeCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (c *charTUIBridgeCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (c *charTUIBridgeCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	if n == 1 {
		call := provider.ToolCall{ID: "tui-bridge-1", Type: "function"}
		call.Function.Name = "tui_bridge_probe"
		call.Function.Arguments = `{}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	// internal/agent.Loop streams whenever a FinalWriter is attached
	// (runStep: stream := opts.FinalWriter != nil), which the TUI always
	// does (tui_start.go passes the streamBridge itself). commitFinalAnswer
	// only writes resp.Content to FinalWriter for the non-streaming case, so
	// a streaming ChatTurn (this one) must write its own deltas to
	// req.StreamWriter, exactly as provider.OpenAICompat's real ChatTurn
	// does and as forceSendIntegrationCompleter does in
	// tui_force_send_integration_test.go.
	if req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, "tool call handled")
	}
	return &provider.Response{Content: "tool call handled", FinishReason: "stop"}, nil
}

func (c *charTUIBridgeCompleter) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestCharacterization_TUIEventBridge pins the one flow the original 5-test
// suite could not reach: a tool-call turn driven through the real interactive
// TUI, not the --json line-mode path every other test in that file uses. All
// 5 original tests drive runConfiguredChat with plainUI:true (or a
// non-interactive list command); none of them construct a TUIModel, so
// nothing in this suite exercised classicAgentState's TUI counterpart -
// tui_events.go's agentEventBridgeCallback, tui_stream.go's streamBridge, or
// tui_start.go's startAI - the translation path the legacytui extraction
// (Item 4's largest clichat/legacytui crossing surface) put most at risk.
//
// This drives the turn through startScrollProgram (tui_program_harness_test.go),
// the same real, headless bubbletea Program already used by
// tui_force_send_integration_test.go's interactive-turn tests: real key
// events (composer typing + Enter) reach the real Update() dispatch, which
// calls the real startAI -> session.SendUserWithEventAndPersistedText ->
// internal/agent.Loop -> agentEventBridgeCallback -> streamBridge ->
// pollCmd's tuiTickMsg -> updateFromDrain chain a live TUI session runs in
// production. No new TUI-testing machinery is added: newSmokeModel,
// startScrollProgram, keyRunes, and hasToolBlock all pre-date this test.
//
// Like the other tests in characterization_test.go, the assertions are
// field/substring checks (a committed tool block, the final assistant
// text), not a byte-for-byte render snapshot - the terminal rendering
// itself is exercised elsewhere (e.g. tui_view_test.go) and is not this
// suite's concern.
func TestCharacterization_TUIEventBridge(t *testing.T) {
	completer := &charTUIBridgeCompleter{}
	session := chat.NewSession(&config.Resolved{Model: "test-model", SystemPrompt: "sys"}, completer)
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	session.Tools.Register(charTUIBridgeTool{})

	sp := startScrollProgram(t, func(m *TUIModel) {
		m.session = session
		m.toolsOn = true
		m.waiting = false
	})

	sp.send(keyRunes("run the probe tool"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})

	// The fake ChatTurn completer answers in-process with no network delay,
	// so the whole turn (tool call + final answer) can complete between two
	// poll ticks - there is no reliable window to catch m.toolRows non-empty
	// mid-turn. What is deterministic, and is the actual bridge behavior
	// under test, is that agent.EventToolStart/EventToolEnd travel through
	// agentEventBridgeCallback -> streamBridge -> updateFromDrain and land as
	// a committed cli.ChatBlockTool, exactly as TestCharacterization_ToolCallRoundTrip
	// pins tool_start/tool_end on the JSON-mode path.
	if !sp.waitUntil(3*time.Second, func(m *TUIModel) bool {
		return !m.waiting && completer.callCount() >= 2
	}) {
		t.Fatal("interactive turn did not finish (tool call + final answer)")
	}

	// sp.probe runs fn on the bubbletea Program's own goroutine (see
	// installProgramProbe), so assertions must not call t.Fatalf inside it -
	// only the test goroutine may fail the test. Capture state, then assert
	// after the probe returns.
	var (
		toolRowsLeft int
		blocks       []cli.ChatBlock
	)
	sp.probe(func(m *TUIModel) {
		toolRowsLeft = len(m.toolRows)
		blocks = append([]cli.ChatBlock(nil), m.blocks...)
	})

	if toolRowsLeft != 0 {
		t.Fatalf("finished turn left live toolRows uncommitted: %d rows", toolRowsLeft)
	}
	if !hasToolBlock(blocks, "tui_bridge_probe") {
		t.Fatalf("tool_start/tool_end never committed a cli.ChatBlockTool for tui_bridge_probe: %+v", blocks)
	}
	if !hasAssistantText(blocks, "tool call handled") {
		t.Fatalf("final assistant message missing from transcript: %+v", blocks)
	}
	if !hasBlockKind(blocks, cli.ChatBlockUser) {
		t.Fatalf("user turn missing from transcript: %+v", blocks)
	}
}
