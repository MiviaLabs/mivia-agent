package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const memoryIndexCoreTierCap = 24

var ErrMemoryIndexEntryNotFound = errors.New("memory index entry not found")

// PromoteMemoryIndexEntry marks one indexed entry as core.
func (s *SQLite) PromoteMemoryIndexEntry(ctx context.Context, id, projectID, orgID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var scope, tier string
	err = tx.QueryRowContext(ctx, `SELECT scope,tier FROM memory_entries WHERE id=? AND ((scope='project' AND project_id=? AND org_id='') OR (scope='org' AND org_id=? AND project_id='')) ORDER BY CASE WHEN scope='project' THEN 0 ELSE 1 END LIMIT 1`, id, projectID, orgID).Scan(&scope, &tier)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMemoryIndexEntryNotFound
	}
	if err != nil {
		return err
	}
	if tier == "core" {
		return tx.Commit()
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_entries WHERE scope=? AND project_id=? AND org_id=? AND tier='core'`, scope, projectIDForScope(scope, projectID), orgIDForScope(scope, orgID)).Scan(&count); err != nil {
		return err
	}
	if count >= memoryIndexCoreTierCap {
		return fmt.Errorf("memory: core tier is full (max %d entries); merge or archive an existing core entry first", memoryIndexCoreTierCap)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memory_entries SET tier='core', indexed_at=CURRENT_TIMESTAMP WHERE id=? AND scope=? AND project_id=? AND org_id=?`, id, scope, projectIDForScope(scope, projectID), orgIDForScope(scope, orgID)); err != nil {
		return err
	}
	return tx.Commit()
}

// FindMemoryIndexEntry returns one indexed entry by ID in the configured buckets.
func (s *SQLite) FindMemoryIndexEntry(ctx context.Context, id, projectID, orgID string) (MemoryIndexDocument, error) {
	var doc MemoryIndexDocument
	err := s.db.QueryRowContext(ctx, `SELECT id,scope,project_id,org_id,source_path,source_hash,title,summary,verdict,tags,created,content,tier FROM memory_entries WHERE id=? AND ((scope='project' AND project_id=? AND org_id='') OR (scope='org' AND org_id=? AND project_id='')) ORDER BY CASE WHEN scope='project' THEN 0 ELSE 1 END LIMIT 1`, id, projectID, orgID).Scan(&doc.ID, &doc.Scope, &doc.ProjectID, &doc.OrgID, &doc.SourcePath, &doc.SourceHash, &doc.Title, &doc.Summary, &doc.Verdict, &doc.Tags, &doc.Created, &doc.Content, &doc.Tier)
	if errors.Is(err, sql.ErrNoRows) {
		return MemoryIndexDocument{}, ErrMemoryIndexEntryNotFound
	}
	return doc, err
}

// CoreMemoryIndexEntries returns the bounded core tier for one scope.
func (s *SQLite) CoreMemoryIndexEntries(ctx context.Context, scope, projectID, orgID string) ([]MemoryIndexDocument, error) {
	if scope != "project" && scope != "org" {
		return nil, fmt.Errorf("memory index scope %q is invalid", scope)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,scope,project_id,org_id,source_path,source_hash,title,summary,verdict,tags,created,content,tier FROM memory_entries WHERE scope=? AND project_id=? AND org_id=? AND tier='core' ORDER BY created DESC,title ASC,id ASC LIMIT ?`, scope, projectIDForScope(scope, projectID), orgIDForScope(scope, orgID), memoryIndexCoreTierCap)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryIndexDocument
	for rows.Next() {
		var doc MemoryIndexDocument
		if err := rows.Scan(&doc.ID, &doc.Scope, &doc.ProjectID, &doc.OrgID, &doc.SourcePath, &doc.SourceHash, &doc.Title, &doc.Summary, &doc.Verdict, &doc.Tags, &doc.Created, &doc.Content, &doc.Tier); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}

// DeleteMemoryIndexEntry removes one indexed entry from the configured buckets.
func (s *SQLite) DeleteMemoryIndexEntry(ctx context.Context, id, projectID, orgID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM memory_entries WHERE id=? AND ((scope='project' AND project_id=? AND org_id='') OR (scope='org' AND org_id=? AND project_id=''))`, id, projectID, orgID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrMemoryIndexEntryNotFound
	}
	return tx.Commit()
}

func projectIDForScope(scope, value string) string {
	if scope == "org" {
		return ""
	}
	return value
}

func orgIDForScope(scope, value string) string {
	if scope == "org" {
		return value
	}
	return ""
}
