package cli

// Characterization suite for the Item 4 "split internal/cli" refactor
// (see mivia-agent-plans/plans/sdk-convergence-stage0-structural-prep.md,
// Item 4 "Delivery mechanics" step 1, and phase-1-cli-composition-split.md
// slice 1.1). These tests pin CURRENT, pre-refactor behavior across the CLI
// surfaces the later slices will move files out of. They must stay green,
// unedited, through every later slice: a refactor that needs one of these
// tests changed is a wrong slice, not a slice to push through.
//
// Deviation from the phase-1 spec, recorded here rather than silently:
// the spec's runChatJSON sketch takes CLI args and drives Execute end to
// end. This suite instead drives runConfiguredChat directly with a
// hand-built config.Resolved and chatInvocation, the same pattern this
// package's own chat_entrypoint_integration_test.go (fakeProviderServer)
// already established for exercising a real session against a stub
// provider. Going through Execute/parseChatInvocation would additionally
// require a real on-disk config file and environment-derived provider
// selection, which buys this suite nothing: the behavior being frozen
// lives in runConfiguredChat and below, not in flag parsing (already
// covered elsewhere, e.g. root_test.go and chat_flags_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// --- stub provider -----------------------------------------------------

// stubServer answers OpenAI-compatible streaming chat completions with one
// scripted SSE body per call, in order; the last scripted body repeats for
// any call beyond the scripted count. It is this package's characterization
// counterpart to chat_entrypoint_integration_test.go's fakeProviderServer
// (non-streaming, wire-content assertions) and to the streaming SSE stubs in
// internal/provider/stream_defects_test.go (sseServer) and
// scripts/e2e_context_compaction.py (_StubHandler) - same OpenAI
// chat-completions wire format, extended here to script a tool-call turn
// followed by a text turn.
type stubServer struct {
	turns []string
	calls int
}

// sseToolCallTurn is one scripted SSE body: a single tool call, assembled in
// one delta (see internal/provider/openai_compat_test.go's
// TestChatTurnStream_ToolCallsAssembled for the fragment-assembly shape this
// simplifies), followed by the finish_reason chunk and [DONE].
func sseToolCallTurn(id, name string, argumentsJSON string) string {
	argsEscaped, _ := json.Marshal(argumentsJSON)
	return fmt.Sprintf(
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":%q,\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
			"data: [DONE]\n\n",
		id, name, string(argsEscaped))
}

// sseTextTurn is one scripted SSE body: a single content delta carrying the
// whole answer, finished in the same chunk (see
// internal/provider/stream_defects_test.go's chunk fixtures for this exact,
// commonly-used shape), followed by [DONE].
func sseTextTurn(content string) string {
	body, _ := json.Marshal(content)
	return fmt.Sprintf(
		"data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n",
		string(body))
}

func newStubServer(turns ...string) *stubServer {
	return &stubServer{turns: turns}
}

func (s *stubServer) handler(w http.ResponseWriter, _ *http.Request) {
	idx := s.calls
	if idx >= len(s.turns) {
		idx = len(s.turns) - 1
	}
	s.calls++
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, s.turns[idx])
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *stubServer) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	return srv.URL
}

// --- driving a JSON-mode chat turn --------------------------------------

// baseResolvedConfig builds the config.Resolved runChatJSON needs to reach
// provider.New and produce a real completer against stub. Mirrors the
// fields chat_entrypoint_integration_test.go's
// TestRunConfiguredChatOneShotAdvertisesTheWholeUnion sets for the same
// reason (provider.New(res) is the one construction point every chat path
// goes through).
func baseResolvedConfig(t *testing.T, stubURL string) *config.Resolved {
	t.Helper()
	res := &config.Resolved{
		ProviderName: "openrouter",
		Model:        "test/model",
		Models:       []string{"test/model"},
		BaseURL:      stubURL,
		APIKey:       "test-key",
		APIKeyEnv:    "TEST_KEY",
		APIKeySet:    true,
		SystemPrompt: "ROOT PROMPT",
		Subagents:    config.DefaultSubagentConfig,
		Tools:        config.ToolsConfig{RunAllowlist: []string{"echo"}},
	}
	res.Subagents.StoreBackend = "sqlite"
	res.Subagents.StorePath = filepath.Join(t.TempDir(), "context.db")
	return res
}

// withPipedStdin replaces os.Stdin with a pipe carrying lines (one chat
// turn each), closed after the last line so replLineMode's scanner sees a
// clean EOF and runConfiguredChat returns. jsonMode's own gate
// (validateJSONModeInvocation) requires stdin to not be a terminal, which a
// test-harness pipe satisfies the same way a real shell pipe would.
func withPipedStdin(t *testing.T, lines []string) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = read
	t.Cleanup(func() { os.Stdin = original })
	go func() {
		defer write.Close()
		for _, line := range lines {
			_, _ = io.WriteString(write, line+"\n")
		}
	}()
}

// runChatJSON starts a chat session in JSON mode (line-mode, piped stdin)
// against stub and returns the full NDJSON stream the process writes, one
// element per line, blank lines dropped. userLines are fed as successive
// chat turns; workspacePath confines file/command tools and is where a
// [[hooks]] workspace config (if any) is read from.
func runChatJSON(t *testing.T, stub *stubServer, workspacePath string, userLines []string) []string {
	t.Helper()
	res := baseResolvedConfig(t, stub.start(t))
	withPipedStdin(t, userLines)
	// enterChatWorkspace (called inside runConfiguredChat) os.Chdir()s into
	// workspacePath and never restores it - chat_entrypoint_integration_test.go's
	// TestRunConfiguredChatOneShotAdvertisesTheWholeUnion relies on the same
	// t.Chdir behavior for the same reason: without it, this process's cwd
	// stays pointed at a t.TempDir() that a later test's cleanup deletes,
	// breaking every subsequent os.Getwd() call in the package.
	t.Chdir(workspacePath)
	invocation := chatInvocation{workspacePath: workspacePath, jsonMode: true, plainUI: true, quiet: true}
	stdout := captureStdout(t)
	err := runConfiguredChat(invocation, res)
	out := stdout()
	if err != nil {
		t.Fatalf("runConfiguredChat: %v\ncaptured stdout:\n%s", err, out)
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// --- NDJSON field-set assertions -----------------------------------------

// charLine decodes one captured line into its raw key set plus the parsed
// object, for field-set assertions that do not depend on values that
// legitimately vary between runs (ids, timestamps, durations).
type charLine struct {
	typ    string
	fields map[string]any
}

func decodeCharLines(t *testing.T, lines []string) []charLine {
	t.Helper()
	out := make([]charLine, 0, len(lines))
	for _, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line is not valid JSON: %v: %q", err, line)
		}
		typ, _ := m["type"].(string)
		if typ == "" {
			t.Fatalf("line has no string \"type\" field: %q", line)
		}
		out = append(out, charLine{typ: typ, fields: m})
	}
	return out
}

// linesOfType filters decoded lines to one NDJSON type, in order.
func linesOfType(lines []charLine, typ string) []charLine {
	var out []charLine
	for _, l := range lines {
		if l.typ == typ {
			out = append(out, l)
		}
	}
	return out
}

// assertFieldSet checks a decoded line carries exactly wantKeys (plus
// "type" itself), regardless of the values those fields hold - this is the
// oracle the phase-1 spec asks for: catch a field appearing, disappearing,
// or being renamed, without breaking on a legitimately varying id or
// timestamp.
func assertFieldSet(t *testing.T, l charLine, wantKeys ...string) {
	t.Helper()
	got := make([]string, 0, len(l.fields))
	for k := range l.fields {
		got = append(got, k)
	}
	want := append([]string{"type"}, wantKeys...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s field set = %v, want %v", l.typ, got, want)
	}
}

// --- flow 1: one tool call, then a final assistant message --------------

func TestCharacterization_ToolCallRoundTrip(t *testing.T) {
	stub := newStubServer(
		sseToolCallTurn("call_1", "run_command", `{"argv":["echo","ok"]}`),
		sseTextTurn("done"),
	)
	ws := t.TempDir()
	lines := runChatJSON(t, stub, ws, []string{"run echo ok"})
	decoded := decodeCharLines(t, lines)

	// Two tool_start lines per call is current, real behavior, not a test
	// bug: internal/agent/loop_tools.go emits one when the call is queued
	// (Input carries the redacted argument preview) and
	// internal/agent/loop_tool_exec.go emits a second right before dispatch
	// (Detail "running", no Input of its own - eventPreview falls back to
	// the detail text). A later slice must keep emitting both, in this
	// order, or this assertion is the one that should change - deliberately,
	// not as a side effect of a file move.
	starts := linesOfType(decoded, "tool_start")
	if len(starts) != 2 {
		t.Fatalf("tool_start count = %d, want 2 (queued + running): %v", len(starts), lines)
	}
	for _, s := range starts {
		assertFieldSet(t, s, "tool_call_id", "name", "input")
		if s.fields["name"] != "run_command" {
			t.Fatalf("tool_start name = %v, want run_command", s.fields["name"])
		}
	}
	if starts[0].fields["input"] != `{"argv":["echo","ok"]}` {
		t.Fatalf("queued tool_start input = %v, want the argument preview", starts[0].fields["input"])
	}
	if starts[1].fields["input"] != "running" {
		t.Fatalf("pre-dispatch tool_start input = %v, want %q", starts[1].fields["input"], "running")
	}

	ends := linesOfType(decoded, "tool_end")
	if len(ends) != 1 {
		t.Fatalf("tool_end count = %d, want 1: %v", len(ends), lines)
	}
	assertFieldSet(t, ends[0], "tool_call_id", "name", "output", "status")
	if ends[0].fields["name"] != "run_command" {
		t.Fatalf("tool_end name = %v, want run_command", ends[0].fields["name"])
	}
	if ends[0].fields["status"] != "ok" {
		t.Fatalf("tool_end status = %v, want ok (echo must succeed): %v", ends[0].fields["status"], lines)
	}

	assertFinalAssistantMessage(t, decoded, "done")
}

// --- flow 2: plain text only, no tool call -------------------------------

func TestCharacterization_PlainTextOnly(t *testing.T) {
	stub := newStubServer(sseTextTurn("hello there"))
	ws := t.TempDir()
	lines := runChatJSON(t, stub, ws, []string{"say hi"})
	decoded := decodeCharLines(t, lines)

	if got := linesOfType(decoded, "tool_start"); len(got) != 0 {
		t.Fatalf("unexpected tool_start in a text-only turn: %v", lines)
	}
	if got := linesOfType(decoded, "tool_end"); len(got) != 0 {
		t.Fatalf("unexpected tool_end in a text-only turn: %v", lines)
	}
	assertFinalAssistantMessage(t, decoded, "hello there")
}

// assertFinalAssistantMessage checks the turn ended cleanly: the streamed
// "chunk" lines concatenate to want, followed by a "done" line, with no
// "error" or "cancelled" line anywhere in the turn.
func assertFinalAssistantMessage(t *testing.T, decoded []charLine, want string) {
	t.Helper()
	if got := linesOfType(decoded, "error"); len(got) != 0 {
		t.Fatalf("unexpected error line(s): %+v", got)
	}
	if got := linesOfType(decoded, "cancelled"); len(got) != 0 {
		t.Fatalf("unexpected cancelled line(s): %+v", got)
	}
	var text strings.Builder
	for _, l := range linesOfType(decoded, "chunk") {
		assertFieldSet(t, l, "text")
		s, _ := l.fields["text"].(string)
		text.WriteString(s)
	}
	if text.String() != want {
		t.Fatalf("assembled chunk text = %q, want %q", text.String(), want)
	}
	done := linesOfType(decoded, "done")
	if len(done) != 1 {
		t.Fatalf("done line count = %d, want 1", len(done))
	}
	assertFieldSet(t, done[0], "session_id")
	if s, _ := done[0].fields["session_id"].(string); s == "" {
		t.Fatalf("done.session_id is empty")
	}
}

// --- flow 3: mivia agents list from a temp workspace ---------------------

func TestCharacterization_AgentsListFromWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := t.TempDir()
	writeCatalogAgent(t, filepath.Join(ws, ".mivia", "agents"), "reviewer",
		"name = \"reviewer\"\ndescription = \"reviews changes\"\ntools = [\"read_file\"]\n")

	var out, errOut strings.Builder
	if err := runAgentsWithIO([]string{"list", "--workspace", ws}, &out, &errOut); err != nil {
		t.Fatalf("agents list: %v (stderr: %s)", err, errOut.String())
	}
	text := out.String()
	for _, want := range []string{"name: reviewer", "source: workspace", "state: selectable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("agents list output missing %q:\n%s", want, text)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected warnings: %s", errOut.String())
	}
}

// --- flow 4: mivia workflows list from a temp workspace -------------------

func TestCharacterization_WorkflowsListFromWorkspace(t *testing.T) {
	ws := t.TempDir()
	writeWorkflowFixture(t, ws, "release", `version = 1
name = "release"
description = "Cut a release."
initial_step = "plan"

[[steps]]
id = "plan"
kind = "human_gate"

[[transitions]]
from = "plan"
to = "success"
match = { status = "approved" }
`)

	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"list", "--workspace", ws}, &out, &errOut); err != nil {
		t.Fatalf("workflows list: %v (stderr: %s)", err, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "Workflows (1):") {
		t.Fatalf("workflows list output missing count header:\n%s", text)
	}
	if !strings.Contains(text, "release") {
		t.Fatalf("workflows list output missing workflow name:\n%s", text)
	}
}

// --- flow 5: a PreToolUse hook blocks a tool call -------------------------

func TestCharacterization_HookBlockedToolCall(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("POSIX script fixture")
	}
	t.Setenv("HOME", t.TempDir())
	ws := t.TempDir()
	mkdirT(t, filepath.Join(ws, ".mivia"))
	writeFileT(t, filepath.Join(ws, ".mivia", "mivia.toml"), `[[hooks]]
event = "PreToolUse"
matcher = "^run_command$"

  [[hooks.handlers]]
  type = "command"
  argv = ["./guard.sh"]
`)
	writeFileT(t, filepath.Join(ws, ".mivia", "guard.sh"), "#!/bin/sh\nprintf 'policy forbids this argv\\n' >&2\nexit 2\n")
	if err := os.Chmod(filepath.Join(ws, ".mivia", "guard.sh"), 0o700); err != nil {
		t.Fatal(err)
	}

	stub := newStubServer(
		sseToolCallTurn("call_1", "run_command", `{"argv":["echo","ok"]}`),
		sseTextTurn("acknowledged"),
	)
	lines := runChatJSON(t, stub, ws, []string{"run echo ok"})
	decoded := decodeCharLines(t, lines)

	ends := linesOfType(decoded, "tool_end")
	if len(ends) != 1 {
		t.Fatalf("tool_end count = %d, want 1: %v", len(ends), lines)
	}
	assertFieldSet(t, ends[0], "tool_call_id", "name", "output", "status")
	if ends[0].fields["status"] != "failed" {
		t.Fatalf("blocked tool_end status = %v, want failed (a hook block has no separate wire status - see runtime.blockedResult/toolEndStatus): %v",
			ends[0].fields["status"], lines)
	}
	output, _ := ends[0].fields["output"].(string)
	if !strings.Contains(output, "policy forbids this argv") {
		t.Fatalf("blocked tool_end output = %q, want it to carry the hook's reason", output)
	}

	assertFinalAssistantMessage(t, decoded, "acknowledged")
}

// --- flow 6: a tool-call turn through the real interactive TUI event bridge -

// charTUIBridgeTool is a minimal tool for TestCharacterization_TUIEventBridge,
// shaped like tui_force_send_integration_test.go's forceSendProbeTool: a
// fixed-string tool with no workspace dependency, so the turn's tool_start /
// tool_end pair is deterministic and platform-independent (unlike run_command
// against "echo", which the JSON-mode flows in this file use).
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
// TUI, not the --json line-mode path every other test in this file uses. All
// 5 original tests drive runConfiguredChat with plainUI:true (or a
// non-interactive list command); none of them construct a tuiModel, so
// nothing in this suite exercised classicAgentState's TUI counterpart -
// tui_events.go's agentEventBridgeCallback, tui_stream.go's streamBridge, or
// tui_start.go's startAI - the translation path the upcoming legacytui
// extraction (Item 4's largest clichat/legacytui crossing surface) puts most
// at risk.
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
// Like the other tests in this file, the assertions are field/substring
// checks (a committed tool block, the final assistant text), not a
// byte-for-byte render snapshot - the terminal rendering itself is exercised
// elsewhere (e.g. tui_view_test.go) and is not this suite's concern.
func TestCharacterization_TUIEventBridge(t *testing.T) {
	completer := &charTUIBridgeCompleter{}
	session := chat.NewSession(&config.Resolved{Model: "test-model", SystemPrompt: "sys"}, completer)
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	session.Tools.Register(charTUIBridgeTool{})

	sp := startScrollProgram(t, func(m *tuiModel) {
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
	// a committed ChatBlockTool, exactly as TestCharacterization_ToolCallRoundTrip
	// pins tool_start/tool_end on the JSON-mode path.
	if !sp.waitUntil(3*time.Second, func(m *tuiModel) bool {
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
		blocks       []ChatBlock
	)
	sp.probe(func(m *tuiModel) {
		toolRowsLeft = len(m.toolRows)
		blocks = append([]ChatBlock(nil), m.blocks...)
	})

	if toolRowsLeft != 0 {
		t.Fatalf("finished turn left live toolRows uncommitted: %d rows", toolRowsLeft)
	}
	if !hasToolBlock(blocks, "tui_bridge_probe") {
		t.Fatalf("tool_start/tool_end never committed a ChatBlockTool for tui_bridge_probe: %+v", blocks)
	}
	if !hasAssistantText(blocks, "tool call handled") {
		t.Fatalf("final assistant message missing from transcript: %+v", blocks)
	}
	if !hasBlockKind(blocks, ChatBlockUser) {
		t.Fatalf("user turn missing from transcript: %+v", blocks)
	}
}

// --- shared fixture writers ------------------------------------------------

func mkdirT(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeFileT(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
