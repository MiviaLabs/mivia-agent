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

// JoinHub is called by the classic REPL and by line mode, each once its own
// EventBus is finalized - it is also what CREATES that bus (sess.EventBus =
// events.New() below). The TUI does not call it and therefore has no bus at
// all. storeDir is derived from the session's
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

// externalTurnState tracks the external turns a hub sink is currently
// relaying. seenRunIDs is a set, not a single scalar: the hub's workspace
// (where hub.lock/hub.sock live) can be shared by several UNRELATED
// sessions at once - e.g. mivia-agent-desktop's own sibling threads, which
// default to one shared per-app workspace unless a project directory is
// picked - so more than one external run can legitimately be in flight
// through this sink at a time, keyed by its own run_id. pendingUserText is
// still a single scalar: it only bridges a KindTurnStart to the very next
// unseen run_id's first event, which - because a session's own turns are
// already single-flight - only needs to survive that narrow window, even
// when multiple OTHER sessions are relaying through the same hub
// concurrently (their own KindTurnStart/first-event pairs are filtered out
// entirely by the SessionID check below before reaching this state at
// all).
type externalTurnState struct {
	mu              sync.Mutex
	pendingUserText string
	seenRunIDs      map[string]struct{}
	// deltaSeenRunIDs tracks which runs have already relayed at least one
	// incremental content chunk (events.Event.Detail=="delta" - see
	// internal/agent/loop.go's teeWriter) - see renderExternalTurnEvent's
	// KindAssistant case for why the final aggregate event is skipped for
	// a run that already streamed live, and kept as a fallback for one
	// that never streamed at all (FinalWriter unset, a non-interactive
	// caller).
	deltaSeenRunIDs map[string]struct{}
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
	state := &externalTurnState{
		seenRunIDs:      make(map[string]struct{}),
		deltaSeenRunIDs: make(map[string]struct{}),
	}
	return func(ev events.Event) {
		if !externalEventBelongsToSession(sess, ev) {
			return
		}
		renderExternalEvent(os.Stdout, state, ev)
	}
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
		state.pendingUserText = ev.Detail
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
	if _, seen := state.seenRunIDs[ev.TurnID]; !seen {
		state.seenRunIDs[ev.TurnID] = struct{}{}
		writeNDJSONEvent(w, ndjsonEvent{
			Type: "external_turn_start", RunID: ev.TurnID, SessionID: ev.SessionID,
			Role: "user", Text: state.pendingUserText,
		})
		state.pendingUserText = ""
	}
	renderExternalTurnEvent(w, state, ev)
	if ev.Kind == events.KindTurnEnd || ev.Kind == events.KindError {
		delete(state.seenRunIDs, ev.TurnID)
		delete(state.deltaSeenRunIDs, ev.TurnID)
	}
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
func renderExternalTurnEvent(w io.Writer, state *externalTurnState, ev events.Event) {
	switch ev.Kind {
	case events.KindAssistant:
		if ev.Content == "" {
			break
		}
		if ev.Detail == "delta" {
			state.deltaSeenRunIDs[ev.TurnID] = struct{}{}
			writeNDJSONEvent(w, ndjsonEvent{Type: "external_chunk", RunID: ev.TurnID, Text: ev.Content})
			break
		}
		if _, streamed := state.deltaSeenRunIDs[ev.TurnID]; !streamed {
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

func errorEventMessage(ev events.Event) string {
	if ev.Err != nil {
		return ev.Err.Error()
	}
	return ev.Detail
}
