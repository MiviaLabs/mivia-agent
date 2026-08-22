package cli

// R3: end-to-end dispatched ask/answer round-trip.
//
// cliorchestrate.DispatchTasksToolForTest.Execute → NewSessionDispatcher → coordinator pool →
// agent multi_step handler → post_message(kind=ask) → peer
// post_message(kind=answer) → asker unblocked. The fake completer emits real
// provider.ToolCall replies so the multi_step handlers actually invoke
// post_message (the existing ask tests call c.Spawn directly, and the schema
// e2e tests only exercise structured output — neither covers this wiring).

import (
	"context"
	"encoding/json"
	"fmt"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Prompt markers let the shared completer tell the two dispatched tasks apart.
const (
	reviewerAskMarker = "[reviewer-ask-e2e]"
	auditorAskMarker  = "[auditor-ask-e2e]"
)

// askToolCompleter drives a dispatched reviewer→auditor ask/answer round-trip
// through real tool calls; the auditor keeps its loop alive with findings
// until the ask lands, then answers it.
//
// A4 determinism: the coordinator dispatches tasks by iterating a map, so
// under GOMAXPROCS=1 the auditor goroutine could run to step exhaustion
// before the reviewer ever posts its ask. askPosted (closed once, on the
// reviewer's post_message(kind=ask) call) blocks the auditor's keep-alive
// branch until the ask fires — a blocked goroutine yields the P, so the
// reviewer is guaranteed to run. After the channel unblocks, keepAliveYield
// yields the P again before each keep-alive finding posts: without it, a
// scheduler preemption right after askPosted closes could let the auditor
// burn its entire step budget in one quantum before the ask reaches its
// mailbox (the ask then wrongly declines "target_terminal").
type askToolCompleter struct {
	name string
	next atomic.Int64

	askPosted     chan struct{}
	askPostedOnce sync.Once
}

func (c *askToolCompleter) Name() string { return c.name }
func (c *askToolCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *askToolCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *askToolCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch {
	case messagesContain(req, reviewerAskMarker):
		return c.reviewerTurn(req), nil
	case messagesContain(req, auditorAskMarker):
		return c.auditorTurn(req), nil
	}
	return &provider.Response{Content: `{"ok":true}`, FinishReason: "stop"}, nil
}

func (c *askToolCompleter) newToolCall(name, args string) provider.ToolCall {
	var call provider.ToolCall
	call.ID = fmt.Sprintf("call_%s_%d", name, c.next.Add(1))
	call.Type = "function"
	call.Function.Name = name
	call.Function.Arguments = args
	return call
}

// reviewerTurn: first turn posts a blocking ask; later turns report the tool
// result (which after a successful round-trip reads {"status":"answered",...}).
func (c *askToolCompleter) reviewerTurn(req provider.Request) *provider.Response {
	if result := lastToolResult(req, toolPostMessage); result != "" {
		return &provider.Response{
			Content:      "reviewer tool result: " + result,
			FinishReason: "stop",
		}
	}
	// The reviewer's blocking ask has been emitted: unblock the auditor's
	// keep-alive loop (closed exactly once).
	c.askPostedOnce.Do(func() { close(c.askPosted) })
	return &provider.Response{
		ToolCalls: []provider.ToolCall{c.newToolCall(toolPostMessage,
			`{"kind":"ask","to_role":"auditor","body":"please verify the fix at L42","wait_seconds":10}`)},
		FinishReason: "tool_calls",
	}
}

// auditorTurn: the keep-alive loop's contract is "continue posting findings
// until the injected ask_id is present, then answer on that same step" — it
// must not return a final response (FinishReason "stop") before the ask
// arrives, or the auditor completes without answering. Before the first
// keep-alive finding the branch blocks on askPosted, which the reviewer closes
// when it emits its post_message(kind=ask) tool call: a blocked goroutine
// reliably yields the P, so a starved -cpu 1 run cannot exhaust the step
// budget before the reviewer's goroutine posts the ask (A4); after the
// channel unblocks, every keep-alive finding additionally yields for
// keepAliveYield so an async preemption can never let the auditor exhaust its
// step budget before the reviewer's ask tool mailbox-delivers (A4).
func (c *askToolCompleter) auditorTurn(req provider.Request) *provider.Response {
	if auditorAnswered(req) {
		return &provider.Response{Content: "auditor answered the ask", FinishReason: "stop"}
	}
	if id := extractAskID(req); id != "" {
		return &provider.Response{
			ToolCalls: []provider.ToolCall{c.newToolCall(toolPostMessage,
				`{"kind":"answer","body":"looks solid","in_reply_to":"`+id+`"}`)},
			FinishReason: "tool_calls",
		}
	}
	// Block until the reviewer's goroutine has emitted its blocking ask (the
	// channel is closed exactly once; later reads return immediately). A
	// GOMAXPROCS=1 scheduler would otherwise let the auditor run all its steps
	// first (A4); a blocked goroutine yields the P with no CPU starvation
	// possible, so the reviewer's worker runs, its ask reaches this task's
	// mailbox, and the ask_id appears at the next step boundary — answered on
	// that same step, preserving the loop contract. The keepAliveYield after
	// the unblock guarantees the reviewer's ask tool — which may have been
	// delayed by an async scheduler preemption right after its ChatTurn —
	// executes and mailbox-delivers the ask before this loop can consume its
	// step budget (A4 under GOMAXPROCS=1).
	<-c.askPosted
	select {
	case <-time.After(keepAliveYield):
	}
	return &provider.Response{
		ToolCalls: []provider.ToolCall{c.newToolCall(toolPostMessage,
			`{"kind":"finding","body":"auditor waiting for the ask"}`)},
		FinishReason: "tool_calls",
	}
}

func messagesContain(req provider.Request, marker string) bool {
	for _, m := range req.Messages {
		if strings.Contains(m.Content, marker) {
			return true
		}
	}
	return false
}

// lastToolResult returns the most recent RoleTool result for a tool name, or
// "" when the loop has not executed that tool yet.
func lastToolResult(req provider.Request, name string) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role == provider.RoleTool && m.Name == name {
			return m.Content
		}
	}
	return ""
}

// extractAskID reads the injected "ask_id: <id>" line a parent-routed ask
// arrives with at a step boundary.
//
// Only user-role messages are scanned. Tool-bearing subagent system prompts
// now carry the shared messaging protocol block (subagents.MessagingProtocolPrompt),
// which itself contains the example text "ask_id: <id>" — documentation, not an
// injected ask. The real injected frame is a user-role <parent-message> block,
// so skipping the system prompt keeps the example from being mistaken for an
// ask.
func extractAskID(req provider.Request) string {
	const prefix = "ask_id: "
	for _, m := range req.Messages {
		if m.Role != provider.RoleUser {
			continue
		}
		idx := strings.Index(m.Content, prefix)
		if idx < 0 {
			continue
		}
		rest := m.Content[idx+len(prefix):]
		if end := strings.IndexByte(rest, '\n'); end >= 0 {
			rest = rest[:end]
		}
		if id := strings.TrimSpace(rest); id != "" {
			return id
		}
	}
	return ""
}

// auditorAnswered reports whether this task's own post_message history already
// contains a successful answer (findings report "posted", never "answered").
func auditorAnswered(req provider.Request) bool {
	for _, m := range req.Messages {
		if m.Role == provider.RoleTool && m.Name == toolPostMessage {
			if strings.Contains(m.Content, `"status":"answered"`) {
				return true
			}
		}
	}
	return false
}

// newAskE2EDispatchTool builds a real session dispatcher (agent → multi_step)
// with reviewer and auditor agents and returns the dispatch_tasks tool, the
// dispatcher, and the shared ledger repo for assertions.
func newAskE2EDispatchTool(t *testing.T, cfg config.SubagentConfig) (*cliorchestrate.DispatchTasksToolForTest, *runtime.Dispatcher, ledger.LedgerRepository) {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	repo := ledger.NewMemoryLedgerRepository()
	comp := &askToolCompleter{name: "ask-e2e", askPosted: make(chan struct{})}
	if cfg.SchemaRetryMax <= 0 {
		cfg.SchemaRetryMax = 2
	}
	if cfg.NestedSteps == 0 {
		// A4 determinism: the auditor keeps its loop alive with real
		// post_message(kind=finding) calls until the reviewer's ask is injected
		// at a step boundary. Both tasks dispatch concurrently (the coordinator
		// walks a map, so either may start first); the auditor's keep-alive
		// branch blocks on the reviewer's askPosted channel before its first
		// finding and yields for keepAliveYield before every finding, so a
		// starved -cpu 1 run cannot exhaust max_steps before the reviewer's ask
		// lands (a blocked goroutine yields the P — no CPU starvation
		// possible). 256 is the backstop budget for a slow-starting reviewer,
		// while the test still fails cleanly if the ask/answer wiring itself
		// breaks.
		cfg.NestedSteps = 256
	}
	if cfg.Messaging.MaxMessagesPerTask < 256 {
		// Each keep-alive finding (and the eventual answer) consumes one
		// max_messages_per_task slot. The default 32 would be exhausted by the
		// auditor's keep-alive before a starved scheduler lets the reviewer's
		// ask arrive, turning the step-budget fix back into a quota failure.
		cfg.Messaging.MaxMessagesPerTask = 512
	}
	if cfg.InlineOutputBytes == 0 {
		cfg.InlineOutputBytes = 4096
	}
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:      reg,
		Completer:     comp,
		ProviderName:  "test",
		Model:         "test-model",
		Config:        cfg,
		Repo:          repo,
		AgentRegistry: testAgentRegistry(t, "reviewer", "auditor"),
	})
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	raw, ok := reg.Get(cliorchestrate.ToolDispatchTasks)
	if !ok {
		d.Close()
		t.Fatal("dispatch_tasks not registered")
	}
	tool, ok := raw.(*cliorchestrate.DispatchTasksToolForTest)
	if !ok {
		d.Close()
		t.Fatalf("dispatch_tasks type %T", raw)
	}
	return tool, d, repo
}

// TestE2E_DispatchAskAnswerRoundTrip exercises the full dispatched path:
// dispatch_tasks → coordinator pool → agent multi_step handler →
// post_message(kind=ask) → peer post_message(kind=answer) → asker unblocked.
func TestE2E_DispatchAskAnswerRoundTrip(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.Routing.Allow = []string{"reviewer->auditor"}
	tool, closer, repo := newAskE2EDispatchTool(t, cfg)
	defer closer.Close()

	args, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{"id": "rev-1", "agent": "reviewer",
				"prompt": reviewerAskMarker + " ask the auditor to verify the fix at L42"},
			{"id": "aud-1", "agent": "auditor",
				"prompt": auditorAskMarker + " you are the auditor; answer any ask from the reviewer"},
		},
	})
	body, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute transport err: %v body=%s", err, body)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("result not JSON array: %v body=%s", err, body)
	}
	if len(parsed) != 2 {
		t.Fatalf("want 2 task results, got %d body=%s", len(parsed), body)
	}

	rev := dispatchResultFor(t, parsed, "rev-1")
	aud := dispatchResultFor(t, parsed, "aud-1")
	if rev["status"] != "completed" {
		t.Fatalf("reviewer status=%v full=%#v", rev["status"], rev)
	}
	if aud["status"] != "completed" {
		t.Fatalf("auditor status=%v full=%#v", aud["status"], aud)
	}

	// The asker's final reply echoes the post_message tool result, so it must
	// show it received the answer: status "answered" with the answer body.
	revOut, ok := rev["output"].(map[string]any)
	if !ok {
		t.Fatalf("reviewer output not a multi_step envelope: %#v", rev["output"])
	}
	revText, _ := revOut["output"].(string)
	if !strings.Contains(revText, `"status":"answered"`) {
		t.Fatalf("reviewer result does not show answered: %q", revText)
	}
	if !strings.Contains(revText, "looks solid") {
		t.Fatalf("reviewer result does not carry the answer body: %q", revText)
	}

	// Both tasks reached terminal (completed) in the ledger.
	runID := findDispatchedRunID(t, repo)
	tasks, err := repo.ListTasks(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("ledger tasks=%d want 2", len(tasks))
	}
	for _, task := range tasks {
		if task.Status != string(ledger.TaskStatusCompleted) {
			t.Fatalf("task %s status=%s want completed", task.TaskID, task.Status)
		}
	}

	// The run ledger carries both the ask and the answer with the answer body.
	kinds, synopses := runMessageKinds(t, repo, runID)
	if !slices.Contains(kinds, string(agentmsg.KindAsk)) {
		t.Fatalf("run messages lack an ask: %v", kinds)
	}
	if !slices.Contains(kinds, string(agentmsg.KindAnswer)) {
		t.Fatalf("run messages lack an answer: %v", kinds)
	}
	if !slices.Contains(synopses, "looks solid") {
		t.Fatalf("answer body missing from ledger synopses: %v", synopses)
	}
}

func dispatchResultFor(t *testing.T, parsed []map[string]any, taskID string) map[string]any {
	t.Helper()
	for _, r := range parsed {
		if r["task_id"] == taskID {
			return r
		}
	}
	t.Fatalf("no dispatch result for task %q in %#v", taskID, parsed)
	return nil
}

func findDispatchedRunID(t *testing.T, repo ledger.LedgerRepository) string {
	t.Helper()
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if len(r.Tasks) == 2 {
			return r.RunID
		}
	}
	t.Fatalf("no dispatched run with 2 tasks found (runs=%d)", len(runs))
	return ""
}

// runMessageKinds collects the kind and synopsis of every task_message event.
func runMessageKinds(t *testing.T, repo ledger.LedgerRepository, runID string) (kinds, synopses []string) {
	t.Helper()
	events, err := repo.ListEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind != coordinator.LifecycleKindTaskMessage {
			continue
		}
		var p struct {
			MessageID string `json:"message_id"`
			Kind      string `json:"kind"`
			Synopsis  string `json:"synopsis"`
		}
		_ = json.Unmarshal(e.Payload, &p)
		if p.Kind != "" {
			kinds = append(kinds, p.Kind)
		}
		if p.Synopsis != "" {
			synopses = append(synopses, p.Synopsis)
		}
	}
	return kinds, synopses
}
