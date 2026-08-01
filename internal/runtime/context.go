package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base32"
)

// Caller identifies the agent invoking a handler. It travels in the context
// because tools receive no Request and must attribute and authorize their work.
type Caller struct {
	SessionID string
	TurnID    string
	ParentID  string
	Depth     int
	// Role is populated by role-aware dispatch. It is empty until roles are in
	// use, but remains part of the principal so later role separation does not
	// require another boundary signature change.
	Role string
}

type callerContextKey struct{}

// NewSessionID returns an unguessable principal identifier for one session.
//
// Unguessability is the whole point: the session ID is the principal that
// orchestration run access is scoped to (INV-AG-9). crypto/rand.Read never
// returns an error and always fills its buffer, crashing the program itself if
// the operating system's source fails, so there is no error path here - and a
// fallback to a weaker source would silently turn the principal into something
// enumerable, which is worse than not starting.
func NewSessionID() string {
	var token [16]byte
	_, _ = rand.Read(token[:])
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(token[:])
}

// ContextWithCaller associates an invocation caller with ctx.
func ContextWithCaller(ctx context.Context, caller Caller) context.Context {
	return context.WithValue(ctx, callerContextKey{}, caller)
}

// CallerFrom returns the caller associated with ctx.
func CallerFrom(ctx context.Context) (Caller, bool) {
	caller, ok := ctx.Value(callerContextKey{}).(Caller)
	return caller, ok
}
