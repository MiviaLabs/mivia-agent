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
