package ledger

import (
	"context"
	"fmt"
	"time"

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

// summarizeEvent decodes one wf_* event into a bounded display summary.
// It returns ok=false for unknown kinds and undecodable payloads. The per-kind
// summary builders live in events_summary.go; this function only dispatches.
func summarizeEvent(ev storage.Event) (EventRecord, bool) {
	var summary string
	var createdAt time.Time
	var ok bool
	switch ev.Kind {
	case eventKindRunCreated:
		summary, createdAt, ok = summarizeRunCreated(ev)
	case eventKindRunStatusChanged:
		summary, createdAt, ok = summarizeRunStatusChanged(ev)
	case eventKindAttemptStarted:
		summary, createdAt, ok = summarizeAttemptStarted(ev)
	case eventKindAttemptCompleted:
		summary, createdAt, ok = summarizeAttemptCompleted(ev)
	case eventKindAttemptPrompt:
		summary, createdAt, ok = summarizeAttemptPrompt(ev)
	case eventKindAttemptExecution:
		summary, createdAt, ok = summarizeAttemptExecution(ev)
	case eventKindAttemptHeartbeat:
		summary, createdAt, ok = summarizeAttemptHeartbeat(ev)
	case eventKindPanelPhaseSet:
		summary, createdAt, ok = summarizePanelPhase(ev)
	case eventKindLoopIncremented:
		summary, createdAt, ok = summarizeLoopIncremented(ev)
	case eventKindApprovalCreated:
		summary, createdAt, ok = summarizeApprovalCreated(ev)
	case eventKindApprovalResolved:
		summary, createdAt, ok = summarizeApprovalResolved(ev)
	case eventKindDeliveryUpserted:
		summary, createdAt, ok = summarizeDeliveryUpserted(ev)
	case eventKindRunDeleted:
		summary, createdAt, ok = summarizeRunDeleted(ev)
	case eventKindRunResumed:
		summary, createdAt, ok = summarizeRunResumed(ev)
	default:
		// Foreign or unknown kind: tolerate like the projection does.
		return EventRecord{}, false
	}
	if !ok {
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
