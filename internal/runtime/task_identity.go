package runtime

import "context"

// TaskIdentity is the coordination identity of a running subagent task
// (run + task + agent). It is distinct from Caller (session principal) and
// from the opaque dispatcher Request.ID. Tools that post messages need this
// to stamp ledger provenance without spoofing.
type TaskIdentity struct {
	RunID  string
	TaskID string
	Agent  string
}

type taskIdentityContextKey struct{}

// ContextWithTaskIdentity associates coordination identity with ctx.
func ContextWithTaskIdentity(ctx context.Context, id TaskIdentity) context.Context {
	return context.WithValue(ctx, taskIdentityContextKey{}, id)
}

// TaskIdentityFrom returns the task identity on ctx, if any.
func TaskIdentityFrom(ctx context.Context) (TaskIdentity, bool) {
	id, ok := ctx.Value(taskIdentityContextKey{}).(TaskIdentity)
	if !ok || id.RunID == "" || id.TaskID == "" {
		return TaskIdentity{}, false
	}
	return id, true
}
