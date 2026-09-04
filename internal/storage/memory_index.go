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
// operator-selected tier for files that still exist.
func (s *SQLite) SyncMemoryIndex(ctx context.Context, scope, projectID, orgID string, docs []MemoryIndexDocument) error {
	if scope != "project" && scope != "org" {
		return fmt.Errorf("memory index scope %q is invalid", scope)
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
	args := []any{scope, projectID, orgID}
	rows, err := tx.QueryContext(ctx, `SELECT source_path FROM memory_sources WHERE scope=? AND project_id=? AND org_id=?`, args...)
	if err != nil {
		return err
	}
	old := make(map[string]struct{})
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return err
		}
		old[path] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, doc := range docs {
		if doc.Scope != scope {
			return fmt.Errorf("memory index scope %q does not match the scan scope %q", doc.Scope, scope)
		}
		delete(old, doc.SourcePath)
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

func validateMemoryIndexDocuments(scope, projectID, orgID string, docs []MemoryIndexDocument) error {
	if scope == "project" && strings.TrimSpace(projectID) == "" {
		return errors.New("memory index project_id is required for a project scan")
	}
	if scope == "org" && strings.TrimSpace(orgID) == "" {
		return errors.New("memory index org_id is required for an organization scan")
	}
	seen := make(map[string]struct{}, len(docs))
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
		key := doc.Scope + "\x00" + doc.ProjectID + "\x00" + doc.OrgID + "\x00" + doc.SourcePath
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate memory index source %q", doc.SourcePath)
		}
		seen[key] = struct{}{}
	}
	return nil
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
