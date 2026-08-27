// Package ledgercore provides shared primitives and infrastructure for event-sourced
// ledger implementations in the mivia agent.
package ledgercore

import (
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// Common ledger error sentinels.
var (
	// ErrNotFound is returned when a requested run or task does not exist.
	ErrNotFound = errors.New("ledger: not found")

	// ErrConflict is returned on optimistic concurrency failures (e.g. CAS mismatch).
	ErrConflict = errors.New("ledger: conflict")

	// ErrDuplicate is returned when creating a run or task with an existing ID or key.
	ErrDuplicate = errors.New("ledger: duplicate")

	// ErrClaimHeld is returned when an exclusive run execution claim is held by another process.
	ErrClaimHeld = errors.New("ledger: run claim held by another executor")

	// ErrClaimNotHeld is returned when releasing or refreshing a claim not owned by the caller.
	ErrClaimNotHeld = errors.New("ledger: run claim not held by this caller")

	// ErrClosed is returned when an operation is attempted on a closed ledger.
	ErrClosed = errors.New("ledger: closed")

	// ErrInvalidTransition is returned when a state transition is not allowed.
	ErrInvalidTransition = errors.New("ledger: invalid state transition")

	// ErrContentNotFound is returned when content-addressed bytes are not found in the store.
	ErrContentNotFound = errors.New("ledger: content not found")
)

// MapStorageError maps storage package error sentinels to ledgercore error sentinels.
func MapStorageError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, storage.ErrClaimHeld):
		return ErrClaimHeld
	case errors.Is(err, storage.ErrClaimNotHeld):
		return ErrClaimNotHeld
	case errors.Is(err, storage.ErrDuplicate):
		return ErrDuplicate
	case errors.Is(err, storage.ErrContentNotFound):
		return ErrContentNotFound
	default:
		return err
	}
}
