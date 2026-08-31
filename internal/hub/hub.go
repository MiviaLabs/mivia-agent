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

	// KindTurnEnd and KindError were withheld until three separate things were
	// true, because each of them alone made relaying a terminal worse than not
	// relaying it. All three now hold:
	//
	//  1. Order. The relay subscribes with SubscribeAcross, so every relayed
	//     kind shares one queue and one delivery goroutine, and the socket path
	//     preserves order from there (TestRelayPreservesCrossKindPublishOrder).
	//     Under SubscribeMany each kind had its own queue and a terminal could
	//     overtake the deltas of the turn it closes.
	//  2. Loss. Every queue on this path is bounded drop-oldest, so a terminal
	//     can arrive with none of its predecessors. The receiver now drops a
	//     terminal for a run it has never seen rather than minting a turn in
	//     order to close it, and marks a finished run done instead of deleting
	//     it, so a straggler cannot re-open it (internal/clichat, see
	//     TestExternalTerminalForAnUnseenRunIsDropped).
	//  3. Privacy. toWire classifies through chat.TurnErrorMessage, so
	//     publishTurnEnd's Err never reaches the wire verbatim - a second
	//     process is told exactly what the local NDJSON surface is told, and no
	//     more (TestToWireNeverSerializesRawErrorText).
	//
	// Withholding them was itself a defect, not a safe default: a second
	// surface saw turns that started, streamed, and then simply stopped, with
	// no way to tell a finished turn from a stalled one.
	events.KindTurnEnd,
	events.KindError,
}

// relayBufSize is the relay subscription's queue capacity. It is set
// explicitly because the relay is ONE subscription spanning every relayed kind
// (SubscribeAcross), so all of them now share one budget where per-kind
// subscriptions each had a private default. Assistant deltas are published per
// write and dominate that budget, so the default 256 would make the bus - not
// the socket - the first place a busy turn sheds events. It sits above
// connBufSize deliberately: the connection's own drop-oldest queue is the
// intended backpressure point, since a drop there is at least per-connection
// rather than shared by every client.
const relayBufSize = 4 * connBufSize

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
