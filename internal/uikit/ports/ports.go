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
//
// DeclaredWindow is the model's own advertised window, reported alongside so a
// surface can say WHY the budget is what it is. The budget can sit far below
// the window for a reason the reader owns - an operator prompt cap in config -
// and a gauge that shows only the budget makes a 1M-window model look like it
// lost most of its capacity. It is display-only: nothing is measured against
// it, and 0 means undeclared.
type ModelInfo struct {
	Name           string
	Provider       string
	ContextWindow  int64
	DeclaredWindow int64
}

// BudgetIsCapped reports whether the usable prompt budget sits below the
// model's declared window by more than its output reserve could explain.
// Surfaces use it to say the budget is a choice rather than the model's limit.
//
// The comparison is deliberately loose: the exact reserve is not carried here,
// and the point is to distinguish "your config caps this" from "this is the
// model", not to audit the arithmetic. A window that is merely reduced by an
// output reserve stays under the threshold and reports false.
func (m ModelInfo) BudgetIsCapped() bool {
	if m.DeclaredWindow <= 0 || m.ContextWindow <= 0 {
		return false
	}
	return m.ContextWindow*2 < m.DeclaredWindow
}

// Usage is cumulative token and cost accounting for a session.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	CostUSD      float64
	// Breakdown splits InputTokens into its parts. Its token fields sum to
	// InputTokens exactly, so a surface can draw the parts beside the total
	// without the two disagreeing. Zero when the adapter has no breakdown.
	Breakdown ContextBreakdown
}

// ContextBreakdown is Usage.InputTokens split into the parts a reader can act
// on: what compaction can reclaim, and what it cannot.
//
// System, ToolSchemas, ExternalSchemas, Memory and Summary are the floor,
// present on every turn whatever was said. Prose, ToolResults, Reasoning and
// Pending are the conversation, which compaction eats. The distinction is the
// actionable one: a session that opens already a third full is carrying an
// expensive floor, and no amount of compacting will move it.
type ContextBreakdown struct {
	System int64
	// ToolSchemas is the cost of the compiled-in tools; ExternalSchemas is the
	// cost of the ones a server supplied. They are separate because only the
	// second is removable by turning something off, which is the whole point
	// of reporting a schema cost at all.
	ToolSchemas     int64
	ExternalSchemas int64
	// ToolCount and ExternalToolCount are numbers of registered schemas, not
	// token costs. They are what make the schema charge actionable, because
	// the unit anyone can remove is a tool or a server, never a token.
	ToolCount         int
	ExternalToolCount int
	Memory            int64
	Summary           int64
	// Skills is what invoked skills are costing, and SkillCount how many
	// invocations are carrying it. A skill's instruction body arrives as a
	// framed user message, so it is conversation compaction can reclaim -
	// but it is not ordinary prose, and a reader looking at a full window
	// needs to see that one skill is the reason.
	Skills      int64
	SkillCount  int
	Prose       int64
	ToolResults int64
	Reasoning   int64
	// Pending is live prompt cost the composition does not yet explain. The
	// session adopts a turn's messages only when the turn finishes, so while
	// one is running its composition describes the PREVIOUS turn, and on the
	// first turn it is the floor alone. Everything the provider prices beyond
	// that is this turn's conversation, and it is held here rather than spread
	// over the other buckets: spreading it grew the system prompt and the tool
	// schemas on screen, the two things that cannot grow, and left the
	// conversation rows reading zero for a whole turn.
	Pending int64
}

// Floor is the part of the estimate compaction cannot reclaim.
func (b ContextBreakdown) Floor() int64 {
	return b.System + b.ToolSchemas + b.ExternalSchemas + b.Memory + b.Summary
}

// Conversation is the part compaction reclaims, including the pending cost of
// a turn in flight: unadopted history is still history.
func (b ContextBreakdown) Conversation() int64 {
	return b.Skills + b.Prose + b.ToolResults + b.Reasoning + b.Pending
}

// Total is Floor plus Conversation.
func (b ContextBreakdown) Total() int64 { return b.Floor() + b.Conversation() }

// WithLiveTotal reconciles this composition with a total the provider
// reported, which is authoritative for how full the window is and carries no
// composition of its own.
//
// The floor is never rescaled. The system prompt, the tool schemas and the
// carried memory are fixed for the session and known exactly, so stretching
// them to absorb a growing total states something false about the two
// quantities a reader most needs to trust. What the composition cannot
// account for goes to Pending, which is what it actually is: the turn the
// session has not adopted yet.
//
// A total BELOW what is already priced means compaction just dropped history.
// There the conversation is scaled down to fit and the floor is still kept
// whole, because compaction removes conversation and never the floor. Only a
// total below even the floor rescales everything, and that is a degenerate
// reading rather than a state the session can be in.
func (b ContextBreakdown) WithLiveTotal(total int64) ContextBreakdown {
	if total <= 0 {
		return b.countsOnly()
	}
	out := b
	out.Pending = 0
	if base := out.Total(); total >= base {
		out.Pending = total - base
		return out
	}
	if floor := out.Floor(); total > floor {
		scaleFields(out.conversationBuckets(), total-floor)
		return out
	}
	scaleFields(out.buckets(), total)
	return out
}

// scaleFields scales the pointed-to values so they sum to exactly target,
// handing the rounding drift to the largest so the parts always add up to the
// number displayed beside them. With nothing to scale, the whole target lands
// on the last field, which callers order to be the one that can honestly
// carry an unattributed amount.
func scaleFields(fields []*int64, target int64) {
	if len(fields) == 0 || target < 0 {
		return
	}
	var sum int64
	for _, f := range fields {
		sum += *f
	}
	if sum <= 0 {
		for _, f := range fields {
			*f = 0
		}
		*fields[len(fields)-1] = target
		return
	}
	var got int64
	for _, f := range fields {
		*f = *f * target / sum
		got += *f
	}
	if drift := target - got; drift != 0 {
		largest := fields[0]
		for _, f := range fields[1:] {
			if *f > *largest {
				largest = f
			}
		}
		*largest += drift
	}
}

// buckets lists every token field in a stable order.
func (b *ContextBreakdown) buckets() []*int64 {
	return []*int64{&b.System, &b.ToolSchemas, &b.ExternalSchemas, &b.Memory, &b.Summary, &b.Skills, &b.Prose, &b.ToolResults, &b.Reasoning, &b.Pending}
}

// conversationBuckets lists the reclaimable fields, Pending last so an empty
// conversation absorbs an unattributable remainder there rather than inventing
// a split across rows that would each be a guess.
func (b *ContextBreakdown) conversationBuckets() []*int64 {
	return []*int64{&b.Skills, &b.Prose, &b.ToolResults, &b.Reasoning, &b.Pending}
}

// countsOnly is an empty breakdown that keeps the schema counts, which are not
// token costs and so survive any rescaling of the costs.
func (b ContextBreakdown) countsOnly() ContextBreakdown {
	return ContextBreakdown{ToolCount: b.ToolCount, ExternalToolCount: b.ExternalToolCount, SkillCount: b.SkillCount}
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
	// Cancel ABORTS this turn. That is the ONE authoritative meaning of
	// this method: the work behind the turn stops, and Events() closes. A
	// caller that only wants to stop watching a turn has no method here -
	// dropping the handle is the way to do that.
	//
	// KNOWN DIVERGENCE. The subagent transcript handles internal/uiadapter
	// hands out (SubagentTranscriptConversation.ActiveTurn / .Send, whose
	// handles are subagentTurnHandle) implement Cancel as DETACH instead:
	// they remove this listener's channel and close it, and the underlying
	// coordinator task keeps running. Stopping such a task really needs
	// SubagentThreads.CancelSubagentTask below, which reaches the
	// coordinator; Cancel on those handles never does.
	//
	// The hazard: ui/screen/conversation's thread.go calls
	// s.thread.active.Cancel() in openThread and closeThread purely to
	// detach the dialog's listener, on a handle from whatever Conversation
	// SubagentThreads.Thread returned - and uiadapter's
	// registerReconstructed contemplates "any foreign ports.Conversation".
	// A foreign Conversation honouring THIS contract would have a real
	// turn aborted by those two sites just because a dialog opened or
	// closed. Register only detach-implementing conversations there.
	//
	// The clean fix is a separate Detach() here, with thread.go pointed at
	// it. Not done: this interface has more than ten implementations, most
	// of them test doubles, and each would have to grow the method.
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
	// CancelSubagentTask stops the coordinator's execution of the ONE
	// dispatched task backing callID, without touching its sibling tasks or
	// the parent turn/run.
	//
	// This is NOT TurnHandle.Cancel() / SubagentTranscriptConversation's own
	// ActiveTurn().Cancel(): those detach a UI listener from this thread's
	// live event stream - "stop watching" - and leave the underlying
	// coordinator task running untouched. CancelSubagentTask reaches past
	// the UI listener into the coordinator itself and stops the task's own
	// execution.
	//
	// ok is false when callID names no task with a live coordinator route
	// (never registered, already terminal and swept, or this dispatch path
	// never wired routing information at all) - a safe no-op, never a
	// panic. A non-nil error means a route was found but the cancel itself
	// failed (e.g. no coordinator wired, or the run is no longer active).
	CancelSubagentTask(callID string) (ok bool, err error)
	// CancelSubagentToolCall cancels ONE in-flight tool call WITHIN the
	// dispatched task backing callID, leaving that task itself, its
	// sibling tasks, and the parent turn/run untouched - the finest
	// cancel granularity, analogous to CancelSubagentTask (whole task).
	// callID identifies the subagent thread exactly as CancelSubagentTask's
	// own callID does; toolCallID identifies the specific call within it
	// (a transcript.Block's CallID).
	//
	// ok is false when there is nothing to cancel: callID names no task
	// with a live coordinator route, or toolCallID names no in-flight
	// call within it (already finished, wrong ID, or the task's own
	// nested loop never published a canceler - e.g. a legacy backend). A
	// non-nil error means a route was found but the cancel attempt
	// itself failed (no coordinator wired, or the run is no longer
	// active) - the same split CancelSubagentTask uses.
	CancelSubagentToolCall(callID, toolCallID string) (ok bool, err error)
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
