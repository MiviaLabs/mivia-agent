package ledgercore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// plainStore only implements minimal storage.Store without optional interfaces.
type plainStore struct {
	mu      sync.Mutex
	claims  map[string]string // runID -> holder
	content map[string][]byte
	err     error
}

func (p *plainStore) Append(ctx context.Context, e storage.Event) error             { return p.err }
func (p *plainStore) AppendBatch(ctx context.Context, events []storage.Event) error { return p.err }
func (p *plainStore) AppendBatchForNewRun(ctx context.Context, runID string, events []storage.Event) error {
	return p.err
}
func (p *plainStore) AppendClaimed(ctx context.Context, e storage.Event, holder string) error {
	return p.err
}
func (p *plainStore) AppendWithExistingClaim(ctx context.Context, e storage.Event, holder string) error {
	return p.err
}
func (p *plainStore) Events(ctx context.Context, runID string) ([]storage.Event, error) {
	if p.err != nil {
		return nil, p.err
	}
	return nil, nil
}
func (p *plainStore) EventsSince(ctx context.Context, runID string, afterSequence int) ([]storage.Event, error) {
	return nil, p.err
}
func (p *plainStore) DeleteRun(ctx context.Context, runID string, throughSequence int) error {
	return p.err
}
func (p *plainStore) AppendAndDeleteRun(ctx context.Context, tombstone storage.Event, claim storage.Claim) error {
	return p.err
}
func (p *plainStore) Changes(ctx context.Context, afterCursor uint64) (map[string]int, uint64, error) {
	return nil, 0, p.err
}
func (p *plainStore) Count(ctx context.Context) (int, error) {
	return 0, p.err
}
func (p *plainStore) ListRunIDs(ctx context.Context) ([]string, error) {
	return nil, p.err
}
func (p *plainStore) Close() error {
	return p.err
}
func (p *plainStore) ClaimRun(ctx context.Context, runID, holder string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if p.claims == nil {
		p.claims = make(map[string]string)
	}
	if existing, ok := p.claims[runID]; ok && existing != holder {
		return storage.ErrClaimHeld
	}
	p.claims[runID] = holder
	return nil
}
func (p *plainStore) TakeoverClaim(ctx context.Context, runID, holder string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if p.claims == nil {
		p.claims = make(map[string]string)
	}
	p.claims[runID] = holder
	return nil
}
func (p *plainStore) ReleaseClaim(ctx context.Context, runID, holder string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if p.claims == nil || p.claims[runID] != holder {
		return storage.ErrClaimNotHeld
	}
	delete(p.claims, runID)
	return nil
}
func (p *plainStore) ClearClaim(ctx context.Context, runID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	delete(p.claims, runID)
	return nil
}
func (p *plainStore) PutContent(ctx context.Context, ref string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if p.content == nil {
		p.content = make(map[string][]byte)
	}
	p.content[ref] = data
	return nil
}
func (p *plainStore) GetContent(ctx context.Context, ref string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	if d, ok := p.content[ref]; ok {
		return d, nil
	}
	return nil, storage.ErrContentNotFound
}

// leaseOnlyStore implements storage.Store and storage.LeaseStore, but NOT storage.FencedLeaseStore.
type leaseOnlyStore struct {
	plainStore
}

func (l *leaseOnlyStore) TakeoverExpiredClaim(ctx context.Context, runID, holder string, maxAge time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	if l.claims == nil {
		l.claims = make(map[string]string)
	}
	l.claims[runID] = holder
	return nil
}

// fencedErrStore implements storage.FencedLeaseStore and storage.ClaimReader with injected errors.
type fencedErrStore struct {
	plainStore
	rawClaim storage.Claim
}

func (f *fencedErrStore) ClaimRunFenced(ctx context.Context, runID, holder string) (storage.Claim, error) {
	return storage.Claim{}, f.err
}
func (f *fencedErrStore) RefreshClaimFenced(ctx context.Context, runID, holder string) (storage.Claim, error) {
	return storage.Claim{}, f.err
}
func (f *fencedErrStore) TakeoverClaimFenced(ctx context.Context, runID, holder string) (storage.Claim, error) {
	return storage.Claim{}, f.err
}
func (f *fencedErrStore) TakeoverExpiredClaimFenced(ctx context.Context, runID, holder string, maxAge time.Duration) (storage.Claim, error) {
	return storage.Claim{}, f.err
}
func (f *fencedErrStore) ReleaseClaimFenced(ctx context.Context, claim storage.Claim) error {
	return f.err
}
func (f *fencedErrStore) AppendClaimedFenced(ctx context.Context, e storage.Event, claim storage.Claim) error {
	return f.err
}
func (f *fencedErrStore) GetClaim(ctx context.Context, runID string) (storage.Claim, error) {
	if f.err != nil {
		return storage.Claim{}, f.err
	}
	return f.rawClaim, nil
}

func TestNewHolderID(t *testing.T) {
	h1 := NewHolderID()
	h2 := NewHolderID()
	if h1 == "" || h2 == "" {
		t.Fatalf("expected non-empty holder IDs, got %q, %q", h1, h2)
	}
	if h1 == h2 {
		t.Fatalf("expected unique holder IDs, got both %q", h1)
	}
	if len(h1) < 4 || h1[:2] != "h-" {
		t.Fatalf("expected holder ID to start with 'h-', got %q", h1)
	}

	tAuto := NewClaimsTracker("")
	if tAuto.Holder() == "" {
		t.Fatalf("expected auto-generated holder ID")
	}
}

func TestClaimsTracker_BasicLifecycle(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	tracker := NewClaimsTracker("h-test1")

	if tracker.Holder() != "h-test1" {
		t.Fatalf("expected holder h-test1, got %q", tracker.Holder())
	}
	if err := tracker.CheckOpen(); err != nil {
		t.Fatalf("expected open, got %v", err)
	}

	runID := "run-123"

	// Empty holder rejected
	if err := tracker.ClaimRun(ctx, store, runID, ""); err != ErrClaimNotHeld {
		t.Fatalf("expected ErrClaimNotHeld on empty holder, got %v", err)
	}
	if err := tracker.RefreshRunClaim(ctx, store, runID, ""); err != ErrClaimNotHeld {
		t.Fatalf("expected ErrClaimNotHeld on empty refresh, got %v", err)
	}
	if err := tracker.TakeoverRunClaim(ctx, store, runID, ""); err != ErrClaimNotHeld {
		t.Fatalf("expected ErrClaimNotHeld on empty takeover, got %v", err)
	}

	// ClaimRun
	if err := tracker.ClaimRun(ctx, store, runID, "h-test1"); err != nil {
		t.Fatalf("ClaimRun failed: %v", err)
	}
	if claim, ok := tracker.GetClaim(runID); !ok || claim.Holder != "h-test1" {
		t.Fatalf("GetClaim mismatch: ok=%v, claim=%+v", ok, claim)
	}

	// SetClaim & AllClaims & DropClaim
	tracker.SetClaim("run-custom", storage.Claim{RunID: "run-custom", Holder: "h-custom"})
	all := tracker.AllClaims()
	if len(all) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(all))
	}
	tracker.DropClaim("run-custom")
	if _, ok := tracker.GetClaim("run-custom"); ok {
		t.Fatalf("expected run-custom dropped")
	}
}

func TestClaimsTracker_ProbesAndRelease(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	tracker := NewClaimsTracker("h-test1")
	runID := "run-123"

	if err := tracker.ClaimRun(ctx, store, runID, "h-test1"); err != nil {
		t.Fatalf("ClaimRun failed: %v", err)
	}

	// IsRunHeld
	held, err := tracker.IsRunHeld(ctx, store, runID)
	if err != nil || !held {
		t.Fatalf("IsRunHeld: got %v, err=%v", held, err)
	}

	// RefreshRunClaim
	if err := tracker.RefreshRunClaim(ctx, store, runID, "h-test1"); err != nil {
		t.Fatalf("RefreshRunClaim failed: %v", err)
	}

	// GetRunClaim
	holder, _, ok, err := tracker.GetRunClaim(ctx, store, runID)
	if err != nil || !ok || holder != "h-test1" {
		t.Fatalf("GetRunClaim: holder=%v, ok=%v, err=%v", holder, ok, err)
	}

	// GetRunClaim on unclaimed run
	holder, _, ok, err = tracker.GetRunClaim(ctx, store, "run-unclaimed")
	if err != nil || ok || holder != "" {
		t.Fatalf("GetRunClaim unclaimed: ok=%v, err=%v", ok, err)
	}

	// Another holder cannot claim
	if err := tracker.ClaimRun(ctx, store, runID, "h-test2"); err != ErrClaimHeld {
		t.Fatalf("expected ErrClaimHeld, got %v", err)
	}

	// ReleaseRun with wrong holder rejected
	if err := tracker.ReleaseRun(ctx, store, runID, "h-wrong"); err != ErrClaimNotHeld {
		t.Fatalf("expected ErrClaimNotHeld, got %v", err)
	}

	// ReleaseRun
	if err := tracker.ReleaseRun(ctx, store, runID, "h-test1"); err != nil {
		t.Fatalf("ReleaseRun failed: %v", err)
	}

	// After release, not held
	held, err = tracker.IsRunHeld(ctx, store, runID)
	if err != nil || held {
		t.Fatalf("IsRunHeld after release: got %v, err=%v", held, err)
	}
}

func TestClaimsTracker_PlainStoreFallback(t *testing.T) {
	ctx := context.Background()
	ps := &plainStore{}
	tracker := NewClaimsTracker("h-plain")

	runID := "run-plain"

	// ClaimRun on plain store
	if err := tracker.ClaimRun(ctx, ps, runID, "h-plain"); err != nil {
		t.Fatalf("ClaimRun plain failed: %v", err)
	}

	// RefreshRunClaim on plain store
	if err := tracker.RefreshRunClaim(ctx, ps, runID, "h-plain"); err != nil {
		t.Fatalf("RefreshRunClaim plain failed: %v", err)
	}

	// TakeoverRunClaim on plain store
	tracker2 := NewClaimsTracker("h-plain2")
	if err := tracker2.TakeoverRunClaim(ctx, ps, runID, "h-plain2"); err != nil {
		t.Fatalf("TakeoverRunClaim plain failed: %v", err)
	}

	// TakeoverExpiredRunClaim on plain store (no LeaseStore interface)
	if err := tracker.TakeoverExpiredRunClaim(ctx, ps, runID, "", time.Minute); err != ErrClaimHeld {
		t.Fatalf("expected ErrClaimHeld on plain store TakeoverExpiredRunClaim, got %v", err)
	}

	// LeaseOnlyStore supports TakeoverExpiredClaim without FencedLeaseStore
	ls := &leaseOnlyStore{}
	if err := tracker.TakeoverExpiredRunClaim(ctx, ls, runID, "h-plain", time.Minute); err != nil {
		t.Fatalf("TakeoverExpiredRunClaim on leaseOnlyStore failed: %v", err)
	}

	// Probes on plain store (no ClaimReader / IsRunHeld interface)
	if _, _, ok, err := tracker.GetRunClaim(ctx, ps, runID); err != nil || ok {
		t.Fatalf("expected ok=false on plain store GetRunClaim, got ok=%v, err=%v", ok, err)
	}
	if held, err := tracker.IsRunHeld(ctx, ps, runID); err != nil || held {
		t.Fatalf("expected false on plain store IsRunHeld, got %v", held)
	}
	if fenced, err := tracker.IsRunTokenFenced(ctx, ps, runID, "h-plain"); err != nil || fenced {
		t.Fatalf("expected false on plain store IsRunTokenFenced, got %v", fenced)
	}

	// ReleaseRun on plain store
	if err := tracker2.ReleaseRun(ctx, ps, runID, "h-plain2"); err != nil {
		t.Fatalf("ReleaseRun plain failed: %v", err)
	}

	// Error paths on plain store
	errStore := &plainStore{err: errors.New("store error")}
	if err := tracker.RefreshRunClaim(ctx, errStore, runID, "h-plain"); err == nil {
		t.Fatalf("expected error on RefreshRunClaim")
	}
	if err := tracker.TakeoverRunClaim(ctx, errStore, runID, "h-plain"); err == nil {
		t.Fatalf("expected error on TakeoverRunClaim")
	}
	if err := tracker.ClearRunClaim(ctx, errStore, runID); err == nil {
		t.Fatalf("expected error on ClearRunClaim")
	}

	// Close with nil context
	if err := tracker.Close(nil, ps); err != nil {
		t.Fatalf("Close with nil context: %v", err)
	}
	// Double close is idempotent
	if err := tracker.Close(nil, ps); err != nil {
		t.Fatalf("double Close: %v", err)
	}
}

func TestClaimsTracker_FencedErrors(t *testing.T) {
	ctx := context.Background()
	fs := &fencedErrStore{plainStore: plainStore{err: errors.New("fenced fail")}}
	tracker := NewClaimsTracker("h-err")
	runID := "run-fenced-err"

	// RefreshClaimFenced error
	if err := tracker.RefreshRunClaim(ctx, fs, runID, "h-err"); err == nil {
		t.Fatalf("expected error from RefreshRunClaim")
	}

	// TakeoverExpiredClaimFenced error
	if err := tracker.TakeoverExpiredRunClaim(ctx, fs, runID, "h-err", time.Minute); err == nil {
		t.Fatalf("expected error from TakeoverExpiredRunClaim")
	}

	// ReleaseClaimFenced error with stored fence
	tracker.SetClaim(runID, storage.Claim{RunID: runID, Holder: "h-err", Fence: 42})
	if err := tracker.ReleaseRun(ctx, fs, runID, "h-err"); err == nil {
		t.Fatalf("expected error from ReleaseRun")
	}

	// GetClaim error
	if _, _, _, err := tracker.GetRunClaim(ctx, fs, runID); err == nil {
		t.Fatalf("expected error from GetRunClaim")
	}

	// GetClaim with invalid acquired_at timestamp
	fsValid := &fencedErrStore{rawClaim: storage.Claim{RunID: runID, Holder: "h-err", AcquiredAt: "bad-time"}}
	if _, _, _, err := tracker.GetRunClaim(ctx, fsValid, runID); err == nil {
		t.Fatalf("expected error from parse invalid timestamp in GetRunClaim")
	}
}

func TestClaimsTracker_TakeoversAndFences(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	tracker1 := NewClaimsTracker("h-1")
	tracker2 := NewClaimsTracker("h-2")

	runID := "run-takeover"
	if err := tracker1.ClaimRun(ctx, store, runID, "h-1"); err != nil {
		t.Fatalf("tracker1 ClaimRun failed: %v", err)
	}

	// TakeoverRunClaim by tracker2
	if err := tracker2.TakeoverRunClaim(ctx, store, runID, "h-2"); err != nil {
		t.Fatalf("tracker2 TakeoverRunClaim failed: %v", err)
	}

	// tracker1 should now be fenced out
	fenced, err := tracker1.IsRunTokenFenced(ctx, store, runID, "h-1")
	if err != nil {
		t.Fatalf("IsRunTokenFenced failed: %v", err)
	}
	if !fenced {
		t.Fatalf("expected h-1 to be fenced out after takeover")
	}

	// TakeoverExpiredRunClaim with default holder fallback
	time.Sleep(10 * time.Millisecond)
	tracker3 := NewClaimsTracker("h-3")
	if err := tracker3.TakeoverExpiredRunClaim(ctx, store, runID, "", 1*time.Millisecond); err != nil {
		t.Fatalf("TakeoverExpiredRunClaim failed: %v", err)
	}

	// ClearRunClaim
	if err := tracker3.ClearRunClaim(ctx, store, runID); err != nil {
		t.Fatalf("ClearRunClaim failed: %v", err)
	}
	held, _ := store.IsRunHeld(ctx, runID)
	if held {
		t.Fatalf("expected run to not be held after ClearRunClaim")
	}
}

func TestClaimsTracker_ClosedChecks(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	tracker := NewClaimsTracker("h-closed")

	if err := tracker.Close(ctx, store); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !tracker.IsClosed() {
		t.Fatalf("expected IsClosed true")
	}
	if err := tracker.CheckOpen(); err != ErrClosed {
		t.Fatalf("expected ErrClosed from CheckOpen, got %v", err)
	}

	runID := "run-closed-test"
	if err := tracker.ClaimRun(ctx, store, runID, "h-closed"); err != ErrClosed {
		t.Fatalf("ClaimRun: want ErrClosed, got %v", err)
	}
	if err := tracker.RefreshRunClaim(ctx, store, runID, "h-closed"); err != ErrClosed {
		t.Fatalf("RefreshRunClaim: want ErrClosed, got %v", err)
	}
	if err := tracker.TakeoverRunClaim(ctx, store, runID, "h-closed"); err != ErrClosed {
		t.Fatalf("TakeoverRunClaim: want ErrClosed, got %v", err)
	}
	if err := tracker.TakeoverExpiredRunClaim(ctx, store, runID, "h-closed", time.Minute); err != ErrClosed {
		t.Fatalf("TakeoverExpiredRunClaim: want ErrClosed, got %v", err)
	}
	if err := tracker.ReleaseRun(ctx, store, runID, "h-closed"); err != ErrClosed {
		t.Fatalf("ReleaseRun: want ErrClosed, got %v", err)
	}
	if err := tracker.ClearRunClaim(ctx, store, runID); err != ErrClosed {
		t.Fatalf("ClearRunClaim: want ErrClosed, got %v", err)
	}
	if _, _, _, err := tracker.GetRunClaim(ctx, store, runID); err != ErrClosed {
		t.Fatalf("GetRunClaim: want ErrClosed, got %v", err)
	}
	if _, err := tracker.IsRunHeld(ctx, store, runID); err != ErrClosed {
		t.Fatalf("IsRunHeld: want ErrClosed, got %v", err)
	}
	if _, err := tracker.IsRunTokenFenced(ctx, store, runID, "h-closed"); err != ErrClosed {
		t.Fatalf("IsRunTokenFenced: want ErrClosed, got %v", err)
	}
}

func TestParseClaimAcquiredAt(t *testing.T) {
	// RFC3339Nano
	t1 := time.Now().UTC()
	s1 := t1.Format(time.RFC3339Nano)
	p1, err := ParseClaimAcquiredAt(s1)
	if err != nil || p1.Unix() != t1.Unix() {
		t.Fatalf("parse RFC3339Nano: err=%v", err)
	}

	// Legacy space format
	s2 := "2026-08-24 12:30:00"
	p2, err := ParseClaimAcquiredAt(s2)
	if err != nil || p2.Year() != 2026 {
		t.Fatalf("parse legacy space format: err=%v", err)
	}

	// Invalid
	if _, err := ParseClaimAcquiredAt("invalid-date"); err == nil {
		t.Fatalf("expected error on invalid date string, got nil")
	}
}

func TestClaimsTracker_FencedReleaseOnClose(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	tracker := NewClaimsTracker("h-test1")

	runID := "run-abc"
	if err := tracker.ClaimRun(ctx, store, runID, "h-test1"); err != nil {
		t.Fatalf("ClaimRun failed: %v", err)
	}

	// Close tracker
	if err := tracker.Close(ctx, store); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Store should have released the claim
	held, err := store.IsRunHeld(ctx, runID)
	if err != nil || held {
		t.Fatalf("expected claim released in store after tracker Close, held=%v, err=%v", held, err)
	}
}

func TestClaimsTracker_CloseVsClaimRace(t *testing.T) {
	for i := 0; i < 20; i++ {
		store := storage.NewMemory()
		tracker := NewClaimsTracker("h-race")
		runID := "run-race"

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			_ = tracker.ClaimRun(context.Background(), store, runID, "h-race")
		}()

		go func() {
			defer wg.Done()
			_ = tracker.Close(context.Background(), store)
		}()

		wg.Wait()

		// If the tracker is closed, the claim MUST NOT be left orphaned in the store
		if tracker.IsClosed() {
			held, _ := store.IsRunHeld(context.Background(), runID)
			if held {
				t.Fatalf("race condition: store left holding active claim after tracker Close()")
			}
		}
	}
}
