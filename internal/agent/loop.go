// Package agent runs the multi-step tool-calling agent loop.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// EventKind classifies agent events for UI.
type EventKind string

const (
	EventAssistant    EventKind = "assistant"
	EventToolStart    EventKind = "tool_start"
	EventToolEnd      EventKind = "tool_end"
	EventStep         EventKind = "step"
	EventPrune        EventKind = "prune"
	EventToolParallel EventKind = "tool_parallel"
)

// Event is a UI-facing agent progress event.
type Event struct {
	Kind    EventKind
	Name    string
	Detail  string
	Content string
}

// Options configures the loop.
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
	// The full result is still visible to the model during the current turn
	// via the tool execution; only the persisted message is truncated.
	MaxToolResultChars int
	// OnEvent is optional; called for tool traces and assistant text.
	OnEvent func(Event)
	// FinalWriter receives the final assistant text (may be empty if only tools).
	FinalWriter io.Writer
}

// Loop owns messages and runs tool turns until completion.
type Loop struct {
	Completer provider.Completer
	Tools     *tools.Registry
	Messages  []provider.Message
}

// toolExecResult holds the outcome of a single tool call execution.
type toolExecResult struct {
	index     int // original position in ToolCalls slice
	toolCall  provider.ToolCall
	result    string
	truncated bool // whether result was truncated for history
}

// Run appends the user message and runs the agent loop. Returns final assistant text.
// Tool calls within a single turn are executed concurrently.
// Results are appended to Messages in the original call order.
func (l *Loop) Run(ctx context.Context, userText string, opts Options) (string, error) {
	// MaxSteps <= 0 means unlimited — the loop runs until the model
	// returns no tool calls or the context is cancelled.
	// Only when MaxSteps > 0 is there an artificial step cap.
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
		if opts.OnEvent != nil {
			if opts.MaxSteps > 0 {
				opts.OnEvent(Event{Kind: EventStep, Detail: fmt.Sprintf("%d/%d", step, opts.MaxSteps)})
			} else {
				opts.OnEvent(Event{Kind: EventStep, Detail: fmt.Sprintf("%d/∞", step)})
			}
		}

		// Prune messages to stay within context budget.
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
		}
		resp, err := l.Completer.ChatTurn(ctx, req)
		if err != nil {
			return lastText, err
		}

		if len(resp.ToolCalls) == 0 {
			lastText = resp.Content
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
			return lastText, nil
		}

		// Assistant message with tool_calls (content may be empty or commentary).
		l.Messages = append(l.Messages, provider.Message{
			Role:      provider.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		if resp.Content != "" && opts.OnEvent != nil {
			opts.OnEvent(Event{Kind: EventAssistant, Content: resp.Content})
		}

		// --- Parallel tool execution ---
		// Fire all tool_start events sequentially so the UI shows all
		// requested tools before any results come in.
		if opts.OnEvent != nil && len(resp.ToolCalls) > 1 {
			names := make([]string, len(resp.ToolCalls))
			for i, tc := range resp.ToolCalls {
				names[i] = tc.Function.Name
			}
			opts.OnEvent(Event{
				Kind:   EventToolParallel,
				Detail: fmt.Sprintf("%d tools: %s", len(resp.ToolCalls), strings.Join(names, ", ")),
			})
		}
		for _, tc := range resp.ToolCalls {
			if opts.OnEvent != nil {
				opts.OnEvent(Event{
					Kind:   EventToolStart,
					Name:   tc.Function.Name,
					Detail: truncate(tc.Function.Arguments, 120),
				})
			}
		}

		// Execute all tool calls concurrently.
		results := executeToolsParallel(ctx, resp.ToolCalls, l.Tools, opts)

		// Append tool results to Messages in original call order.
		// Sort by index since goroutines may finish out of order.
		sort.Slice(results, func(i, j int) bool {
			return results[i].index < results[j].index
		})
		for _, r := range results {
			if opts.OnEvent != nil {
				detail := truncate(r.result, 160)
				if r.truncated {
					detail = truncate(r.result, 140) + " ..."
				}
				opts.OnEvent(Event{
					Kind:   EventToolEnd,
					Name:   r.toolCall.Function.Name,
					Detail: detail,
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
}

// executeToolsParallel runs all tool calls concurrently.
// Returns results in arbitrary order (caller should sort by .index).
func executeToolsParallel(ctx context.Context, calls []provider.ToolCall, reg *tools.Registry, opts Options) []toolExecResult {
	n := len(calls)
	if n == 0 {
		return nil
	}
	results := make([]toolExecResult, n)
	var wg sync.WaitGroup

	for i, tc := range calls {
		wg.Add(1)
		go func(idx int, call provider.ToolCall) {
			defer wg.Done()
			result, err := reg.Execute(ctx, call.Function.Name, json.RawMessage(call.Function.Arguments))
			if err != nil {
				result = fmt.Sprintf("error: %v", err)
			}
			// Cap tool result stored in history to prevent context blowup.
			maxResult := opts.MaxToolResultChars
			truncated := false
			if maxResult > 0 && len(result) > maxResult {
				result = result[:maxResult] + fmt.Sprintf("\n... (truncated %d bytes, full result used during execution)", len(result)-maxResult)
				truncated = true
			}
			results[idx] = toolExecResult{
				index:     idx,
				toolCall:  call,
				result:    result,
				truncated: truncated,
			}
		}(i, tc)
	}
	wg.Wait()
	return results
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
