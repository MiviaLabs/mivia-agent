package events

import "context"

// Handler receives events published on the Bus.
type Handler interface {
	HandleEvent(ctx context.Context, ev Event)
}

// HandlerFunc is an adapter that allows ordinary functions to be used as
// Handler implementations.
type HandlerFunc func(ctx context.Context, ev Event)

// HandleEvent implements the Handler interface.
func (f HandlerFunc) HandleEvent(ctx context.Context, ev Event) {
	f(ctx, ev)
}
