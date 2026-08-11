package chat

import (
	"context"
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func isInterruptedTurn(ctx context.Context, turnErr error) bool {
	if errors.Is(turnErr, context.Canceled) || errors.Is(turnErr, context.DeadlineExceeded) {
		return true
	}
	return ctx != nil && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded))
}

func resolveTurnExecutionSurface(sessionTools *tools.Registry, sessionDispatcher *runtime.Dispatcher, turn *TurnOptions) (*tools.Registry, *runtime.Dispatcher) {
	if turn == nil {
		return sessionTools, sessionDispatcher
	}
	if turn.Tools != nil {
		sessionTools = turn.Tools
	}
	if turn.Dispatcher != nil {
		sessionDispatcher = turn.Dispatcher
	}
	return sessionTools, sessionDispatcher
}

func replaceNewestUserText(messages []provider.Message, userText, persistedText string) {
	if userText == persistedText {
		return
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == provider.RoleUser && messages[i].Content == userText {
			messages[i].Content = persistedText
			return
		}
	}
}
