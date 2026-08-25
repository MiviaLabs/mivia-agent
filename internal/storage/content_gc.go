package storage

import (
	"context"
	"regexp"
)

// workflowContentRefPattern matches the workflow ledger's own content
// reference format ("sha256:<64 lowercase hex>"), minted by
// internal/workflows/ledger and internal/workflows/controller (see
// agent_step_errors.go, panel_types.go). It is DELIBERATELY narrower than
// the coordinator/subagent ledger's own reference format
// ("ref:<kind>:<64 hex>", internal/ledger/contentref): PruneOrphanedContent
// must never delete a row it did not mint, so it only ever considers rows
// whose ref matches this exact pattern - a live chat/subagent content row
// is a different prefix in the same table and stays untouched no matter
// what its own referencing events look like.
var workflowContentRefPattern = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

// PruneOrphanedContent deletes every workflow-ledger content row
// ("sha256:" prefix) that no live event payload references any longer.
//
// AppendAndDeleteRun (DeleteRun's storage layer) strips a deleted run down
// to a tombstone event, but never touches the content table: the run's
// output/error/diff blobs it referenced become permanently unreachable
// (no live event payload names them) yet permanently retained (nothing
// ever deletes them) - a live finding from a session that mass-deleted
// dozens of stacked-delivery runs and left their content orphaned.
//
// Safe by construction: it computes the live set by scanning every row
// CURRENTLY in the events table (a deleted run's real events are already
// gone, only its tombstone remains, so its content refs are already
// unreachable and correctly excluded) and only ever deletes rows matching
// workflowContentRefPattern - a coordinator/subagent/chat content row
// (a different ref prefix) is never a match and is never considered.
func (s *SQLite) PruneOrphanedContent(ctx context.Context) (removed int, err error) {
	live, err := s.liveWorkflowContentRefs(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ref FROM content WHERE ref LIKE 'sha256:%'`)
	if err != nil {
		return 0, err
	}
	var orphans []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return 0, err
		}
		if !live[ref] {
			orphans = append(orphans, ref)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()
	if len(orphans) == 0 {
		return 0, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return 0, err
	}
	for _, ref := range orphans {
		if _, err := tx.ExecContext(ctx, `DELETE FROM content WHERE ref = ?`, ref); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(orphans), nil
}

// liveWorkflowContentRefs scans every event payload currently in the events
// table for a workflow content-ref occurrence, across every run - not just
// one - so a ref shared between two runs (content-addressed, so identical
// bytes always mint the identical ref) is kept alive as long as ANY run
// still references it, even after one of the sharing runs is deleted.
func (s *SQLite) liveWorkflowContentRefs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM events`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	live := make(map[string]bool)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		for _, ref := range workflowContentRefPattern.FindAll(payload, -1) {
			live[string(ref)] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return live, nil
}
