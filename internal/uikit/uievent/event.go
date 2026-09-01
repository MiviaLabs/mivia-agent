// Package uievent defines the canonical UI event stream: the contract
// between the harness and all three renderers (TUI, plain stream, JSON).
//
// internal/uikit/** must not import bubbletea or lipgloss.
package uievent

import "time"

// Kind identifies the shape of an Event's Body. Each Kind has exactly one
// Body implementation.
type Kind string

const (
	KindTurnStart   Kind = "turn.start"
	KindTextDelta   Kind = "text.delta"
	KindTextEnd     Kind = "text.end"
	KindReasoning   Kind = "reasoning.delta"
	KindToolPending Kind = "tool.pending" // needs approval
	KindToolStart   Kind = "tool.start"
	KindToolOutput  Kind = "tool.output" // incremental; also carries subagent progress
	// KindAssistantReset tells the transcript to discard the assistant text it
	// holds for the current turn, because the turn is being re-driven from the
	// beginning.
	KindAssistantReset Kind = "assistant.reset"
	KindToolEnd        Kind = "tool.end"
	KindPlan           Kind = "plan"   // to-do/plan checklist update
	KindNotice         Kind = "notice" // free-text advisory line, e.g. context-usage warning
	KindHook           Kind = "hook"   // a lifecycle hook fired for a tool call
	KindUsage          Kind = "usage"
	KindError          Kind = "error"
	KindTurnEnd        Kind = "turn.end"
)

// Event is one item in the UI event stream. TurnID and Seq fence late
// events from a cancelled turn and let renderers stay stateless with
// respect to cancellation: drop anything with Seq <= the last seen Seq
// for a TurnID that has already ended.
type Event struct {
	Kind   Kind
	TurnID string
	Seq    uint64
	At     time.Time
	Body   Body
}

// Body is sealed: only types in this package may implement it. This gives
// exhaustive handling in renderers (a type switch over Body covers every
// case the compiler can check) instead of a map[string]any.
type Body interface {
	isBody()
}

// TurnStartBody is the Body for KindTurnStart. Input is the user's
// message text that opened the turn.
type TurnStartBody struct {
	Input string `json:"input"`
}

func (TurnStartBody) isBody() {}

// TextDeltaBody is the Body for KindTextDelta: one incremental chunk of
// streamed assistant text.
type TextDeltaBody struct {
	Text string `json:"text"`
}

func (TextDeltaBody) isBody() {}

// TextEndBody is the Body for KindTextEnd: the full accumulated assistant
// text for this message, for a one-time markdown render.
type TextEndBody struct {
	Text string `json:"text"`
}

func (TextEndBody) isBody() {}

// ReasoningDeltaBody is the Body for KindReasoning: one incremental chunk
// of hidden reasoning text. WordCount is set on the final chunk of a
// reasoning span so renderers can print a collapsed summary.
type ReasoningDeltaBody struct {
	Text      string `json:"text"`
	WordCount int    `json:"word_count,omitempty"`
}

func (ReasoningDeltaBody) isBody() {}

// ToolPendingBody is the Body for KindToolPending: a tool call awaiting
// approval. Diff carries the proposed edit for file-edit tools, so the
// approval prompt can show what the call would change before it runs;
// nil for tools with no diff to preview.
type ToolPendingBody struct {
	ToolCallID string         `json:"tool_call_id"`
	Name       string         `json:"name"`
	Args       map[string]any `json:"args,omitempty"`
	Diff       *Diff          `json:"diff,omitempty"`
}

func (ToolPendingBody) isBody() {}

// ToolStartBody is the Body for KindToolStart: an approved or
// auto-approved tool call has begun executing.
type ToolStartBody struct {
	ToolCallID string         `json:"tool_call_id"`
	Name       string         `json:"name"`
	Args       map[string]any `json:"args,omitempty"`
}

func (ToolStartBody) isBody() {}

// AssistantResetBody is the Body for KindAssistantReset. Reason is a short,
// content-free classification of why the turn restarted.
type AssistantResetBody struct {
	Reason string `json:"reason,omitempty"`
}

func (AssistantResetBody) isBody() {}

// Progress carries subagent step progress. It is optional on
// ToolOutputBody; nil means ordinary incremental tool output.
type Progress struct {
	Step           int      `json:"step"`
	TotalSteps     int      `json:"total_steps"`
	ElapsedSeconds float64  `json:"elapsed_seconds"`
	ToolCalls      int      `json:"tool_calls,omitempty"`
	Status         string   `json:"status"` // e.g. "running", "blocked"
	Log            []string `json:"log,omitempty"`
}

// ToolOutputBody is the Body for KindToolOutput: an incremental chunk of
// tool output, or a subagent progress update when Progress is non-nil.
type ToolOutputBody struct {
	ToolCallID string    `json:"tool_call_id"`
	Chunk      string    `json:"chunk,omitempty"`
	Progress   *Progress `json:"progress,omitempty"`
}

func (ToolOutputBody) isBody() {}

// DiffLineKind identifies one line of a unified diff hunk.
type DiffLineKind string

const (
	DiffLineContext DiffLineKind = "context"
	DiffLineAdd     DiffLineKind = "add"
	DiffLineDel     DiffLineKind = "del"
)

// DiffLine is one line of a diff hunk.
type DiffLine struct {
	Kind DiffLineKind `json:"kind"`
	Text string       `json:"text"`
}

// DiffHunk is one hunk of a unified diff, with its header (e.g.
// "@@ -14,7 +14,11 @@ func (u *Uploader) put(") and its lines.
type DiffHunk struct {
	Header string     `json:"header"`
	Lines  []DiffLine `json:"lines"`
}

// Diff is a structured unified diff for a file edit. After, when
// present, is the file's full post-edit content: the Files tab's
// source view reads it, and anything that only needs the change (the
// transcript, the approval preview) ignores it. Deleted marks a whole-
// file removal: hunks still describe what was lost, and After is
// absent by definition.
type Diff struct {
	Path    string     `json:"path"`
	Added   int        `json:"added"`
	Removed int        `json:"removed"`
	Deleted bool       `json:"deleted,omitempty"`
	Hunks   []DiffHunk `json:"hunks"`
	After   []string   `json:"after,omitempty"`
}

// ToolEndBody is the Body for KindToolEnd: a tool call has finished.
type ToolEndBody struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Result     string `json:"result,omitempty"`
	Err        string `json:"err,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Diff       *Diff  `json:"diff,omitempty"`
}

func (ToolEndBody) isBody() {}

// PlanItem is one line of a plan/to-do checklist.
type PlanItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// PlanBody is the Body for KindPlan: the current state of the turn's
// plan/to-do checklist.
type PlanBody struct {
	Items []PlanItem `json:"items"`
	Done  int        `json:"done"`
	Total int        `json:"total"`
}

func (PlanBody) isBody() {}

// NoticeBody is the Body for KindNotice: a free-text advisory line that
// is not an error, e.g. a context-usage warning.
type NoticeBody struct {
	Text string `json:"text"`
}

func (NoticeBody) isBody() {}

// HookBody is the Body for KindHook: one lifecycle hook execution for a
// tool call, with its program, event, tool, and the bounded/redacted input
// and output the operator is shown. Structured rather than folded into
// NoticeBody so a renderer can show input/output distinctly and collapse
// the row - a bare Text string cannot carry that shape.
type HookBody struct {
	// Event is PreToolUse, PostToolUse, or Stop.
	Event string `json:"event"`
	// Program is the hook script's name (not its path).
	Program string `json:"program"`
	// Tool is the tool this hook fired for.
	Tool string `json:"tool"`
	// Input is the bounded, redacted tool input the hook saw.
	Input string `json:"input,omitempty"`
	// Output is what the hook said: advisory text, or the block reason.
	// Empty means it ran silently, which is normal and still worth showing.
	Output string `json:"output,omitempty"`
	// Denied is true for the PreToolUse run that blocked the call.
	Denied bool `json:"denied,omitempty"`
}

func (HookBody) isBody() {}

// UsageBody is the Body for KindUsage: token and cost accounting for the
// turn so far.
type UsageBody struct {
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	CachedTokens   int64   `json:"cached_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
}

func (UsageBody) isBody() {}

// ErrorBody is the Body for KindError. Fatal means the turn cannot
// continue.
type ErrorBody struct {
	Text  string `json:"text"`
	Fatal bool   `json:"fatal"`
}

func (ErrorBody) isBody() {}

// TurnEndBody is the Body for KindTurnEnd. Reason is one of "completed",
// "cancelled", or "error".
type TurnEndBody struct {
	Reason string `json:"reason"`
}

func (TurnEndBody) isBody() {}

// EventMsg wraps one Event for message delivery. It satisfies tea.Msg
// without an import.
//
// Source carries the channel this Event was read from. It lets a
// consumer keep draining the SAME channel purely from the Msg, with no
// dependency on any session registry still holding a reference to it:
// a registry lookup that misses (a session not, or no longer, tracked)
// must not stop the read loop, or the channel fills and its writer -
// which may be invoked synchronously from the agent loop emitting a
// tool event - blocks forever. Optional: a producer with no session
// registry to fall back through (single-session callers) may leave it
// nil.
type EventMsg struct {
	SessionID string
	Event     Event
	Source    <-chan Event
}
