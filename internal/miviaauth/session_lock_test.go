package miviaauth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Tests for the refresh lock's contract and for Whoami, split out of
// service_test.go to keep either file readable.

// TestServiceEnsureProceedsWhenNoLockIsAvailable: refusing to authenticate
// because a lock file could not be made would be worse than the rare race the
// lock prevents.
func TestServiceEnsureProceedsWhenNoLockIsAvailable(t *testing.T) {
	fake := &fakeSessionClient{}
	svc, path := newTestService(t, fake)
	mustSave(t, path, farFutureToken())
	svc.acquire = stubAcquire(lockUnavailable)

	bearer, err := svc.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v, want it to proceed unlocked", err)
	}
	if bearer != "far-future-bearer" {
		t.Errorf("Ensure() = %q", bearer)
	}
}

// TestServiceEnsureLockBusyUsesTheOtherProcessToken: the holder finished its
// refresh, so by the time this caller gives up waiting the stored token is
// already usable and there is nothing left to do.
func TestServiceEnsureLockBusyUsesTheOtherProcessToken(t *testing.T) {
	fake := &fakeSessionClient{}
	svc, path := newTestService(t, fake)
	mustSave(t, path, farFutureToken())
	svc.acquire = stubAcquire(lockBusy)

	bearer, err := svc.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v, want the other process's token", err)
	}
	if bearer != "far-future-bearer" {
		t.Errorf("Ensure() = %q", bearer)
	}
	if _, refresh, _, _ := fake.counts(); refresh != 0 {
		t.Errorf("refresh calls = %d, want 0: the holder already did the work", refresh)
	}
}

// TestServiceEnsureLockBusyStillNeedingRefreshRefusesRatherThanRacing: the
// alternative is refreshing alongside the holder, and two refreshes of a
// one-time-use token cost BOTH processes the session.
func TestServiceEnsureLockBusyStillNeedingRefreshRefusesRatherThanRacing(t *testing.T) {
	fake := &fakeSessionClient{}
	svc, path := newTestService(t, fake)
	mustSave(t, path, nearExpiryToken())
	before := snapshotFile(t, path)
	svc.acquire = stubAcquire(lockBusy)

	_, err := svc.Ensure(context.Background())
	if !errors.Is(err, ErrRefreshBusy) {
		t.Fatalf("Ensure() error = %v, want ErrRefreshBusy", err)
	}
	if _, refresh, _, _ := fake.counts(); refresh != 0 {
		t.Errorf("refresh calls = %d, want 0: refreshing here is the exact race the lock prevents", refresh)
	}
	before.assertUnchanged(t, path)
}

// TestServiceEnsureLockBusyWithNoStoredTokenReportsReauthNotBusy: the holder
// was a logout, or a refresh the server refused. Telling the user to retry
// would be a lie, and this caller never held the lock, so it deletes nothing.
func TestServiceEnsureLockBusyWithNoStoredTokenReportsReauthNotBusy(t *testing.T) {
	svc, _ := newTestService(t, &fakeSessionClient{})
	svc.acquire = stubAcquire(lockBusy)

	_, err := svc.Ensure(context.Background())
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("Ensure() error = %v, want ErrReauthRequired", err)
	}
	if errors.Is(err, ErrRefreshBusy) {
		t.Error("reported contention when the user is simply logged out")
	}
}

func TestLockFileIsSeparateFromTheSessionFile(t *testing.T) {
	svc, path := newTestService(t, &fakeSessionClient{})
	if svc.lockPath() == path {
		t.Fatal("the lock path is the session file itself; Save renames a new inode over it, so such a lock guards nothing")
	}
}

// --- Whoami ------------------------------------------------------------

func TestServiceWhoamiReturnsIdentityAndExpiry(t *testing.T) {
	want := Identity{ID: "u1", Email: "user@example.com", OrganizationID: "org-1", Role: "member", DisplayName: "Jane"}
	fake := &fakeSessionClient{meIdentity: want}
	svc, path := newTestService(t, fake)
	stored := farFutureToken()
	mustSave(t, path, stored)

	got, err := svc.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() error = %v", err)
	}
	if got.Identity != want {
		t.Errorf("Identity = %+v, want %+v", got.Identity, want)
	}
	if !got.ExpiresAt.Truncate(time.Second).Equal(stored.ExpiresAt.Truncate(time.Second)) {
		t.Errorf("ExpiresAt = %v, want the stored token's %v", got.ExpiresAt, stored.ExpiresAt)
	}
	if len(fake.meBearer) != 1 || fake.meBearer[0] != "far-future-bearer" {
		t.Errorf("Me called with %v, want the ensured bearer", fake.meBearer)
	}
}

func TestServiceWhoamiNotLoggedIn(t *testing.T) {
	svc, _ := newTestService(t, &fakeSessionClient{})
	if _, err := svc.Whoami(context.Background()); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("Whoami() error = %v, want ErrReauthRequired", err)
	}
}

// TestServiceWhoamiMe401ClearsTokenAndReturnsReauth: the session was revoked
// or the user deleted between Ensure and Me.
func TestServiceWhoamiMe401ClearsTokenAndReturnsReauth(t *testing.T) {
	fake := &fakeSessionClient{meErr: definitive401()}
	svc, path := newTestService(t, fake)
	mustSave(t, path, farFutureToken())

	if _, err := svc.Whoami(context.Background()); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("Whoami() error = %v, want ErrReauthRequired", err)
	}
	if !fileGone(t, path) {
		t.Error("the rejected session was left on disk")
	}
}

// TestServiceWhoamiInterceptedMe401LeavesTokenUntouched: whoami's fast path
// reaches Me with a bearer that never touched the real server, which makes it
// the most likely place to meet a portal's 401 -- and the most expensive
// place to believe one.
func TestServiceWhoamiInterceptedMe401LeavesTokenUntouched(t *testing.T) {
	fake := &fakeSessionClient{meErr: intercepted401()}
	svc, path := newTestService(t, fake)
	mustSave(t, path, farFutureToken())
	before := snapshotFile(t, path)

	_, err := svc.Whoami(context.Background())
	if err == nil {
		t.Fatal("Whoami() returned nil after a failed Me")
	}
	if errors.Is(err, ErrReauthRequired) {
		t.Error("a 401 that was not the API's was treated as definitive")
	}
	before.assertUnchanged(t, path)
}

// TestServiceWhoamiMe401DoesNotDeleteAnotherLogin covers the window Whoami
// cannot lock away: ensureToken releases the lock before Me runs, so a
// `mivia login` in another shell can land in between. The delete is a
// compare-and-delete for exactly this reason.
func TestServiceWhoamiMe401DoesNotDeleteAnotherLogin(t *testing.T) {
	fake := &fakeSessionClient{meErr: definitive401()}
	svc, path := newTestService(t, fake)
	mustSave(t, path, farFutureToken())

	// The other shell's login lands after ensureToken read the old token and
	// before the 401 about it comes back -- the exact window the lock cannot
	// cover, because Me runs outside it.
	newer := tokenAt("bearer-from-another-shell", "rt-newer", 24*time.Hour)
	svc.client = &loginDuringMeClient{fake: fake, path: path, newer: newer, t: t}

	if _, err := svc.Whoami(context.Background()); err == nil {
		t.Fatal("Whoami() returned nil")
	}
	if fileGone(t, path) {
		t.Fatal("a stale 401 destroyed a session another process had just saved")
	}
	if got := mustLoad(t, path); got.Bearer != "bearer-from-another-shell" {
		t.Errorf("stored Bearer = %q, want the newer session intact", got.Bearer)
	}
}

// --- Logout ------------------------------------------------------------

// TestLockUnavailableStillRunsTheGuardedWork is the regression for a real
// defect: an earlier draft returned lockUnavailable WITHOUT running the
// guarded function, so on a machine where the lock file could not be created
// -- including the very first run, before ~/.mivia/ exists -- Logout skipped
// its Load entirely and then tried to revoke a zero-valued token, calling the
// server with an empty bearer. "Proceed unlocked" has to mean proceed.
func TestLockUnavailableStillRunsTheGuardedWork(t *testing.T) {
	ran := false
	result, err := withRefreshLock(filepath.Join(t.TempDir(), "nested", "auth.json.lock"), func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("withRefreshLock() error = %v", err)
	}
	if !ran {
		t.Fatal("the guarded function did not run")
	}
	if result != lockHeld {
		t.Errorf("result = %v, want the lock to be taken after the parent directory is created", result)
	}
}

// TestLogoutOnAFreshMachineMakesNoNetworkCall is the same defect seen from
// the command's side: nothing is stored, so nothing should be revoked.
func TestLogoutOnAFreshMachineMakesNoNetworkCall(t *testing.T) {
	fake := &fakeSessionClient{}
	// A path whose parent does not exist yet, as on a first run.
	svc := NewService(fake, filepath.Join(t.TempDir(), ".mivia", "auth.json"))

	outcome, err := svc.Logout(context.Background())
	if err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if outcome.ServerRevoked || outcome.RevokeErr != nil {
		t.Errorf("outcome = %+v, want a no-op", outcome)
	}
	if _, _, revoke, _ := fake.counts(); revoke != 0 {
		t.Errorf("revoke calls = %d, want 0 with nothing stored", revoke)
	}
}

// TestLoginSavesEvenWhenTheLockIsBusy: the user asked to log in, and a wedged
// neighbouring process must not make that impossible. Only ensureToken takes
// the strict form, because only it can spend a one-time-use refresh token.
func TestLoginSavesEvenWhenTheLockIsBusy(t *testing.T) {
	fake := &fakeSessionClient{loginToken: farFutureToken()}
	svc, path := newTestService(t, fake)
	svc.acquire = stubAcquire(lockBusy)

	if err := svc.Login(context.Background(), "user@example.com", []byte("pw")); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if got := mustLoad(t, path).RefreshToken; got != "rt-far" {
		t.Errorf("stored RefreshToken = %q, want the login to have been persisted anyway", got)
	}
}

// TestLogoutDeletesEvenWhenTheLockIsBusy: logout's contract is that it always
// succeeds locally. Telling a user they are logged out while leaving the
// session file behind would be worse than the race this skips.
func TestLogoutDeletesEvenWhenTheLockIsBusy(t *testing.T) {
	fake := &fakeSessionClient{}
	svc, path := newTestService(t, fake)
	mustSave(t, path, farFutureToken())
	svc.acquire = stubAcquire(lockBusy)

	if _, err := svc.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if !fileGone(t, path) {
		t.Error("Logout() left the session file behind because the lock was busy")
	}
}
