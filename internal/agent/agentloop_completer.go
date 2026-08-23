// Package agent - completer adapter for the SDK-backed inner loop.
//
// agentLoopCompleter adapts an internal/provider.Completer (CLI shape)
// to the mivia-ai-sdk/provider.Completer shape so the SDK-backed
// inner loop can drive today's per-provider implementations
// unchanged. The wrapper is unexported because only the dispatcher
// introduced by commit 3 ever instantiates it.
//
// The wrapper is the minimum-viable adapter: Chat translates
// ChatTurn-or-Chat into one SDK Response, and ChatStream emits a
// single content chunk followed by one terminal chunk. True
// streaming and request-shape translation are tracked as
// follow-ups; the dispatcher gates them on commit 3.
package agent

import (
	"context"
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// agentLoopCompleter adapts an internal/provider.Completer (CLI shape)
// to the mivia-ai-sdk/provider.Completer shape so the SDK-backed
// inner loop can drive today's per-provider implementations
// unchanged.
type agentLoopCompleter struct {
	cli provider.Completer
}

// Compile-time assertion: the wrapper satisfies the SDK's
// provider.Completer shape. A future SDK interface change fails at
// build time inside this package, not inside the dispatcher.
var _ sdkshape.Completer = (*agentLoopCompleter)(nil)

// newAgentLoopCompleter builds the wrapper. A nil completer is a
// programmer error and returns an error rather than a wrapper that
// nil-derefs on the first call, per the repo rule that internal
// packages return errors instead of panicking.
func newAgentLoopCompleter(c provider.Completer) (*agentLoopCompleter, error) {
	if c == nil {
		return nil, errors.New("agent: nil CLI completer")
	}
	return &agentLoopCompleter{cli: c}, nil
}

// Name implements provider.Completer. It forwards to the wrapped
// CLI completer so the SDK loop's Name()-keyed lookups match the
// CLI runtime's own naming.
func (a *agentLoopCompleter) Name() string { return a.cli.Name() }

// Chat implements provider.Completer. It calls the CLI's ChatTurn
// (the only path that surfaces tool calls) and falls back to Chat
// only when ChatTurn returns a nil *Response. The returned SDK
// Response carries the assistant message, the tool calls, and the
// FinishReason the CLI produced. Usage is not propagated; the
// SDK treats Reported=false Usage as "no observation", which is
// the correct neutral value here.
func (a *agentLoopCompleter) Chat(ctx context.Context, req sdkshape.Request) (sdkshape.Response, error) {
	cliReq := translateAgentLoopRequest(req)
	if r, err := a.cli.ChatTurn(ctx, cliReq); err == nil && r != nil {
		return convertToSDKResponse(*r), nil
	} else if err != nil {
		return sdkshape.Response{}, err
	}
	// Fallback: ChatTurn returned (nil, nil) - defensive only.
	content, err := a.cli.Chat(ctx, cliReq)
	if err != nil {
		return sdkshape.Response{}, err
	}
	return sdkshape.Response{
		Message: sdkshape.Message{
			Role:    sdkshape.RoleAssistant,
			Content: content,
		},
		FinishReason: "stop",
	}, nil
}

// ChatStream implements provider.Completer. It returns a buffered
// channel of one content Chunk and one terminal Chunk, then closes
// the channel. The body invokes the CLI's synchronous Chat and
// emits the content as a single delta chunk; true streaming is
// deferred to a follow-up commit. The goroutine selects on
// ctx.Done() before each emit so a cancellation does not block
// the channel close.
func (a *agentLoopCompleter) ChatStream(ctx context.Context, req sdkshape.Request) (<-chan sdkshape.Chunk, error) {
	ch := make(chan sdkshape.Chunk, 1)
	go func() {
		defer close(ch)
		cliReq := translateAgentLoopRequest(req)
		content, err := a.cli.Chat(ctx, cliReq)
		if err != nil {
			select {
			case ch <- sdkshape.Chunk{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		if content != "" {
			select {
			case ch <- sdkshape.Chunk{Delta: content}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case ch <- sdkshape.Chunk{Done: true, FinishReason: "stop"}:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// translateAgentLoopRequest projects an SDK Request onto a CLI
// Request. Commit 2 returns the zero provider.Request; the SDK
// request is discarded. Commit 3 supplies the field-level mapping.
func translateAgentLoopRequest(_ sdkshape.Request) provider.Request {
	return provider.Request{}
}

// convertToSDKResponse projects a CLI Response onto an SDK
// Response. The helper is file-local (lowercase) because no other
// wrapper in this package needs it. Tool calls are copied by ID
// and Name; the CLI's Arguments string is wrapped in []byte
// because the SDK's ToolCall carries a byte payload. An empty
// CLI Arguments becomes a nil slice so the SDK's Validate
// treats it as "no payload yet" rather than "zero-length payload".
func convertToSDKResponse(r provider.Response) sdkshape.Response {
	toolCalls := make([]sdkshape.ToolCall, len(r.ToolCalls))
	for i, tc := range r.ToolCalls {
		toolCalls[i] = sdkshape.ToolCall{
			Index: i,
			ID:    tc.ID,
			Name:  tc.Function.Name,
		}
		if tc.Function.Arguments != "" {
			toolCalls[i].Arguments = []byte(tc.Function.Arguments)
		}
	}
	return sdkshape.Response{
		Message: sdkshape.Message{
			Role:             sdkshape.RoleAssistant,
			Content:          r.Content,
			ReasoningContent: r.ReasoningContent,
			ToolCalls:        toolCalls,
		},
		ToolCalls:    toolCalls,
		FinishReason: r.FinishReason,
	}
}
