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

// MailboxDrainFunc drains pending parent→child messages at a step boundary.
type MailboxDrainFunc func() []ParentMessage

// ParentMessage is a parent→child envelope fragment for step-boundary inject.
type ParentMessage struct {
	Kind      string // "steer", "answer", or "ask"
	Body      string
	MessageID string // correlation id (required for kind=ask answers)
}

type mailboxDrainKey struct{}

// ContextWithMailboxDrain associates a drain function with ctx.
func ContextWithMailboxDrain(ctx context.Context, drain MailboxDrainFunc) context.Context {
	return context.WithValue(ctx, mailboxDrainKey{}, drain)
}

// MailboxDrainFrom returns the drain function on ctx, if any.
func MailboxDrainFrom(ctx context.Context) (MailboxDrainFunc, bool) {
	fn, ok := ctx.Value(mailboxDrainKey{}).(MailboxDrainFunc)
	return fn, ok && fn != nil
}
