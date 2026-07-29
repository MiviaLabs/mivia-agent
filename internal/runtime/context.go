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
func NewSessionID() string {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		panic("generate session ID: " + err.Error())
	}
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
