package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// setLeaseAt stamps context_sessions.lease_at directly, bypassing RenewLease,
// so tests can construct a fresh or stale lease deterministically instead of
// racing a real ticker.
func setLeaseAt(t *testing.T, s *SQLite, principal contextstate.Principal, at *time.Time) {
	t.Helper()
	var value any
	if at != nil {
		value = at.Unix()
	}
	if _, err := s.db.Exec(`UPDATE context_sessions SET lease_at=? WHERE workspace_id=? AND session_id=? AND subject_id=?`, value, principal.WorkspaceID, principal.SessionID, principal.SubjectID); err != nil {
		t.Fatalf("set lease_at: %v", err)
	}
}

// TestReclaimSessionRejectsLiveSession confirms a fresh lease blocks a second
// principal's ReclaimSession with ErrSessionLiveElsewhere, instead of the
// live owner being silently evicted mid-turn.
func TestReclaimSessionRejectsLiveSession(t *testing.T) {
	ctx := context.Background()
	s, owner := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, owner)
	fresh := time.Now()
	setLeaseAt(t, s, owner, &fresh)

	rival, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReclaimSession(ctx, rival, owner.SessionID)
	if !errors.Is(err, contextstate.ErrSessionLiveElsewhere) {
		t.Fatalf("ReclaimSession error = %v, want ErrSessionLiveElsewhere", err)
	}

	// The owner's capability must be untouched: the takeover was rejected.
	var digest string
	if err := s.db.QueryRow(`SELECT capability_digest FROM context_sessions WHERE workspace_id=? AND session_id=?`, owner.WorkspaceID, owner.SessionID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != owner.CapabilityDigest() {
		t.Fatalf("capability_digest changed despite rejected takeover")
	}
}

// TestReclaimSessionAllowsStaleTakeover confirms a lease older than the TTL
// cutoff, and the NULL/pre-migration case, both allow takeover - and that the
// takeover updates capability_digest and lease_at atomically in the same
// UPDATE.
func TestReclaimSessionAllowsStaleTakeover(t *testing.T) {
	tests := []struct {
		name  string
		lease *time.Time
	}{
		{name: "stale", lease: timePtr(time.Now().Add(-sessionLeaseTTL - time.Minute))},
		{name: "null-pre-migration", lease: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			s, owner := openContextTestStore(t)
			defer s.Close()
			seedContextSession(t, s, owner)
			setLeaseAt(t, s, owner, test.lease)

			rival, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
			if err != nil {
				t.Fatal(err)
			}
			before := time.Now().Unix()
			if _, err := s.ReclaimSession(ctx, rival, owner.SessionID); err != nil {
				t.Fatalf("ReclaimSession: %v", err)
			}
			after := time.Now().Unix()

			var digest string
			var leaseAt int64
			if err := s.db.QueryRow(`SELECT capability_digest,lease_at FROM context_sessions WHERE workspace_id=? AND session_id=?`, owner.WorkspaceID, owner.SessionID).Scan(&digest, &leaseAt); err != nil {
				t.Fatal(err)
			}
			if digest != rival.CapabilityDigest() {
				t.Fatalf("capability_digest = %q, want rival's %q", digest, rival.CapabilityDigest())
			}
			if leaseAt < before || leaseAt > after {
				t.Fatalf("lease_at = %d, want within [%d,%d] (stamped fresh by the takeover)", leaseAt, before, after)
			}
		})
	}
}

// TestReclaimSessionRaceLoses runs two concurrent ReclaimSession calls
// against the same stale session and asserts exactly one succeeds, never a
// split/partial write.
func TestReclaimSessionRaceLoses(t *testing.T) {
	ctx := context.Background()
	s, owner := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, owner)
	stale := time.Now().Add(-sessionLeaseTTL - time.Minute)
	setLeaseAt(t, s, owner, &stale)

	rivalA, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
	if err != nil {
		t.Fatal(err)
	}
	rivalB, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = s.ReclaimSession(ctx, rivalA, owner.SessionID)
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = s.ReclaimSession(ctx, rivalB, owner.SessionID)
	}()
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1 (errs=%v)", successes, errs)
	}

	var digest string
	if err := s.db.QueryRow(`SELECT capability_digest FROM context_sessions WHERE workspace_id=? AND session_id=?`, owner.WorkspaceID, owner.SessionID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != rivalA.CapabilityDigest() && digest != rivalB.CapabilityDigest() {
		t.Fatalf("capability_digest = %q, want one of the two rivals' digests (no split/partial write)", digest)
	}
}

func timePtr(t time.Time) *time.Time { return &t }
