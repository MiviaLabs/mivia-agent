package storage

import (
	"context"
	"fmt"
	"strings"
)

// SearchMemoryIndex searches the derived Markdown index. Scope "all" reads
// the project and configured organization buckets.
func (s *SQLite) SearchMemoryIndex(ctx context.Context, scope, projectID, orgID, text string, limit int) ([]MemoryIndexDocument, error) {
	if limit <= 0 {
		limit = 8
	}
	if scope != "project" && scope != "org" && scope != "all" {
		return nil, fmt.Errorf("memory index scope %q is invalid", scope)
	}
	if orgID != "" {
		normalized, err := normalizeMemoryOrgID(orgID)
		if err != nil {
			return nil, err
		}
		orgID = normalized
	}
	literal := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(text))
	where := []string{"(lower(title) LIKE ? ESCAPE '\\' OR lower(summary) LIKE ? ESCAPE '\\' OR lower(content) LIKE ? ESCAPE '\\')"}
	args := []any{"%" + literal + "%", "%" + literal + "%", "%" + literal + "%"}
	if scope == "project" || scope == "all" {
		where = append(where, "(scope='project' AND project_id=? AND org_id='')")
		args = append(args, projectID)
	}
	if scope == "org" || scope == "all" {
		where = append(where, "(scope='org' AND org_id=? AND project_id='')")
		args = append(args, orgID)
	}
	query := `SELECT id,scope,project_id,org_id,source_path,source_hash,title,summary,verdict,tags,created,content,tier FROM memory_entries WHERE ` + where[0] + ` AND (` + strings.Join(where[1:], " OR ") + `) ORDER BY created DESC,title ASC,id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
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

// CountMemoryIndex returns the number of indexed entries in one scope.
func (s *SQLite) CountMemoryIndex(ctx context.Context, scope, projectID, orgID string) (int, error) {
	if scope != "project" && scope != "org" {
		return 0, fmt.Errorf("memory index scope %q is invalid", scope)
	}
	query := `SELECT COUNT(*) FROM memory_entries WHERE scope=? AND project_id=? AND org_id=?`
	var count int
	if err := s.db.QueryRowContext(ctx, query, scope, projectID, orgID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count memory index: %w", err)
	}
	return count, nil
}
