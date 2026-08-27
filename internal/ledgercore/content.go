package ledgercore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// StoreContent persists bytes under a content-addressed reference.
func StoreContent(ctx context.Context, store storage.Store, ref string, data []byte) error {
	return store.PutContent(ctx, ref, data)
}

// LoadContent retrieves stored bytes under ref. It returns ErrContentNotFound if absent.
// When ref carries the "sha256:" prefix, the stored bytes are verified against
// the ref's embedded hex digest; other ref shapes are returned verbatim.
func LoadContent(ctx context.Context, store storage.Store, ref string) ([]byte, error) {
	data, err := store.GetContent(ctx, ref)
	if err != nil {
		if errors.Is(err, storage.ErrContentNotFound) {
			return nil, ErrContentNotFound
		}
		return nil, err
	}
	if hexDigest, ok := strings.CutPrefix(ref, "sha256:"); ok {
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if !strings.EqualFold(got, hexDigest) {
			return nil, fmt.Errorf("content digest mismatch for %q: sha256(data) = %s", ref, got)
		}
	}
	return data, nil
}
