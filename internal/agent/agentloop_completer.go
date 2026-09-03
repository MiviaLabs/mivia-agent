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
	// defaults carries the CLI Options the SDK loop's bare
	// provider.Request never sets (run.go's runChat builds Model,
	// Messages, and Tools only): the reasoning dial, the token and
	// temperature ceilings, the per-request timeout, the replay
	// suppression flag, and the session key. translateAgentLoopRequest
	// merges them into every converted request.
	defaults turnRequestDefaults
	// onFinish records each response's FinishReason. The CLI Loop's
	// LastFinishReason has no SDK Result carrier, so the wrapper -
	// the only place the provider's reason still exists - reports it
	// back through this callback.
	onFinish func(finishReason string)
	// onChat bumps the shared turn's iteration counter at the top of
	// each Chat call so the tool shims can stamp
	// runtime.Request.Step (later-step re-issues must re-run reads).
	onChat func()
	// onUsage reports each completed provider call's CLI request and
	// response pair, the carrier the SDK path uses for the legacy
	// emitTurnUsage calibration/token-usage update. Nil drops the
	// report.
	onUsage func(ctx context.Context, req provider.Request, resp *provider.Response)
	// advertised returns the run's pinned advertised ToolSpec snapshot
	// (nil when none exists); applyAdvertisedTools replaces the
	// request's registry-derived tools with it. See sdk_advertised.go
	// for the recovery-request safety note. Nil disables the override.
	advertised func() []provider.ToolSpec
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
	return newAgentLoopCompleterWithDefaults(c, turnRequestDefaults{}, nil, nil, nil)
}

// newAgentLoopCompleterWithDefaults builds the wrapper with the
// per-turn request defaults, an optional finish-reason recorder, an
// optional per-Chat iteration bump, and an optional per-call usage
// reporter. Nil callbacks drop the report.
func newAgentLoopCompleterWithDefaults(c provider.Completer, defaults turnRequestDefaults, onFinish func(string), onChat func(), onUsage func(context.Context, provider.Request, *provider.Response)) (*agentLoopCompleter, error) {
	if c == nil {
		return nil, errors.New("agent: nil CLI completer")
	}
	return &agentLoopCompleter{cli: c, defaults: defaults, onFinish: onFinish, onChat: onChat, onUsage: onUsage}, nil
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
	cliReq := applyAdvertisedTools(applyStreaming(mergeTurnDefaults(translateAgentLoopRequest(req), a.defaults), req), a.advertised)
	if a.onChat != nil {
		a.onChat()
	}
	if r, err := a.cli.ChatTurn(ctx, cliReq); err == nil && r != nil {
		if a.onFinish != nil {
			a.onFinish(r.FinishReason)
		}
		if a.onUsage != nil {
			a.onUsage(ctx, cliReq, r)
		}
		return convertToSDKResponse(*r), nil
	} else if err != nil {
		return sdkshape.Response{}, err
	}
	// Fallback: ChatTurn returned (nil, nil) - defensive only.
	content, err := a.cli.Chat(ctx, cliReq)
	if err != nil {
		return sdkshape.Response{}, err
	}
	if a.onUsage != nil {
		a.onUsage(ctx, cliReq, &provider.Response{Content: content})
	}
	return sdkshape.Response{
		Message: sdkshape.Message{
			Role:    sdkshape.RoleAssistant,
			Content: content,
		},
		FinishReason: "stop",
	}, nil
}

// ChatStream implements provider.Completer. Like Chat, it goes through
// the CLI's ChatTurn - the only path that surfaces tool calls - and
// forwards content, tool calls, the real finish reason, and usage as
// chunks. Content still reaches a live StreamWriter as deltas arrive;
// when the SDK request carries no StreamingWriter the assembled content
// is emitted as one delta chunk instead. The goroutine selects on
// ctx.Done() before each emit so a cancellation does not block the
// channel close.
//
// It previously called the CLI's plain ChatStream, which returns content
// only: every tool call the model made was dropped on the floor and the
// finish reason was hardcoded to "stop", so a turn that acted looked
// exactly like a turn that decided to stop. The SDK loop calls Chat and
// never ChatStream today, so nothing shipped through that path - but a
// seam that silently discards the model's actions is not one to leave
// armed.
func (a *agentLoopCompleter) ChatStream(ctx context.Context, req sdkshape.Request) (<-chan sdkshape.Chunk, error) {
	ch := make(chan sdkshape.Chunk, 1)
	go func() {
		defer close(ch)
		cliReq := applyAdvertisedTools(applyStreaming(mergeTurnDefaults(translateAgentLoopRequest(req), a.defaults), req), a.advertised)
		if a.onChat != nil {
			a.onChat()
		}
		resp, err := a.chatStreamResponse(ctx, cliReq)
		if err != nil {
			select {
			case ch <- sdkshape.Chunk{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		if a.onFinish != nil {
			a.onFinish(resp.FinishReason)
		}
		if a.onUsage != nil {
			a.onUsage(ctx, cliReq, resp)
		}
		emitStreamChunks(ctx, ch, convertToSDKResponse(*resp), cliReq.StreamWriter != nil)
	}()
	return ch, nil
}

// chatStreamResponse runs one streaming turn and returns the whole
// response. ChatTurn is the tool-call-bearing path; the plain ChatStream
// fallback exists only for the defensive (nil, nil) return that mirrors
// Chat's own fallback, and a fallback answer carries no tool calls to
// lose because the call that produced it could not report any.
func (a *agentLoopCompleter) chatStreamResponse(ctx context.Context, cliReq provider.Request) (*provider.Response, error) {
	if r, err := a.cli.ChatTurn(ctx, cliReq); err != nil {
		return nil, err
	} else if r != nil {
		return r, nil
	}
	content, err := a.cli.ChatStream(ctx, cliReq, cliReq.StreamWriter)
	if err != nil {
		return nil, err
	}
	return &provider.Response{Content: content, FinishReason: "stop"}, nil
}

// emitStreamChunks emits one turn's response as SDK chunks: the content
// delta (suppressed when a live writer already received it byte by
// byte), one chunk per tool call, then the terminal Done chunk carrying
// the finish reason and usage. Every send selects on ctx.Done() so a
// cancelled turn cannot block the channel close.
func emitStreamChunks(ctx context.Context, ch chan<- sdkshape.Chunk, resp sdkshape.Response, liveWriter bool) {
	send := func(c sdkshape.Chunk) bool {
		select {
		case ch <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if resp.Message.Content != "" && !liveWriter {
		if !send(sdkshape.Chunk{Delta: resp.Message.Content}) {
			return
		}
	}
	for i := range resp.ToolCalls {
		call := resp.ToolCalls[i]
		if !send(sdkshape.Chunk{ToolCallDelta: &call}) {
			return
		}
	}
	send(sdkshape.Chunk{Done: true, FinishReason: resp.FinishReason, Usage: resp.Usage})
}

// applyStreaming turns the SDK request's StreamingWriter into the CLI
// request's live-stream plumbing: Stream on (the legacy loop streams
// whenever a FinalWriter is attached) and StreamWriter as the delta
// sink. A nil StreamingWriter leaves the CLI request unchanged. A live
// streaming turn never rides the wire-stream transport, so an explicit
// streaming shape clears the flag mergeTurnDefaults may have set.
func applyStreaming(cliReq provider.Request, req sdkshape.Request) provider.Request {
	if req.StreamingWriter == nil {
		return cliReq
	}
	cliReq.Stream = true
	cliReq.StreamWriter = req.StreamingWriter
	cliReq.StreamTransport = false
	return cliReq
}
