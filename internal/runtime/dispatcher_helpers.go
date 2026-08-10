package runtime

import (
	"context"
	"errors"
	"time"
)

var errDispatcherClosed = errors.New("dispatcher is closed")

func (d *Dispatcher) isClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

func (d *Dispatcher) failedInvocation(req Request, meta Metadata, started time.Time, err error) Result {
	if errors.Is(err, errDispatcherClosed) {
		return dispatcherClosedResult(req)
	}
	return d.failResult(req, meta, started, err, nil)
}

// joinHookContext keeps both events' advice, separated. Neither event may
// silence the other: a PreToolUse note about the workspace and a PostToolUse
// formatter's report are about different things.
func joinHookContext(pre, post string) string {
	switch {
	case pre == "":
		return post
	case post == "":
		return pre
	default:
		return pre + "\n" + post
	}
}

// waitDuplicateResult parks a duplicate invocation on its waiter channel and
// returns the owner's recorded result, or a canceled result when the duplicate
// caller gives up first.
func waitDuplicateResult(ctx context.Context, req Request, waiter chan Result) Result {
	select {
	case result := <-waiter:
		if errors.Is(result.Err, errDispatcherClosed) {
			return dispatcherClosedResult(req)
		}
		result.Metadata.Status = "duplicate"
		return result
	case <-ctx.Done():
		return Result{ID: req.ID, Name: req.Name, Kind: req.Kind, Err: ctx.Err(), Metadata: Metadata{ID: req.ID, Name: req.Name, Kind: string(req.Kind), Status: "canceled"}}
	}
}

func dispatcherClosedResult(req Request) Result {
	return Result{
		ID:   req.ID,
		Name: req.Name,
		Kind: req.Kind,
		Err:  errDispatcherClosed,
		Metadata: Metadata{
			ID:     req.ID,
			Name:   req.Name,
			Kind:   string(req.Kind),
			Status: "closed",
		},
	}
}

func deliverWaiters(waiters []chan Result, result Result) {
	for _, waiter := range waiters {
		select {
		case waiter <- result:
		default:
		}
	}
}

// closedResult returns a pre-buffered waiter carrying one recorded result.
func closedResult(result Result) chan Result {
	waiter := make(chan Result, 1)
	waiter <- result
	return waiter
}
