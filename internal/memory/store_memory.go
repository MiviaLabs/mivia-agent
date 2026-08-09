package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// In-memory backend (tests, ephemeral sessions, store_backend = "memory")
// ---------------------------------------------------------------------------

type memRow struct {
	e         Entry
	id, org   string
	createdAt time.Time
}

type memStore struct {
	mu      sync.RWMutex
	cfg     Config
	project []memRow
	org     []memRow
}

func newMemStore(cfg Config) *memStore {
	return &memStore{cfg: cfg}
}

func (s *memStore) Save(ctx context.Context, e Entry) (Result, error) {
	if err := e.Validate(s.cfg.limits()); err != nil {
		return Result{}, err
	}
	if e.Scope == ScopeOrg && s.cfg.OrgID == "" {
		return Result{}, errors.New("org scope is not configured: set [memory] org_id in the user config file")
	}
	if e.Created == "" {
		e.Created = time.Now().Format("2006-01-02")
	}
	rendered := e.Render()
	id := entryID(e.Scope, e.Title, rendered)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := &s.project
	org := ""
	if e.Scope == ScopeOrg {
		rows = &s.org
		org = s.cfg.OrgID
	}
	for _, row := range *rows {
		if row.id == id {
			return Result{ID: id, Scope: e.Scope, Org: org, Title: e.Title, Verdict: e.Verdict, Tags: append([]string(nil), e.Tags...), Created: e.Created, Snippet: e.Summary}, nil
		}
	}
	if len(*rows) >= s.cfg.MaxEntries {
		return Result{}, fmt.Errorf("memory store is full (max_entries=%d); consolidate or raise [memory] max_entries", s.cfg.MaxEntries)
	}
	*rows = append(*rows, memRow{e: e, id: id, org: org, createdAt: time.Now()})
	return Result{ID: id, Scope: e.Scope, Org: org, Title: e.Title, Verdict: e.Verdict, Tags: append([]string(nil), e.Tags...), Created: e.Created, Snippet: e.Summary}, nil
}

func (s *memStore) Search(ctx context.Context, q Query) ([]Result, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, errors.New("query is required")
	}
	limit := s.searchLimit(q.MaxResults)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Result
	switch q.Scope {
	case ScopeProject:
		out = s.matchRows(s.project, text, limit)
	case ScopeOrg:
		if s.cfg.OrgID == "" {
			return nil, nil
		}
		out = s.matchRows(s.org, text, limit)
	case ScopeAll:
		proj := s.matchRows(s.project, text, limit)
		var org []Result
		if s.cfg.OrgID != "" {
			org = s.matchRows(s.org, text, limit)
		}
		out = mergeRanked(proj, org, text, limit)
	default:
		return nil, fmt.Errorf("scope must be project, org, or all, got %q", q.Scope)
	}
	return out, nil
}

func (s *memStore) searchLimit(requested int) int {
	if requested <= 0 || requested > s.cfg.MaxSearchResults {
		return s.cfg.MaxSearchResults
	}
	return requested
}

func (s *memStore) matchRows(rows []memRow, text string, limit int) []Result {
	lowerText := strings.ToLower(text)
	type scored struct {
		r    Result
		rank int
	}
	matched := make([]scored, 0, len(rows))
	for _, row := range rows {
		// Match the same text the sqlite backend searches: the rendered
		// content includes the tags line and the references block, so the
		// in-memory backend must include them too (backend parity).
		body := strings.Join(row.e.Tags, ", ")
		if len(row.e.References) > 0 {
			body += "\n" + strings.Join(row.e.References, "\n")
		}
		body += "\n" + row.e.Good + "\n" + row.e.Bad + "\n" + row.e.Why
		rank := rankMatch(row.e.Title, row.e.Summary, body, lowerText)
		if rank < 0 {
			continue
		}
		matched = append(matched, scored{Result{
			ID:      row.id,
			Scope:   row.e.Scope,
			Org:     row.org,
			Title:   row.e.Title,
			Verdict: row.e.Verdict,
			Tags:    append([]string(nil), row.e.Tags...),
			Created: row.e.Created,
			Snippet: row.e.Summary,
		}, rank})
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].rank != matched[j].rank {
			return matched[i].rank < matched[j].rank
		}
		if matched[i].r.Created != matched[j].r.Created {
			return matched[i].r.Created > matched[j].r.Created
		}
		return matched[i].r.Title < matched[j].r.Title
	})
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out := make([]Result, len(matched))
	for i, m := range matched {
		out[i] = m.r
	}
	return out
}

func (s *memStore) Count(ctx context.Context, scope Scope) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch scope {
	case ScopeProject:
		return len(s.project), nil
	case ScopeOrg:
		return len(s.org), nil
	default:
		return 0, fmt.Errorf("scope must be project or org, got %q", scope)
	}
}

func (s *memStore) Close() error { return nil }
