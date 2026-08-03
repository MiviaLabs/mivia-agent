package ledger

import "context"

// spoolGrantStore is the optional durable remainder-grant surface of the
// underlying storage.Store. It is deliberately not part of storage.Store: only
// sqlite-backed stores persist spool grants, so the repository forwards only
// when the store actually implements it. The method set matches
// remainder.SpoolGrantStore structurally, so the repository satisfies that
// interface without importing the remainder package.
type spoolGrantStore interface {
	GrantSpool(ctx context.Context, ref, principal string) error
	CheckSpoolGrant(ctx context.Context, ref, principal string) (bool, error)
}

// GrantSpool forwards a durable remainder grant to the underlying store when
// it supports one. Stores without the surface ignore the grant, which keeps
// in-process-only visibility for memory-backed repositories.
func (s *StorageLedgerRepository) GrantSpool(ctx context.Context, ref, principal string) error {
	if grantStore, ok := s.store.(spoolGrantStore); ok {
		return grantStore.GrantSpool(ctx, ref, principal)
	}
	return nil
}

// CheckSpoolGrant forwards a durable remainder grant lookup to the underlying
// store when it supports one, reporting no durable grant otherwise.
func (s *StorageLedgerRepository) CheckSpoolGrant(ctx context.Context, ref, principal string) (bool, error) {
	if grantStore, ok := s.store.(spoolGrantStore); ok {
		return grantStore.CheckSpoolGrant(ctx, ref, principal)
	}
	return false, nil
}
