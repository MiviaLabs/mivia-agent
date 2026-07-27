package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
	if r.err != nil {
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

var sensitiveToolText = regexp.MustCompile(`(?i)(password|passwd|token|secret|api[_-]?key|authorization)(?:[-_ ]?[A-Za-z0-9]*)?\s*[:=]?\s*[^\s,;]*|bearer\s+[A-Za-z0-9._~-]+|(?:sk-ant-|sk-|ghp_|github_pat_)[A-Za-z0-9._~-]+|-----BEGIN [A-Z ]+PRIVATE KEY-----`)

func redactToolInput(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	// Default: operator-visible args (bounded). Opt-in full field redaction.
	if !tools.RedactToolArgs() {
		return truncatePreview(raw, 256)
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return truncatePreview(sensitiveToolText.ReplaceAllString(raw, "$1=[redacted]"), 256)
	}
	redactJSONValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return "[invalid input]"
	}
	return truncatePreview(string(encoded), 256)
}

func redactJSONValue(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, nested := range current {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "token") ||
				strings.Contains(lower, "secret") || strings.Contains(lower, "api_key") ||
				strings.Contains(lower, "authorization") {
				current[key] = "[redacted]"
				continue
			}
			if lower == "content" {
				if text, ok := nested.(string); ok {
					current[key] = fmt.Sprintf("[content %d bytes]", len(text))
					continue
				}
			}
			redactJSONValue(nested)
		}
	case []any:
		for _, nested := range current {
			redactJSONValue(nested)
		}
	}
}

func redactToolOutput(output string) string {
	return truncatePreview(sensitiveToolText.ReplaceAllString(output, "[redacted]"), 512)
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
	limitToolBatchResults(results, opts.MaxToolBatchResultChars)
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
	emit(opts, Event{
		Kind:       EventToolEnd,
		ToolCallID: r.toolCall.ID,
		Name:       r.toolCall.Function.Name,
		Detail:     toolEndDetail(r),
		Output:     redactToolOutput(r.result),
	})
}

func prepareToolTasks(ctx context.Context, calls []provider.ToolCall, reg *tools.Registry, timeout time.Duration) []toolTask {
	tasks := make([]toolTask, len(calls))
	for i, call := range calls {
		raw := json.RawMessage(call.Function.Arguments)
		capability := reg.Capability(call.Function.Name, raw)
		callTimeout := resolveToolCallTimeout(timeout, capability.Timeout)
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
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

func executeToolTask(idx int, task *toolTask, reg *tools.Registry, scheduler *toolScheduler, opts Options, results []toolExecResult, finished *atomic.Int32) {
	release, err := scheduler.acquire(task.callCtx, task.capability.ResourceKey)
	if err != nil {
		results[idx] = toolExecResult{index: idx, toolCall: task.call, result: "error: " + err.Error(), err: err}
		emitToolEnd(opts, results[idx])
		if finished != nil {
			finished.Add(1)
		}
		return
	}
	if err := task.callCtx.Err(); err != nil {
		release()
		results[idx] = toolExecResult{index: idx, toolCall: task.call, result: "error: " + err.Error(), err: err}
		emitToolEnd(opts, results[idx])
		if finished != nil {
			finished.Add(1)
		}
		return
	}
	// Promote UI status from queued → running when work actually starts.
	emit(opts, Event{
		Kind:       EventToolStart,
		ToolCallID: task.call.ID,
		Name:       task.call.Function.Name,
		Detail:     "running",
	})
	r := opts.Dispatcher.Invoke(task.callCtx, runtime.Request{
		ID:       task.call.ID,
		ParentID: opts.ParentID,
		TurnID:   opts.TurnID,
		Depth:    opts.Depth,
		Budget:   opts.Budget,
		Kind:     runtime.Tool,
		Name:     task.call.Function.Name,
		Input:    task.raw,
		Timeout:  task.timeout,
	})
	result, err := string(r.Output), r.Err
	release()
	// Keep model-visible tool bodies; only synthesize an error when empty.
	if err != nil && strings.TrimSpace(result) == "" {
		result = fmt.Sprintf("error: %v", err)
	}
	result, truncated := capToolResult(result, opts.MaxToolResultChars, task.capability.MaxResultBytes)
	results[idx] = toolExecResult{index: idx, toolCall: task.call, result: result, truncated: truncated, err: err}
	emitToolEnd(opts, results[idx])
	if finished != nil {
		finished.Add(1)
	}
}
