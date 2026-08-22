// scripted_completer_test.go supplies a provider.Completer fake for
// uiadapter tests. It mirrors the shape of the clichat test fake
// (internal/clichat/deferred_tool_loading_integration_test.go:21-100) but
// deliberately stays in package uiadapter_test so the uiadapter test
// surface does not pull in any cli-family import. The block chan is
// exposed so tests that need ChatTurn to block until released can pin
// it; default scriptedCompleter does not block, and nullCompleter
// returns empty responses for tests that do not care what the model said.
package uiadapter_test

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// scriptedCompleter replays a fixed sequence of provider.Responses. The
// last entry repeats if more requests arrive than scripted turns.
//
// Not provider.ContextAccountingAware: chat.Session treats the zero value
// as "bill everything", which is what these tests want.
type scriptedCompleter struct {
	mu    sync.Mutex
	turns []provider.Response
	calls atomic.Int32
	// block lets a test pin ChatTurn until it is closed. nil disables the
	// wait; tests that need a deterministic cancellation point set this
	// before the first request.
	block chan struct{}
}

func (c *scriptedCompleter) Name() string { return "scripted" }

func (c *scriptedCompleter) ChatStream(_ context.Context, req provider.Request, w io.Writer) (string, error) {
	resp, _ := c.ChatTurn(context.Background(), req)
	if w != nil && resp != nil {
		_, _ = io.WriteString(w, resp.Content)
	}
	if resp == nil {
		return "", nil
	}
	return resp.Content, nil
}

func (c *scriptedCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return resp.Content, nil
}

func (c *scriptedCompleter) ChatTurn(ctx context.Context, _ provider.Request) (*provider.Response, error) {
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := int(c.calls.Add(1)) - 1
	if idx >= len(c.turns) {
		idx = len(c.turns) - 1
	}
	if idx < 0 {
		return &provider.Response{FinishReason: "stop"}, nil
	}
	resp := c.turns[idx]
	return &resp, nil
}

// nullCompleter returns a single empty assistant response for every call.
// Useful for tests that drive the session but do not assert on model output.
type nullCompleter struct{}

func (c *nullCompleter) Name() string { return "null" }

func (c *nullCompleter) ChatStream(_ context.Context, _ provider.Request, _ io.Writer) (string, error) {
	return "", nil
}

func (c *nullCompleter) Chat(_ context.Context, _ provider.Request) (string, error) {
	return "", nil
}

func (c *nullCompleter) ChatTurn(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return &provider.Response{FinishReason: "stop"}, nil
}

// toolResponse builds a provider.Response carrying a single tool call.
// Tool calls live on a Response, not on a free function, so a test can
// chain them with assistant content turns.
func toolResponse(id, name, args string) provider.Response {
	call := provider.ToolCall{ID: id, Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = args
	return provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}
}

// assistantResponse builds a plain assistant content response.
func assistantResponse(content string) provider.Response {
	return provider.Response{Content: content, FinishReason: "stop"}
}
