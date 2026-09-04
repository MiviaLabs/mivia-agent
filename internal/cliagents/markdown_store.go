package cliagents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

var ErrMarkdownStoreUnsupported = errors.New("memory: operation is not supported by markdown store")
var ErrMarkdownIndexStale = errors.New("memory: markdown saved but index is stale")

// markdownSource is the store's view of the Markdown files: the flat
// per-scope operations plus the derived directory accessors the reconciler
// watches. memory.MarkdownSource is the production implementation; tests
// wrap it to count scans.
type markdownSource interface {
	Scan(ctx context.Context, scope memory.Scope) ([]memory.MarkdownDocument, error)
	Save(ctx context.Context, e memory.Entry) (memory.MarkdownDocument, error)
	Delete(ctx context.Context, path string) error
	ProjectDir() string
	OrgDir() string
}

type MarkdownStoreConfig struct {
	Source           markdownSource
	Index            *storage.SQLite
	ProjectID, OrgID string
	MaxSearchResults int
	Limits           memory.Limits
	ReadOnly         bool
}

type markdownStore struct {
	cfg MarkdownStoreConfig
	mu  sync.Mutex

	// Freshness state, guarded by mu. A reconciler-attached store skips the
	// pre-read refresh for a scope only while the scope is neither degraded
	// nor older than the fallback TTL; on any doubt the read path rescans.
	lastSync           map[memory.Scope]time.Time
	degraded           map[memory.Scope]bool
	reconcilerAttached bool
	fallback           time.Duration
}

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
	if err := s.refresh(ctx, memory.ScopeProject); err != nil {
		return nil, err
	}
	if cfg.OrgID != "" {
		if err := s.refresh(ctx, memory.ScopeOrg); err != nil {
			return nil, err
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

// syncScope scans one scope, applies one index sync, and stamps freshness on
// success. It takes s.mu, so the reconciler serializes with every store
// operation on the same lock.
func (s *markdownStore) syncScope(ctx context.Context, scope memory.Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncScopeLocked(ctx, scope)
}

// syncScopeLocked is syncScope for callers already holding s.mu.
func (s *markdownStore) syncScopeLocked(ctx context.Context, scope memory.Scope) error {
	if err := s.refresh(ctx, scope); err != nil {
		return err
	}
	s.stampSync(scope)
	return nil
}

// stampSync records a successful sync of scope. Callers hold s.mu.
func (s *markdownStore) stampSync(scope memory.Scope) {
	if s.lastSync == nil {
		s.lastSync = make(map[memory.Scope]time.Time)
	}
	s.lastSync[scope] = time.Now()
}

// scopeIsFresh reports whether the pre-read refresh may be skipped: a
// reconciler owns the store, the scope's watch is healthy, and the last sync
// is inside the fallback TTL. Callers hold s.mu.
func (s *markdownStore) scopeIsFresh(scope memory.Scope) bool {
	if !s.reconcilerAttached || s.fallback <= 0 || s.degraded[scope] {
		return false
	}
	last, ok := s.lastSync[scope]
	return ok && time.Since(last) < s.fallback
}

// refreshForRead refreshes scope before a read unless the reconciler keeps it
// fresh. Callers hold s.mu.
func (s *markdownStore) refreshForRead(ctx context.Context, scope memory.Scope) error {
	if s.scopeIsFresh(scope) {
		return nil
	}
	if err := s.refresh(ctx, scope); err != nil {
		return err
	}
	s.stampSync(scope)
	return nil
}

// setDegraded marks a scope's watch unhealthy or recovered. The reconciler
// calls it; degraded scopes always rescan on read.
func (s *markdownStore) setDegraded(scope memory.Scope, degraded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.degraded == nil {
		s.degraded = make(map[memory.Scope]bool)
	}
	s.degraded[scope] = degraded
}

func (s *markdownStore) Save(ctx context.Context, e memory.Entry) (memory.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if err := s.syncScopeLocked(ctx, e.Scope); err != nil {
		return memory.Result{}, err
	}
	return memory.Result{ID: doc.ID, Scope: e.Scope, Org: s.cfg.OrgID, Title: e.Title, Verdict: e.Verdict, Tags: append([]string(nil), e.Tags...), Created: doc.Entry.Created, Snippet: e.Summary}, nil
}

func (s *markdownStore) Search(ctx context.Context, q memory.Query) ([]memory.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.Scope == memory.ScopeAll || q.Scope == memory.ScopeProject {
		if err := s.refreshForRead(ctx, memory.ScopeProject); err != nil {
			return nil, err
		}
	}
	if (q.Scope == memory.ScopeAll || q.Scope == memory.ScopeOrg) && s.cfg.OrgID != "" {
		if err := s.refreshForRead(ctx, memory.ScopeOrg); err != nil {
			return nil, err
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshForRead(ctx, scope); err != nil {
		return 0, err
	}
	return s.cfg.Index.CountMemoryIndex(ctx, string(scope), projectID(scope, s.cfg.ProjectID), orgID(scope, s.cfg.OrgID))
}
func (s *markdownStore) PromoteToCore(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.ReadOnly {
		return errors.New("memory store is read-only")
	}
	if err := s.refreshForRead(ctx, memory.ScopeProject); err != nil {
		return err
	}
	if s.cfg.OrgID != "" {
		if err := s.refreshForRead(ctx, memory.ScopeOrg); err != nil {
			return err
		}
	}
	err := s.cfg.Index.PromoteMemoryIndexEntry(ctx, id, s.cfg.ProjectID, s.cfg.OrgID)
	if errors.Is(err, storage.ErrMemoryIndexEntryNotFound) {
		return memory.ErrEntryNotFound
	}
	return err
}
func (s *markdownStore) CoreEntries(ctx context.Context, scope memory.Scope) ([]memory.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if scope != memory.ScopeProject && scope != memory.ScopeOrg {
		return nil, fmt.Errorf("scope must be project or org, got %q", scope)
	}
	if err := s.refreshForRead(ctx, scope); err != nil {
		return nil, err
	}
	rows, err := s.cfg.Index.CoreMemoryIndexEntries(ctx, string(scope), s.cfg.ProjectID, s.cfg.OrgID)
	if err != nil {
		return nil, err
	}
	return indexResults(rows), nil
}
func (s *markdownStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.ReadOnly {
		return errors.New("memory store is read-only")
	}
	if err := s.refresh(ctx, memory.ScopeProject); err != nil {
		return err
	}
	if s.cfg.OrgID != "" {
		if err := s.refresh(ctx, memory.ScopeOrg); err != nil {
			return err
		}
	}
	doc, err := s.cfg.Index.FindMemoryIndexEntry(ctx, id, s.cfg.ProjectID, s.cfg.OrgID)
	if errors.Is(err, storage.ErrMemoryIndexEntryNotFound) {
		return memory.ErrEntryNotFound
	}
	if err != nil {
		return err
	}
	if err := s.cfg.Source.Delete(ctx, doc.SourcePath); err != nil {
		return err
	}
	if err := s.syncScopeLocked(ctx, memory.Scope(doc.Scope)); err != nil {
		return err
	}
	return nil
}
func (*markdownStore) Close() error { return nil }
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

func indexResults(rows []storage.MemoryIndexDocument) []memory.Result {
	out := make([]memory.Result, 0, len(rows))
	for _, row := range rows {
		out = append(out, memory.Result{ID: row.ID, Scope: memory.Scope(row.Scope), Org: row.OrgID, Title: row.Title, Verdict: memory.Verdict(row.Verdict), Tags: splitTags(row.Tags), Created: row.Created, Snippet: row.Summary})
	}
	return out
}
