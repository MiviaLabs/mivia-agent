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
// and approval reasons are truncated to the summary bound.
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

// ListEvents returns the run's audit trail, ordered by event sequence. The
// listing is paged: limit <= 0 means DefaultEventPageSize, offset skips that
// many events. Events whose kind or payload is not a known wf_* shape are
// skipped (matching the projection's tolerance), so a listing never fails on
// a foreign or undecodable event. Returns ErrNotFound when the run is absent.
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
	if limit <= 0 {
		limit = DefaultEventPageSize
	}
	start := offset
	if start < 0 {
		start = 0
	}
	if start > len(events) {
		start = len(events)
	}
	end := start + limit
	if end > len(events) {
		end = len(events)
	}
	out := make([]EventRecord, 0, end-start)
	for _, ev := range events[start:end] {
		record, ok := summarizeEvent(ev)
		if !ok {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// summarizeEvent decodes one wf_* event into a bounded display summary.
// It returns ok=false for unknown kinds and undecodable payloads.
func summarizeEvent(ev storage.Event) (EventRecord, bool) {
	var summary string
	var createdAt time.Time
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
		summary = fmt.Sprintf("attempt completed: %s -> %s (transition %d, match %s, output %s)",
			p.Status, p.ToStepID, p.TransitionIndex, shortDigest(p.MatchDigest), shortRef(p.OutputRef))
		createdAt = p.CreatedAt
	case eventKindLoopIncremented:
		p, err := unmarshalLoopIncremented(ev.Payload)
		if err != nil {
			return EventRecord{}, false
		}
		summary = fmt.Sprintf("loop incremented: %s -> %d", p.LoopName, p.Iterations)
		createdAt = p.CreatedAt
	case eventKindApprovalCreated:
		p, err := unmarshalApprovalCreated(ev.Payload)
		if err != nil {
			return EventRecord{}, false
		}
		summary = fmt.Sprintf("approval created: %s (step %q)", p.Approval.ApprovalID, p.Approval.StepID)
		createdAt = p.CreatedAt
	case eventKindApprovalResolved:
		p, err := unmarshalApprovalResolved(ev.Payload)
		if err != nil {
			return EventRecord{}, false
		}
		summary = fmt.Sprintf("approval resolved: %s %s by %s", p.ApprovalID, p.Status, p.Actor)
		if p.Reason != "" {
			summary += " reason: " + p.Reason
		}
		createdAt = p.CreatedAt
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
