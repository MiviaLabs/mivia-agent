package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// MemoryIndexDocument is the derived representation of one Markdown memory.
// SourcePath and SourceHash identify the canonical file that produced it.
type MemoryIndexDocument struct {
	ID, Scope, ProjectID, OrgID  string
	SourcePath, SourceHash       string
	Title, Summary, Verdict      string
	Tags, Created, Content, Tier string
}

// SyncMemoryIndex updates one project or organization scope from a complete
// Markdown scan. It removes rows for deleted source files and preserves the
// operator-selected tier for files that still exist. A sibling process
// holding the database write lock longer than busy_timeout is retried
// (retrySQLiteBusy): the body is one full transaction, so a retry re-reads
// current state.
func (s *SQLite) SyncMemoryIndex(ctx context.Context, scope, projectID, orgID string, docs []MemoryIndexDocument) error {
	return retrySQLiteBusy(ctx, func() error {
		return s.syncMemoryIndexOnce(ctx, scope, projectID, orgID, docs)
	})
}

// syncMemoryIndexOnce runs one complete sync transaction. It is retried as a
// whole; no partial state survives a failed attempt.
func (s *SQLite) syncMemoryIndexOnce(ctx context.Context, scope, projectID, orgID string, docs []MemoryIndexDocument) error {
	if scope != "project" && scope != "org" {
		return fmt.Errorf("memory index scope %q is invalid", scope)
	}
	if orgID != "" {
		normalized, err := normalizeMemoryOrgID(orgID)
		if err != nil {
			return err
		}
		orgID = normalized
	}
	if err := validateMemoryIndexDocuments(scope, projectID, orgID, docs); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old := make(map[string]struct{})
	sources, entries, err := loadMemoryIndexScopeState(ctx, tx, scope, projectID, orgID, old)
	if err != nil {
		return err
	}
	for _, doc := range docs {
		if doc.Scope != scope {
			return fmt.Errorf("memory index scope %q does not match the scan scope %q", doc.Scope, scope)
		}
		delete(old, doc.SourcePath)
		if memoryIndexDocUnchanged(doc, sources, entries) {
			continue
		}
		if err := upsertMemoryIndexDocument(ctx, tx, doc); err != nil {
			return err
		}
	}
	for path := range old {
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entries WHERE scope=? AND project_id=? AND org_id=? AND source_path=?`, scope, projectID, orgID, path); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_sources WHERE scope=? AND project_id=? AND org_id=? AND source_path=?`, scope, projectID, orgID, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// memoryIndexEntryRef is one memory_entries row identity, keyed by path in the
// scope state.
type memoryIndexEntryRef struct{ id, sourceHash string }

// loadMemoryIndexScopeState reads the scope's current rows inside the sync
// transaction. old collects the memory_sources paths (the scanned-path
// bookkeeping: any path left in old after the doc loop was removed on disk and
// gets deleted), sources maps path -> source_hash, and entries maps path ->
// matching entry refs. The schema does not enforce memory_entries source_path
// uniqueness (context_schema_v16), so one path can carry more than one ref.
func loadMemoryIndexScopeState(ctx context.Context, tx *sql.Tx, scope, projectID, orgID string, old map[string]struct{}) (map[string]string, map[string][]memoryIndexEntryRef, error) {
	sources := make(map[string]string)
	rows, err := tx.QueryContext(ctx, `SELECT source_path, source_hash FROM memory_sources WHERE scope=? AND project_id=? AND org_id=?`, scope, projectID, orgID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var path, hash string
		if err := rows.Scan(&path, &hash); err != nil {
			rows.Close()
			return nil, nil, err
		}
		old[path] = struct{}{}
		sources[path] = hash
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	entries := make(map[string][]memoryIndexEntryRef)
	rows, err = tx.QueryContext(ctx, `SELECT source_path, id, source_hash FROM memory_entries WHERE scope=? AND project_id=? AND org_id=?`, scope, projectID, orgID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var path string
		var ref memoryIndexEntryRef
		if err := rows.Scan(&path, &ref.id, &ref.sourceHash); err != nil {
			rows.Close()
			return nil, nil, err
		}
		entries[path] = append(entries[path], ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	return sources, entries, nil
}

// memoryIndexDocUnchanged reports whether doc is already indexed exactly as
// scanned, so the sync can skip the upsert and leave every row - including
// the operator-selected tier - untouched. The skip requires the path in BOTH
// maps with identical hashes, an identical id, and exactly one memory_entries
// row for the path: more than one match is treated as changed and healed by
// the full upsert, whose same-path-different-id DELETE stays the enforcer of
// at most one entries row per path.
func memoryIndexDocUnchanged(doc MemoryIndexDocument, sources map[string]string, entries map[string][]memoryIndexEntryRef) bool {
	hash, ok := sources[doc.SourcePath]
	if !ok || hash != doc.SourceHash {
		return false
	}
	refs, ok := entries[doc.SourcePath]
	if !ok || len(refs) != 1 {
		return false
	}
	return refs[0].id == doc.ID && refs[0].sourceHash == doc.SourceHash
}

func validateMemoryIndexDocuments(scope, projectID, orgID string, docs []MemoryIndexDocument) error {
	if scope == "project" && strings.TrimSpace(projectID) == "" {
		return errors.New("memory index project_id is required for a project scan")
	}
	if scope == "org" && strings.TrimSpace(orgID) == "" {
		return errors.New("memory index org_id is required for an organization scan")
	}
	seen := make(map[string]struct{}, len(docs))
	seenIDs := make(map[string]struct{}, len(docs))
	for _, doc := range docs {
		if doc.Scope != "project" && doc.Scope != "org" {
			return fmt.Errorf("memory index scope %q is invalid", doc.Scope)
		}
		if doc.Scope != scope {
			return fmt.Errorf("memory index scope %q does not match the scan scope %q", doc.Scope, scope)
		}
		if doc.Scope == "project" && (doc.ProjectID != projectID || doc.OrgID != "") {
			return fmt.Errorf("memory index project_id %q does not match %q", doc.ProjectID, projectID)
		}
		if doc.Scope == "org" && (doc.ProjectID != "" || doc.OrgID != orgID || strings.TrimSpace(orgID) == "") {
			return fmt.Errorf("memory index org_id %q does not match %q", doc.OrgID, orgID)
		}
		if doc.ID == "" || doc.SourcePath == "" {
			return errors.New("memory index id and source_path are required")
		}
		if _, exists := seenIDs[doc.ID]; exists {
			return fmt.Errorf("duplicate memory index id %q", doc.ID)
		}
		seenIDs[doc.ID] = struct{}{}
		key := doc.Scope + "\x00" + doc.ProjectID + "\x00" + doc.OrgID + "\x00" + doc.SourcePath
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate memory index source %q", doc.SourcePath)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func normalizeMemoryOrgID(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "..") {
		return "", errors.New("memory index org_id is invalid")
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' || r == '/' {
			continue
		}
		return "", fmt.Errorf("memory index org_id contains unsupported character %q", r)
	}
	return value, nil
}

func upsertMemoryIndexDocument(ctx context.Context, tx *sql.Tx, doc MemoryIndexDocument) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entries WHERE scope=? AND project_id=? AND org_id=? AND source_path=? AND id<>?`, doc.Scope, doc.ProjectID, doc.OrgID, doc.SourcePath, doc.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_sources(scope,project_id,org_id,source_path,source_hash,indexed_at) VALUES(?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(scope,project_id,org_id,source_path) DO UPDATE SET source_hash=excluded.source_hash,indexed_at=CURRENT_TIMESTAMP`, doc.Scope, doc.ProjectID, doc.OrgID, doc.SourcePath, doc.SourceHash); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE memory_entries SET source_hash=?,title=?,summary=?,verdict=?,tags=?,created=?,content=?,indexed_at=CURRENT_TIMESTAMP WHERE id=? AND scope=? AND project_id=? AND org_id=?`, doc.SourceHash, doc.Title, doc.Summary, doc.Verdict, doc.Tags, doc.Created, doc.Content, doc.ID, doc.Scope, doc.ProjectID, doc.OrgID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_entries(id,scope,project_id,org_id,source_path,source_hash,title,summary,verdict,tags,created,content,tier,indexed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)`, doc.ID, doc.Scope, doc.ProjectID, doc.OrgID, doc.SourcePath, doc.SourceHash, doc.Title, doc.Summary, doc.Verdict, doc.Tags, doc.Created, doc.Content, "archive")
	return err
}
