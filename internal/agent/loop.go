package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type EventKind string

const (
	EventAssistant         EventKind = "assistant"
	EventToolStart         EventKind = "tool_start"
	EventToolEnd           EventKind = "tool_end"
	EventStep              EventKind = "step"
	EventPrune             EventKind = "prune"
	EventToolParallel      EventKind = "tool_parallel"
	EventSubagentStart     EventKind = "subagent_start"
	EventSubagentEnd       EventKind = "subagent_end"
	EventSubagentHeartbeat EventKind = "subagent_heartbeat"
)

type Event struct {
	Kind       EventKind
	ToolCallID string // stable correlation key for tool lifecycle events
	Name       string
	Detail     string
	Content    string
	Input      string // bounded, redacted tool input preview
	Output     string // bounded, redacted tool output preview
}

type Options struct {
	Model       string
	Temperature *float64
	MaxTokens   *int
	MaxSteps    int
	// MaxContextTokens sets the approximate token limit for the prompt context.
	// When exceeded, old messages are pruned (keeping system prompt and recent turns).
	// 0 or negative means no pruning.
	MaxContextTokens int
	// MaxToolResultChars caps each tool result stored in conversation history.
	// This prevents a single large output (e.g. read_file of 256KB) from
	// exceeding the context budget. 0 means no cap (use full result).
	MaxToolResultChars      int
	MaxToolCallsPerBatch    int
	MaxToolBatchResultChars int
	MaxConcurrentTools      int
	ToolTimeout             time.Duration
	RequestTimeout          time.Duration
	ParentID                string
	TurnID                  string
	Depth                   int
	Budget                  int
	Dispatcher              *runtime.Dispatcher
	OnEvent                 func(Event)
	FinalWriter             io.Writer
}

type Loop struct {
	Completer provider.Completer
	Tools     *tools.Registry
	Messages  []provider.Message
}

type toolExecResult struct {
	index     int // original position in ToolCalls slice
	toolCall  provider.ToolCall
	result    string
	truncated bool // whether result was truncated for history
	err       error
}

type toolTask struct {
	call       provider.ToolCall
	raw        json.RawMessage
	capability tools.Capability
	timeout    time.Duration
	callCtx    context.Context
	cancel     context.CancelFunc
}

type toolScheduler struct {
	limit chan struct{}
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func newToolScheduler(limit int) *toolScheduler {
	if limit <= 0 {
		limit = 4
	}
	return &toolScheduler{limit: make(chan struct{}, limit), locks: make(map[string]chan struct{})}
}
func (s *toolScheduler) acquire(ctx context.Context, key string) (func(), error) {
	select {
	case s.limit <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if key == "" {
		return func() { <-s.limit }, nil
	}
	s.mu.Lock()
	lock := s.locks[key]
	if lock == nil {
		lock = make(chan struct{}, 1)
		s.locks[key] = lock
	}
	s.mu.Unlock()
	select {
	case lock <- struct{}{}:
		return func() { <-lock; <-s.limit }, nil
	case <-ctx.Done():
		<-s.limit
		return nil, ctx.Err()
	}
}
func (l *Loop) Run(ctx context.Context, userText string, opts Options) (string, error) {
	if l.Completer == nil {
		return "", fmt.Errorf("nil completer")
	}
	if l.Tools == nil {
		return "", fmt.Errorf("nil tools")
	}
	l.Messages = append(l.Messages, provider.Message{Role: provider.RoleUser, Content: userText})
	toolSpecs := l.Tools.OpenAITools()
	var lastText string
	for step := 1; ; step++ {
		if opts.MaxSteps > 0 && step > opts.MaxSteps {
			return lastText, fmt.Errorf("agent exceeded max_steps (%d)", opts.MaxSteps)
		}
		l.emitStep(opts, step)

		text, done, err := l.runStep(ctx, toolSpecs, opts)
		if err != nil {
			return lastText, err
		}
		if done {
			return text, nil
		}
		if text != "" {
			lastText = text
		}
	}
}
func (l *Loop) emitStep(opts Options, step int) {
	if opts.OnEvent == nil {
		return
	}
	if opts.MaxSteps > 0 {
		opts.OnEvent(Event{Kind: EventStep, Detail: fmt.Sprintf("%d/%d", step, opts.MaxSteps)})
		return
	}
	opts.OnEvent(Event{Kind: EventStep, Detail: fmt.Sprintf("%d/∞", step)})
}
func (l *Loop) runStep(ctx context.Context, toolSpecs []provider.ToolSpec, opts Options) (text string, done bool, err error) {
	beforeTokens := provider.MessagesTokens(l.Messages)
	l.Messages = provider.PruneMessagesKeepTurns(l.Messages, opts.MaxContextTokens)
	afterTokens := provider.MessagesTokens(l.Messages)
	if afterTokens < beforeTokens && opts.OnEvent != nil {
		opts.OnEvent(Event{
			Kind:   EventPrune,
			Detail: fmt.Sprintf("pruned ~%d tokens (before=%d after=%d budget=%d)", beforeTokens-afterTokens, beforeTokens, afterTokens, opts.MaxContextTokens),
		})
	}

	req := provider.Request{
		Model:       opts.Model,
		Messages:    l.Messages,
		Temperature: opts.Temperature,
		MaxTokens:   opts.MaxTokens,
		Tools:       toolSpecs,
		ToolChoice:  "auto",
		Stream:      false,
		Timeout:     opts.RequestTimeout,
	}

	// Emit periodic "still thinking" heartbeat during the blocking model call.
	heartbeat, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	if opts.OnEvent != nil {
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			started := time.Now()
			for {
				select {
				case <-ticker.C:
					elapsed := time.Since(started)
					opts.OnEvent(Event{
						Kind:   EventStep,
						Detail: fmt.Sprintf("model thinking (%d s)", int(elapsed.Seconds())),
					})
				case <-heartbeat.Done():
					return
				}
			}
		}()
	}
	resp, err := l.Completer.ChatTurn(heartbeat, req)
	if err != nil {
		return "", false, err
	}

	if len(resp.ToolCalls) == 0 {
		l.Messages = append(l.Messages, provider.Message{
			Role:    provider.RoleAssistant,
			Content: resp.Content,
		})
		if opts.FinalWriter != nil && resp.Content != "" {
			_, _ = io.WriteString(opts.FinalWriter, resp.Content)
		}
		if opts.OnEvent != nil && resp.Content != "" {
			opts.OnEvent(Event{Kind: EventAssistant, Content: resp.Content})
		}
		return resp.Content, true, nil
	}

	l.Messages = append(l.Messages, provider.Message{
		Role:      provider.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	})
	if resp.Content != "" && opts.OnEvent != nil {
		opts.OnEvent(Event{Kind: EventAssistant, Content: resp.Content})
	}
	l.runToolBatch(ctx, resp.ToolCalls, opts)
	return resp.Content, false, nil
}
func (l *Loop) runToolBatch(ctx context.Context, calls []provider.ToolCall, opts Options) {
	if opts.OnEvent != nil && len(calls) > 1 {
		names := make([]string, len(calls))
		for i, tc := range calls {
			names[i] = tc.Function.Name
		}
		opts.OnEvent(Event{
			Kind:   EventToolParallel,
			Detail: fmt.Sprintf("%d tools: %s", len(calls), strings.Join(names, ", ")),
		})
	}
	for _, tc := range calls {
		if opts.OnEvent != nil {
			input := redactToolInput(tc.Function.Arguments)
			opts.OnEvent(Event{
				Kind:       EventToolStart,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Detail:     "queued",
				Input:      input,
			})
		}
	}
	results := executeToolsParallel(ctx, calls, l.Tools, opts)
	sort.Slice(results, func(i, j int) bool {
		return results[i].index < results[j].index
	})
	for _, r := range results {
		if opts.OnEvent != nil {
			detail := "completed"
			if r.err != nil {
				detail = "failed"
			}
			if r.truncated {
				detail = "completed (truncated)"
			}
			output := redactToolOutput(r.result)
			opts.OnEvent(Event{
				Kind:       EventToolEnd,
				ToolCallID: r.toolCall.ID,
				Name:       r.toolCall.Function.Name,
				Detail:     detail,
				Output:     output,
			})
		}
		l.Messages = append(l.Messages, provider.Message{
			Role:       provider.RoleTool,
			ToolCallID: r.toolCall.ID,
			Name:       r.toolCall.Function.Name,
			Content:    r.result,
		})
	}
}

var sensitiveToolText = regexp.MustCompile(`(?i)(password|passwd|token|secret|api[_-]?key|authorization)(?:[-_ ]?[A-Za-z0-9]*)?\s*[:=]?\s*[^\s,;]*|bearer\s+[A-Za-z0-9._~-]+|(?:sk-ant-|sk-|ghp_|github_pat_)[A-Za-z0-9._~-]+|-----BEGIN [A-Z ]+PRIVATE KEY-----`)

func redactToolInput(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
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

	if runToolWorkers(ctx, calls, executeN, tasks, results, reg, scheduler, opts, workers) {
		return results
	}
	limitToolBatchResults(results, opts.MaxToolBatchResultChars)
	return results
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

func runToolWorkers(ctx context.Context, calls []provider.ToolCall, executeN int, tasks []toolTask, results []toolExecResult, reg *tools.Registry, scheduler *toolScheduler, opts Options, workers int) bool {
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				executeToolTask(idx, &tasks[idx], reg, scheduler, opts, results)
			}
		}()
	}
	for i := 0; i < executeN; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			fillCanceledResults(ctx, calls, tasks, results, i)
			close(jobs)
			wg.Wait()
			return true
		}
	}
	close(jobs)
	wg.Wait()
	return false
}

func fillCanceledResults(ctx context.Context, calls []provider.ToolCall, tasks []toolTask, results []toolExecResult, start int) {
	for j := start; j < len(calls); j++ {
		err := tasks[j].callCtx.Err()
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = context.Canceled
		}
		results[j] = toolExecResult{index: j, toolCall: tasks[j].call, result: "error: " + err.Error(), err: err}
	}
}

func executeToolTask(idx int, task *toolTask, reg *tools.Registry, scheduler *toolScheduler, opts Options, results []toolExecResult) {
	release, err := scheduler.acquire(task.callCtx, task.capability.ResourceKey)
	if err != nil {
		results[idx] = toolExecResult{index: idx, toolCall: task.call, result: "error: " + err.Error(), err: err}
		return
	}
	if err := task.callCtx.Err(); err != nil {
		release()
		results[idx] = toolExecResult{index: idx, toolCall: task.call, result: "error: " + err.Error(), err: err}
		return
	}
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
}
