package ledgercore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestContent_StoreAndLoad(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()

	data := []byte("hello ledger content")
	ref := "ref:test:custom-123"

	// StoreContent
	if err := StoreContent(ctx, store, ref, data); err != nil {
		t.Fatalf("StoreContent failed: %v", err)
	}

	// LoadContent
	got, err := LoadContent(ctx, store, ref)
	if err != nil {
		t.Fatalf("LoadContent failed: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("LoadContent mismatch: got %q, want %q", got, data)
	}

	// LoadContent on missing ref returns ErrContentNotFound
	_, err = LoadContent(ctx, store, "ref:missing")
	if !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("expected ErrContentNotFound, got %v", err)
	}

	// Failing store
	errStore := &plainStore{err: errors.New("db fail")}
	if err := StoreContent(ctx, errStore, ref, data); err == nil {
		t.Fatalf("expected error from StoreContent with failing store")
	}
	if _, err := LoadContent(ctx, errStore, ref); err == nil {
		t.Fatalf("expected error from LoadContent with failing store")
	}
}

func TestContent_Sha256Verification(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()

	data := []byte("content to hash")
	sum := sha256.Sum256(data)
	validRef := "sha256:" + hex.EncodeToString(sum[:])

	// Store valid
	if err := StoreContent(ctx, store, validRef, data); err != nil {
		t.Fatalf("StoreContent failed: %v", err)
	}

	// Load valid
	got, err := LoadContent(ctx, store, validRef)
	if err != nil {
		t.Fatalf("LoadContent valid failed: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("got %q, want %q", got, data)
	}

	// Store corrupted bytes under validRef directly in store
	_ = store.PutContent(ctx, validRef, []byte("corrupted bytes"))
	_, err = LoadContent(ctx, store, validRef)
	if err == nil {
		t.Fatalf("expected error on sha256 mismatch, got nil")
	}
}
