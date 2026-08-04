package runtime

import "context"

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
		result.Metadata.Status = "duplicate"
		return result
	case <-ctx.Done():
		return Result{ID: req.ID, Name: req.Name, Kind: req.Kind, Err: ctx.Err(), Metadata: Metadata{ID: req.ID, Name: req.Name, Kind: string(req.Kind), Status: "canceled"}}
	}
}

// closedResult returns a pre-buffered waiter carrying one recorded result.
func closedResult(result Result) chan Result {
	waiter := make(chan Result, 1)
	waiter <- result
	return waiter
}
