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
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
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
	for _, r := range results {
		l.Messages = append(l.Messages, provider.Message{
			Role:       provider.RoleTool,
			ToolCallID: r.toolCall.ID,
			Name:       r.toolCall.Function.Name,
			Content:    r.result,
		})
	}
}

func toolEndDetail(r toolExecResult) string {
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

func redactToolInput(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	// Default: operator-visible args, bounded and passed through the workspace
	// redaction policy. With no policy configured that policy redacts nothing -
	// see .mivia/rules/10-security-privacy.md. RedactToolArgs opts into the
	// stricter whole-field elision below; it is a separate control from the
	// patterns and stays meaningful when no policy is set.
	if !tools.RedactToolArgs() {
		return truncatePreview(redact.Text(raw), 256)
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return truncatePreview(redact.Text(raw), 256)
	}
	encoded, err := json.Marshal(redactJSONValue(value))
	if err != nil {
		return "[invalid input]"
	}
	return truncatePreview(string(encoded), 256)
}

const redactJSONMaxDepth = 64

// redactJSONValue prepares a decoded tool-argument value for the opt-in
// preview: file bodies are reduced to a byte count, then the workspace policy
// elides values by key name and scrubs the remaining string leaves. Scrubbing
// the leaves keeps opt-in mode a superset of the default path, since key-name
// elision alone misses a credential embedded in an innocuously named field
// ("command", "args").
//
// Key names and patterns come from the policy, never from here. The content
// elision does not: it is preview-size control rather than credential
// redaction - it keeps a whole file body out of every EventBus sink - so it
// applies whether or not a workspace configured any patterns.
func redactJSONValue(value any) any {
	return redact.JSONValue(elideContentPreviews(value, 0))
}

// elideContentPreviews replaces a string value under a "content" key with its
// size. depth stops at redactJSONMaxDepth so deeply nested or crafted input
// cannot overflow the stack.
func elideContentPreviews(value any, depth int) any {
	if depth > redactJSONMaxDepth {
		return value
	}
	switch current := value.(type) {
	case map[string]any:
		for key, nested := range current {
			if strings.ToLower(key) == "content" {
				if text, ok := nested.(string); ok {
					current[key] = fmt.Sprintf("[content %d bytes]", len(text))
					continue
				}
			}
			current[key] = elideContentPreviews(nested, depth+1)
		}
	case []any:
		for i, nested := range current {
			current[i] = elideContentPreviews(nested, depth+1)
		}
	}
	return value
}

const defaultToolPreviewMaxBytes = 512
const editToolPreviewMaxBytes = 8192

func redactToolOutput(output string) string { return redactToolOutputForTool("", output) }

func redactToolOutputForTool(name, output string) string {
	maxBytes := defaultToolPreviewMaxBytes
	if name == "write_file" || name == "search_replace" {
		maxBytes = editToolPreviewMaxBytes
	}
	return truncatePreview(redact.Text(output), maxBytes)
}

func truncatePreview(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
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
				results[i] = toolExecResult{index: i, toolCall: call, result: "error: " + err.Error(), err: err}
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
			results[i] = toolExecResult{index: i, toolCall: calls[i], result: "error: " + err.Error(), err: err}
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
	tasks := prepareToolTasks(ctx, calls, reg, opts.ToolTimeout)
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

// startToolBatchHeartbeat emits EventStep progress while tools run so the UI
// is not silent for multi-minute batches. Returns a stop func.
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
					Kind: EventStep,
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

func prepareToolTasks(ctx context.Context, calls []provider.ToolCall, reg *tools.Registry, timeout time.Duration) []toolTask {
	tasks := make([]toolTask, len(calls))
	for i, call := range calls {
		raw := json.RawMessage(call.Function.Arguments)
		capability := reg.Capability(call.Function.Name, raw)
		callTimeout := resolveToolCallTimeout(timeout, capability.Timeout)
		// Only the timeout DURATION is fixed here. The clock starts in the
		// worker (see executeToolTask): a batch is prepared in full up front but
		// runs `workers` at a time, so a deadline armed here would be spent
		// while the call waits on the jobs channel or on a resource lock, and
		// trailing calls would expire without ever executing. The per-task
		// context stays cancel-only so batch teardown still reaches every task.
		callCtx, cancel := context.WithCancel(ctx)
		tasks[i] = toolTask{
			call: call, raw: raw, capability: capability,
			timeout: callTimeout, callCtx: callCtx, cancel: cancel,
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
		results[j] = toolExecResult{index: j, toolCall: tasks[j].call, result: "error: " + err.Error(), err: err}
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
	results[idx] = toolExecResult{index: idx, toolCall: task.call, result: "error: " + err.Error(), err: err}
	emitToolEnd(opts, results[idx])
	if finished != nil {
		finished.Add(1)
	}
}

func executeToolTask(idx int, task *toolTask, reg *tools.Registry, scheduler *toolScheduler, opts Options, results []toolExecResult, finished *atomic.Int32) {
	// The dispatcher is the authorization boundary, but a loop must never gain
	// reach from a wider dispatcher than the registry it exposed to the model.
	if _, ok := reg.Get(task.call.Function.Name); !ok {
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
		ID:        task.call.ID,
		ParentID:  opts.ParentID,
		TurnID:    opts.TurnID,
		SessionID: opts.SessionID,
		Role:      opts.Role,
		Depth:     opts.Depth,
		Budget:    opts.Budget,
		Kind:      runtime.Tool,
		Name:      task.call.Function.Name,
		Input:     task.raw,
		Timeout:   task.timeout,
	})
	result, err := string(r.Output), r.Err
	release()
	// Keep model-visible tool bodies; only synthesize an error when empty.
	if err != nil && strings.TrimSpace(result) == "" {
		result = fmt.Sprintf("error: %v", err)
	}
	result, truncated := capToolResult(result, opts.MaxToolResultChars, task.capability.MaxResultBytes)
	// Hook context is attached AFTER the tool result was capped, and rides above
	// that cap within its own fixed bound (runtime.MaxHookContextBytes). Paying
	// for a formatter's advice out of the tool's own budget would destroy real
	// result bytes to make room for commentary about them.
	result = appendHookContext(result, r.HookContext)
	marker := ""
	if tool, ok := reg.Get(task.call.Function.Name); ok {
		if ephemeral, ok := tool.(tools.EphemeralResultTool); ok {
			marker = ephemeral.EphemeralResultMarker(task.raw)
		}
	}
	results[idx] = toolExecResult{
		index: idx, toolCall: task.call, result: result,
		truncated: truncated, err: err, ephemeralMarker: marker, hookRuns: r.HookRuns,
	}
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

// ScrubEphemeralToolMessages runs after the final provider step, before a
// session adopts the turn. It preserves assistant/tool pairing while removing
// resource bodies from all subsequent history and persistence.
func ScrubEphemeralToolMessages(messages []provider.Message, reg *tools.Registry) {
	if reg == nil {
		return
	}
	argsByCallID := make(map[string]json.RawMessage)
	for i := range messages {
		if messages[i].Role != provider.RoleAssistant {
			continue
		}
		for _, call := range messages[i].ToolCalls {
			argsByCallID[call.ID] = json.RawMessage(call.Function.Arguments)
		}
	}
	for i := range messages {
		message := &messages[i]
		if message.Role != provider.RoleTool {
			continue
		}
		tool, ok := reg.Get(message.Name)
		if !ok {
			continue
		}
		if ephemeral, ok := tool.(tools.EphemeralResultTool); ok {
			message.Content = ephemeral.EphemeralResultMarker(argsByCallID[message.ToolCallID])
		}
	}
}
