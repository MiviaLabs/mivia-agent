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

// MailboxAccess bundles the mailbox-related hooks a step-boundary consumer may
// need: draining pending parent→child messages, interrupting the current step,
// and asking whether messages are pending. All fields are optional; a bundle
// with every field nil is treated as absent by the readers.
type MailboxAccess struct {
	Drain     MailboxDrainFunc
	Interrupt func() <-chan struct{}
	Pending   func() bool
	// PendingInterrupt reports whether an Interrupt-flagged steer is queued
	// (the strict gate for the loop watcher's signal branch; Pending is the
	// len-based gate for the watchdog branch).
	PendingInterrupt func() bool
}

type mailboxAccessKey struct{}

// ContextWithMailboxAccess associates a MailboxAccess bundle with ctx.
func ContextWithMailboxAccess(ctx context.Context, access MailboxAccess) context.Context {
	return context.WithValue(ctx, mailboxAccessKey{}, access)
}

// MailboxAccessFrom returns the MailboxAccess bundle on ctx, if any. A bare
// context, or one holding a nil bundle (every field nil), reports not-ok.
func MailboxAccessFrom(ctx context.Context) (MailboxAccess, bool) {
	access, ok := ctx.Value(mailboxAccessKey{}).(MailboxAccess)
	if !ok || access.Drain == nil && access.Interrupt == nil && access.Pending == nil && access.PendingInterrupt == nil {
		return MailboxAccess{}, false
	}
	return access, true
}

// ContextWithMailboxDrain associates a drain function with ctx. It is a thin
// wrapper over ContextWithMailboxAccess.
func ContextWithMailboxDrain(ctx context.Context, drain MailboxDrainFunc) context.Context {
	return ContextWithMailboxAccess(ctx, MailboxAccess{Drain: drain})
}

// MailboxDrainFrom returns the drain function on ctx, if any.
func MailboxDrainFrom(ctx context.Context) (MailboxDrainFunc, bool) {
	access, ok := MailboxAccessFrom(ctx)
	if !ok {
		return nil, false
	}
	return access.Drain, access.Drain != nil
}
