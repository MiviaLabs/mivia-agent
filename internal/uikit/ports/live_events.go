package ports

import "github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"

// BackgroundConversation is an optional capability interface asserted by
// consumers (same pattern as AsyncCompactionRunner): a conversation
// reports whether it is driven off-screen by an automation run rather
// than by a foreground UI session. A type that does not implement it is
// treated as foreground.
type BackgroundConversation interface {
	IsBackground() bool
}

// LiveEventsConversation is an optional capability interface asserted by
// consumers (same pattern as AsyncCompactionRunner): a conversation can
// fan its turn events out to extra read-only viewers while the primary
// per-turn channel keeps its single consumer. The events channel a
// subscription returns is NEVER closed by the publisher; Done and Stale
// on LiveSubscription are the only lifecycle signals a viewer gets.
type LiveEventsConversation interface {
	SubscribeLive() (<-chan uievent.Event, LiveSubscription)
}

// ForegroundMarker is an optional capability interface asserted by the
// UI: it tracks whether the UI currently owns this conversation as its
// active session. Send consults it to decide whether the turn may
// install the process-wide subagent-progress registrar: a conversation
// the screen owns may (the user is watching it), an off-screen one may
// not (it would hijack another session's display). New conversations
// start foreground so the startup session never loses the display; a
// conversation marked background via BackgroundConversation's setter
// starts unowned.
type ForegroundMarker interface {
	SetForeground(on bool)
	IsForeground() bool
}

// LiveSubscription is one viewer's handle on a SubscribeLive stream.
//
// Done closes when the subscription is closed - either by the viewer's
// own Close or because the conversation is shutting down. A closed Done
// means "stop reading silently": the viewer must never interpret it as
// the end of a turn and must not drain or reconcile anything on it.
//
// Stale closes once if the fan-out overflowed this subscription's
// buffer. The viewer should reload history and resubscribe; events
// dropped before that are gone.
//
// Close unregisters this viewer. It is safe to call more than once. The
// events channel itself is never closed by the publisher: a viewer that
// stops reading must call Close instead of waiting for a close that
// never comes.
type LiveSubscription interface {
	Done() <-chan struct{}
	Stale() <-chan struct{}
	Close()
}
