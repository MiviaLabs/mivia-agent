package ledger

import "context"

type runContextKey struct{}

// ContextWithRunID binds a workflow mutation to its run.
func ContextWithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runContextKey{}, runID)
}

// RunIDFromContext returns the workflow run bound to a mutation context.
func RunIDFromContext(ctx context.Context) (string, bool) {
	runID, ok := ctx.Value(runContextKey{}).(string)
	return runID, ok && runID != ""
}
