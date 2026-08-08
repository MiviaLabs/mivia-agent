package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func (l *Loop) runToolBatch(ctx context.Context, calls []provider.ToolCall, opts Options) {
	if len(calls) > 1 {
		names := make([]string, len(calls))
		for i, tc := range calls {
			names[i] = tc.Function.Name
		}
		emit(opts, Event{
			Kind:   EventToolParallel,
			Detail: fmt.Sprintf("%d tools: %s", len(calls), strings.Join(names, ", ")),
		})
	}
	for _, tc := range calls {
		input := redactToolInput(tc.Function.Arguments)
		emit(opts, Event{
			Kind:       EventToolStart,
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Detail:     "queued",
			Input:      input,
		})
	}
	// ToolEnd events fire per-tool from workers as each finishes (not after batch).
	results := executeToolsParallel(ctx, calls, l.Tools, opts)
	sort.Slice(results, func(i, j int) bool {
		return results[i].index < results[j].index
	})
	bodies := shapeBatchResults(results, opts)
	for i, r := range results {
		l.Messages = append(l.Messages, provider.Message{
			Role:       provider.RoleTool,
			ToolCallID: r.toolCall.ID,
			Name:       r.toolCall.Function.Name,
			Content:    bodies[i],
		})
	}
}

// processToolCalls filters malformed tool calls, records every call the model
// made plus bounded error results in history, executes the valid batch, and
// returns the outcome.
func (l *Loop) processToolCalls(ctx context.Context, resp *provider.Response, trimmed string, opts Options) (stepOutcome, error) {
	// Identify first: what history announces, what is dispatched, and what
	// answers each call must all agree on one ID per call.
	calls := identifiedToolCalls(resp.ToolCalls)
	validCalls, errorResults := filterValidToolCalls(calls)
	if err := l.workLimits.reserveToolBatch(len(validCalls)); err != nil {
		return stepOutcome{}, err
	}
	l.Messages = append(l.Messages, provider.Message{
		Role:             provider.RoleAssistant,
		Content:          resp.Content,
		ToolCalls:        recordedToolCalls(calls),
		ReasoningContent: resp.ReasoningContent,
		CreatedAt:        time.Now(),
	})
	if trimmed != "" {
		emit(opts, Event{Kind: EventAssistant, Content: resp.Content, Detail: "interim"})
	}
	l.Messages = append(l.Messages, errorResults...)
	l.runToolBatch(ctx, validCalls, opts)
	out := stepOutcome{finishReason: resp.FinishReason}
	if trimmed != "" {
		out.text = resp.Content
	}
	// A step whose calls were all malformed has nothing left to do: if the
	// model also said nothing renderable, treat the step as finished instead of
	// looping on an empty turn.
	if len(validCalls) == 0 && trimmed == "" {
		out.done = true
	}
	return out, nil
}

// filterValidToolCalls separates tool calls whose arguments are valid JSON (or
// empty/whitespace) from malformed ones. A malformed call would fail to
// unmarshal in the tools registry, so it is never dispatched: it becomes a
// bounded RoleTool error instead, so the model sees its call was skipped rather
// than leaving a silent gap. Execution arguments are passed through verbatim;
// only what history records is normalized (see recordedToolCalls).
func filterValidToolCalls(calls []provider.ToolCall) (valid []provider.ToolCall, errorResults []provider.Message) {
	for _, call := range calls {
		if wellFormedArguments(call.Function.Arguments) {
			valid = append(valid, call)
			continue
		}
		errorResults = append(errorResults, provider.Message{
			Role:       provider.RoleTool,
			ToolCallID: call.ID,
			Name:       call.Function.Name,
			Content:    "error: tool call arguments were not valid JSON; call skipped",
		})
	}
	return valid, errorResults
}

// recordedToolCalls returns the assistant-message form of a step's tool calls:
// EVERY call the model made, including the malformed ones filterValidToolCalls
// refuses to dispatch, with arguments normalized to a JSON object whenever the
// model sent none or sent bytes that are not JSON.
//
// Recording only the dispatched subset was a data-loss bug, not a repair. The
// skipped call still gets a RoleTool error carrying its tool_call_id, so
// dropping the call from the assistant message left that result answering a
// call no message announced. Strict context planning
// (provider.ValidateToolPairing) repairs nothing by design and rejected the
// whole history with `orphan tool result`, failing the turn and discarding the
// preparation - work the agent had already finished. Normalizing arguments
// covers the same class: that validator also rejects a recorded call whose
// arguments are absent or unparseable, which a model calling a no-argument tool
// produces routinely.
func recordedToolCalls(calls []provider.ToolCall) []provider.ToolCall {
	recorded := make([]provider.ToolCall, 0, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.Function.Arguments) == "" || !json.Valid([]byte(call.Function.Arguments)) {
			call.Function.Arguments = "{}"
		}
		recorded = append(recorded, call)
	}
	return recorded
}

// identifiedToolCalls gives an ID to every call the provider left without one.
//
// A tool result is bound to its call by id and by nothing else, so an
// unidentified call cannot be answered: it was recorded with an empty ID, which
// provider.ValidateToolPairing rejects outright, and its result carried an
// empty tool_call_id, which is the orphan case again. The ID is ours to author
// because the assistant message the provider sees is ours to author - the model
// never sent one to contradict.
//
// The value is random rather than a counter. IDs must not repeat across the
// WHOLE history (ValidateToolPairing rejects a reused ID), and history outlives
// any one Loop: a per-turn counter would re-issue its first ID on the next
// turn, against messages the session carried forward.
func identifiedToolCalls(calls []provider.ToolCall) []provider.ToolCall {
	identified := make([]provider.ToolCall, 0, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" {
			call.ID = "call_" + runtime.NewSessionID()
		}
		identified = append(identified, call)
	}
	return identified
}

// wellFormedArguments reports whether a call may be dispatched: absent
// arguments mean "no arguments" and parse as such, anything else must be JSON.
func wellFormedArguments(arguments string) bool {
	return strings.TrimSpace(arguments) == "" || json.Valid([]byte(arguments))
}

func toolEndDetail(r toolExecResult) string {
	// A duplicate never re-ran, so its failure signal is judged against the
	// ORIGINAL recorded body the dedup cache served, not against the
	// suppression notice that replaced it: a run_command duplicate reports its
	// non-zero child exit in the recorded header with err==nil, and reading the
	// notice (which carries no status) would silently downgrade a failed
	// duplicate to completed.
	if r.duplicate {
		if r.err != nil || toolResultBodyFailed(r.toolCall.Function.Name, r.originalBody) {
			return "failed (duplicate)"
		}
		return "completed (duplicate)"
	}
	// Failed takes precedence over truncation (skeptic: both can be set).
	if r.err != nil || toolResultBodyFailed(r.toolCall.Function.Name, r.result) {
		if r.truncated {
			return "failed (truncated)"
		}
		return "failed"
	}
	if r.truncated {
		return "completed (truncated)"
	}
	return "completed"
}

// toolResultBodyFailed detects failure signals inside tool result text when
// Execute returned err=nil - only run_command does that, reporting a non-zero
// child exit in its result header while the call itself succeeded.
//
// The check is scoped by tool name because every other tool returns content
// verbatim: file text opening with "Error handling…" or a grep hit quoting
// "exit=1" is data, not a status, and scanning it reported healthy calls as
// failed. Bodies the loop synthesizes ("error: …") always carry a non-nil err,
// so scoping here loses no failure signal.
func toolResultBodyFailed(name, body string) bool {
	if name != tools.RunCommandToolName || body == "" {
		return false
	}
	// Header shape: "command: …\ncwd: …\nexit=<status>\n". exit=0 is success;
	// any other status (1, 127, timeout, canceled, error) is failure.
	for _, line := range strings.Split(body, "\n") {
		status, ok := strings.CutPrefix(strings.TrimSpace(line), "exit=")
		if !ok {
			continue
		}
		return status != "0"
	}
	return false
}

func executeToolsParallel(ctx context.Context, calls []provider.ToolCall, reg *tools.Registry, opts Options) []toolExecResult {
	n := len(calls)
	if n == 0 {
		return nil
	}
	if opts.Dispatcher == nil {
		var err error
		opts.Dispatcher, err = runtime.NewToolDispatcher(reg, runtime.Policy{})
		if err != nil {
			results := make([]toolExecResult, len(calls))
			for i, call := range calls {
				results[i] = errorExecResult(i, call, err)
				emitToolEnd(opts, results[i])
			}
			return results
		}
	}
	results := make([]toolExecResult, n)
	executeN := n
	if opts.MaxToolCallsPerBatch > 0 && executeN > opts.MaxToolCallsPerBatch {
		executeN = opts.MaxToolCallsPerBatch
		for i := executeN; i < n; i++ {
			err := fmt.Errorf("tool batch budget exceeded: max %d calls", opts.MaxToolCallsPerBatch)
			results[i] = errorExecResult(i, calls[i], err)
			emitToolEnd(opts, results[i])
		}
	}
	scheduler := newToolScheduler(opts.MaxConcurrentTools)
	workers := opts.MaxConcurrentTools
	if workers <= 0 {
		workers = 4
	}
	if workers > n {
		workers = n
	}
	tasks := prepareToolTasks(ctx, calls, reg, opts.ToolTimeout, opts.Step)
	defer func() {
		for _, task := range tasks {
			task.cancel()
		}
	}()

	var finished atomic.Int32
	// already-emitted budget-skipped tools count toward "done"
	for i := executeN; i < n; i++ {
		finished.Add(1)
	}
	stopHB := startToolBatchHeartbeat(ctx, opts, executeN, n, &finished)

	if runToolWorkers(ctx, calls, executeN, tasks, results, reg, scheduler, opts, workers, &finished) {
		stopHB()
		return results
	}
	stopHB()
	return results
}

// toolBatchHeartbeatInterval is the UI progress cadence during tool batches.
// Overridable in tests.
var toolBatchHeartbeatInterval = 2 * time.Second

// startToolBatchHeartbeat emits EventHeartbeat progress while tools run so
// the UI is not silent for multi-minute batches. Returns a stop func.
func startToolBatchHeartbeat(ctx context.Context, opts Options, executeN, total int, finished *atomic.Int32) func() {
	if executeN <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	// Capture outside goroutine closure to avoid data race on the package-level
	// variable (toolBatchHeartbeatInterval is overridable in tests).
	interval := toolBatchHeartbeatInterval
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		started := time.Now()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				emit(opts, Event{
					Kind: EventHeartbeat,
					Detail: fmt.Sprintf("tools %d/%d done · %s",
						finished.Load(), total, time.Since(started).Round(time.Second)),
				})
			}
		}
	}()
	return func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}
}

func emitToolEnd(opts Options, r toolExecResult) {
	output := redactToolOutputForTool(r.toolCall.Function.Name, r.result)
	if r.ephemeralMarker != "" {
		output = r.ephemeralMarker
	}
	emit(opts, Event{
		Kind:       EventToolEnd,
		ToolCallID: r.toolCall.ID,
		Name:       r.toolCall.Function.Name,
		Detail:     toolEndDetail(r),
		Output:     output,
	})
}

func prepareToolTasks(ctx context.Context, calls []provider.ToolCall, reg *tools.Registry, timeout time.Duration, step int) []toolTask {
	tasks := make([]toolTask, len(calls))
	for i, call := range calls {
		raw := json.RawMessage(call.Function.Arguments)
		capability := reg.Capability(call.Function.Name, raw)
		callTimeout := resolveToolCallTimeout(timeout, capability.Timeout)
		// A model-supplied per-call timeout_seconds overrides the capability
		// default: it may extend or tighten the budget the loop arms for this
		// call, clamped to the enclosing step/task deadline so a huge request
		// can never outlive the turn that owns it. Without the param the
		// capability stays the default and the hang bound is unchanged.
		if requested := requestedToolTimeout(raw); requested > 0 {
			callTimeout = clampToDeadline(ctx, requested)
		}
		// Only the timeout DURATION is fixed here. The clock starts in the
		// worker (see executeToolTask): a batch is prepared in full up front but
		// runs `workers` at a time, so a deadline armed here would be spent
		// while the call waits on the jobs channel or on a resource lock, and
		// trailing calls would expire without ever executing. The per-task
		// context stays cancel-only so batch teardown still reaches every task.
		callCtx, cancel := context.WithCancel(ctx)
		tasks[i] = toolTask{
			call: call, raw: raw, capability: capability,
			timeout: callTimeout, callCtx: callCtx, cancel: cancel, step: step,
			// prepareToolTasks is the SINGLE enforcement point for read-class
			// freshness: every production Tool dispatch funnels through
			// executeToolTask, so stamping SkipDedup here covers the root loop,
			// subagent loops, and any future loop-driven surface.
			skipDedup: !capability.Dedups(),
		}
	}
	return tasks
}

func runToolWorkers(ctx context.Context, calls []provider.ToolCall, executeN int, tasks []toolTask, results []toolExecResult, reg *tools.Registry, scheduler *toolScheduler, opts Options, workers int, finished *atomic.Int32) bool {
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				executeToolTask(idx, &tasks[idx], reg, scheduler, opts, results, finished)
			}
		}()
	}
	for i := 0; i < executeN; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			fillCanceledResults(ctx, calls, tasks, results, i, opts, finished)
			close(jobs)
			wg.Wait()
			return true
		}
	}
	close(jobs)
	wg.Wait()
	return false
}

func fillCanceledResults(ctx context.Context, calls []provider.ToolCall, tasks []toolTask, results []toolExecResult, start int, opts Options, finished *atomic.Int32) {
	for j := start; j < len(calls); j++ {
		if results[j].toolCall.ID != "" || results[j].result != "" || results[j].err != nil {
			continue // already finished
		}
		err := tasks[j].callCtx.Err()
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = context.Canceled
		}
		results[j] = errorExecResult(j, tasks[j].call, err)
		emitToolEnd(opts, results[j])
		if finished != nil {
			finished.Add(1)
		}
	}
}

// failToolTask closes out a call that never reached the dispatcher.
//
// Three preconditions produce one - a tool the registry does not expose, a
// scheduler slot that never arrived, a budget already spent - and each must
// leave the same shape behind: a result in the slot, a tool_end row, and the
// finished counter advanced. Missing that last one on any path hangs the whole
// batch, which is why the three sites share this rather than repeat it.
func failToolTask(idx int, task *toolTask, opts Options, results []toolExecResult, finished *atomic.Int32, err error) {
	results[idx] = errorExecResult(idx, task.call, err)
	emitToolEnd(opts, results[idx])
	if finished != nil {
		finished.Add(1)
	}
}

func executeToolTask(idx int, task *toolTask, reg *tools.Registry, scheduler *toolScheduler, opts Options, results []toolExecResult, finished *atomic.Int32) {
	// The dispatcher is the authorization boundary, but a loop must never gain
	// reach from a wider dispatcher than the registry it exposed to the model.
	if _, ok := reg.Get(task.call.Function.Name); !ok {
		// A staged tool is absent from the registry until the boundary
		// publishes it, so the denial must say publication is pending instead
		// of reading as an unknown tool: load_tools promised next-turn
		// availability, and the generic message is indistinguishable from a
		// hallucinated name. The session supplies the full message, which
		// announces why publication is deferred. Execution is denied either
		// way (INV-AG-29).
		if opts.StagedToolMessage != nil {
			if msg, ok := opts.StagedToolMessage(task.call.Function.Name); ok {
				failToolTask(idx, task, opts, results, finished, fmt.Errorf("%s", msg))
				return
			}
		}
		failToolTask(idx, task, opts, results, finished,
			fmt.Errorf("tool %q is not available to this agent", task.call.Function.Name))
		return
	}
	release, err := scheduler.acquire(task.callCtx, task.capability.ResourceKey)
	if err != nil {
		failToolTask(idx, task, opts, results, finished, err)
		return
	}
	// Arm the per-call budget only now that the call actually owns a worker and
	// its resource lock, so queue and lock waits are not charged against it.
	execCtx, cancelExec := context.WithTimeout(task.callCtx, task.timeout)
	defer cancelExec()
	if err := execCtx.Err(); err != nil {
		release()
		failToolTask(idx, task, opts, results, finished, err)
		return
	}
	// Promote UI status from queued → running when work actually starts.
	emit(opts, Event{
		Kind:       EventToolStart,
		ToolCallID: task.call.ID,
		Name:       task.call.Function.Name,
		Detail:     "running",
	})
	r := opts.Dispatcher.Invoke(execCtx, runtime.Request{
		ID:       task.call.ID,
		ParentID: opts.ParentID,
		TurnID:   opts.TurnID,
		Step:     task.step,
		// SkipDedup is stamped from the tool's capability class in
		// prepareToolTasks: read-class calls always execute fresh, write-class
		// calls keep the per-turn dedup.
		SkipDedup: task.skipDedup,
		SessionID: opts.SessionID,
		Role:      opts.Role,
		Depth:     opts.Depth,
		Budget:    opts.Budget,
		Kind:      runtime.Tool,
		Name:      task.call.Function.Name,
		Input:     task.raw,
		Timeout:   task.timeout,
	})
	release()
	results[idx] = buildExecResult(idx, task, reg, opts, r)
	emitToolEnd(opts, results[idx])
	// After the tool row, not before it: a hook row belongs under the call it
	// ran for. PreToolUse fired earlier in wall-clock time, but a transcript
	// that interleaved it with the tool's own start would put a gate's verdict
	// above a call the reader has not been told about yet.
	emitHookRuns(opts, task.call.ID, r.HookRuns)
	if finished != nil {
		finished.Add(1)
	}
}

// ScrubEphemeralToolMessages lives in loop_scrub.go.
