package remainder

// Visibility and degradation paths: a spool with no store, a caller with no
// principal, bytes that vanished under a live grant, and a store fault that
// must not be reported as absence.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type faultyStore struct{ MemoryStore }

var errStoreFault = errors.New("disk on fire")

func (f *faultyStore) LoadContent(context.Context, string) ([]byte, error) {
	return nil, errStoreFault
}

// untypedStore has no IsContentNotFound, so isNotFound falls back to the
// package sentinel.
type untypedStore struct{ err error }

func (untypedStore) StoreContent(context.Context, string, []byte) error { return nil }
func (u untypedStore) LoadContent(context.Context, string) ([]byte, error) {
	return nil, u.err
}

func TestSpoolMintsNoRefWithoutAStoreOrAPrincipal(t *testing.T) {
	ctx := context.Background()
	var nilSpool *Spool
	if ref := nilSpool.Spool(ctx, "p", []byte("body")); ref != "" {
		t.Fatalf("nil spool minted %q", ref)
	}
	if ref := NewSpool(nil).Spool(ctx, "p", []byte("body")); ref != "" {
		t.Fatalf("storeless spool minted %q", ref)
	}
	s := NewSpool(NewMemoryStore())
	if ref := s.Spool(ctx, "", []byte("body")); ref != "" {
		t.Fatalf("principal-less spool minted %q", ref)
	}
	if ref := s.Spool(ctx, "p", nil); ref != "" {
		t.Fatalf("empty body minted %q", ref)
	}
	if ref := NewSpool(FailingStore{}).Spool(ctx, "p", []byte("body")); ref != "" {
		t.Fatalf("failed store minted %q", ref)
	}

	restore := mintReference
	mintReference = func(string, []byte) string { return "" }
	defer func() { mintReference = restore }()
	if ref := s.Spool(ctx, "p", []byte("body")); ref != "" {
		t.Fatalf("an empty mint became the ref %q", ref)
	}
	if _, err := s.Load(ctx, "p", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unminted body left a grant behind: %v", err)
	}
}

func TestLoadRefusesWithoutAStoreOrAPrincipal(t *testing.T) {
	ctx := context.Background()
	if _, err := NewSpool(nil).Load(ctx, "p", "ref:output:x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("storeless Load = %v, want ErrNotFound", err)
	}
	if _, err := NewSpool(NewMemoryStore()).Load(ctx, "", "ref:output:x"); !errors.Is(err, ErrDenied) {
		t.Fatalf("principal-less Load = %v, want ErrDenied", err)
	}
}

func TestLoadReportsExpiryWhenTheBytesAreGone(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	s := NewSpool(store)
	ref := s.Spool(ctx, "owner", []byte("body"))
	if ref == "" {
		t.Fatal("spool minted no ref")
	}

	store.Delete(ref)
	if _, err := s.Load(ctx, "owner", ref); !errors.Is(err, ErrExpired) {
		t.Fatalf("Load after deletion = %v, want ErrExpired", err)
	}

	// A ref nobody holds and nothing stores is simply unknown.
	if _, err := s.Load(ctx, "stranger", ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load of a vanished ref by a stranger = %v, want ErrNotFound", err)
	}

	// Re-spooling refreshes the grant, and an explicit expiry overrides it.
	again := s.Spool(ctx, "owner", []byte("body"))
	if _, err := s.Load(ctx, "stranger", again); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-principal Load = %v, want ErrDenied", err)
	}
	s.MarkExpired(again)
	if _, err := s.Load(ctx, "owner", again); !errors.Is(err, ErrExpired) {
		t.Fatalf("Load after MarkExpired = %v, want ErrExpired", err)
	}
	var nilSpool *Spool
	nilSpool.MarkExpired(again) // must not panic
}

func TestLoadSurfacesStoreFaultsRatherThanCallingThemAbsence(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	s := NewSpool(store)
	ref := s.Spool(ctx, "owner", []byte("body"))

	faulty := &Spool{store: &faultyStore{}, grants: map[string]map[string]grant{ref: {"owner": {}}}}
	if _, err := faulty.Load(ctx, "owner", ref); !errors.Is(err, errStoreFault) {
		t.Fatalf("granted Load over a faulty store = %v, want the store fault", err)
	}
	if _, err := faulty.Load(ctx, "stranger", ref); !errors.Is(err, errStoreFault) {
		t.Fatalf("ungranted Load over a faulty store = %v, want the store fault", err)
	}
}

func TestIsNotFoundFallsBackToTheSentinelWithoutAReporter(t *testing.T) {
	if isNotFound(NewMemoryStore(), nil) {
		t.Fatal("a nil error was reported as not-found")
	}
	if !isNotFound(untypedStore{}, ErrNotFound) {
		t.Fatal("the package sentinel was not recognised without a reporter")
	}
	if isNotFound(untypedStore{}, errStoreFault) {
		t.Fatal("an untyped store fault was masked as absence")
	}
}

func TestContentStoreAdapterWithoutAStore(t *testing.T) {
	ctx := context.Background()
	var adapter ContentStoreAdapter
	if err := adapter.StoreContent(ctx, "ref", []byte("x")); err == nil || !strings.Contains(err.Error(), "nil content store") {
		t.Fatalf("StoreContent = %v, want a nil-store refusal", err)
	}
	if _, err := adapter.LoadContent(ctx, "ref"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LoadContent = %v, want ErrNotFound", err)
	}
	if adapter.IsContentNotFound(ErrNotFound) {
		t.Fatal("an adapter with no sentinel classified an error as not-found")
	}

	backed := ContentStoreAdapter{Store: NewMemoryStore(), NotFoundError: ErrNotFound}
	if err := backed.StoreContent(ctx, "ref", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if data, err := backed.LoadContent(ctx, "ref"); err != nil || string(data) != "x" {
		t.Fatalf("LoadContent = %q, %v", data, err)
	}
	if !backed.IsContentNotFound(ErrNotFound) {
		t.Fatal("the configured sentinel was not recognised")
	}
}

func TestMemoryStoreIgnoresEmptyWritesAndCopies(t *testing.T) {
	ctx := context.Background()
	m := &MemoryStore{} // nil map: StoreContent must initialise it
	if err := m.StoreContent(ctx, "ref", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.LoadContent(ctx, "ref"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an empty write was stored: %v", err)
	}
	body := []byte("body")
	if err := m.StoreContent(ctx, "ref", body); err != nil {
		t.Fatal(err)
	}
	body[0] = 'B'
	got, err := m.LoadContent(ctx, "ref")
	if err != nil || string(got) != "body" {
		t.Fatalf("LoadContent = %q, %v (store did not copy)", got, err)
	}
	m.Delete("ref")
	if _, err := m.LoadContent(ctx, "ref"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete left the ref readable: %v", err)
	}
}

func TestFailingStoreRejectsEverything(t *testing.T) {
	ctx := context.Background()
	if err := (FailingStore{}).StoreContent(ctx, "ref", []byte("x")); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("StoreContent = %v, want ErrStoreUnavailable", err)
	}
	if _, err := (FailingStore{}).LoadContent(ctx, "ref"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LoadContent = %v, want ErrNotFound", err)
	}
	if !(FailingStore{}).IsContentNotFound(ErrNotFound) {
		t.Fatal("FailingStore did not recognise its own not-found sentinel")
	}
}
