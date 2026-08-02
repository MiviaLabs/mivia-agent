package remainder_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

// durableGrantStore is a ContentStore that also persists spool grants, like
// the sqlite-backed ledger repository. loadErr makes LoadContent fail with a
// real store fault after a grant has been recorded, so tests can drive the
// durable-grant fallback in Spool.Load both ways: bytes removed (ErrExpired)
// and store error (propagated verbatim).
type durableGrantStore struct {
	mu      sync.Mutex
	grants  map[string]map[string]bool
	data    map[string][]byte
	loadErr error
}

func newDurableGrantStore() *durableGrantStore {
	return &durableGrantStore{
		grants: make(map[string]map[string]bool),
		data:   make(map[string][]byte),
	}
}

func (d *durableGrantStore) StoreContent(_ context.Context, ref string, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	d.data[ref] = cp
	return nil
}

func (d *durableGrantStore) LoadContent(_ context.Context, ref string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loadErr != nil {
		return nil, d.loadErr
	}
	data, ok := d.data[ref]
	if !ok {
		return nil, remainder.ErrNotFound
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func (d *durableGrantStore) IsContentNotFound(err error) bool {
	return errors.Is(err, remainder.ErrNotFound)
}

func (d *durableGrantStore) GrantSpool(_ context.Context, ref, principal string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.grants[ref] == nil {
		d.grants[ref] = make(map[string]bool)
	}
	d.grants[ref][principal] = true
	return nil
}

func (d *durableGrantStore) CheckSpoolGrant(_ context.Context, ref, principal string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.grants[ref][principal], nil
}

func (d *durableGrantStore) dropContent(ref string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.data, ref)
}

// TestLoadDurableGrantWithMissingBytes covers the durable-grant fallback in
// Load: after a restart the in-memory grant map is empty, and when the durable
// grant survives but the bytes are gone the ref must read as ErrExpired (not
// denied and not not-found) - retention expiry, not a corrupt key.
func TestLoadDurableGrantWithMissingBytes(t *testing.T) {
	ctx := context.Background()
	store := newDurableGrantStore()
	spool := remainder.NewSpool(store)
	const principal = "session-a"
	body := []byte("durable-but-removed")

	ref := spool.Spool(ctx, principal, body)
	if ref == "" {
		t.Fatal("spool minted no ref")
	}
	store.dropContent(ref)

	fresh := remainder.NewSpool(store) // restart: empty in-memory grants
	_, err := fresh.Load(ctx, principal, ref)
	if err != remainder.ErrExpired {
		t.Fatalf("Load with durable grant but no bytes = %v, want ErrExpired", err)
	}
}

// TestLoadDurableGrantStoreError covers the other durable-grant fallback edge:
// when the grant exists but LoadContent fails with a real store error (not the
// not-found sentinel), the error is propagated verbatim rather than masked as
// an absence or a denial.
func TestLoadDurableGrantStoreError(t *testing.T) {
	ctx := context.Background()
	store := newDurableGrantStore()
	spool := remainder.NewSpool(store)
	const principal = "session-a"

	ref := spool.Spool(ctx, principal, []byte("durable-body"))
	if ref == "" {
		t.Fatal("spool minted no ref")
	}
	storeErr := errors.New("content store unavailable")
	store.loadErr = storeErr

	fresh := remainder.NewSpool(store)
	_, err := fresh.Load(ctx, principal, ref)
	if !errors.Is(err, storeErr) {
		t.Fatalf("Load with durable grant and store fault = %v, want %v", err, storeErr)
	}
}
