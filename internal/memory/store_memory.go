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
	tier      string // "core" or "" (treated as "archive")
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

// Save on this backend has no consolidation trigger.
// (plan 76, D2): consolidation was scoped to the removed durable database -
// it exists to keep a committed database growing gracefully, and this backend has
// nothing committed to grow. A store_backend = "memory" config still hits
// the hard MaxEntries refusal at the cap with no auto-consolidation; this is
// an intentional asymmetry, not an oversight (confirmed in Step 5 review).
func (s *memStore) Save(ctx context.Context, e Entry) (Result, error) {
	if s.cfg.ReadOnly {
		return Result{}, errors.New("memory store is read-only")
	}
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
	org := ""
	if e.Scope == ScopeOrg {
		org = s.cfg.OrgID
	}
	// The content-addressed id includes the org identity, so an identical entry
	// under a different org id cannot
	// collide with an existing row.
	id := entryID(e.Scope, org, e.Title, rendered)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := &s.project
	if e.Scope == ScopeOrg {
		rows = &s.org
	}
	for _, row := range *rows {
		// Defense-in-depth: the id is already org-namespaced, but the
		// dedup loop also requires the row to belong to the same org so a
		// stale or cross-org row can never answer another org's save.
		if row.id == id && row.org == org {
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
		out = mergeRanked(proj, org, parseQuery(text), text, limit)
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
	p := parseQuery(text)
	if p.zeroToken {
		return s.matchParsed(rows, text, p, limit)
	}
	res, _ := relaxSearch(p.tokens, func(tokens []string) ([]Result, error) {
		pp := p
		pp.tokens = tokens
		return s.matchParsed(rows, text, pp, limit), nil
	})
	return res
}

func (s *memStore) matchParsed(rows []memRow, text string, p parsedQuery, limit int) []Result {
	lowerText := strings.ToLower(text)
	type scored struct {
		r    Result
		rank int
	}
	matched := make([]scored, 0, len(rows))
	for _, row := range rows {
		// Search matches lower(content), and the
		// content column holds the FULL rendered Markdown (e.Render(), stored
		// by Save on both backends). The in-memory backend must search the same
		// text, so a query matching only rendered metadata - the verdict or
		// scope line, the created line, a section heading, tags, or references
		// - returns the same results on both backends.
		rank := rankMatch(row.e.Title, row.e.Summary, row.e.Render(), lowerText, p)
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
		if matched[i].r.Title != matched[j].r.Title {
			return matched[i].r.Title < matched[j].r.Title
		}
		return matched[i].r.ID < matched[j].r.ID
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

func (s *memStore) PromoteToCore(ctx context.Context, id string) error {
	if s.cfg.ReadOnly {
		return errors.New("memory store is read-only")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rows := range [][]memRow{s.project, s.org} {
		for i := range rows {
			if rows[i].id != id {
				continue
			}
			if rows[i].tier == "core" {
				return nil
			}
			var count int
			for _, r := range rows {
				if r.tier == "core" && r.org == rows[i].org {
					count++
				}
			}
			if count >= CoreTierCap {
				return ErrCoreTierFull
			}
			rows[i].tier = "core"
			return nil
		}
	}
	return ErrEntryNotFound
}

func (s *memStore) CoreEntries(ctx context.Context, scope Scope) ([]Result, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rows []memRow
	switch scope {
	case ScopeProject:
		rows = s.project
	case ScopeOrg:
		if s.cfg.OrgID == "" {
			return nil, nil
		}
		rows = s.org
	default:
		return nil, fmt.Errorf("scope must be project or org, got %q", scope)
	}
	var out []Result
	for _, row := range rows {
		if row.tier != "core" {
			continue
		}
		out = append(out, Result{
			ID: row.id, Scope: row.e.Scope, Org: row.org, Title: row.e.Title,
			Verdict: row.e.Verdict, Tags: append([]string(nil), row.e.Tags...),
			Created: row.e.Created, Snippet: row.e.Summary,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > CoreTierCap {
		out = out[:CoreTierCap]
	}
	return out, nil
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

// Delete removes one entry (by id) from the project or org slice. Returns
// ErrEntryNotFound if
// no row with that id exists. Refused on a read-only store.
func (s *memStore) Delete(ctx context.Context, id string) error {
	if s.cfg.ReadOnly {
		return errors.New("memory store is read-only")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rows := range []*[]memRow{&s.project, &s.org} {
		for i := range *rows {
			if (*rows)[i].id == id {
				*rows = append((*rows)[:i], (*rows)[i+1:]...)
				return nil
			}
		}
	}
	return ErrEntryNotFound
}

func (s *memStore) Close() error { return nil }
