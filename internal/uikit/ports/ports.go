// Package ports defines the consumer-side interfaces between the UI and
// the harness. The adapter lives in internal/uikit/session (a later
// phase); nothing here depends on it, and nothing here depends on
// mivia-agent's internal/chat, internal/events, or mivia-ai-sdk.
//
// The event/turn source is split across two systems, neither finished
// today: mivia-ai-sdk owns the model-completion loop (its agentloop
// package is plan-only as of this writing — no discriminated event
// stream, no turn/seq envelope, a single-in-flight approval gate) and the
// mivia-agent CLI harness owns workflow/subagent orchestration, plan
// state, diffs, and context-usage accounting. These interfaces exist so
// the session adapter can merge both into one uievent.Event stream
// without either source's shape leaking into the UI layer.
//
// internal/uikit/** must not import bubbletea or lipgloss.
package ports

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// ToolCall represents a tool call and its output in conversation history.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
	Output    string
}

// Message is one turn of conversation history.
type Message struct {
	Role      string // "user" | "assistant"
	Text      string
	Reasoning string
	At        time.Time
	Diffs     []uievent.Diff
	ToolCalls []ToolCall
}

// ModelInfo names the model and provider currently bound to a session.
// ContextWindow is the session's USABLE PROMPT BUDGET in tokens - the
// model's context window minus its output reserve - because that is what
// history is actually measured and compacted against. A percentage taken
// against the raw window instead reads far below the real fill level and
// can never reach 100%. 0 means unknown, and callers must show no context
// percentage rather than divide by it.
type ModelInfo struct {
	Name          string
	Provider      string
	ContextWindow int64
}

// Usage is cumulative token and cost accounting for a session.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	CostUSD      float64
}

// Conversation is the read/write surface a UI drives. It never calls the
// agent directly; it sends intents and reads state back.
type Conversation interface {
	Send(ctx context.Context, in intent.Send) (TurnHandle, error)
	ActiveTurn() (TurnHandle, bool)
	History() []Message
	Model() ModelInfo
	ContextUsage() Usage
	Title() string
	ID() string
}

// TurnHandle represents one in-flight or completed turn. Events() is the
// shape that matters: directly selectable from a tea.Cmd, so no goroutine
// needs to touch the model and no bridge-and-ticker is needed to fake it.
type TurnHandle interface {
	ID() string
	Events() <-chan uievent.Event // closed on turn end
	Cancel()
	// CancelToolCall cancels ONE in-flight tool call by its call ID,
	// leaving the rest of the turn (and any concurrent sibling tool
	// calls) running. It returns whether a matching in-flight call
	// was found; a miss (already finished, wrong ID, or nothing
	// in flight) is a no-op that returns false.
	CancelToolCall(callID string) bool
}

// SubagentThreads resolves the conversation thread of one dispatched
// subagent, keyed by the tool call that dispatched it. A thread is an
// ordinary Conversation - the same Send/History surface the main chat
// drives - so the UI renders it with the same screen, not a parallel
// one. ok is false when no thread exists for the call; the caller then
// falls back to whatever summary it has.
type SubagentThreads interface {
	Thread(callID string) (Conversation, bool)
}

// Decision is the user's answer to an ApprovalRequest.
type Decision int

const (
	DecisionOnce Decision = iota
	DecisionAlways
	DecisionDeny
	DecisionDenyAlways
)

// ApprovalRequest is a tool call awaiting a user decision.
type ApprovalRequest struct {
	ID       string
	ToolName string
	TurnID   string
	Args     map[string]any
}

// Approver is the approval-prompt surface. Pending delivers one request at
// a time; Resolve answers it by ID.
type Approver interface {
	Pending() <-chan ApprovalRequest
	Resolve(id string, decision Decision)
}

// Notices is the out-of-band advisory stream: events that belong to no turn.
//
// TurnHandle.Events covers everything a turn produces, and closes when the
// turn ends, so it cannot carry anything the harness learns while nothing is
// running - a background uploader stopping, for instance. Notices is the
// channel for exactly that, and it stays open for the life of the adapter.
//
// The stream is best-effort and lossy by contract: a producer that would
// block drops the notice rather than stall the work that raised it. Callers
// therefore treat a notice as advisory, never as state to reconcile against.
type Notices interface {
	// Notices returns the advisory stream. It is never closed while the
	// adapter lives, so a reader may block on it indefinitely. A nil
	// return means this adapter raises no notices.
	Notices() <-chan uievent.Event
}

// RemoteInputEvent is one instruction a second device queued for a session
// through the sync API, already verified by the time it reaches here:
// chatsync.InputPoller has checked session ownership, message shape, and
// author identity before this ever crosses the port (internal/chatsync's
// validateRemoteInput). Nothing on this side re-checks any of that - the
// port's whole contract is that a value on the channel is safe to run.
type RemoteInputEvent struct {
	ID         string
	Kind       string // "message" injects Body as a turn; "cancel" stops the targeted session's active turn (Body empty)
	SessionID  string
	Body       string
	ReceivedAt time.Time
}

// RemoteInputs is the inbound steering surface: sibling to Notices, same
// contract (read once at startup, never closed while the adapter lives, a
// nil return means this adapter raises none). Unlike Notices it is not
// DROPPED under backpressure - see internal/uiadapter's
// SessionPool.RemoteInputs implementation - a slow or absent reader stalls
// the producer instead of an event being silently discarded. That is
// narrower than "never lost": SessionPool.RemoteInputs' own doc comment
// spells out the residual crash-window risk this buffering does not close.
//
// RemoteInputEvent.SessionID may name ANY pooled session, not only the one
// currently on screen: the consumer (internal/ui/screen/conversation)
// resolves it against its own foreground/background session state exactly
// as uievent.EventMsg.SessionID already does for turn events.
type RemoteInputs interface {
	RemoteInputs() <-chan RemoteInputEvent
}

// SessionSummary describes one existing session for listing and resuming.
type SessionSummary struct {
	ID        string
	Title     string
	UpdatedAt time.Time
	Active    bool
	State     string   // e.g. "idle", "thinking", "running", "streaming", "done"
	Lines     []string // recent transcript or activity lines for preview

	// IsCurrent marks the session the picker was opened from. It is
	// independent of Active/State: the current session is not
	// necessarily the one with a turn running.
	IsCurrent bool

	// Turns is the number of completed conversation turns in the
	// session, and ContextTokens is its context usage in raw tokens
	// (rendered as "Nk ctx", the same convention ModelView's context
	// window already uses). Both are zero-value-safe: a session whose
	// adapter cannot report them renders with no turns/ctx column
	// rather than a fabricated number - see sessionPicker.View's own
	// zero check (wireframes-panes.md section 12.2).
	Turns         int
	ContextTokens int
}

// SessionMeta describes a saved session for listing/loading.
type SessionMeta struct {
	Name      string
	UpdatedAt time.Time
}

// SessionStore is the session list/load/save surface.
type SessionStore interface {
	List() ([]SessionMeta, error)
	Load(name string) error
	Save(name string) error
}
