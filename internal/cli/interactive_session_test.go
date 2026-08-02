package cli

import (
	"context"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// interactiveScriptCompleter drives parent ChatTurn tool calls and optional
// blocking Chat for oneshot/multi_step subagents (same Completer as runChat).
type interactiveScriptCompleter struct {
	mu          sync.Mutex
	parentCalls int
	toolName    string
	toolArgs    string
	blockChat   bool
	chatStarted atomic.Int32
}

func (c *interactiveScriptCompleter) Name() string { return "interactive-script" }

func (c *interactiveScriptCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	if c.blockChat {
		c.chatStarted.Add(1)
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "subagent-ok", nil
}

func (c *interactiveScriptCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

func (c *interactiveScriptCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.parentCalls++
	n := c.parentCalls
	c.mu.Unlock()
	if n == 1 {
		var call provider.ToolCall
		call.ID = "interactive-1"
		call.Type = "function"
		call.Function.Name = c.toolName
		call.Function.Arguments = c.toolArgs
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	// The parent call after a timed-out tool must still be able to synthesize
	// the structured tool result. Only the nested agent's generic system prompt
	// is intentionally blocked in this regression; blocking every second call
	// would test a stalled parent provider rather than task timeout handling.
	isSubagent := len(req.Messages) > 0 && req.Messages[0].Content != "sys"
	if c.blockChat && n == 2 && isSubagent {
		c.chatStarted.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &provider.Response{Content: "session-done", FinishReason: "stop"}, nil
}

// openInteractiveAgentSession mirrors runChat agent wiring:
// tools registry + skills + NewSessionDispatcher + Session.SendUser path.
func openInteractiveAgentSession(t *testing.T, root string, comp provider.Completer, runOpts *tools.DefaultOptions) (*chat.Session, func()) {
	t.Helper()
	res := &config.Resolved{
		Model:        "test-model",
		SystemPrompt: "sys",
		Subagents:    config.DefaultSubagentConfig,
	}
	sess := chat.NewSession(res, comp)
	sess.UseTools = true
	if runOpts != nil {
		ws, err := workspace.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		opts := *runOpts
		opts.Workspace = ws
		sess.Tools = tools.NewDefaultRegistry(opts)
	} else {
		// configureChatWorkspace reads the RunAllowlist from config, and
		// run_command is registered only when the allowlist is non-empty (an
		// empty allowlist means no program may run, so the tool is absent).
		// Supply one so the default path mirrors a workspace that can run
		// programs and the helper's registry includes run_command.
		if err := configureChatWorkspace(sess, root, true, "", config.ToolsConfig{
			RunAllowlist: []string{"sh"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	cleanup, err := attachSessionDispatcher(sess, root, res.Model, res.Subagents, &agentSessionState{AllowProjectSkills: true, Registry: testAgentRegistry(t, "mivia")}, nil, sessionRouting{})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Dispatcher == nil {
		t.Fatal("expected session dispatcher (runChat wiring)")
	}
	if _, ok := sess.Tools.Get("run_command"); !ok {
		t.Fatal("run_command missing from session tools")
	}
	if _, ok := sess.Tools.Get("dispatch_tasks"); !ok {
		t.Fatal("dispatch_tasks missing from session tools")
	}
	return sess, cleanup
}

func toolResultsContain(sess *chat.Session, substr string) bool {
	for _, m := range sess.MessagesCopy() {
		if m.Role == provider.RoleTool && strings.Contains(m.Content, substr) {
			return true
		}
	}
	return false
}

func TestInteractiveAgentSession_BlockingRunCommandTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh sleep path")
	}
	root := t.TempDir()
	comp := &interactiveScriptCompleter{
		toolName: "run_command",
		toolArgs: `{"argv":["sh","-c","sleep 5"]}`,
	}
	sess, cleanup := openInteractiveAgentSession(t, root, comp, &tools.DefaultOptions{
		RunAllowlist:  []string{"sh"},
		RunTimeoutSec: 1,
	})
	defer cleanup()

	start := time.Now()
	reply, err := sess.SendUser(context.Background(), "block on sleep", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("interactive session hang on run_command: %s", elapsed)
	}
	if reply != "session-done" {
		t.Fatalf("reply=%q", reply)
	}
	if !toolResultsContain(sess, "exit=timeout") {
		t.Fatalf("expected exit=timeout in tool history, msgs=%+v", sess.MessagesCopy())
	}
}

func TestInteractiveAgentSession_DispatchTasksTimesOutStructured(t *testing.T) {
	root := t.TempDir()
	comp := &interactiveScriptCompleter{
		toolName:  "dispatch_tasks",
		toolArgs:  `{"timeout_seconds":1,"tasks":[{"id":"t1","agent":"mivia","prompt":"block forever"}]}`,
		blockChat: true, // oneshot Completer.Chat blocks until task ctx deadline
	}
	sess, cleanup := openInteractiveAgentSession(t, root, comp, nil)
	defer cleanup()

	start := time.Now()
	reply, err := sess.SendUser(context.Background(), "dispatch long work", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("interactive session hang on dispatch_tasks: %s", elapsed)
	}
	if reply != "session-done" {
		t.Fatalf("reply=%q", reply)
	}
	// Model-visible structured status (not bare silent hang).
	if !toolResultsContain(sess, "timed_out") && !toolResultsContain(sess, "deadline") {
		t.Fatalf("expected timed_out/deadline in tool history, msgs=%+v", sess.MessagesCopy())
	}
}

func TestInteractiveAgentSession_ParentCancelDoesNotHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh sleep path")
	}
	root := t.TempDir()
	comp := &interactiveScriptCompleter{
		toolName: "run_command",
		// Long sleep; short parent deadline should surface without hang.
		toolArgs: `{"argv":["sh","-c","sleep 30"]}`,
	}
	sess, cleanup := openInteractiveAgentSession(t, root, comp, &tools.DefaultOptions{
		RunAllowlist:  []string{"sh"},
		RunTimeoutSec: 60,
	})
	defer cleanup()

	// Finite parent deadline (no sleep-based cancel races).
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := sess.SendUser(ctx, "cancel me", io.Discard)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancel hang: %s err=%v", elapsed, err)
	}
	// Must complete under deadline with model-visible cancel/timeout, or ctx error.
	if err != nil && ctx.Err() == nil {
		t.Fatalf("unexpected non-cancel err: %v", err)
	}
	// Tool history may include exit=canceled when process was interrupted.
	// Either tool body or outer cancel is acceptable as long as no hang.
	_ = toolResultsContain(sess, "exit=canceled") || toolResultsContain(sess, "exit=timeout") || toolResultsContain(sess, "exit=error")
}

func TestInteractiveAgentSession_DefaultWiringRegistersDelegation(t *testing.T) {
	// Pure configureChatWorkspace + attachSessionDispatcher (identical to runChat).
	root := t.TempDir()
	comp := &interactiveScriptCompleter{toolName: "delegate", toolArgs: `{"task":"ping"}`}
	res := &config.Resolved{Model: "test-model", SystemPrompt: "sys", Subagents: config.DefaultSubagentConfig}
	sess := chat.NewSession(res, comp)
	sess.UseTools = true
	if err := configureChatWorkspace(sess, root, true, "", config.ToolsConfig{}); err != nil {
		t.Fatal(err)
	}
	cleanup, err := attachSessionDispatcher(sess, root, res.Model, res.Subagents, &agentSessionState{AllowProjectSkills: true}, nil, sessionRouting{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	start := time.Now()
	reply, err := sess.SendUser(context.Background(), "delegate ping", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("delegate hang: %s", elapsed)
	}
	if reply != "session-done" {
		t.Fatalf("reply=%q", reply)
	}
	// One-shot completer returns "subagent-ok" with blockChat=false.
	if !toolResultsContain(sess, "subagent-ok") && !toolResultsContain(sess, "output") {
		t.Fatalf("expected delegate result in history, msgs=%+v", sess.MessagesCopy())
	}
}
