// Package config holds every timing, limit, and threshold used by the new
// UI in one place. No view-layer package may hold a literal for any of
// these; import this package instead.
package config

import "time"

// Text-delta batching: one Msg per token would be one render per token
// even with the cell-based renderer. Accumulate and flush on this tick.
const TextDeltaFlushInterval = 40 * time.Millisecond

// SpinnerFPS bounds the activity-indicator repaint rate.
const SpinnerFPS = 10

// MaxTranscriptLines bounds the in-model view window; full history stays
// in the session, only this many lines are held for repaint.
const MaxTranscriptLines = 2000

// MaxToolOutputBytes bounds how much of one tool call's output is kept
// for inline display before it is elided with a "see full output" note.
const MaxToolOutputBytes = 64 * 1024

// BodyIndent is the column count a block body is indented by. The design
// contradicted itself here: wireframes-panes.md section 2 says 2 columns
// and section 11 says 4. Every drawn wireframe in sections 4, 5, 11 and
// 13 uses 4, so 4 wins and section 2 is amended.
const BodyIndent = 4

// CollapseThresholdLines is the body height at or above which a block
// first renders collapsed. wireframes-panes.md section 5: "open under 12
// body lines, closed at or above".
const CollapseThresholdLines = 12

// Prose is wrapped to a measure, not to the terminal width, so long
// lines stay readable on a wide terminal. wireframes-panes.md section 14.
const (
	ProseMeasureNarrow = 76
	ProseMeasureWide   = 92
)

// Layout breakpoints. At or above BreakpointWide the UI adds fields and
// widens dialogs. Below BreakpointPlainStream the interactive renderer is
// not viable and the plain stream renderer is chosen instead - at
// startup only, never as a runtime switch.
const (
	BreakpointPlainStream = 40
	BreakpointNarrow      = 80
	BreakpointWide        = 120
)

// Dialog geometry. wireframes-panes.md sections 8 and 12: a fixed-width
// wash, inset from the left, clipped rather than wrapped. Section 14's
// conflicting "58" is amended to DialogWidth, which fits inside
// BreakpointNarrow with room to spare and matches the drawn mocks.
const (
	DialogWidth     = 62
	DialogWidthWide = 72
	DialogInset     = 8
)

// ClipMarker terminates a row that was clipped to fit its panel. A wash
// has no drawn edge, so the marker is the only clip signal.
const ClipMarker = "~"

// MaxCompletionRows bounds the slash-completion list height.
// wireframes-panes.md section 10. This caps RENDERED ROWS only: every
// candidate is still scored and reachable by scrolling. Capping the
// candidate set instead is a known defect class - it makes later matches
// unreachable. See docs/design/ux-rules.md rule 5.7.
const MaxCompletionRows = 6

// CancelDoublePressWindow is how long after a cancelling Ctrl-C a second
// Ctrl-C quits. Outside the window the second press cancels again.
// docs/design/ux-rules.md rule 1.3.
const CancelDoublePressWindow = 500 * time.Millisecond

// WCAG 2.1 contrast ratios. AA is the shipped gate for first-party
// themes; AAALarge is used by mivia-high-contrast, which targets AAA.
const (
	WCAGAABody   = 4.5
	WCAGAALarge  = 3.0
	WCAGAAALarge = 7.0
)

// CVDSeparationThreshold is the CIE76 dE floor below which two status
// colours read as the same hue at terminal text sizes under dichromat
// simulation. Research-panes.md section 3: the mivia-dark shipped set
// sits at 18.5, below this floor, by an explicit vividness trade — encode
// per-theme budgets, not a single hard gate, so that trade stays visible.
const CVDSeparationThreshold = 20.0

// ApprovalDefaultInline and ApprovalDefaultDialog: an inline approval
// prompt defaults to "once" (the call was judged safe enough to ask
// casually); promotion to a full dialog is itself the signal that it was
// not, so a dialog defaults to "deny". research-panes.md finding 4.
const (
	ApprovalDefaultInline = "once"
	ApprovalDefaultDialog = "deny"
)
