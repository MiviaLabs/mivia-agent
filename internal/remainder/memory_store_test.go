package remainder_test

// Content-addressing and idempotency of the in-process content store: the
// same body must mint the same ref (sdkadapter.Mint is deterministic),
// and storing it twice must not duplicate storage. Also covers the missing
// basics: Load roundtrip and not-found reporting.

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

func TestMemoryStoreIdempotent(t *testing.T) {
	ctx := context.Background()
	store := remainder.NewMemoryStore()
	body := []byte(strings.Repeat("idempotent-body-", 8))

	// Content addressing: the same data mints the same ref every time.
	first := sdkadapter.Mint(sdkadapter.KindOutput, body)
	if first == "" {
		t.Fatal("expected a non-empty ref for non-empty body")
	}
	if err := store.StoreContent(ctx, first, body); err != nil {
		t.Fatalf("first store: %v", err)
	}

	again := sdkadapter.Mint(sdkadapter.KindOutput, body)
	if again != first {
		t.Fatalf("same content minted different refs: %q vs %q", first, again)
	}
	if err := store.StoreContent(ctx, again, body); err != nil {
		t.Fatalf("duplicate store: %v", err)
	}

	if got := store.Len(); got != 1 {
		t.Fatalf("Len after duplicate store = %d, want 1 (no duplicate storage)", got)
	}
	// The stored body must still round-trip intact after the duplicate write.
	got, err := store.LoadContent(ctx, first)
	if err != nil {
		t.Fatalf("LoadContent after duplicate store: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("loaded body mismatch after duplicate store")
	}
}

func TestMemoryStoreDistinctContent(t *testing.T) {
	ctx := context.Background()
	store := remainder.NewMemoryStore()
	bodyA := []byte("distinct-body-a")
	bodyB := []byte("distinct-body-b")

	refA := sdkadapter.Mint(sdkadapter.KindOutput, bodyA)
	refB := sdkadapter.Mint(sdkadapter.KindOutput, bodyB)
	if refA == "" || refB == "" {
		t.Fatal("expected non-empty refs")
	}
	if refA == refB {
		t.Fatalf("distinct content collided on ref %q", refA)
	}

	if err := store.StoreContent(ctx, refA, bodyA); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreContent(ctx, refB, bodyB); err != nil {
		t.Fatal(err)
	}
	if got := store.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}

	gotA, err := store.LoadContent(ctx, refA)
	if err != nil || string(gotA) != string(bodyA) {
		t.Fatalf("LoadContent(refA) = %q, %v", gotA, err)
	}
	gotB, err := store.LoadContent(ctx, refB)
	if err != nil || string(gotB) != string(bodyB) {
		t.Fatalf("LoadContent(refB) = %q, %v", gotB, err)
	}
}

func TestMemoryStoreLoadRoundtrip(t *testing.T) {
	ctx := context.Background()
	store := remainder.NewMemoryStore()
	body := []byte("load-roundtrip-body")

	ref := sdkadapter.Mint(sdkadapter.KindOutput, body)
	if err := store.StoreContent(ctx, ref, body); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadContent(ctx, ref)
	if err != nil {
		t.Fatalf("LoadContent: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("loaded %q, want %q", got, body)
	}

	// Loaded bytes are a copy: mutating them must not corrupt the store.
	got[0] = 'X'
	again, err := store.LoadContent(ctx, ref)
	if err != nil {
		t.Fatalf("second LoadContent: %v", err)
	}
	if string(again) != string(body) {
		t.Fatalf("store corrupted by a mutation of a loaded copy: %q", again)
	}
}

func TestMemoryStoreMissingIsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store := remainder.NewMemoryStore()

	// A ref nobody stored is unknown: the package sentinel, not a store fault.
	ref := sdkadapter.Mint(sdkadapter.KindOutput, []byte("never-stored"))
	if _, err := store.LoadContent(ctx, ref); err != remainder.ErrNotFound {
		t.Fatalf("LoadContent(missing) = %v, want ErrNotFound", err)
	}
	if !store.IsContentNotFound(remainder.ErrNotFound) {
		t.Fatal("IsContentNotFound did not recognise ErrNotFound")
	}
}
