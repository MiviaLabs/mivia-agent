// Package hub lets several `mivia` processes working the same session - a
// terminal TUI and, e.g., mivia-agent-desktop's spawned `mivia chat --json`
// process - see each other's live turns. Exactly one process per workspace
// (identified by the directory its context store lives in) becomes the hub;
// every later process for that workspace is a client. Every participant
// republishes its own chat.Session.EventBus stream to the hub, which
// rebroadcasts it to every other connected participant. There is no
// central/remote server: the hub is just whichever local process got there
// first, and ownership migrates to another process if it exits.
package hub

import (
	"context"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// relayedKinds is what a hub participant forwards to (and renders from) the
// hub - the subset of events.Kind meaningful to a second live surface.
// Workflow/invocation/UI-system kinds are deliberately excluded: they are
// process-local concerns (e.g. terminal resize), not conversation content.
var relayedKinds = []events.Kind{
	// KindTurnStart's Detail carries the user's own submitted text (published
	// by chat.Session.publishTurnStart, internal/chat/turn_events.go) - a hub
	// receiver treats it as "a new external turn is starting," using Detail for
	// the synthetic user turn it inserts.
	//
	// Its TurnID IS the turn's real id, the same "turn:N" every later event of
	// that turn carries, so a receiver may correlate on TurnID equality. That
	// was not always true: the publish used to happen in the surface, before
	// the id was minted, so this comment used to warn that the id was "a
	// throwaway, surface-local label". Moving the publish into the session
	// fixed it, and also gave the TUI - which had no publish at all - the turn
	// boundaries every other surface had.
	events.KindTurnStart,
	events.KindAssistant,
	events.KindThinking,
	events.KindToolStart,
	events.KindToolEnd,
	events.KindSubagentStart,
	events.KindSubagentEnd,
	events.KindSubagentHeartbeat,
	events.KindSubagentDone,
	// KindCompaction carries the typed, content-free compaction payload (see
	// events.CompactionEvent) - safe to relay by construction (INV-AG-32:
	// no prompts, tool arguments, hidden content, or summary payloads).
	events.KindCompaction,

	// KindTurnEnd and KindError are deliberately NOT relayed, although both
	// now have producers (chat.Session publishes them since fbeaf398). They
	// were listed here before those producers existed, so the list promised a
	// second surface events that never arrived; relaying them now would be
	// worse than the promise.
	//
	// This receiver treats "first event carrying a run id" as "the turn began"
	// (renderExternalEvent). That needs arrival order, and the bus does not
	// give it: SubscribeMany registers one subscription per kind, each with its
	// own queue, so a terminal routinely overtakes the assistant deltas of the
	// turn it closes. The receiver would mint an external_turn_start from the
	// terminal, emit external_done, drop the run, then mint a SECOND start when
	// the content arrived - duplicated turns, content after done, and a
	// seenRunIDs entry nothing reclaims.
	//
	// Holding them back also keeps raw error text off the wire. publishTurnEnd
	// sets Err, wire.go serializes err.Error() verbatim, and this process's own
	// NDJSON deliberately emits a classified string instead
	// (jsonTurnErrorMessage) - relaying would leave a second process better
	// informed than the local one, in the direction that leaks.
	//
	// Add them back once the receiver stops inferring turn start from arrival
	// order, and once the error text is classified at the boundary.
}

// dialSocketTimeout bounds how long a client waits to connect to an
// already-elected hub before treating it as unreachable (stale socket file,
// owner mid-exit) and retrying election itself.
const dialSocketTimeout = 500 * time.Millisecond

// reconnectBackoff is how long a client waits after losing the hub
// connection before re-attempting election. A live surface without a hub is
// degraded (no cross-process visibility) but never broken - the caller's own
// conversation keeps working regardless - so this favors a bounded,
// unhurried retry over a tight loop.
const reconnectBackoff = 2 * time.Second

// Sink renders an event this process did not itself originate (received
// from the hub) onto whatever live surface this process presents - stdout
// NDJSON for line-mode, the TUI's renderer for the TUI. nil is valid: a
// surface with nothing useful to do with it yet just drops it.
type Sink func(events.Event)

// Handle is a live hub membership. Leave unwinds it (releases the election
// lock if this process owned it, or closes the client connection) and stops
// the background retry loop. Safe to call once; Join's caller should defer
// it.
type Handle struct {
	cancel context.CancelFunc
}

// Leave unwinds this process's hub membership. Safe to call on a nil
// Handle or more than once.
func (h *Handle) Leave() {
	if h == nil || h.cancel == nil {
		return
	}
	h.cancel()
}

// Join makes sess a member of storeDir's hub - the directory its context
// store lives in (so hub.lock/hub.sock sit right beside context.db
// regardless of managed-worktree or config-path routing). This process
// becomes the hub owner if none exists yet, or a client of the existing one
// otherwise, and keeps retrying (electing a new owner if the current one
// disappears) for the life of the returned Handle. sink renders events
// received FROM other processes; nil is valid.
//
// Join returns immediately; membership runs in a background goroutine.
func Join(storeDir string, sess *chat.Session, sink Sink) *Handle {
	ctx, cancel := context.WithCancel(context.Background())
	go membershipLoop(ctx, storeDir, sess, sink)
	return &Handle{cancel: cancel}
}

// lockFilePath is the single source for the hub's election lock path -
// shared with TryAcquireMaintenanceLock (maintenance_lock.go) so ordinary hub
// ownership and maintenance exclusivity can never target different files.
func lockFilePath(storeDir string) string { return filepath.Join(storeDir, "hub.lock") }

func membershipLoop(ctx context.Context, storeDir string, sess *chat.Session, sink Sink) {
	lockPath := lockFilePath(storeDir)
	for ctx.Err() == nil {
		if lock, ok := tryAcquireLock(lockPath); ok {
			runAsOwner(ctx, storeDir, lock, sess, sink)
		} else {
			runAsClient(ctx, storeDir, sess, sink)
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-time.After(reconnectBackoff):
		case <-ctx.Done():
			return
		}
	}
}
