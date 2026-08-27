package clichat

// E5: end-to-end dispatched ask/decline — the integration seam for the R9
// ask-decline path.
//
// The coordinator tests prove production PRODUCES the decline sentinel with a
// REAL pool (TestEarlyFenceDeclinesAskWhenResponderHandlerDone); the CLI wait
// tests prove production CONSUMES it by injecting the sentinel directly
// (parked_wait_test.go). Nothing proves the two compose in one real run:
//
//	postMessageTool.Execute(kind=ask, to_role=auditor, wait_seconds=N) parks
//	→ a real responder task finishes mid-park without answering
//	→ the CLI surfaces {"status":"no_answer","reason":"target_terminal"}
//	  (the unified reason from agentmsg.DeclineReasonResponderTerminal).
//
// This test closes that seam with the newAskE2EDispatchTool harness pattern:
// a real NewSessionDispatcher + coordinator pool + agent registry +
// tool-call completer. The fake completer emits real provider.ToolCall
// replies, so the multi_step handlers actually invoke post_message, park, and
// unblock through the coordinator's mailbox/fence — no sentinel injection.
//
// Mutation sensitivity: if the decline wiring broke (no early fence, or the
// CLI wait site stopped recognizing the sentinel), the asker would burn its
// full 30s park and report {"status":"no_answer","reason":"timed_out"}. The
// reason assertion (built from the CONSTANT, never a literal) then fails, and
// the <5s elapsed bound fails too — the discriminator is reason==target_terminal,
// NOT timed_out.

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
	declineReviewerMarker = "[reviewer-ask-decline-e2e]"
	declineAuditorMarker  = "[auditor-ask-decline-e2e]"
)

// declineToolCompleter drives a dispatched reviewer→auditor run where the
// auditor (responder) finishes WITHOUT answering the reviewer's blocking ask.
//
// Reviewer turn 1 emits post_message(kind=ask, to_role=auditor, wait_seconds=30)
// and parks; turn 2 echoes the tool result (the decline JSON). Auditor turn 1
// posts a keep-alive finding only after the reviewer has emitted the ask
// (askPosted closed); once the injected "ask_id:" appears at a step boundary
// the auditor signals askObserved, HOLDS on releaseAuditor so the test can
// observe the asker's live park (E6), then returns a FINAL text reply — its
// handler finishes mid-park and the coordinator's early fence delivers the
// decline sentinel that unblocks the parked asker.
//
// A4 determinism: the auditor's keep-alive blocks on askPosted (closed exactly
// once, when the reviewer's ask tool call is emitted) before its first finding,
// and only finishes AFTER the injected ask_id shows at a step boundary — so the
// ask is always delivered (mailboxed) before the responder's handler returns.
// Under GOMAXPROCS=1 a starved auditor goroutine yields the P while blocked, so
// the reviewer's worker always runs and its ask reaches the auditor's mailbox;
// the keep-alive loop (256-step budget backstop) keeps posting findings until
// the injection lands. The auditor then parks on releaseAuditor (a channel),
// which the test closes only after the park observation — a blocked goroutine
// again yields the P, so the test's main goroutine runs deterministically.
type declineToolCompleter struct {
	name string
	next atomic.Int64

	askPosted       chan struct{} // closed by the reviewer when it emits the ask tool call
	askPostedOnce   sync.Once
	askObserved     chan struct{} // closed by the auditor when the injected ask_id appears
	askObservedOnce sync.Once
	releaseAuditor  chan struct{} // closed by the test once the asker's park has been observed
}

func (c *declineToolCompleter) Name() string { return c.name }

func (c *declineToolCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}

func (c *declineToolCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

func (c *declineToolCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch {
	case messagesContain(req, declineReviewerMarker):
		return c.reviewerTurn(req), nil
	case messagesContain(req, declineAuditorMarker):
		return c.auditorTurn(req), nil
	}
	return &provider.Response{Content: `{"ok":true}`, FinishReason: "stop"}, nil
}

func (c *declineToolCompleter) newToolCall(name, args string) provider.ToolCall {
	var call provider.ToolCall
	call.ID = fmt.Sprintf("call_%s_%d", name, c.next.Add(1))
	call.Type = "function"
	call.Function.Name = name
	call.Function.Arguments = args
	return call
}

// reviewerTurn: turn 1 posts a blocking ask (30s wait); later turns report the
// tool result, which after the decline reads {"status":"no_answer",...}.
func (c *declineToolCompleter) reviewerTurn(req provider.Request) *provider.Response {
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
			`{"kind":"ask","to_role":"auditor","body":"please verify the fix at L42","wait_seconds":30}`)},
		FinishReason: "tool_calls",
	}
}

// auditorTurn: never answers the ask. Before the ask is injected it blocks on
// askPosted (closed when the reviewer emits its ask tool call) and posts
// keep-alive findings so the loop reaches another step boundary; once the
// injected ask_id is present it holds on releaseAuditor (E6 observation
// window), then returns a FINAL text reply — its handler finishes mid-park,
// and the coordinator's early fence must decline the parked ask.
func (c *declineToolCompleter) auditorTurn(req provider.Request) *provider.Response {
	if id := extractAskID(req); id != "" {
		c.askObservedOnce.Do(func() { close(c.askObserved) })
		<-c.releaseAuditor
		return &provider.Response{
			Content:      "auditor: ask received; wrapping up WITHOUT answering",
			FinishReason: "stop",
		}
	}
	// Block until the reviewer's goroutine has emitted its blocking ask (the
	// channel is closed exactly once; later reads return immediately). A
	// GOMAXPROCS=1 scheduler would otherwise let the auditor run all its steps
	// first (A4); a blocked goroutine yields the P with no CPU starvation
	// possible, so the reviewer's worker runs and its ask reaches this task's
	// mailbox. The 256-step budget is the backstop for a slow-starting
	// reviewer, not the primary mechanism.
	<-c.askPosted
	return &provider.Response{
		ToolCalls: []provider.ToolCall{c.newToolCall(toolPostMessage,
			`{"kind":"finding","body":"auditor keep-alive while waiting for the ask"}`)},
		FinishReason: "tool_calls",
	}
}

// newAskDeclineE2EDispatchTool builds a real session dispatcher (agent →
// multi_step) with reviewer and auditor agents and returns the dispatch_tasks
// tool, the dispatcher, the shared ledger repo, and the live completer (for
// the askObserved/releaseAuditor channels). Mirrors newAskE2EDispatchTool.
func newAskDeclineE2EDispatchTool(t *testing.T, cfg config.SubagentConfig) (*cliorchestrate.DispatchTasksToolForTest, *runtime.Dispatcher, ledger.LedgerRepository, *declineToolCompleter) {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	repo := ledger.NewMemoryLedgerRepository()
	comp := &declineToolCompleter{
		name:           "ask-decline-e2e",
		askPosted:      make(chan struct{}),
		askObserved:    make(chan struct{}),
		releaseAuditor: make(chan struct{}),
	}
	if cfg.SchemaRetryMax <= 0 {
		cfg.SchemaRetryMax = 2
	}
	if cfg.NestedSteps == 0 {
		// A4 determinism: the auditor's keep-alive posts findings until the
		// reviewer's ask is injected at a step boundary. 256 is the backstop
		// budget for a slow-starting reviewer; the test still fails cleanly if
		// the decline wiring itself breaks.
		cfg.NestedSteps = 256
	}
	if cfg.Messaging.MaxMessagesPerTask < 256 {
		// Each keep-alive finding consumes one max_messages_per_task slot; a
		// starved scheduler may need several findings before the ask lands.
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
	return tool, d, repo, comp
}

// TestE2EAskDeclineResponderTerminal closes the R9 seam in one real run:
// a blocking ask parks the asker, a live responder finishes without answering,
// and the CLI surfaces {"status":"no_answer","reason":DeclineReasonResponderTerminal}
// (the constant's value) instead of burning the 30s park into timed_out.
func TestE2EAskDeclineResponderTerminal(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.Routing.Allow = []string{"reviewer->auditor"}
	tool, closer, repo, comp := newAskDeclineE2EDispatchTool(t, cfg)
	defer closer.Close()

	// Always release the responder, even on an early assertion failure, so its
	// worker goroutine and the dispatched run do not leak into later
	// -count=10 iterations.
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(comp.releaseAuditor) }) }
	defer release()

	args, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{"id": "rev-1", "agent": "reviewer",
				"prompt": declineReviewerMarker + " ask the auditor to verify the fix at L42"},
			{"id": "aud-1", "agent": "auditor",
				"prompt": declineAuditorMarker + " you are the auditor; you are overloaded and will wrap up without answering any ask"},
		},
	})
	type execResult struct {
		body string
		err  error
	}
	execCh := make(chan execResult, 1)
	start := time.Now()
	go func() {
		body, err := tool.Execute(context.Background(), args)
		execCh <- execResult{body: body, err: err}
	}()

	// Determinism: the auditor only closes askObserved after the injected ask
	// appears at a step boundary, so the ask was mailbox-delivered BEFORE the
	// responder's handler can finish (see waitForAskObserved).
	waitForAskObserved(t, comp, release)
	runID := waitForDispatchedRunID(t, repo, "rev-1", "aud-1")
	parkMsgID := assertParked(t, runID)

	// Release the responder: its handler now finishes without answering, and
	// the coordinator's early fence must decline the parked ask.
	release()

	var res execResult
	select {
	case res = <-execCh:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatched run did not complete within 10s after releasing the responder")
	}
	if res.err != nil {
		t.Fatalf("Execute transport err: %v body=%s", res.err, res.body)
	}
	assertDeclinedResult(t, res.body, time.Since(start))
	assertLedgerDeclinedEvent(t, repo, runID, parkMsgID)
}

// waitForAskObserved blocks until the auditor's step boundary has seen the
// injected ask_id (deterministic condition wait, no time.Sleep): the ask was
// mailbox-delivered BEFORE the responder's handler can finish — the mid-park
// decline seam, not an ask-time decline and not a mailbox-already-terminal
// decline. On timeout the responder is released before failing so its worker
// goroutine and the dispatched run do not leak into later -count=10 iterations.
func waitForAskObserved(t *testing.T, comp *declineToolCompleter, release func()) {
	t.Helper()
	select {
	case <-comp.askObserved:
	case <-time.After(10 * time.Second):
		release()
		t.Fatal("ask never reached the auditor's step boundary within 10s")
	}
}

// assertParked verifies the run's live park registry — the same data
// inspect_agents renders as "parks" — shows exactly the asker's park with its
// message_id while the responder holds. It reads record.coord.ParkedQuestions
// directly (white-box, same package) rather than going through the
// inspect_agents principal gate; the value is identical to what that tool
// surfaces. Returns the park's message_id for cross-checking against the
// ledger ask.
func assertParked(t *testing.T, runID string) string {
	t.Helper()
	rawHandle, ok := cliorchestrate.RunHandlesForTest.Load(runID)
	if !ok {
		t.Fatalf("no orchestration handle for run %s", runID)
	}
	record, ok := rawHandle.(*cliorchestrate.OrchestrationHandleForTest)
	if !ok || cliorchestrate.CoordinatorOfHandle(record) == nil {
		t.Fatalf("run handle %T is not an cliorchestrate.OrchestrationHandleForTest with a coordinator", rawHandle)
	}
	parks := cliorchestrate.CoordinatorOfHandle(record).ParkedQuestions(runID)
	if len(parks) != 1 {
		t.Fatalf("while asker parked, parks = %+v, want exactly 1 (the asker's park)", parks)
	}
	var parkMsgID string
	for _, p := range parks {
		if p.TaskID != "rev-1" {
			t.Fatalf("parked task = %q, want the asker rev-1", p.TaskID)
		}
		parkMsgID = p.MessageID
		if p.MessageID == "" || p.ExpiresAt.IsZero() {
			t.Fatalf("park %+v must carry message_id and expires_at", p)
		}
	}
	return parkMsgID
}

// assertDeclinedResult verifies the dispatched run's JSON result: both tasks
// completed, the reviewer's final reply surfaces {"status":"no_answer"} with
// the UNIFIED terminal reason (built from the constant so it survives any
// future value change) and never timed_out, and the auditor never answered.
// The <5s bound is the discriminator: if the decline wiring broke, the asker
// would burn its full 30s park and report timed_out.
func assertDeclinedResult(t *testing.T, body string, elapsed time.Duration) {
	t.Helper()
	if elapsed > 5*time.Second {
		t.Fatalf("run took %v; the decline must unblock the asker's 30s park, expected well under 5s", elapsed)
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
	// surface the decline: status no_answer with the UNIFIED terminal reason,
	// built from the constant so it survives any future value change. If the
	// decline wiring broke, the asker would burn 30s and report timed_out —
	// the reason assertion is the discriminator.
	revOut, ok := rev["output"].(map[string]any)
	if !ok {
		t.Fatalf("reviewer output not a multi_step envelope: %#v", rev["output"])
	}
	revText, _ := revOut["output"].(string)
	if !strings.Contains(revText, `"status":"no_answer"`) {
		t.Fatalf("reviewer result does not show no_answer: %q", revText)
	}
	wantReason := agentmsg.DeclineReasonResponderTerminal
	if !strings.Contains(revText, `"reason":"`+wantReason+`"`) {
		t.Fatalf("reviewer result does not carry the unified terminal reason %q: %q", wantReason, revText)
	}
	if strings.Contains(revText, "timed_out") {
		t.Fatalf("reviewer result must not report timed_out: %q", revText)
	}

	// The auditor's final reply is its wrap-up text; it must never have
	// answered the ask.
	audOut, ok := aud["output"].(map[string]any)
	if !ok {
		t.Fatalf("auditor output not a multi_step envelope: %#v", aud["output"])
	}
	audText, _ := audOut["output"].(string)
	if strings.Contains(audText, `"status":"answered"`) {
		t.Fatalf("auditor must not have answered the ask: %q", audText)
	}
}

// assertLedgerDeclinedEvent verifies both tasks are terminal in the ledger,
// the run carries an ask AND the E6 decline event (task_ask_declined
// attributed to the asker), and the park observed mid-run belonged to the very
// ask the ledger records.
func assertLedgerDeclinedEvent(t *testing.T, repo ledger.LedgerRepository, runID, parkMsgID string) {
	t.Helper()
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
	kinds, _ := runMessageKinds(t, repo, runID)
	if !slices.Contains(kinds, string(agentmsg.KindAsk)) {
		t.Fatalf("run messages lack an ask: %v", kinds)
	}
	events, err := repo.ListEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	declined := false
	for _, e := range events {
		if e.Kind == coordinator.LifecycleKindTaskAskDeclined && e.TaskID == "rev-1" {
			declined = true
		}
	}
	if !declined {
		t.Fatal("run ledger lacks a task_ask_declined event attributed to the asker")
	}
	if got := askMessageID(t, repo, runID); got != parkMsgID {
		t.Fatalf("parked message_id %q does not match the ledger ask %q", parkMsgID, got)
	}
}

// waitForDispatchedRunID blocks until the repo contains a run with exactly the
// given task IDs (the dispatched run) and returns its run id. The run is
// spawned before the asker parks, so by the time askObserved fires it already
// exists; the poll is only a scheduling backstop (deterministic condition wait,
// no time.Sleep).
func waitForDispatchedRunID(t *testing.T, repo ledger.LedgerRepository, want ...string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		runs, err := repo.ListRuns(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			if len(r.Tasks) != len(want) {
				continue
			}
			ids := make(map[string]bool, len(r.Tasks))
			for _, task := range r.Tasks {
				ids[task.TaskID] = true
			}
			match := true
			for _, id := range want {
				if !ids[id] {
					match = false
					break
				}
			}
			if match {
				return r.RunID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no dispatched run with tasks %v found while asker parked", want)
		}
		select {
		case <-time.After(2 * time.Millisecond):
		case <-time.After(time.Millisecond):
		}
	}
}

// askMessageID returns the message_id of the run's KindAsk task_message event.
func askMessageID(t *testing.T, repo ledger.LedgerRepository, runID string) string {
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
		}
		if len(e.Payload) > 0 {
			_ = json.Unmarshal(e.Payload, &p)
		}
		if p.Kind == string(agentmsg.KindAsk) && p.MessageID != "" {
			return p.MessageID
		}
	}
	return ""
}
