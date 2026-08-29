package storage

import (
	"context"
	"database/sql"
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
// takeover updates capability_digest WITHOUT stamping lease_at. Only a real
// heartbeat tick (RenewLease) may mark a lease fresh - see ReclaimSession's
// doc comment for why stamping on takeover itself caused every ordinary
// sequential access (mivia compact, a quick chat -p turn) to poison the row
// for the next process for up to sessionLeaseTTL, with no rival in sight.
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
			if _, err := s.ReclaimSession(ctx, rival, owner.SessionID); err != nil {
				t.Fatalf("ReclaimSession: %v", err)
			}

			var digest string
			var leaseAt sql.NullInt64
			if err := s.db.QueryRow(`SELECT capability_digest,lease_at FROM context_sessions WHERE workspace_id=? AND session_id=?`, owner.WorkspaceID, owner.SessionID).Scan(&digest, &leaseAt); err != nil {
				t.Fatal(err)
			}
			if digest != rival.CapabilityDigest() {
				t.Fatalf("capability_digest = %q, want rival's %q", digest, rival.CapabilityDigest())
			}
			if leaseAt.Valid && leaseAt.Int64 >= time.Now().Add(-sessionLeaseTTL).Unix() {
				t.Fatalf("lease_at = %v, want left untouched by the takeover (NULL or still-stale), not stamped fresh", leaseAt)
			}

			// The new owner must be immediately re-reclaimable by yet another
			// process too (e.g. a rapid succession of one-shot commands): a
			// takeover must not itself create a fresh-lease window.
			another, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReclaimSession(ctx, another, owner.SessionID); err != nil {
				t.Fatalf("second ReclaimSession immediately after the first: %v, want success (a takeover must not poison the row for the next resume)", err)
			}
		})
	}
}

// TestReclaimSessionConcurrentTakeoversBothSucceedCleanly runs two
// concurrent ReclaimSession calls against the same stale, never-renewed
// session. Since a takeover no longer stamps lease_at (see ReclaimSession's
// doc comment), NEITHER call's lease predicate is invalidated by the
// other's success - both are expected to succeed, one after the other
// (s.writeMu and SQLite's own write-transaction lock still fully serialize
// them; there is no torn or split write either way). The property this
// still guards is the one that actually matters: the row always ends up
// wholly owned by exactly one of the two rivals, never a partial/mixed
// write - the lease is a liveness hint for ordinary sequential access, not
// a mutual-exclusion primitive; that job belongs to capability_digest
// equality checks at commit time (authorizeContextSessionTx), unchanged by
// this fix.
func TestReclaimSessionConcurrentTakeoversBothSucceedCleanly(t *testing.T) {
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

	for i, err := range errs {
		if err != nil {
			t.Fatalf("ReclaimSession[%d]: %v, want success (a takeover must not poison the row for a concurrent resume attempt with no heartbeat in between)", i, err)
		}
	}

	var digest string
	if err := s.db.QueryRow(`SELECT capability_digest FROM context_sessions WHERE workspace_id=? AND session_id=?`, owner.WorkspaceID, owner.SessionID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != rivalA.CapabilityDigest() && digest != rivalB.CapabilityDigest() {
		t.Fatalf("capability_digest = %q, want exactly one of the two rivals' digests (no split/partial write)", digest)
	}
}

// TestReclaimSessionRefusesConcurrentTakeoverOnceLeaseIsFresh confirms the
// property that DOES still hold once a real heartbeat has renewed: a rival
// ReclaimSession racing a fresh RenewLease is refused - proving the
// TOCTOU-safe conditional UPDATE (staleness embedded in its own WHERE
// clause) rather than a separate check-then-write.
func TestReclaimSessionRefusesConcurrentTakeoverOnceLeaseIsFresh(t *testing.T) {
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

	var wg sync.WaitGroup
	var renewErr, reclaimErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		renewErr = s.RenewLease(ctx, owner, owner.SessionID)
	}()
	go func() {
		defer wg.Done()
		_, reclaimErr = s.ReclaimSession(ctx, rival, owner.SessionID)
	}()
	wg.Wait()

	if renewErr != nil {
		t.Fatalf("RenewLease: %v", renewErr)
	}
	if !errors.Is(reclaimErr, contextstate.ErrSessionLiveElsewhere) {
		t.Fatalf("ReclaimSession racing a fresh renewal: err = %v, want ErrSessionLiveElsewhere", reclaimErr)
	}

	var digest string
	if err := s.db.QueryRow(`SELECT capability_digest FROM context_sessions WHERE workspace_id=? AND session_id=?`, owner.WorkspaceID, owner.SessionID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != owner.CapabilityDigest() {
		t.Fatalf("capability_digest = %q, want owner's untouched %q", digest, owner.CapabilityDigest())
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// TestReleaseLeaseLetsAnOrdinaryQuitThenResumeSucceedImmediately reproduces a
// real, reported incident: a session that ran long enough for its heartbeat
// to renew even once looked "live" to ReclaimSession for the rest of
// sessionLeaseTTL after the owning process quit cleanly, so an ordinary
// "quit, then resume" within that window (very likely - the TTL is minutes,
// not seconds) was refused with ErrSessionLiveElsewhere even though nothing
// was still using the session. ReleaseLease (called by
// Session.ReleaseContextLease on process/surface shutdown) must clear the
// lease so the very next reclaim attempt - simulating the resumed process -
// succeeds immediately, without waiting out the TTL.
func TestReleaseLeaseLetsAnOrdinaryQuitThenResumeSucceedImmediately(t *testing.T) {
	ctx := context.Background()
	s, owner := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, owner)

	// The owning process's heartbeat renewed at least once while it was
	// running - lease_at is fresh, not stale.
	fresh := time.Now()
	setLeaseAt(t, s, owner, &fresh)

	// Confirm the bug this test guards against: without a release, a resume
	// attempted moments later is refused, not because anything is still
	// live, but because the lease simply hasn't aged past the TTL yet.
	rivalWithoutRelease, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimSession(ctx, rivalWithoutRelease, owner.SessionID); !errors.Is(err, contextstate.ErrSessionLiveElsewhere) {
		t.Fatalf("pre-condition failed: ReclaimSession error = %v, want ErrSessionLiveElsewhere (a fresh, unreleased lease must still block a reclaim)", err)
	}

	// The owning process now quits cleanly and releases its lease.
	if err := s.ReleaseLease(ctx, owner, owner.SessionID); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}

	// The resumed process's reclaim must now succeed immediately - no TTL
	// wait required, because the lease was explicitly released, not merely
	// aged out.
	rival, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimSession(ctx, rival, owner.SessionID); err != nil {
		t.Fatalf("ReclaimSession after ReleaseLease: %v, want success (this is the ordinary quit-then-resume flow)", err)
	}
}

// TestReleaseLeaseIsANoOpForACapabilityAlreadyReclaimedAway confirms
// ReleaseLease, like RenewLease, is scoped by capability_digest: a process
// whose session was already reclaimed by a rival cannot use ReleaseLease to
// clear the RIVAL's lease (it has no standing to affect a row it no longer
// owns).
func TestReleaseLeaseIsANoOpForACapabilityAlreadyReclaimedAway(t *testing.T) {
	ctx := context.Background()
	s, owner := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, owner)
	stale := time.Now().Add(-sessionLeaseTTL - time.Minute)
	setLeaseAt(t, s, owner, &stale)

	rival, err := contextstate.NewPrincipal(owner.WorkspaceID, owner.SessionID, owner.SubjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimSession(ctx, rival, owner.SessionID); err != nil {
		t.Fatalf("ReclaimSession: %v", err)
	}
	freshUnderRival := time.Now()
	setLeaseAt(t, s, rival, &freshUnderRival)

	// owner's capability is stale now (rival took over); owner's ReleaseLease
	// must not touch rival's fresh lease.
	if err := s.ReleaseLease(ctx, owner, owner.SessionID); err != nil {
		t.Fatalf("ReleaseLease (ousted owner): %v", err)
	}
	var leaseAt int64
	if err := s.db.QueryRow(`SELECT lease_at FROM context_sessions WHERE workspace_id=? AND session_id=?`, owner.WorkspaceID, owner.SessionID).Scan(&leaseAt); err != nil {
		t.Fatal(err)
	}
	if leaseAt != freshUnderRival.Unix() {
		t.Fatalf("lease_at = %d, want rival's fresh renewal (%d) untouched by the ousted owner's ReleaseLease", leaseAt, freshUnderRival.Unix())
	}
}
