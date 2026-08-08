package coordinator

import (
	"context"
	"errors"
)

// ErrWaitOnlyJoinLost means a remote child became locally runnable before a
// wait-only join could attach. The caller must obtain an actor permit first.
var ErrWaitOnlyJoinLost = errors.New("panel wait-only join became locally runnable")

type panelWaitOnlyContextKey struct{}

// ContextWithPanelWaitOnlyJoin prevents a wait-only caller from taking over a
// child that is no longer held by another executor.
func ContextWithPanelWaitOnlyJoin(ctx context.Context) context.Context {
	return context.WithValue(ctx, panelWaitOnlyContextKey{}, true)
}

func panelWaitOnlyJoin(ctx context.Context) bool {
	v, _ := ctx.Value(panelWaitOnlyContextKey{}).(bool)
	return v
}
