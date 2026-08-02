// Package remainder owns the truncated-result spool and its caller-scoped
// visibility grants.
//
// When a tool result is shortened, the full body is stored under a content
// reference and the principal that received the truncation notice is granted
// read access. The model pages that body via the host's read_output tool.
//
// Invariants:
//   - INV-AG-10 / INV-CE-07-A: a reference is handed to the model only after a
//     successful store; a store failure yields an empty ref (plain notice).
//   - INV-CE-07-C: store failure must not fail the tool call.
//   - Visibility is caller-scoped: only the principal that received the grant
//     may Load the ref (stricter than open ledger_read digest lookup).
package remainder

import (
	"context"
	"errors"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/contentref"
)

// Sentinel load failures. The read_output tool maps each to a distinct
// model-visible status so callers can tell them apart.
var (
	// ErrNotFound means the ref is unknown (never spooled, or never stored).
	ErrNotFound = errors.New("remainder not found")
	// ErrDenied means content exists (or was granted to someone else) but the
	// calling principal was not the recipient of the remainder ref.
	ErrDenied = errors.New("remainder access denied")
	// ErrExpired means the principal once held a grant but the remainder is no
	// longer available (retention expiry or explicit expiry). Distinct from
	// not-found so the model does not treat a timed-out ref as a corrupt key.
	ErrExpired = errors.New("remainder expired")
)

// ContentStore is the durable byte store the spool writes to. ledger
// repositories satisfy it; the interface keeps this package free of ledger
// imports so agent and runtime layers can use it without cycles.
type ContentStore interface {
	StoreContent(ctx context.Context, ref string, data []byte) error
	LoadContent(ctx context.Context, ref string) ([]byte, error)
}

// NotFoundReporter is implemented by stores that surface a typed not-found
// error (ledger.ErrContentNotFound). When the store does not implement it,
// any LoadContent error is treated as not-found for visibility decisions.
type NotFoundReporter interface {
	IsContentNotFound(err error) bool
}

// Spool stores truncated tool-result bodies and gates reads by principal.
type Spool struct {
	store ContentStore

	mu     sync.RWMutex
	grants map[string]map[string]grant // ref → principal → grant
}

type grant struct {
	expired bool
}

// mintReference is a test seam. Reference cannot return "" for non-empty data
// under a known kind, but INV-AG-10 says a mint that produced nothing must not
// become a ref, so the check stays and is exercised through the seam.
var mintReference = contentref.Reference

// NewSpool returns a principal-scoped remainder spool over store.
// A nil store is allowed: Spool then never mints refs (degrades to plain notices).
func NewSpool(store ContentStore) *Spool {
	return &Spool{
		store:  store,
		grants: make(map[string]map[string]grant),
	}
}

// Spool stores the full body under a content-addressed output ref and grants
// principal read access. It returns the minted ref, or "" when the body is
// empty, the principal is empty, the store is nil, or the write fails.
// A failed write never invents a ref (INV-AG-10 / INV-CE-07-A/C).
func (s *Spool) Spool(ctx context.Context, principal string, data []byte) string {
	if s == nil || s.store == nil || principal == "" || len(data) == 0 {
		return ""
	}
	ref := mintReference(contentref.KindOutput, data)
	if ref == "" {
		return ""
	}
	if err := s.store.StoreContent(ctx, ref, data); err != nil {
		return ""
	}
	s.mu.Lock()
	if s.grants[ref] == nil {
		s.grants[ref] = make(map[string]grant)
	}
	// Successful re-spool refreshes an expired grant for this principal.
	s.grants[ref][principal] = grant{}
	s.mu.Unlock()
	return ref
}

// Load returns the stored body when principal holds a live grant.
// Errors are the package sentinels ErrNotFound, ErrDenied, or ErrExpired.
func (s *Spool) Load(ctx context.Context, principal, ref string) ([]byte, error) {
	if s == nil || s.store == nil {
		return nil, ErrNotFound
	}
	if principal == "" {
		return nil, ErrDenied
	}

	s.mu.RLock()
	principalGrants := s.grants[ref]
	g, hasGrant := principalGrants[principal]
	s.mu.RUnlock()

	if hasGrant {
		if g.expired {
			return nil, ErrExpired
		}
		data, err := s.store.LoadContent(ctx, ref)
		if err != nil {
			if isNotFound(s.store, err) {
				// Grant existed; bytes are gone → retention expiry, not a bad key.
				return nil, ErrExpired
			}
			return nil, err
		}
		return data, nil
	}

	// No grant for this principal. Distinguish cross-principal denial from a
	// completely unknown ref by probing the store (bytes may exist for others).
	_, err := s.store.LoadContent(ctx, ref)
	if err != nil {
		if isNotFound(s.store, err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return nil, ErrDenied
}

// MarkExpired marks every grant on ref as expired. Used by retention and tests.
// The stored bytes may still be present; Load reports ErrExpired either way.
func (s *Spool) MarkExpired(ref string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for principal, g := range s.grants[ref] {
		g.expired = true
		s.grants[ref][principal] = g
	}
}

func isNotFound(store ContentStore, err error) bool {
	if err == nil {
		return false
	}
	if reporter, ok := store.(NotFoundReporter); ok {
		return reporter.IsContentNotFound(err)
	}
	// Fallback: treat any error as not-found only when the store has no typed
	// reporter - prefer false so a real store fault is not masked as absence.
	return errors.Is(err, ErrNotFound)
}

// ContentStoreAdapter adapts a store whose not-found sentinel is known.
type ContentStoreAdapter struct {
	Store         ContentStore
	NotFoundError error
}

// StoreContent forwards to the wrapped store.
func (a ContentStoreAdapter) StoreContent(ctx context.Context, ref string, data []byte) error {
	if a.Store == nil {
		return errors.New("remainder: nil content store")
	}
	return a.Store.StoreContent(ctx, ref, data)
}

// LoadContent forwards to the wrapped store.
func (a ContentStoreAdapter) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	if a.Store == nil {
		return nil, ErrNotFound
	}
	return a.Store.LoadContent(ctx, ref)
}

// IsContentNotFound reports whether err is the configured not-found sentinel.
func (a ContentStoreAdapter) IsContentNotFound(err error) bool {
	return a.NotFoundError != nil && errors.Is(err, a.NotFoundError)
}
