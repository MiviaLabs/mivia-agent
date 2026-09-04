package cliagents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

var ErrMarkdownStoreUnsupported = errors.New("memory: operation is not supported by markdown store")
var ErrMarkdownIndexStale = errors.New("memory: markdown saved but index is stale")

type MarkdownStoreConfig struct {
	Source           memory.MarkdownSource
	Index            *storage.SQLite
	ProjectID, OrgID string
	MaxSearchResults int
	Limits           memory.Limits
	ReadOnly         bool
}

type markdownStore struct{ cfg MarkdownStoreConfig }

var _ memory.Store = (*markdownStore)(nil)

func OpenMarkdownStore(ctx context.Context, cfg MarkdownStoreConfig) (memory.Store, error) {
	if cfg.Index == nil {
		return nil, errors.New("memory: markdown index is required")
	}
	if cfg.ProjectID == "" {
		return nil, errors.New("memory: project ID is required")
	}
	if cfg.MaxSearchResults <= 0 {
		cfg.MaxSearchResults = memory.DefaultMaxSearchResults
	}
	s := &markdownStore{cfg: cfg}
	if !cfg.ReadOnly {
		if err := s.refresh(ctx, memory.ScopeProject); err != nil {
			return nil, err
		}
		if cfg.OrgID != "" {
			if err := s.refresh(ctx, memory.ScopeOrg); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

func (s *markdownStore) refresh(ctx context.Context, scope memory.Scope) error {
	docs, err := s.cfg.Source.Scan(ctx, scope)
	if err != nil {
		return err
	}
	rows := make([]storage.MemoryIndexDocument, 0, len(docs))
	for _, doc := range docs {
		rows = append(rows, storage.MemoryIndexDocument{ID: doc.ID, Scope: string(scope), ProjectID: projectID(scope, s.cfg.ProjectID), OrgID: orgID(scope, s.cfg.OrgID), SourcePath: doc.Path, SourceHash: doc.Hash, Title: doc.Entry.Title, Summary: doc.Entry.Summary, Verdict: string(doc.Entry.Verdict), Tags: strings.Join(doc.Entry.Tags, ", "), Created: doc.Entry.Created, Content: doc.Entry.Render()})
	}
	if err := s.cfg.Index.SyncMemoryIndex(ctx, string(scope), projectID(scope, s.cfg.ProjectID), orgID(scope, s.cfg.OrgID), rows); err != nil {
		return fmt.Errorf("%w: %v", ErrMarkdownIndexStale, err)
	}
	return nil
}

func (s *markdownStore) Save(ctx context.Context, e memory.Entry) (memory.Result, error) {
	if s.cfg.ReadOnly {
		return memory.Result{}, errors.New("memory store is read-only")
	}
	if err := e.Validate(s.cfg.Limits); err != nil {
		return memory.Result{}, err
	}
	doc, err := s.cfg.Source.Save(ctx, e)
	if err != nil {
		return memory.Result{}, err
	}
	if err := s.refresh(ctx, e.Scope); err != nil {
		return memory.Result{}, err
	}
	return memory.Result{ID: doc.ID, Scope: e.Scope, Org: s.cfg.OrgID, Title: e.Title, Verdict: e.Verdict, Tags: append([]string(nil), e.Tags...), Created: doc.Entry.Created, Snippet: e.Summary}, nil
}

func (s *markdownStore) Search(ctx context.Context, q memory.Query) ([]memory.Result, error) {
	if !s.cfg.ReadOnly {
		if q.Scope == memory.ScopeAll || q.Scope == memory.ScopeProject {
			if err := s.refresh(ctx, memory.ScopeProject); err != nil {
				return nil, err
			}
		}
		if (q.Scope == memory.ScopeAll || q.Scope == memory.ScopeOrg) && s.cfg.OrgID != "" {
			if err := s.refresh(ctx, memory.ScopeOrg); err != nil {
				return nil, err
			}
		}
	}
	rows, err := s.cfg.Index.SearchMemoryIndex(ctx, string(q.Scope), s.cfg.ProjectID, s.cfg.OrgID, q.Text, searchLimit(q.MaxResults, s.cfg.MaxSearchResults))
	if err != nil {
		return nil, err
	}
	out := make([]memory.Result, 0, len(rows))
	for _, row := range rows {
		out = append(out, memory.Result{ID: row.ID, Scope: memory.Scope(row.Scope), Org: row.OrgID, Title: row.Title, Verdict: memory.Verdict(row.Verdict), Tags: splitTags(row.Tags), Created: row.Created, Snippet: row.Summary})
	}
	return out, nil
}

func (s *markdownStore) Count(ctx context.Context, scope memory.Scope) (int, error) {
	if !s.cfg.ReadOnly {
		if err := s.refresh(ctx, scope); err != nil {
			return 0, err
		}
	}
	return s.cfg.Index.CountMemoryIndex(ctx, string(scope), projectID(scope, s.cfg.ProjectID), orgID(scope, s.cfg.OrgID))
}
func (*markdownStore) PromoteToCore(context.Context, string) error {
	return ErrMarkdownStoreUnsupported
}
func (*markdownStore) CoreEntries(context.Context, memory.Scope) ([]memory.Result, error) {
	return nil, ErrMarkdownStoreUnsupported
}
func (*markdownStore) Delete(context.Context, string) error { return ErrMarkdownStoreUnsupported }
func (*markdownStore) Close() error                         { return nil }
func projectID(scope memory.Scope, value string) string {
	if scope == memory.ScopeOrg {
		return ""
	}
	return value
}
func orgID(scope memory.Scope, value string) string {
	if scope == memory.ScopeOrg {
		return value
	}
	return ""
}
func searchLimit(requested, maximum int) int {
	if requested <= 0 || requested > maximum {
		return maximum
	}
	return requested
}
func splitTags(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
