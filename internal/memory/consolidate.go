package memory

import (
	"context"
	"database/sql"
	"strings"
)

// consolidateThresholdRatio triggers consolidation at 80% of MaxEntries
// (decision 2), inside Save's existing transaction rather than a second one
// (see the Step 2 correction on t5a/t5b in plan 76: a second BeginTx here
// would deadlock against Save's still-open transaction under SQLite's
// single-writer semantics).
const consolidateThresholdRatio = 0.8

// consolidate reduces the (scope, org) row count within tx by merging
// near-duplicate archive-tier entries (D3) and, if that alone isn't enough,
// evicting the lowest-`created` archive-tier row (D2, D4 - reusing the
// existing created-column ordering rather than adding a parallel recency
// mechanism). "core" entries are never evicted; two core entries can still
// merge if they are near-duplicates. Best-effort: an error here is returned
// to the caller (Save), which still holds its own count check as the final
// authority on whether the incoming insert fits.
func (s *sqliteStore) consolidate(ctx context.Context, tx *sql.Tx, scope Scope, org string, maxEntries int) error {
	if err := s.mergeNearDuplicates(ctx, tx, scope, org); err != nil {
		return err
	}
	for {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories WHERE scope = ? AND org = ?", string(scope), org).Scan(&count); err != nil {
			return err
		}
		if count < maxEntries {
			return nil
		}
		evicted, err := s.evictOldestArchive(ctx, tx, scope, org)
		if err != nil {
			return err
		}
		if !evicted {
			// Nothing left to merge or evict (e.g. every remaining row is
			// core): stop and let Save's own cap check report the failure.
			return nil
		}
	}
}

// consolidateRow is one row's fields needed to compare and merge entries.
type consolidateRow struct {
	id, title, summary, tags, content, created, tier string
}

// mergeNearDuplicates does one pass over (scope, org)'s rows: for each pair
// whose title+summary tokens are at or above similarityMergeThreshold, the
// later-created row's tags are folded into the earlier-created (kept) row
// and the later row is deleted. Each row participates in at most one merge
// per pass - a merged-away id is skipped as a further candidate - so this is
// a bounded single pass, not a fixed point; a highly duplicated bucket may
// need more than one Save cycle to fully consolidate, which is an acceptable
// trade against unbounded work inside one transaction.
func (s *sqliteStore) mergeNearDuplicates(ctx context.Context, tx *sql.Tx, scope Scope, org string) error {
	rows, err := tx.QueryContext(ctx, "SELECT id, title, summary, tags, content, created, tier FROM memories WHERE scope = ? AND org = ? ORDER BY created ASC, id ASC", string(scope), org)
	if err != nil {
		return err
	}
	var all []consolidateRow
	for rows.Next() {
		var r consolidateRow
		if err := rows.Scan(&r.id, &r.title, &r.summary, &r.tags, &r.content, &r.created, &r.tier); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	consumed := make(map[string]bool, len(all))
	for i := 0; i < len(all); i++ {
		if consumed[all[i].id] {
			continue
		}
		keep := all[i]
		keepTokens, _ := tokenize(keep.title + " " + keep.summary)
		for j := i + 1; j < len(all); j++ {
			if consumed[all[j].id] {
				continue
			}
			candidate := all[j]
			candTokens, _ := tokenize(candidate.title + " " + candidate.summary)
			if jaccardSimilarity(keepTokens, candTokens) < similarityMergeThreshold {
				continue
			}
			// A core entry must never be the one a merge deletes (D2: "core
			// entries are never auto-evicted - only merged"). If exactly
			// one side is core, the core row survives regardless of
			// creation order - bug found in Step 5 hostile audit: without
			// this, an archive row created before a later-promoted core
			// near-duplicate would survive and the core row would be
			// silently deleted.
			deleteID := candidate.id
			survivorID := keep.id
			mergedTags := mergeTagLists(keep.tags, candidate.tags)
			if candidate.tier == "core" && keep.tier != "core" {
				deleteID = keep.id
				survivorID = candidate.id
				keep = candidate
				keepTokens = candTokens
			}
			if _, err := tx.ExecContext(ctx, "UPDATE memories SET tags = ? WHERE id = ?", mergedTags, survivorID); err != nil {
				return err
			}
			if err := s.deleteRow(ctx, tx, deleteID); err != nil {
				return err
			}
			consumed[deleteID] = true
		}
	}
	return nil
}

// mergeTagLists unions two comma-joined tag strings, deduplicated, in the
// order first seen (keep's tags first).
func mergeTagLists(a, b string) string {
	seen := map[string]bool{}
	var out []string
	for _, tag := range append(splitTags(a), splitTags(b)...) {
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return strings.Join(out, ", ")
}

// evictOldestArchive deletes the lowest-`created` archive-tier row for
// (scope, org). Returns false when no evictable (archive-tier) row exists.
//
// `created` is caller-supplied (Entry.Created, validated only as a
// well-formed YYYY-MM-DD date - entry.go's Validate places no bound on past
// or future values) - a self-preserving agent could set a far-future
// Created on its own new entries to make them never look "oldest" and dodge
// eviction. This reuses the same `created` column decision 4/D4 already
// locked for search ranking and consolidation ordering (plan 76); it cannot
// retroactively change an existing rival entry's Created (Save only
// inserts, never updates), so the exposure is limited to self-preservation,
// not tampering with other entries. Flagged, not fixed, in Step 5+ review -
// closing it would mean deriving eviction order from the server-side
// `created_at` timestamp instead, a design change beyond this plan's scope.
func (s *sqliteStore) evictOldestArchive(ctx context.Context, tx *sql.Tx, scope Scope, org string) (bool, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		"SELECT id FROM memories WHERE scope = ? AND org = ? AND tier = 'archive' ORDER BY created ASC, id ASC LIMIT 1",
		string(scope), org).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.deleteRow(ctx, tx, id); err != nil {
		return false, err
	}
	return true, nil
}

// deleteRow removes one row and, when FTS5 is active, its index entry in
// the same transaction - a row consolidation drops must not leave a stale
// search hit behind.
func (s *sqliteStore) deleteRow(ctx context.Context, tx *sql.Tx, id string) error {
	if s.fts {
		if _, err := tx.ExecContext(ctx, "DELETE FROM memories_fts WHERE rowid = (SELECT rowid FROM memories WHERE id = ?)", id); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM memories WHERE id = ?", id)
	return err
}
