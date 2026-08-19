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
	KindToolEnd     Kind = "tool.end"
	KindPlan        Kind = "plan"   // to-do/plan checklist update
	KindNotice      Kind = "notice" // free-text advisory line, e.g. context-usage warning
	KindUsage       Kind = "usage"
	KindError       Kind = "error"
	KindTurnEnd     Kind = "turn.end"
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

// Progress carries subagent step progress. It is optional on
// ToolOutputBody; nil means ordinary incremental tool output.
type Progress struct {
	Step           int      `json:"step"`
	TotalSteps     int      `json:"total_steps"`
	ElapsedSeconds float64  `json:"elapsed_seconds"`
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

// Diff is a structured unified diff for a file edit.
type Diff struct {
	Path    string     `json:"path"`
	Added   int        `json:"added"`
	Removed int        `json:"removed"`
	Hunks   []DiffHunk `json:"hunks"`
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
type EventMsg struct {
	Event Event
}
