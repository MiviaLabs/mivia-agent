package ledger

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// This file holds one bounded summary builder per wf_* event kind plus the
// small display utilities they share. The kind dispatcher lives in
// events_list.go (summarizeEvent); keeping every builder here bounds the
// dispatcher to one switch over kinds and keeps each builder short.

// summarizeRunCreated renders the summary for a wf_run_created event.
func summarizeRunCreated(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalRunCreated(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("run created: workflow %q digest %s", p.Run.WorkflowName, p.Run.WorkflowDigest), p.CreatedAt, true
}

// summarizeRunStatusChanged renders the summary for a wf_run_status_changed event.
func summarizeRunStatusChanged(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalRunStatusChanged(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("status changed: %s (version %d)", p.Status, p.Version), p.CreatedAt, true
}

// summarizeAttemptStarted renders the summary for a wf_attempt_started event.
func summarizeAttemptStarted(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalAttemptStarted(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("attempt started: step %q attempt %d (%s)", p.Attempt.StepID, p.Attempt.AttemptNo, p.Attempt.AttemptID), p.CreatedAt, true
}

// summarizeAttemptCompleted renders the summary for a wf_attempt_completed event.
func summarizeAttemptCompleted(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalAttemptCompleted(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	summary := fmt.Sprintf("attempt completed: %s -> %s (transition %d, match %s, output %s%s)",
		p.Status, p.ToStepID, p.TransitionIndex, shortDigest(p.MatchDigest), shortRef(p.OutputRef), errorRefSummary(p.ErrorRef))
	return summary, p.CreatedAt, true
}

// summarizeAttemptPrompt renders the bounded, ref-only summary for a
// wf_attempt_prompt event. The typed payload carries ONLY attempt identity and
// the content ref; a payload missing either identifier is not decodable, and no
// prompt text is ever rendered into the audit trail.
func summarizeAttemptPrompt(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalAttemptPrompt(ev.Payload)
	if err != nil || p.AttemptID == "" || p.PromptRef == "" {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("attempt %s prompt ref %s", p.AttemptID, p.PromptRef), p.CreatedAt, true
}

// summarizeAttemptExecution renders the summary for a wf_attempt_execution
// event, including the bounded transient-retry reason when one is recorded.
func summarizeAttemptExecution(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalAttemptExecution(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	summary := fmt.Sprintf("attempt %s executed by coordinator run %s task %s", p.AttemptID, p.CoordinatorRunID, p.TaskID)
	if p.Reason != "" {
		summary += " reason: " + p.Reason
	}
	return summary, p.CreatedAt, true
}

// summarizeAttemptHeartbeat renders the bounded summary for a
// wf_attempt_heartbeat event: the attempt id and the heartbeat instant. No
// other state is carried by the payload, so nothing else can leak.
func summarizeAttemptHeartbeat(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalAttemptHeartbeat(ev.Payload)
	if err != nil || p.AttemptID == "" || p.HeartbeatAt.IsZero() {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("attempt %s heartbeat at %s", p.AttemptID, p.HeartbeatAt.Format(time.RFC3339)), p.CreatedAt, true
}

// summarizePanelPhase renders the summary for a wf_panel_phase_set event.
func summarizePanelPhase(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalPanelPhase(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("panel phase set: attempt %s -> %s (version %d)", p.AttemptID, p.Phase, p.Version), p.CreatedAt, true
}

// summarizeLoopIncremented renders the bounded summary for a loop event.
func summarizeLoopIncremented(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalLoopIncremented(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("loop incremented: %s -> %d", p.LoopName, p.Iterations), p.CreatedAt, true
}

// summarizeApprovalCreated renders the summary for a wf_approval_created event.
func summarizeApprovalCreated(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalApprovalCreated(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("approval created: %s (step %q)", p.Approval.ApprovalID, p.Approval.StepID), p.CreatedAt, true
}

// summarizeApprovalResolved renders the bounded summary for an approval event.
func summarizeApprovalResolved(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalApprovalResolved(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	summary := fmt.Sprintf("approval resolved: %s %s by %s", p.ApprovalID, p.Status, p.Actor)
	if p.Reason != "" {
		summary += " reason: " + p.Reason
	}
	return summary, p.CreatedAt, true
}

// summarizeDeliveryUpserted renders the summary for a wf_delivery_upserted event.
func summarizeDeliveryUpserted(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalDeliveryUpserted(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	d := p.Delivery
	summary := fmt.Sprintf("delivery %s: %s (mode %s, base %s)", d.IdempotencyKey, d.Status, d.Mode, d.BaseRef)
	if d.URL != "" {
		summary += " url: " + d.URL
	}
	return summary, p.CreatedAt, true
}

// summarizeRunDeleted renders the summary for a wf_run_deleted tombstone.
func summarizeRunDeleted(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalRunDeleted(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("run deleted: %s", p.RunID), p.DeletedAt, true
}

// summarizeRunResumed renders the summary for a wf_run_resumed event. The
// phrase "re-entered" covers both real resumes and the duplicate-admission
// path (StartNew with created=false also fires when a concurrent admission of
// the same invocation key wins, in which case nothing was resumed). The
// payload carries ONLY the run id: a deterministic event ID plus a
// byte-identical payload make a retried resume idempotent under the real
// clock, so no payload timestamp is persisted for this observational event
// (the event row's append time is the resume instant).
func summarizeRunResumed(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalRunResumed(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("run re-entered: %s", p.RunID), time.Time{}, true
}

// errorRefSummary renders the error reference suffix for an attempt summary.
func errorRefSummary(ref string) string {
	if ref == "" {
		return ""
	}
	return " error " + ref
}

// shortDigest renders a digest prefix for display; empty stays empty.
func shortDigest(digest string) string {
	if digest == "" {
		return "-"
	}
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// shortRef renders a content reference for display; empty stays empty.
func shortRef(ref string) string {
	if ref == "" {
		return "-"
	}
	return ref
}

// truncateSummary bounds one summary to MaxEventSummaryBytes without splitting
// a UTF-8 rune.
func truncateSummary(s string) string {
	if len(s) <= MaxEventSummaryBytes {
		return s
	}
	const ellipsis = "…"
	cut := MaxEventSummaryBytes - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
