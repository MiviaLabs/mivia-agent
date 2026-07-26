// Package agent runs the multi-step tool-calling agent loop.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// EventKind classifies agent events for UI.
type EventKind string

const (
	EventAssistant EventKind = "assistant"
	EventToolStart EventKind = "tool_start"
	EventToolEnd   EventKind = "tool_end"
	EventStep      EventKind = "step"
	EventPrune     EventKind = "prune"
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

// Run appends the user message and runs the agent loop. Returns final assistant text.
func (l *Loop) Run(ctx context.Context, userText string, opts Options) (string, error) {
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 30
	}
	if l.Completer == nil {
		return "", fmt.Errorf("nil completer")
	}
	if l.Tools == nil {
		return "", fmt.Errorf("nil tools")
	}

	l.Messages = append(l.Messages, provider.Message{Role: provider.RoleUser, Content: userText})
	toolSpecs := l.Tools.OpenAITools()

	var lastText string
	for step := 1; step <= opts.MaxSteps; step++ {
		if opts.OnEvent != nil {
			opts.OnEvent(Event{Kind: EventStep, Detail: fmt.Sprintf("%d/%d", step, opts.MaxSteps)})
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

		for _, tc := range resp.ToolCalls {
			name := tc.Function.Name
			argStr := tc.Function.Arguments
			if opts.OnEvent != nil {
				opts.OnEvent(Event{Kind: EventToolStart, Name: name, Detail: truncate(argStr, 120)})
			}
			result, err := l.Tools.Execute(ctx, name, json.RawMessage(argStr))
			if err != nil {
				result = fmt.Sprintf("error: %v", err)
			}
			if opts.OnEvent != nil {
				opts.OnEvent(Event{Kind: EventToolEnd, Name: name, Detail: truncate(result, 160)})
			}
			l.Messages = append(l.Messages, provider.Message{
				Role:       provider.RoleTool,
				ToolCallID: tc.ID,
				Name:       name,
				Content:    result,
			})
		}
	}
	return lastText, fmt.Errorf("agent exceeded max_steps (%d)", opts.MaxSteps)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
