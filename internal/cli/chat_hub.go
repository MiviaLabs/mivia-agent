package cli

// Wires internal/hub into this process's live chat surfaces: publishing
// this process's own turns onto the session's EventBus (hub.Join then
// forwards whatever's published there to every other connected process) and
// rendering events RECEIVED from other processes onto whichever surface is
// running. Line-mode --json is the only surface with a rendering sink today
// (chatHubSink) - the TUI and classic REPL still join and publish (so a
// desktop app observing them sees their turns), but do not yet render an
// external turn into their own display; see chat_repl.go/tui_start.go call
// sites for where that would extend.

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/hub"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// joinHub is the one call every live chat surface (TUI, classic REPL,
// line-mode) makes once its own EventBus (if any - the TUI manages its own,
// see tui_run.go) is finalized. storeDir is derived from the session's
// already-open context store rather than recomputed from workspace-routing
// logic, so it's automatically correct for the repository-root/managed-
// worktree/config-override cases setupChatSessionContext itself resolves -
// hub.lock/hub.sock end up right beside context.db. Returns nil (a
// no-op Handle) if the session has no SQLite-backed context store to key
// off, which should not happen for a live chat session but is not this
// package's invariant to enforce.
func joinHub(sess *chat.Session, sink hub.Sink) *hub.Handle {
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
	return joinHub(sess, nil).Leave
}

func startLineModeHub(sess *chat.Session, jsonMode bool) func() {
	return joinHub(sess, chatHubSink(jsonMode)).Leave
}

// publishTurnStartForHub tells this process's hub participants that a new
// turn is starting, carrying the user's own submitted text so an external
// live surface can show what was typed, not just the reply that follows.
// See internal/hub's relayedKinds doc comment for why TurnID here need not
// match the "turn:%d" id later events carry.
func publishTurnStartForHub(sess *chat.Session, text string) {
	if sess == nil || sess.EventBus == nil {
		return
	}
	sess.EventBus.Publish(events.Event{
		Kind: events.KindTurnStart, Timestamp: time.Now(),
		SessionID: sess.SessionID, Detail: text,
	})
}

// externalTurnState tracks, per session, the one external turn a hub sink is
// currently relaying - at most one, matching the single-turn-in-flight
// invariant a session already enforces. pendingUserText holds a KindTurnStart
// Detail that hasn't yet been matched to a following turn's first event;
// activeTurnID (mivia's own internal "turn:%d" id, once seen) is how every
// subsequent event for that same turn is recognized as a continuation rather
// than a second new turn.
type externalTurnState struct {
	mu              sync.Mutex
	pendingUserText string
	activeTurnID    string
}

// chatHubSink returns a hub.Sink that renders another process's events as
// the same external_* NDJSON vocabulary line-mode --json already documents
// (chat_json_writer.go), or nil when jsonMode is false - a plain-text
// line-mode or one-shot invocation has no framing to carry this on, so
// there's nothing useful to render.
func chatHubSink(jsonMode bool) hub.Sink {
	if !jsonMode {
		return nil
	}
	state := &externalTurnState{}
	return func(ev events.Event) {
		renderExternalEvent(os.Stdout, state, ev)
	}
}

func renderExternalEvent(w io.Writer, state *externalTurnState, ev events.Event) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if ev.Kind == events.KindTurnStart {
		state.pendingUserText = ev.Detail
		state.activeTurnID = ""
		return
	}
	if ev.TurnID == "" {
		return
	}
	if state.activeTurnID != ev.TurnID {
		state.activeTurnID = ev.TurnID
		writeNDJSONEvent(w, ndjsonEvent{
			Type: "external_turn_start", RunID: ev.TurnID, SessionID: ev.SessionID,
			Role: "user", Text: state.pendingUserText,
		})
		state.pendingUserText = ""
	}
	renderExternalTurnEvent(w, ev)
}

func renderExternalTurnEvent(w io.Writer, ev events.Event) {
	switch ev.Kind {
	case events.KindAssistant:
		if ev.Content != "" {
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
	case events.KindToolEnd:
		writeNDJSONEvent(w, ndjsonEvent{
			Type: "external_tool_end", RunID: ev.TurnID, ToolCallID: ev.ToolCallID,
			Name: ev.Name, Output: ev.Output, Status: toolEndStatus(ev.Detail),
		})
	case events.KindSubagentDone, events.KindTurnEnd:
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
