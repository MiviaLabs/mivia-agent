package ledger

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// EventRecord is one workflow run event with a bounded, human-safe summary.
// The summary never contains raw payloads: agent output is content-addressed
// (refs/digests only), the run_created payload never echoes the snapshot JSON,
// approval reasons are truncated to the summary bound, and wf_attempt_prompt
// summaries are REF-ONLY (attempt_id + prompt_ref), never the prompt body.
type EventRecord struct {
	ID        string
	Kind      string
	Sequence  int
	CreatedAt time.Time
	Summary   string
}

// MaxEventSummaryBytes bounds one event summary for operator display.
const MaxEventSummaryBytes = 512

// DefaultEventPageSize is the page size when ListEvents is called without a
// limit. It bounds one CLI listing without forcing callers to page.
const DefaultEventPageSize = 200

// eventKindAttemptPrompt marks a wf_attempt_prompt event: an attempt invoked
// the model with a content-addressed prompt. The event payload carries prompt
// content, so its listing summary is REF-ONLY (attempt_id + prompt_ref) and
// must never render the prompt body. Declared here (rather than events.go)
// because this change is scoped to the listing file.
const eventKindAttemptPrompt = "wf_attempt_prompt"

// ListEvents returns the run's audit trail, ordered by event sequence. The
// listing is paged over the DECODABLE stream: unknown/undecodable events are
// filtered out first, then limit/offset slice the decodable events, so each
// page holds up to `limit` decodable events and never comes back short while
// decodable events remain. limit <= 0 means DefaultEventPageSize, offset skips
// that many decodable events. A limit/offset large enough to overflow the
// page bounds is clamped to the trail (an offset past the trail is an empty
// page), never a slice-bounds panic. Events whose kind or payload is not a known
// wf_* shape are skipped (matching the projection's tolerance), so a listing
// never fails on a foreign or undecodable event. Returns ErrNotFound when the
// run is absent.
func (s *StorageRepository) ListEvents(ctx context.Context, runID string, limit, offset int) ([]EventRecord, error) {
	if err := s.ensureBuilt(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	p, ok := s.proj[runID]
	if !ok || !p.HasRun {
		s.mu.RUnlock()
		return nil, ErrNotFound
	}
	s.mu.RUnlock()

	events, err := s.store.Events(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("read workflow events for %s: %w", runID, err)
	}
	// Decodable-select BEFORE slicing: pages count decodable events, so a page
	// is full whenever at least `limit` decodable events remain, even with
	// foreign events interleaved in the raw stream.
	decodable := make([]EventRecord, 0, len(events))
	for _, ev := range events {
		record, ok := summarizeEvent(ev)
		if !ok {
			continue
		}
		decodable = append(decodable, record)
	}
	if limit <= 0 {
		limit = DefaultEventPageSize
	}
	start := offset
	if start < 0 {
		start = 0
	}
	if start > len(decodable) {
		return nil, nil
	}
	end := start + limit
	// end < start means start+limit overflowed int64 and wrapped negative;
	// clamp it like any other past-the-end page instead of slicing with a
	// negative bound and panicking.
	if end > len(decodable) || end < start {
		end = len(decodable)
	}
	return decodable[start:end], nil
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

// summarizeLoopIncremented renders the bounded summary for a loop event.
func summarizeLoopIncremented(ev storage.Event) (string, time.Time, bool) {
	p, err := unmarshalLoopIncremented(ev.Payload)
	if err != nil {
		return "", time.Time{}, false
	}
	return fmt.Sprintf("loop incremented: %s -> %d", p.LoopName, p.Iterations), p.CreatedAt, true
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

// summarizeEvent decodes one wf_* event into a bounded display summary.
// It returns ok=false for unknown kinds and undecodable payloads.
func summarizeEvent(ev storage.Event) (EventRecord, bool) {
	var summary string
	var createdAt time.Time
	var ok bool
	switch ev.Kind {
	case eventKindRunCreated:
		p, err := unmarshalRunCreated(ev.Payload)
		if err != nil {
			return EventRecord{}, false
		}
		summary = fmt.Sprintf("run created: workflow %q digest %s", p.Run.WorkflowName, p.Run.WorkflowDigest)
		createdAt = p.CreatedAt
	case eventKindRunStatusChanged:
		p, err := unmarshalRunStatusChanged(ev.Payload)
		if err != nil {
			return EventRecord{}, false
		}
		summary = fmt.Sprintf("status changed: %s (version %d)", p.Status, p.Version)
		createdAt = p.CreatedAt
	case eventKindAttemptStarted:
		p, err := unmarshalAttemptStarted(ev.Payload)
		if err != nil {
			return EventRecord{}, false
		}
		summary = fmt.Sprintf("attempt started: step %q attempt %d (%s)", p.Attempt.StepID, p.Attempt.AttemptNo, p.Attempt.AttemptID)
		createdAt = p.CreatedAt
	case eventKindAttemptCompleted:
		p, err := unmarshalAttemptCompleted(ev.Payload)
		if err != nil {
			return EventRecord{}, false
		}
		summary = fmt.Sprintf("attempt completed: %s -> %s (transition %d, match %s, output %s%s)",
			p.Status, p.ToStepID, p.TransitionIndex, shortDigest(p.MatchDigest), shortRef(p.OutputRef), errorRefSummary(p.ErrorRef))
		createdAt = p.CreatedAt
	case eventKindAttemptPrompt:
		summary, createdAt, ok = summarizeAttemptPrompt(ev)
		if !ok {
			return EventRecord{}, false
		}
	case eventKindLoopIncremented:
		summary, createdAt, ok = summarizeLoopIncremented(ev)
		if !ok {
			return EventRecord{}, false
		}
	case eventKindApprovalCreated:
		p, err := unmarshalApprovalCreated(ev.Payload)
		if err != nil {
			return EventRecord{}, false
		}
		summary = fmt.Sprintf("approval created: %s (step %q)", p.Approval.ApprovalID, p.Approval.StepID)
		createdAt = p.CreatedAt
	case eventKindApprovalResolved:
		summary, createdAt, ok = summarizeApprovalResolved(ev)
		if !ok {
			return EventRecord{}, false
		}
	case eventKindDeliveryUpserted:
		p, err := unmarshalDeliveryUpserted(ev.Payload)
		if err != nil {
			return EventRecord{}, false
		}
		d := p.Delivery
		summary = fmt.Sprintf("delivery %s: %s (mode %s, base %s)", d.IdempotencyKey, d.Status, d.Mode, d.BaseRef)
		if d.URL != "" {
			summary += " url: " + d.URL
		}
		createdAt = p.CreatedAt
	default:
		// Foreign or unknown kind: tolerate like the projection does.
		return EventRecord{}, false
	}
	return EventRecord{
		ID:        ev.ID,
		Kind:      ev.Kind,
		Sequence:  ev.Sequence,
		CreatedAt: createdAt,
		Summary:   truncateSummary(summary),
	}, true
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
