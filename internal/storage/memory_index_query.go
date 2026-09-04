package storage

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// SearchMemoryIndex searches the derived Markdown index. Scope "all" reads
// the project and configured organization buckets.
func (s *SQLite) SearchMemoryIndex(ctx context.Context, scope, projectID, orgID, text string, limit int) ([]MemoryIndexDocument, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("query is required")
	}
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
	phrases := memoryQueryPhrases(text)
	tokens := memoryQueryTokens(text)
	if len(tokens) == 0 && len(phrases) == 0 {
		tokens = []string{text}
	}
	match := make([]string, 0, len(tokens))
	args := make([]any, 0, len(tokens)*3)
	for _, token := range tokens {
		literal := escapeMemoryLike(strings.ToLower(token))
		match = append(match, "(lower(title) LIKE ? ESCAPE '\\' OR lower(summary) LIKE ? ESCAPE '\\' OR lower(content) LIKE ? ESCAPE '\\')")
		args = append(args, "%"+literal+"%", "%"+literal+"%", "%"+literal+"%")
	}
	for _, phrase := range phrases {
		literal := escapeMemoryLike(strings.ToLower(phrase))
		match = append(match, "(lower(title) LIKE ? ESCAPE '\\' OR lower(summary) LIKE ? ESCAPE '\\' OR lower(content) LIKE ? ESCAPE '\\')")
		args = append(args, "%"+literal+"%", "%"+literal+"%", "%"+literal+"%")
	}
	scopes := make([]string, 0, 2)
	if scope == "project" || scope == "all" {
		scopes = append(scopes, "(scope='project' AND project_id=? AND org_id='')")
		args = append(args, projectID)
	}
	if scope == "org" || scope == "all" {
		scopes = append(scopes, "(scope='org' AND org_id=? AND project_id='')")
		args = append(args, orgID)
	}
	rankText := strings.ToLower(strings.ReplaceAll(text, `"`, ""))
	where := strings.Join(match, " AND ") + " AND (" + strings.Join(scopes, " OR ") + ")"
	rankCase := `CASE WHEN lower(title)=? THEN 0 WHEN lower(title) LIKE ? ESCAPE '\' THEN 1 WHEN lower(summary) LIKE ? ESCAPE '\' THEN 2 WHEN lower(content) LIKE ? ESCAPE '\' THEN 3`
	rankArgs := []any{rankText, "%" + escapeMemoryLike(rankText) + "%", "%" + escapeMemoryLike(rankText) + "%", "%" + escapeMemoryLike(rankText) + "%"}
	if len(tokens) > 0 {
		for rank, field := range []string{"title", "summary", "content"} {
			parts := make([]string, len(tokens))
			for i, token := range tokens {
				parts[i] = "lower(" + field + ") LIKE ? ESCAPE '\\'"
				rankArgs = append(rankArgs, "%"+escapeMemoryLike(strings.ToLower(token))+"%")
			}
			rankCase += " WHEN (" + strings.Join(parts, " AND ") + ") THEN " + fmt.Sprint(rank+4)
		}
	}
	query := `SELECT id,scope,project_id,org_id,source_path,source_hash,title,summary,verdict,tags,created,content,tier FROM memory_entries WHERE ` + where + ` ORDER BY ` + rankCase + ` ELSE 7 END, created DESC,title ASC,id ASC LIMIT ?`
	args = append(args, rankArgs...)
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

func escapeMemoryLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func memoryQueryTokens(value string) []string {
	stop := map[string]bool{"a": true, "an": true, "the": true, "of": true, "to": true, "for": true, "on": true, "in": true, "at": true, "with": true, "by": true, "and": true, "or": true, "from": true, "as": true, "is": true, "are": true, "was": true, "be": true, "it": true, "its": true, "that": true}
	seen := map[string]bool{}
	var out []string
	var current []rune
	flush := func() {
		if len(current) == 0 {
			return
		}
		token := strings.ToLower(string(current))
		current = nil
		if !stop[token] && !seen[token] && len(out) < 64 {
			seen[token] = true
			out = append(out, token)
		}
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if len(current) < 64 {
				current = append(current, r)
			}
		} else {
			flush()
		}
	}
	flush()
	return out
}

func memoryQueryPhrases(value string) []string {
	var phrases []string
	if memoryAllQuotesDoubled(value) {
		phrase := strings.TrimSpace(strings.ReplaceAll(value, `""`, `"`))
		if phrase != "" {
			return []string{phrase}
		}
	}
	for len(value) > 0 {
		start := strings.IndexByte(value, '"')
		if start < 0 {
			break
		}
		value = value[start+1:]
		end := strings.IndexByte(value, '"')
		if end < 0 {
			break
		}
		if phrase := strings.ReplaceAll(value[:end], `""`, `"`); phrase != "" {
			phrases = append(phrases, phrase)
		}
		value = value[end+1:]
	}
	return phrases
}

func memoryAllQuotesDoubled(value string) bool {
	saw := false
	for i := 0; i < len(value); i++ {
		if value[i] != '"' {
			continue
		}
		saw = true
		if i+1 >= len(value) || value[i+1] != '"' {
			return false
		}
		i++
	}
	return saw
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
