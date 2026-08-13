// Projection and catch-up for the plan/task ledger. The projection is derived
// state: it is rebuilt from the durable event log of each plan run, so a fresh
// Store instance over the same storage backend sees identical state after a
// restart, and incremental catch-up keeps several instances coherent.
package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// catchUp folds store events appended since this instance's cursor into the
// projection. Runs outside the tksp: namespace are foreign (workflow wfr-,
// coordinator run-, ...) and are never read; advancing the cursor past their
// appends keeps the probe constant-time.
func (s *Store) catchUp(ctx context.Context) error {
	maxSequences, cursor, err := s.store.Changes(ctx, s.cursor)
	if err != nil {
		return fmt.Errorf("read store changes: %w", err)
	}
	var behind []string
	for runID, maxSeq := range maxSequences {
		if !strings.HasPrefix(runID, runIDPrefix) {
			continue
		}
		if uint64(maxSeq) > s.applied[runID] {
			behind = append(behind, runID)
		}
	}
	sort.Strings(behind)
	for _, runID := range behind {
		if err := s.rebuildRunFromStore(ctx, runID); err != nil {
			return err
		}
	}
	if cursor > s.cursor {
		s.cursor = cursor
	}
	return nil
}

// rebuildRunFromStore rebuilds one plan run's projection from its full event
// log, so the in-memory state matches durable state.
func (s *Store) rebuildRunFromStore(ctx context.Context, runID string) error {
	events, err := s.store.Events(ctx, runID)
	if err != nil {
		return fmt.Errorf("read events for %s: %w", runID, err)
	}
	return s.rebuildRun(runID, events)
}

// rebuildRun folds one plan run's event log into the projection, ordered by
// sequence (then row ID). Unknown kinds are ignored (forward compatibility);
// an undecodable KNOWN kind fails loudly, mirroring the workflow ledger.
func (s *Store) rebuildRun(runID string, events []storage.Event) error {
	sorted := append([]storage.Event(nil), events...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Sequence != sorted[j].Sequence {
			return sorted[i].Sequence < sorted[j].Sequence
		}
		return sorted[i].RowID < sorted[j].RowID
	})

	planRef := strings.TrimPrefix(runID, runIDPrefix)
	state := &planState{tasks: make(map[string]Task)}
	var maxSeq uint64
	for _, ev := range sorted {
		if uint64(ev.Sequence) > maxSeq {
			maxSeq = uint64(ev.Sequence)
		}
		switch ev.Kind {
		case eventKindPlanStored:
			var p planStoredPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return fmt.Errorf("decode %s: %w", ev.Kind, err)
			}
			state.plan = p.Plan
		case eventKindPlanBound:
			var p planBoundPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return fmt.Errorf("decode %s: %w", ev.Kind, err)
			}
			state.plan.Scope = p.Scope
			state.binds++
		case eventKindTaskCreated:
			var p taskCreatedPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return fmt.Errorf("decode %s: %w", ev.Kind, err)
			}
			state.tasks[p.Task.ID] = p.Task
		case eventKindTaskTransitioned:
			var p taskTransitionedPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return fmt.Errorf("decode %s: %w", ev.Kind, err)
			}
			task, ok := state.tasks[p.TaskID]
			if !ok {
				return fmt.Errorf("transition for unknown task %s", p.TaskID)
			}
			task.Status = p.ToStatus
			state.tasks[p.TaskID] = task
			state.journal = append(state.journal, Transition{
				PlanRef: planRef, TaskID: p.TaskID,
				FromStatus: p.FromStatus, ToStatus: p.ToStatus, At: p.CreatedAt,
			})
		default:
			// Unknown kind: ignore (forward compatibility).
		}
	}
	if state.plan.ID == "" {
		// No surviving plan_stored event (e.g. an append that failed before
		// committing): drop the stale in-memory record.
		delete(s.plans, planRef)
	} else {
		s.plans[planRef] = state
	}
	if maxSeq > s.applied[runID] {
		s.applied[runID] = maxSeq
	}
	return nil
}

// nextSequence mints the next sequence number for a plan run, starting from
// the higher of the applied watermark and this instance's own previous
// allocations. allocated keeps a failed append from ever reusing a sequence
// that was already handed out.
func (s *Store) nextSequence(runID string) uint64 {
	next := s.applied[runID]
	if s.allocated[runID] > next {
		next = s.allocated[runID]
	}
	next++
	s.allocated[runID] = next
	return next
}

func sortedPlanRefs(plans map[string]*planState) []string {
	refs := make([]string, 0, len(plans))
	for ref := range plans {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func sortedTaskIDs(tasks map[string]Task) []string {
	ids := make([]string, 0, len(tasks))
	for id := range tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
