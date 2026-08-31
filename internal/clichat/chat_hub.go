package clichat

// Wires internal/hub into this process's live chat surfaces: publishing
// this process's own turns onto the session's EventBus (hub.Join then
// forwards whatever's published there to every other connected process) and
// rendering events RECEIVED from other processes onto whichever surface is
// running. Line-mode --json is the only surface with a rendering sink today
// (chatHubSink).
//
// The classic REPL joins and publishes, so a desktop app observing it sees its
// turns. THE TUI DOES NOT. It never calls JoinHub, and uiadapter/build.go
// constructs its session with EventBus: nil, so every publish on that surface
// returns immediately and nothing is relayed. That is the real gap: not a
// missing publish, but a missing bus. This comment previously claimed the TUI
// joined and published, and cited a tui_start.go that does not exist and never
// has - a claim that survived long enough to hide six event kinds having no
// producer at all.

import (
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/hub"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// attachSessionEventBus gives a session its event bus, for every surface.
//
// JoinHub used to be the only thing that created one, and only the classic
// REPL and line mode call it - so the TUI, the surface most people run, had no
// bus at all. Every publish on it returned immediately, which is why turn
// lifecycle and subagent events reached no consumer there however correct the
// producers were. The gap was never a missing publish; it was a missing bus.
//
// JoinHub reuses whatever this installed rather than replacing it, so a
// subscriber registered before the join is not orphaned.
func attachSessionEventBus(sess *chat.Session) {
	if sess == nil || sess.EventBus != nil {
		return
	}
	sess.EventBus = events.New()
}

// JoinHub is called by the classic REPL and by line mode. It REUSES the
// session's bus, which runConfiguredChatOnce now creates for every surface -
// it only mints one as a fallback for a session built some other way. The TUI
// still does not join, so its events stay in-process; that is a rendering gap,
// no longer a producer one. storeDir is derived from the session's
// already-open context store rather than recomputed from workspace-routing
// logic, so it's automatically correct for the repository-root/managed-
// worktree/config-override cases setupChatSessionContext itself resolves -
// hub.lock/hub.sock end up right beside context.db. Returns nil (a
// no-op Handle) if the session has no SQLite-backed context store to key
// off, which should not happen for a live chat session but is not this
// package's invariant to enforce.
func JoinHub(sess *chat.Session, sink hub.Sink) *hub.Handle {
	if sess.EventBus == nil {
		sess.EventBus = events.New()
	}
	store, ok := sess.ContextStore().(*storage.SQLite)
	if !ok || store == nil {
		return nil
	}
	return hub.Join(filepath.Dir(store.Path()), sess, sink)
}

// startClassicReplHub/startLineModeHub join the hub for their surface and
// return the cleanup to defer - a one-line call site keeps chat_repl.go
// (which has its own LOC budget) from re-spelling joinHub's two-line
// call+defer pattern at each of its two surfaces.
func startClassicReplHub(sess *chat.Session) func() {
	return JoinHub(sess, nil).Leave
}

func startLineModeHub(sess *chat.Session, jsonMode bool) func() {
	return JoinHub(sess, chatHubSink(sess, jsonMode)).Leave
}

// maxTrackedExternalRuns bounds how many external runs this sink remembers.
//
// The previous state kept two unbounded sets and pruned them when a run's
// terminal arrived - but terminals were not relayed, so nothing was ever
// pruned and both sets grew for the life of the process. Relaying terminals
// (which the hub now does) does not fix that either: with drop-oldest queues on
// the path, a terminal is exactly the event a busy turn is most likely to lose,
// so a prune-on-terminal design leaks whenever the relay sheds load.
//
// A fixed cap with oldest-first eviction leaks nothing regardless of what
// arrives. The cost is that an evicted run can be re-minted if it is still live
// after 64 later runs started, which is far outside anything a shared workspace
// produces.
const maxTrackedExternalRuns = 64

// externalRun is one external turn this sink is relaying.
type externalRun struct {
	// userText is the submitting user's own text, carried by KindTurnStart's
	// Detail, held until the external_turn_start line is written.
	userText string
	// started records that external_turn_start was written for this run, so no
	// later event - including a duplicate or a reordered one - can mint a
	// second turn for the same run_id.
	started bool
	// streamed records that at least one incremental chunk
	// (events.Event.Detail=="delta", see internal/agent/loop.go's teeWriter)
	// was relayed, so the turn-final aggregate is suppressed for this run and
	// kept only as the fallback for a run that never streamed at all
	// (FinalWriter unset, a non-interactive caller).
	streamed bool
	// done records that a terminal was relayed. The entry deliberately OUTLIVES
	// the turn: it is what makes a late or duplicated event unable to re-open a
	// finished run.
	done bool
}

// externalTurnState tracks the external turns a hub sink is currently
// relaying, keyed by run id. It is a map, not a single scalar, because the
// hub's workspace (where hub.lock/hub.sock live) can be shared by several
// UNRELATED sessions at once - e.g. mivia-agent-desktop's sibling threads,
// which default to one shared per-app workspace unless a project directory is
// picked - so more than one external run can legitimately be in flight here.
//
// Every queue between the publishing process and this sink is bounded
// drop-oldest, so this receiver must tolerate LOSS as well as order: an event
// may be the first one it ever sees for a run whose predecessors were dropped.
// That is why a terminal for an unknown run is discarded rather than minting a
// turn to immediately close (see renderExternalEvent).
type externalTurnState struct {
	mu   sync.Mutex
	runs map[string]*externalRun
	// order is run ids in first-seen order, so eviction is oldest-first. It is
	// append-only alongside runs and compacted at eviction time.
	order []string
	// dropped is the last cumulative loss count reported by the hub, so a
	// report is emitted per advance rather than per event.
	dropped uint64
	// pendingUserText bridges a KindTurnStart that carries NO run id to the
	// next unseen run's first event. Every current publisher stamps the real
	// turn id on KindTurnStart (chat.Session.publishTurnStart), so this is the
	// compatibility path for an older peer relaying into a newer one - not the
	// normal route.
	pendingUserText string
}

func newExternalTurnState() *externalTurnState {
	return &externalTurnState{runs: make(map[string]*externalRun)}
}

// run returns the tracked run for id, creating and bounding it if new.
// Called with state.mu held.
func (s *externalTurnState) run(id string) *externalRun {
	if r, ok := s.runs[id]; ok {
		s.touch(id)
		return r
	}
	r := &externalRun{}
	s.runs[id] = r
	s.order = append(s.order, id)
	for len(s.order) > maxTrackedExternalRuns {
		delete(s.runs, s.order[0])
		s.order = s.order[1:]
	}
	return r
}

// touch moves id to the newest end of the eviction order. Without it the order
// is first-SEEN rather than least-recently-used, so a turn that streams for
// minutes ages out while it is still live - and an evicted live run is re-minted
// as a SECOND external_turn_start with empty text, or has its terminal dropped
// by the unknown-run guard. Both are the duplicated-turn defect the run map
// exists to prevent. Called with s.mu held.
func (s *externalTurnState) touch(id string) {
	for i, cur := range s.order {
		if cur == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			s.order = append(s.order, id)
			return
		}
	}
}

// known reports whether id has been seen before, without creating it.
// Called with state.mu held.
func (s *externalTurnState) known(id string) bool {
	_, ok := s.runs[id]
	return ok
}

// chatHubSink returns a hub.Sink that renders another process's events for
// THIS session as the same external_* NDJSON vocabulary line-mode --json
// already documents (chat_json_writer.go), or nil when jsonMode is false -
// a plain-text line-mode or one-shot invocation has no framing to carry
// this on, so there's nothing useful to render.
//
// The hub relays every session sharing its workspace, not just this one
// (see externalTurnState's doc comment) - filtering here on
// ev.SessionID == sess.SessionID is what makes "another session on the
// same shared workspace" a silent no-op instead of a different
// conversation's turns bleeding into this one's transcript. An external
// participant that hasn't set an EventBus SessionID at all (a defensive
// case only - every wired publish site sets one) is also dropped rather
// than matched by two empty strings.
func chatHubSink(sess *chat.Session, jsonMode bool) hub.Sink {
	if !jsonMode {
		return nil
	}
	return newChatHubSink(sess, os.Stdout)
}

// newChatHubSink is chatHubSink with the destination injected, so a test can
// exercise the REAL sink instead of re-spelling its body. The previous test
// helper was a hand-copied double, which meant deleting the loss report from
// the shipped sink left the whole suite green.
func newChatHubSink(sess *chat.Session, w io.Writer) hub.Sink {
	state := newExternalTurnState()
	return func(ev events.Event, r hub.Receipt) {
		if !externalEventBelongsToSession(sess, ev) {
			return
		}
		reportExternalLoss(w, state, r)
		renderExternalEvent(w, state, ev)
	}
}

// reportExternalLoss emits one "external_dropped" line each time the hub's
// cumulative loss counter advances.
//
// The relay is deliberately lossy - every queue on the path is bounded
// drop-oldest - and until now that loss was completely silent at this end. A
// consumer reading external_chunk text had no way to tell a short answer from
// a truncated one. It reports the DELTA as well as the total, because the
// delta is what the consumer missed since the last line and the total is what
// makes two reports comparable.
//
// The counter only ever advances, so this emits per jump rather than per
// event: a quiet stream produces nothing at all.
func reportExternalLoss(w io.Writer, state *externalTurnState, r hub.Receipt) {
	// The write stays UNDER the lock. The hub calls this sink from one
	// goroutine per connected client (hub.owner.accept), so releasing the lock
	// before writing lets a receipt of 9 claim the counter, get overtaken, and
	// print after a receipt of 5 - a consumer diffing total_dropped then
	// computes a negative delta, and loss lines float away from the events they
	// describe. renderExternalEvent already writes under the same lock.
	state.mu.Lock()
	defer state.mu.Unlock()
	last := state.dropped
	if r.Dropped <= last {
		return
	}
	state.dropped = r.Dropped
	writeNDJSONEvent(w, ndjsonEvent{
		Type: "external_dropped", Dropped: r.Dropped - last, TotalDropped: r.Dropped,
	})
}

// externalEventBelongsToSession reports whether ev, received from the hub,
// is this process's own session's activity rather than an unrelated
// session sharing the same hub workspace (see externalTurnState's doc
// comment) - the regression this guards is a relayed event rendering into
// the wrong session/thread's transcript. An event with no SessionID at all
// (a defensive case only - every wired publish site sets one; turn lifecycle
// events come from chat.Session, internal/chat/turn_events.go) is rejected
// rather than matched by two empty strings.
func externalEventBelongsToSession(sess *chat.Session, ev events.Event) bool {
	return ev.SessionID != "" && ev.SessionID == sess.SessionID
}

func renderExternalEvent(w io.Writer, state *externalTurnState, ev events.Event) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if ev.Kind == events.KindTurnStart {
		if ev.TurnID == "" {
			// Older peer: no run id to key on, so bridge the text to the next
			// unseen run's first event.
			state.pendingUserText = ev.Detail
			return
		}
		r := state.run(ev.TurnID)
		if r.started || r.done {
			// A duplicate or reordered start for a run already under way. Minting
			// again would split one external turn into two in the consumer's
			// transcript.
			return
		}
		r.userText = ev.Detail
		startExternalTurn(w, state, ev.TurnID, ev.SessionID, r)
		return
	}
	// Compaction is NOT a turn: it must not mint an "external_turn_start"
	// (no user text belongs to it) nor fall under the run-minting logic
	// below, or a post-turn compact would fabricate a phantom external turn.
	if ev.Kind == events.KindCompaction {
		renderExternalCompaction(w, ev)
		return
	}
	if ev.TurnID == "" {
		return
	}
	terminal := ev.Kind == events.KindTurnEnd || ev.Kind == events.KindError
	if terminal && !state.known(ev.TurnID) {
		// A terminal is the FIRST thing this sink has seen for this run: every
		// hop on the relay path is bounded drop-oldest, so its predecessors
		// were shed. Minting a turn here just to close it fabricates an empty
		// turn in the consumer's transcript. The consumer has no turn open, so
		// there is nothing to close - drop it.
		return
	}
	r := state.run(ev.TurnID)
	if !r.started {
		if r.userText == "" {
			r.userText = state.pendingUserText
			state.pendingUserText = ""
		}
		startExternalTurn(w, state, ev.TurnID, ev.SessionID, r)
	}
	renderExternalTurnEvent(w, r, ev)
	if terminal {
		// Marked, never deleted: the entry is what stops a late or duplicated
		// event from re-opening a finished run. Eviction is by age instead
		// (maxTrackedExternalRuns).
		r.done = true
	}
}

// startExternalTurn writes the external_turn_start line for a run and marks it
// started. Called with state.mu held.
func startExternalTurn(w io.Writer, state *externalTurnState, runID, sessionID string, r *externalRun) {
	r.started = true
	writeNDJSONEvent(w, ndjsonEvent{
		Type: "external_turn_start", RunID: runID, SessionID: sessionID,
		Role: "user", Text: r.userText,
	})
	r.userText = ""
	state.pendingUserText = ""
}

// renderExternalCompaction relays a compaction another process committed on
// this session as its own typed NDJSON event. An older publisher that sent
// only the Detail string (no typed payload) still announces the compaction -
// a consumer's context indicator refresh is better served by a payload-less
// notice than by silence. Called with state.mu held.
func renderExternalCompaction(w io.Writer, ev events.Event) {
	line := ndjsonEvent{Type: "external_compaction", RunID: ev.TurnID, Message: ev.Detail}
	if ev.Compaction != nil {
		line.Compaction = compactionPayload(*ev.Compaction)
	}
	writeNDJSONEvent(w, line)
}

// renderExternalTurnEvent's KindAssistant case only relays a "delta" chunk// (streamed live - see teeWriter) as it arrives, and only falls back to
// relaying the turn-end aggregate (Detail!="delta") when this run never
// streamed one at all - a run that already got live deltas would otherwise
// see the same content twice, once incrementally and once again in full.
func renderExternalTurnEvent(w io.Writer, r *externalRun, ev events.Event) {
	switch ev.Kind {
	case events.KindAssistant:
		if ev.Content == "" {
			break
		}
		if ev.Detail == "delta" {
			r.streamed = true
			writeNDJSONEvent(w, ndjsonEvent{Type: "external_chunk", RunID: ev.TurnID, Text: ev.Content})
			break
		}
		if !r.streamed {
			writeNDJSONEvent(w, ndjsonEvent{Type: "external_chunk", RunID: ev.TurnID, Text: ev.Content})
		}
	case events.KindThinking:
		if ev.Content != "" {
			writeNDJSONEvent(w, ndjsonEvent{Type: "external_thinking", RunID: ev.TurnID, Text: ev.Content})
		}
	case events.KindToolStart, events.KindSubagentStart:
		writeNDJSONEvent(w, ndjsonEvent{
			Type: "external_tool_start", RunID: ev.TurnID, ToolCallID: ev.ToolCallID,
			Name: ev.Name, Input: ev.Input,
		})
	case events.KindToolEnd, events.KindSubagentEnd:
		writeNDJSONEvent(w, ndjsonEvent{
			Type: "external_tool_end", RunID: ev.TurnID, ToolCallID: ev.ToolCallID,
			Name: ev.Name, Output: ev.Output, Status: toolEndStatus(ev.Detail),
		})
	// KindSubagentDone is deliberately NOT a case here: it retires one
	// subagent inside the turn, not the turn itself. Mapping it to
	// "external_done" (as this once did) made a consumer mark the whole
	// external turn finished - and drop it from any live-agents view -
	// the moment the run's first subagent completed, mid-turn.
	case events.KindTurnEnd:
		writeNDJSONEvent(w, ndjsonEvent{Type: "external_done", RunID: ev.TurnID})
	case events.KindError:
		writeNDJSONEvent(w, ndjsonEvent{Type: "external_error", RunID: ev.TurnID, Message: errorEventMessage(ev)})
	}
}

// errorEventMessage renders a relayed error. ev.Err here is ALWAYS the
// classified string: this is only reached from renderExternalEvent, whose sole
// production feeds are hub's two readLoops, and fromWire rebuilds Err from
// WireEvent.ErrorText, which toWire produced via chat.TurnErrorMessage.
//
// It is therefore the same shape the semgrep rule bans inside internal/hub, on
// the same data path, left in place deliberately rather than swept: classifying
// twice would collapse "chat turn failed: could not persist session state" to
// the generic message, because fromWire's errors.New does not match the
// sentinels errors.Is looks for. The rule cannot protect it - a blanket
// .Error() ban is not viable in this package - so the guard is this comment
// plus the wiring. If renderExternalEvent is ever subscribed to the LOCAL bus,
// an unclassified provider error reaches stdout here and this stops being safe.
func errorEventMessage(ev events.Event) string {
	if ev.Err != nil {
		return ev.Err.Error()
	}
	return ev.Detail
}
