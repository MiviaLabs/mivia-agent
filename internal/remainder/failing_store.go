package remainder

import (
	"context"
	"errors"
)

// ErrStoreUnavailable is returned by FailingStore on every write.
var ErrStoreUnavailable = errors.New("remainder content store unavailable")

// FailingStore implements ContentStore but rejects every StoreContent call.
// Used to prove INV-CE-07-C / INV-AG-10: a failed spool omits the ref.
type FailingStore struct{}

// StoreContent always fails.
func (FailingStore) StoreContent(context.Context, string, []byte) error {
	return ErrStoreUnavailable
}

// LoadContent always reports not found.
func (FailingStore) LoadContent(context.Context, string) ([]byte, error) {
	return nil, ErrNotFound
}

// IsContentNotFound always true for the not-found sentinel.
func (FailingStore) IsContentNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
