package cli

// E7: end-to-end three-agent ask chain (A→B→C), one hop longer than
// TestE2E_DispatchAskAnswerRoundTrip. asker→auditor→go-engineer, answer
// relayed back to unblock the asker.
//
// A4 determinism (-count=10 -cpu 1/4, -race): three concurrent tasks race
// under GOMAXPROCS=1, so a keep-alive agent could exhaust its step budget
// before its asker even posts. Two mechanisms prevent that: (1) channel
// handoffs (askPosted/relayPosted) block each hop's first action until the
// prior hop's ask tool call actually fires; (2) keepAliveYield yields the P
// right after a channel unblocks, so the asker's goroutine — the only
// runnable one at that instant — gets to mailbox-deliver its ask before the
// keep-alive loop can spin through its step budget in one scheduler quantum.
//
// No wall-clock negative case here: the mid-park decline/no_answer path is
// unit-covered elsewhere, and a timing-based negative e2e is flaky by
// construction.

import (
	"context"
	"encoding/json"
	"fmt"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Prompt markers let the shared completer tell the three dispatched tasks
// apart, and the end's factual one-line answer body the assertions check for.
const (
	chainAskerMarker   = "[chain-asker-e2e]"
	chainRelayerMarker = "[chain-relayer-e2e]"
	chainEndMarker     = "[chain-end-e2e]"
	chainAnswerBody    = "HEAD hash is 3f9a2c1"

	// keepAliveYield is how long a keep-alive finding parks before posting.
	// Blocked goroutines yield the P (no CPU starvation), so the prior hop's
	// runnable asker runs its ask tool — and mailbox-delivers the ask — inside
	// this window, guaranteeing the keep-alive loop can never burn its step
	// budget in one scheduler quantum under GOMAXPROCS=1. Small enough to keep
	// the happy path fast (the chain completes in a handful of findings).
	keepAliveYield = time.Millisecond
)

// chainToolCompleter drives a dispatched asker→auditor→go-engineer ask chain
// through real tool calls.
//
// asker: turn 1 posts a blocking ask (wait_seconds=120, sized for the whole
// relay); turn 2 echoes the tool result, which after a successful chain reads
// {"status":"answered",...} with the end's answer body.
//
// relayer (auditor): keeps its loop alive with findings until the asker's ask
// is injected at a step boundary (phase 1→2), relays it to go-engineer with a
// blocking ask, forwards the end's answer back to the asker once it unblocks
// (phase 3), then reports done (phase 4). Phase detection keys on the relayer's
// own post_message history: the end's answer result carries an "answer" field
// (phase 3) while the relayer's own answer-to-asker result carries in_reply_to
// without an "answer" field (phase 4) — checked in that order so a stale
// injected ask_id in history can never re-trigger a relay.
//
// end (go-engineer): answers the relayed ask with a scripted factual one-line
// value (deterministic; the e2e verifies the messaging chain, not git state),
// then wraps up. The scripted value is permitted in place of running a real
// command so the test has no environment dependence.
type chainToolCompleter struct {
	name string
	next atomic.Int64

	askPosted       chan struct{} // closed once when the asker emits its ask tool call
	askPostedOnce   sync.Once
	relayPosted     chan struct{} // closed once when the relayer emits its ask-to-end tool call
	relayPostedOnce sync.Once
}

func (c *chainToolCompleter) Name() string { return c.name }

func (c *chainToolCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}

func (c *chainToolCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

func (c *chainToolCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch {
	case messagesContain(req, chainAskerMarker):
		return c.askerTurn(req), nil
	case messagesContain(req, chainRelayerMarker):
		return c.relayerTurn(req), nil
	case messagesContain(req, chainEndMarker):
		return c.endTurn(req), nil
	}
	return &provider.Response{Content: `{"ok":true}`, FinishReason: "stop"}, nil
}

func (c *chainToolCompleter) newToolCall(name, args string) provider.ToolCall {
	var call provider.ToolCall
	call.ID = fmt.Sprintf("call_%s_%d", name, c.next.Add(1))
	call.Type = "function"
	call.Function.Name = name
	call.Function.Arguments = args
	return call
}

// askerTurn: first turn posts a blocking ask (wait sized for the whole relay);
// later turns report the tool result, which after a successful chain reads
// {"status":"answered",...} with the end's answer body.
func (c *chainToolCompleter) askerTurn(req provider.Request) *provider.Response {
	if result := lastToolResult(req, toolPostMessage); result != "" {
		return &provider.Response{
			Content:      "asker tool result: " + result,
			FinishReason: "stop",
		}
	}
	// The asker's blocking ask has been emitted: unblock the relayer's
	// keep-alive loop (closed exactly once).
	c.askPostedOnce.Do(func() { close(c.askPosted) })
	return &provider.Response{
		ToolCalls: []provider.ToolCall{c.newToolCall(toolPostMessage,
			`{"kind":"ask","to_role":"auditor","body":"verify HEAD","wait_seconds":120}`)},
		FinishReason: "tool_calls",
	}
}

// relayerTurn implements the four relay phases described on the completer.
func (c *chainToolCompleter) relayerTurn(req provider.Request) *provider.Response {
	// Phase 4: the relayer's answer back to the asker landed — the chain is
	// complete for this task. The peer-answer result carries in_reply_to and
	// no "answer" field, which is what distinguishes it from the end's answer.
	if relayerAnsweredAsker(req) {
		return &provider.Response{
			Content:      "relayer completed the chain",
			FinishReason: "stop",
		}
	}
	// Phase 3: the end answered the relayed ask; forward the end's answer to
	// the asker (in_reply_to = the asker's ask_id, still in message history).
	if res := lastToolResult(req, toolPostMessage); res != "" &&
		strings.Contains(res, `"status":"answered"`) && strings.Contains(res, `"answer":`) {
		askerAskID := extractAskID(req)
		return &provider.Response{
			ToolCalls: []provider.ToolCall{c.newToolCall(toolPostMessage,
				`{"kind":"answer","body":`+strconv.Quote(answerFromToolResult(res))+`,"in_reply_to":"`+askerAskID+`"}`)},
			FinishReason: "tool_calls",
		}
	}
	// Phase 2: the asker's ask is injected at a step boundary — relay it to
	// go-engineer with its own blocking ask (wait sized for the relay).
	if id := extractAskID(req); id != "" {
		c.relayPostedOnce.Do(func() { close(c.relayPosted) })
		return &provider.Response{
			ToolCalls: []provider.ToolCall{c.newToolCall(toolPostMessage,
				`{"kind":"ask","to_role":"go-engineer","body":"confirm hash","wait_seconds":120}`)},
			FinishReason: "tool_calls",
		}
	}
	// Phase 1: keep the loop alive until the asker's ask is injected. The
	// first receive blocks on askPosted (closed exactly once, when the asker
	// emits its ask tool call) so no step is burned before the asker has even
	// emitted; the keepAliveYield after it guarantees the asker's ask tool —
	// which may have been delayed by an async scheduler preemption right after
	// its ChatTurn — executes and mailbox-delivers the ask before this loop
	// can consume its step budget (A4 under GOMAXPROCS=1).
	<-c.askPosted
	select {
	case <-time.After(keepAliveYield):
	}
	return &provider.Response{
		ToolCalls: []provider.ToolCall{c.newToolCall(toolPostMessage,
			`{"kind":"finding","body":"relayer keep-alive while waiting for the ask"}`)},
		FinishReason: "tool_calls",
	}
}

// endTurn: once the relayed ask is injected, answer it with a factual
// one-liner in_reply_to = the relayer's ask_id; before that, keep the loop
// alive with findings, blocking on relayPosted and yielding for
// keepAliveYield so the relayer's ask-to-end tool is guaranteed to have run
// (A4).
func (c *chainToolCompleter) endTurn(req provider.Request) *provider.Response {
	// The end's own answer landed (its post_message history shows "answered").
	if auditorAnswered(req) {
		return &provider.Response{
			Content:      "end answered the relayed ask",
			FinishReason: "stop",
		}
	}
	if id := extractAskID(req); id != "" {
		return &provider.Response{
			ToolCalls: []provider.ToolCall{c.newToolCall(toolPostMessage,
				`{"kind":"answer","body":"`+chainAnswerBody+`","in_reply_to":"`+id+`"}`)},
			FinishReason: "tool_calls",
		}
	}
	<-c.relayPosted
	select {
	case <-time.After(keepAliveYield):
	}
	return &provider.Response{
		ToolCalls: []provider.ToolCall{c.newToolCall(toolPostMessage,
			`{"kind":"finding","body":"end keep-alive while waiting for the relayed ask"}`)},
		FinishReason: "tool_calls",
	}
}

// relayerAnsweredAsker reports whether the relayer's own answer to the asker
// landed: its latest post_message result is an "answered" peer answer, which
// carries in_reply_to but no "answer" field (the end's answer result carries
// the "answer" field instead — that is the phase-3 discriminator).
func relayerAnsweredAsker(req provider.Request) bool {
	res := lastToolResult(req, toolPostMessage)
	return res != "" && strings.Contains(res, `"status":"answered"`) && strings.Contains(res, `"in_reply_to":"`)
}

// answerFromToolResult extracts the "answer" field of a post_message result
// (the end's answer to the relayer's relayed ask).
func answerFromToolResult(result string) string {
	var p struct {
		Answer string `json:"answer"`
	}
	_ = json.Unmarshal([]byte(result), &p)
	return p.Answer
}

// countKind counts how many message kinds in kinds equal want.
func countKind(kinds []string, want string) int {
	n := 0
	for _, k := range kinds {
		if k == want {
			n++
		}
	}
	return n
}

// newAskChainE2EDispatchTool builds a real session dispatcher (agent →
// multi_step) with asker, auditor, and go-engineer agents and returns the
// dispatch_tasks tool, the dispatcher, and the shared ledger repo for
// assertions. Mirrors newAskE2EDispatchTool with a third agent.
func newAskChainE2EDispatchTool(t *testing.T, cfg config.SubagentConfig) (*cliorchestrate.DispatchTasksToolForTest, *runtime.Dispatcher, ledger.LedgerRepository) {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	repo := ledger.NewMemoryLedgerRepository()
	comp := &chainToolCompleter{
		name:        "ask-chain-e2e",
		askPosted:   make(chan struct{}),
		relayPosted: make(chan struct{}),
	}
	if cfg.SchemaRetryMax <= 0 {
		cfg.SchemaRetryMax = 2
	}
	if cfg.NestedSteps == 0 {
		// A4 determinism: the relayer and end keep their loops alive with real
		// post_message(kind=finding) calls, each blocked on the prior hop's
		// askPosted/relayPosted channel before its first finding and yielding
		// for keepAliveYield before every finding so the ask tool can never be
		// starved. 256 is the backstop budget for a slow-starting asker, while
		// the test still fails cleanly if the ask/answer wiring itself breaks.
		cfg.NestedSteps = 256
	}
	if cfg.Messaging.MaxMessagesPerTask < 256 {
		// Each keep-alive finding consumes one max_messages_per_task slot; a
		// starved scheduler may need several findings before each ask lands.
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
		AgentRegistry: testAgentRegistry(t, "asker", "auditor", "go-engineer"),
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

// TestE2E_AskChainThreeAgentRelay exercises the full dispatched 3-agent chain:
// dispatch_tasks → coordinator pool → asker post_message(kind=ask, wait sized
// for the whole relay) → auditor relays → go-engineer answers → auditor
// forwards → asker unblocked with the end's answer. All three tasks run in one
// batch (no depends_on) and must all complete.
func TestE2E_AskChainThreeAgentRelay(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.Routing.Allow = []string{"asker->auditor", "auditor->go-engineer"}
	tool, closer, repo := newAskChainE2EDispatchTool(t, cfg)
	defer closer.Close()

	args, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{"id": "ask-1", "agent": "asker",
				"prompt": chainAskerMarker + " verify HEAD via the auditor, who relays to go-engineer"},
			{"id": "rel-1", "agent": "auditor",
				"prompt": chainRelayerMarker + " you are the auditor; relay any ask from the asker to go-engineer and forward the answer back"},
			{"id": "end-1", "agent": "go-engineer",
				"prompt": chainEndMarker + " you are go-engineer; answer any relayed ask from the auditor"},
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
	if len(parsed) != 3 {
		t.Fatalf("want 3 task results, got %d body=%s", len(parsed), body)
	}

	asker := dispatchResultFor(t, parsed, "ask-1")
	relayer := dispatchResultFor(t, parsed, "rel-1")
	end := dispatchResultFor(t, parsed, "end-1")
	for taskID, res := range map[string]map[string]any{"ask-1": asker, "rel-1": relayer, "end-1": end} {
		if res["status"] != "completed" {
			t.Fatalf("task %s status=%v full=%#v\nall results: %s", taskID, res["status"], res, body)
		}
	}

	// The asker's final reply echoes the post_message tool result, so it must
	// show the RELAYED answer: status "answered" with the end's answer body.
	askerOut, ok := asker["output"].(map[string]any)
	if !ok {
		t.Fatalf("asker output not a multi_step envelope: %#v", asker["output"])
	}
	askerText, _ := askerOut["output"].(string)
	if !strings.Contains(askerText, `"status":"answered"`) {
		t.Fatalf("asker result does not show answered: %q", askerText)
	}
	if !strings.Contains(askerText, chainAnswerBody) {
		t.Fatalf("asker result does not carry the end's answer body: %q", askerText)
	}

	// Ledger: all three tasks terminal, and the chain of ask/answer messages
	// is recorded — two asks (asker→auditor, auditor→go-engineer), two answers
	// (go-engineer→auditor, auditor→asker) — with the end's answer body.
	runID := waitForDispatchedRunID(t, repo, "ask-1", "rel-1", "end-1")
	tasks, err := repo.ListTasks(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("ledger tasks=%d want 3", len(tasks))
	}
	for _, task := range tasks {
		if task.Status != string(ledger.TaskStatusCompleted) {
			t.Fatalf("task %s status=%s want completed", task.TaskID, task.Status)
		}
	}
	kinds, synopses := runMessageKinds(t, repo, runID)
	if n := countKind(kinds, string(agentmsg.KindAsk)); n != 2 {
		t.Fatalf("run messages ask count=%d want 2 (asker->auditor, auditor->go-engineer): %v", n, kinds)
	}
	if n := countKind(kinds, string(agentmsg.KindAnswer)); n != 2 {
		t.Fatalf("run messages answer count=%d want 2 (go-engineer->auditor, auditor->asker): %v", n, kinds)
	}
	if !slices.Contains(synopses, chainAnswerBody) {
		t.Fatalf("end's answer body missing from ledger synopses: %v", synopses)
	}
}
