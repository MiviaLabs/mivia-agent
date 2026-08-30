package miviaauth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeSessionClient is a hand-written test double for sessionClient. It lets
// tests control each call's outcome and record what was sent, without any
// network round trip.
type fakeSessionClient struct {
	mu sync.Mutex

	loginToken Token
	loginErr   error
	loginCalls int

	refreshToken Token
	refreshErr   error
	refreshCalls int
	refreshSent  []string

	revokeErr    error
	revokeErrs   []error
	revokeCalls  int
	revokeBearer []string
	revokeSent   []string

	meIdentity Identity
	meErr      error
	meCalls    int
	meBearer   []string
}

func (f *fakeSessionClient) Login(_ context.Context, _ string, _ []byte) (Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginCalls++
	return f.loginToken, f.loginErr
}

func (f *fakeSessionClient) Refresh(_ context.Context, refreshToken string) (Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshCalls++
	f.refreshSent = append(f.refreshSent, refreshToken)
	return f.refreshToken, f.refreshErr
}

func (f *fakeSessionClient) Revoke(_ context.Context, bearer, refreshToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeBearer = append(f.revokeBearer, bearer)
	f.revokeSent = append(f.revokeSent, refreshToken)
	idx := f.revokeCalls
	f.revokeCalls++
	if idx < len(f.revokeErrs) {
		return f.revokeErrs[idx]
	}
	return f.revokeErr
}

func (f *fakeSessionClient) Me(_ context.Context, bearer string) (Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meCalls++
	f.meBearer = append(f.meBearer, bearer)
	return f.meIdentity, f.meErr
}

func (f *fakeSessionClient) counts() (login, refresh, revoke, me int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loginCalls, f.refreshCalls, f.revokeCalls, f.meCalls
}

// newTestService builds a Service over a temp auth path.
func newTestService(t *testing.T, client sessionClient) (*Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	return NewService(client, path), path
}

// stubAcquire builds an acquire that reports result, honouring the real
// contract: the guarded function runs for lockHeld and lockUnavailable, and
// does NOT run for lockBusy. A stub that skipped fn for lockUnavailable would
// hide the exact defect this contract exists to prevent.
func stubAcquire(result lockResult) acquireFunc {
	return func(_ string, fn func() error) (lockResult, error) {
		if result == lockBusy {
			return result, nil
		}
		return result, fn()
	}
}

// definitive401 is a 401 the API demonstrably sent.
func definitive401() error {
	return &StatusError{StatusCode: http.StatusUnauthorized, FromAPI: true, Detail: "Invalid or expired session"}
}

// intercepted401 is a 401 from something that is not the API -- a captive
// portal, a proxy, an SSO interstitial.
func intercepted401() error {
	return &StatusError{StatusCode: http.StatusUnauthorized}
}

func transient503() error {
	return &StatusError{StatusCode: http.StatusServiceUnavailable, FromAPI: true}
}

func tokenAt(bearer, refresh string, expiresIn time.Duration) Token {
	return Token{
		Bearer:         bearer,
		RefreshToken:   refresh,
		ExpiresAt:      time.Now().Add(expiresIn),
		OrganizationID: "org-1",
		Role:           "admin",
	}
}

func farFutureToken() Token  { return tokenAt("far-future-bearer", "rt-far", 24*time.Hour) }
func nearExpiryToken() Token { return tokenAt("near-expiry-bearer", "rt-near", 1*time.Minute) }
func expiredToken() Token    { return tokenAt("expired-bearer", "rt-expired", -1*time.Hour) }

// legacyToken is a session file written before the /v1 contract: a bearer
// from the previous backend and no refresh token at all.
func legacyToken() Token {
	return Token{
		Bearer:         "go-mivia-era-bearer",
		ExpiresAt:      time.Now().Add(24 * time.Hour),
		OrganizationID: "org-1",
		Role:           "admin",
	}
}

func mustSave(t *testing.T, path string, tok Token) {
	t.Helper()
	if err := Save(path, tok); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
}

func mustLoad(t *testing.T, path string) Token {
	t.Helper()
	tok, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return tok
}

func fileGone(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// fileSnapshot captures content and modification time, so "untouched" can be
// asserted as byte-for-byte rather than as mere existence.
type fileSnapshot struct {
	data []byte
	mod  time.Time
}

func snapshotFile(t *testing.T, path string) fileSnapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fileSnapshot{data: data, mod: info.ModTime()}
}

func (s fileSnapshot) assertUnchanged(t *testing.T, path string) {
	t.Helper()
	now := snapshotFile(t, path)
	if string(now.data) != string(s.data) {
		t.Errorf("%s content changed\n before: %s\n  after: %s", path, s.data, now.data)
	}
	if !now.mod.Equal(s.mod) {
		t.Errorf("%s was rewritten (mtime %v -> %v)", path, s.mod, now.mod)
	}
}

func TestServiceLoginSuccessSavesToken(t *testing.T) {
	fake := &fakeSessionClient{loginToken: farFutureToken()}
	svc, path := newTestService(t, fake)

	if err := svc.Login(context.Background(), "user@example.com", []byte("pw")); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	got := mustLoad(t, path)
	if got.Bearer != "far-future-bearer" {
		t.Errorf("stored Bearer = %q", got.Bearer)
	}
	if got.RefreshToken != "rt-far" {
		t.Errorf("stored RefreshToken = %q, want the refresh token persisted", got.RefreshToken)
	}
}

func TestServiceLoginErrorDoesNotSave(t *testing.T) {
	fake := &fakeSessionClient{loginErr: definitive401()}
	svc, path := newTestService(t, fake)

	if err := svc.Login(context.Background(), "user@example.com", []byte("pw")); err == nil {
		t.Fatal("Login() returned nil for a rejected credential")
	}
	if !fileGone(t, path) {
		t.Error("Login() wrote a session file despite failing")
	}
}

// --- Ensure: the session-keeping contract ------------------------------

func TestServiceLogoutRevokesAndDeletes(t *testing.T) {
	fake := &fakeSessionClient{}
	svc, path := newTestService(t, fake)
	mustSave(t, path, farFutureToken())

	outcome, err := svc.Logout(context.Background())
	if err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if !outcome.ServerRevoked {
		t.Error("ServerRevoked = false after a successful revoke")
	}
	if len(fake.revokeBearer) != 1 || fake.revokeBearer[0] != "far-future-bearer" {
		t.Errorf("revoke bearers = %v, want the stored bearer", fake.revokeBearer)
	}
	if len(fake.revokeSent) != 1 || fake.revokeSent[0] != "rt-far" {
		t.Errorf("revoke refresh tokens = %v, want the stored one", fake.revokeSent)
	}
	if !fileGone(t, path) {
		t.Error("Logout() left the session file behind")
	}
	if _, refresh, _, _ := fake.counts(); refresh != 0 {
		t.Errorf("refresh calls = %d, want 0: a live bearer needs no refresh to revoke", refresh)
	}
}

func TestServiceLogoutRevokeErrorStillDeletesAndReturnsNil(t *testing.T) {
	fake := &fakeSessionClient{revokeErr: errors.New("dial tcp: no route to host")}
	svc, path := newTestService(t, fake)
	mustSave(t, path, farFutureToken())

	outcome, err := svc.Logout(context.Background())
	if err != nil {
		t.Fatalf("Logout() error = %v, want logout to succeed locally even offline", err)
	}
	if outcome.ServerRevoked {
		t.Error("ServerRevoked = true after a failed revoke")
	}
	if outcome.RevokeErr == nil {
		t.Error("RevokeErr = nil; the caller cannot warn about what it does not learn")
	}
	if !fileGone(t, path) {
		t.Error("Logout() left the session file behind after a revoke failure")
	}
}

func TestServiceLogoutNothingStoredReturnsNilWithoutRevoke(t *testing.T) {
	fake := &fakeSessionClient{}
	svc, _ := newTestService(t, fake)

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

func TestServiceEnsureFastPathNoRefresh(t *testing.T) {
	fake := &fakeSessionClient{}
	svc, path := newTestService(t, fake)
	mustSave(t, path, farFutureToken())

	bearer, err := svc.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if bearer != "far-future-bearer" {
		t.Errorf("Ensure() = %q, want the stored bearer", bearer)
	}
	if _, refresh, _, _ := fake.counts(); refresh != 0 {
		t.Errorf("refresh calls = %d, want 0 for a token nowhere near expiry", refresh)
	}
}

func TestServiceEnsureNearExpiryRefreshesAndSaves(t *testing.T) {
	fake := &fakeSessionClient{refreshToken: tokenAt("fresh-bearer", "rt-new", time.Hour)}
	svc, path := newTestService(t, fake)
	mustSave(t, path, nearExpiryToken())

	bearer, err := svc.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if bearer != "fresh-bearer" {
		t.Errorf("Ensure() = %q, want the refreshed bearer", bearer)
	}
	if got := fake.refreshSent; len(got) != 1 || got[0] != "rt-near" {
		t.Errorf("refresh sent %v, want the stored refresh token", got)
	}
	stored := mustLoad(t, path)
	if stored.RefreshToken != "rt-new" {
		t.Errorf("stored RefreshToken = %q, want the ROTATED token persisted", stored.RefreshToken)
	}
}

func TestServiceEnsureMissingTokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	fake := &fakeSessionClient{}
	svc := NewService(fake, path)

	_, err := svc.Ensure(context.Background())
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("Ensure() error = %v, want ErrReauthRequired", err)
	}
	if fake.refreshCalls != 0 || fake.loginCalls != 0 {
		t.Errorf("refreshCalls = %d, loginCalls = %d, want 0, 0", fake.refreshCalls, fake.loginCalls)
	}
}

func TestServiceEnsureRefresh401ClearsTokenAndReturnsReauth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := Save(path, nearExpiryToken()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	fake := &fakeSessionClient{refreshErr: definitive401()}
	svc := NewService(fake, path)

	_, err := svc.Ensure(context.Background())
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("Ensure() error = %v, want ErrReauthRequired", err)
	}
	if _, err := Load(path); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() after Ensure() error = %v, want ErrNotFound", err)
	}
}

func TestServiceEnsureRefresh503LeavesTokenUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	tok := nearExpiryToken()
	if err := Save(path, tok); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	before := snapshotFile(t, path)
	fake := &fakeSessionClient{refreshErr: transient503()}
	svc := NewService(fake, path)

	got, err := svc.Ensure(context.Background())
	if err == nil {
		t.Fatal("Ensure() error = nil, want a wrapped transient error")
	}
	if errors.Is(err, ErrReauthRequired) {
		t.Errorf("Ensure() error = %v, want NOT ErrReauthRequired", err)
	}
	if got != "" {
		t.Errorf("Ensure() bearer = %q, want empty string alongside error", got)
	}

	// Byte-for-byte, not merely present: a transient failure that silently
	// rewrote the file would still have burned the stored refresh token.
	before.assertUnchanged(t, path)
}

func TestServiceEnsureRefreshNetworkErrorLeavesTokenUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	tok := nearExpiryToken()
	if err := Save(path, tok); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	before := snapshotFile(t, path)
	fake := &fakeSessionClient{refreshErr: errors.New("dial tcp: connection refused")}
	svc := NewService(fake, path)

	got, err := svc.Ensure(context.Background())
	if err == nil {
		t.Fatal("Ensure() error = nil, want a wrapped transient error")
	}
	if errors.Is(err, ErrReauthRequired) {
		t.Errorf("Ensure() error = %v, want NOT ErrReauthRequired", err)
	}
	if got != "" {
		t.Errorf("Ensure() bearer = %q, want empty string alongside error", got)
	}

	before.assertUnchanged(t, path)
}

// TestServiceEnsureExpiredBearerWithRefreshTokenRefreshesSilently is the
// contract this rewire exists to fix. The old code short-circuited an expired
// bearer straight to re-authentication, because refresh used to be driven by
// the bearer itself. Under the /v1 contract the refresh token is what buys a
// new session, and it outlives the bearer by 30 days, so an expired bearer is
// a reason to refresh -- not a reason to make the user log in again.
func TestServiceEnsureExpiredBearerWithRefreshTokenRefreshesSilently(t *testing.T) {
	fake := &fakeSessionClient{refreshToken: tokenAt("fresh-bearer", "rt-new", time.Hour)}
	svc, path := newTestService(t, fake)
	mustSave(t, path, expiredToken())

	bearer, err := svc.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v, want a silent refresh", err)
	}
	if bearer != "fresh-bearer" {
		t.Errorf("Ensure() = %q, want the refreshed bearer", bearer)
	}
	if _, refresh, _, _ := fake.counts(); refresh != 1 {
		t.Errorf("refresh calls = %d, want exactly 1", refresh)
	}
	if mustLoad(t, path).RefreshToken != "rt-new" {
		t.Error("the rotated refresh token was not persisted")
	}
}

// TestServiceEnsureLegacyTokenWithoutRefreshTokenClearsAndReturnsReauth
// covers upgrading over an ~/.mivia/auth.json written by the previous
// release. Its bearer belongs to a backend this client no longer talks to, so
// there is nothing to salvage even though it has not expired -- but it must
// degrade to a clear "log in again", never a crash or a confusing 401.
func TestServiceEnsureLegacyTokenWithoutRefreshTokenClearsAndReturnsReauth(t *testing.T) {
	fake := &fakeSessionClient{}
	svc, path := newTestService(t, fake)
	mustSave(t, path, legacyToken())

	_, err := svc.Ensure(context.Background())
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("Ensure() error = %v, want ErrReauthRequired", err)
	}
	if !fileGone(t, path) {
		t.Error("the unusable legacy session file was left in place")
	}
	if _, refresh, _, _ := fake.counts(); refresh != 0 {
		t.Errorf("refresh calls = %d, want 0: there is no refresh token to send", refresh)
	}
}

func TestServiceEnsureCorruptTokenFileClearsAndReturnsReauth(t *testing.T) {
	svc, path := newTestService(t, &fakeSessionClient{})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Ensure(context.Background()); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("Ensure() error = %v, want ErrReauthRequired", err)
	}
	if !fileGone(t, path) {
		t.Error("the corrupt session file was left in place")
	}
}

// TestServiceEnsureIntercepted401LeavesTokenUntouched is the reason
// StatusError.FromAPI exists. A captive portal or corporate proxy can answer
// any request with a bare 401; believing it would destroy a refresh token
// that is still valid for up to 30 days server-side.
func TestServiceEnsureIntercepted401LeavesTokenUntouched(t *testing.T) {
	fake := &fakeSessionClient{refreshErr: intercepted401()}
	svc, path := newTestService(t, fake)
	mustSave(t, path, nearExpiryToken())
	before := snapshotFile(t, path)

	_, err := svc.Ensure(context.Background())
	if err == nil {
		t.Fatal("Ensure() returned nil after a failed refresh")
	}
	if errors.Is(err, ErrReauthRequired) {
		t.Error("a 401 that was not the API's was treated as definitive")
	}
	before.assertUnchanged(t, path)
}

// TestServiceEnsureRotationSaveFailureClearsFileAndReturnsSessionLost covers
// the one window this design cannot make safe. The server commits the
// rotation before it serializes its response, so once the refresh succeeds
// the token on disk is already dead; if the replacement cannot be written,
// the session is genuinely lost. The file must go -- leaving it would mean
// the next refresh presents a rotated-away token, which the server reads as
// theft -- and the error must say so loudly.
func TestServiceEnsureRotationSaveFailureClearsFileAndReturnsSessionLost(t *testing.T) {
	fake := &fakeSessionClient{refreshToken: tokenAt("fresh-bearer", "rt-new", time.Hour)}
	svc, path := newTestService(t, fake)
	mustSave(t, path, nearExpiryToken())

	// Fail the write through the store's own fsync seam: portable, and it
	// exercises the same branch a full disk would.
	original := syncFile
	syncFile = func(*os.File) error { return errors.New("no space left on device") }
	t.Cleanup(func() { syncFile = original })

	tok, err := svc.ensureToken(context.Background())
	if !errors.Is(err, ErrSessionLost) {
		t.Fatalf("ensureToken() error = %v, want ErrSessionLost", err)
	}
	if tok != (Token{}) {
		t.Errorf("ensureToken() returned %+v alongside an error; the session is gone, so there is no usable token to hand back", tok)
	}
	if !fileGone(t, path) {
		t.Error("the dead refresh token was left on disk, where the next refresh would present it as a reused token")
	}
}

// --- the refresh lock --------------------------------------------------

// TestServiceLogoutRefreshesOnlyAfterA401ThenRevokes: revoke is
// bearer-gated, so an idle-for-an-hour logout would otherwise leave a 30-day
// refresh token alive server-side.
func TestServiceLogoutRefreshesOnlyAfterA401ThenRevokes(t *testing.T) {
	fake := &fakeSessionClient{
		revokeErrs:   []error{definitive401(), nil},
		refreshToken: tokenAt("fresh-bearer", "rt-new", time.Hour),
	}
	svc, path := newTestService(t, fake)
	mustSave(t, path, expiredToken())

	outcome, err := svc.Logout(context.Background())
	if err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if !outcome.ServerRevoked {
		t.Errorf("ServerRevoked = false; RevokeErr = %v", outcome.RevokeErr)
	}
	if _, refresh, revoke, _ := fake.counts(); refresh != 1 || revoke != 2 {
		t.Errorf("refresh=%d revoke=%d, want one refresh and two revokes", refresh, revoke)
	}
	if got := fake.revokeBearer[1]; got != "fresh-bearer" {
		t.Errorf("second revoke used bearer %q, want the refreshed one", got)
	}
	if !fileGone(t, path) {
		t.Error("Logout() left the session file behind")
	}
}

// TestServiceLogoutDoesNotRefreshOnALiveRevoke pins the narrowness of that
// retry: rotating resets the session's expiry to a fresh 30 days, so
// refreshing when it is not needed would leave a LONGER-lived orphan if the
// follow-up revoke then failed.
func TestServiceLogoutDoesNotRefreshOnALiveRevoke(t *testing.T) {
	fake := &fakeSessionClient{}
	svc, path := newTestService(t, fake)
	mustSave(t, path, expiredToken())

	if _, err := svc.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, refresh, _, _ := fake.counts(); refresh != 0 {
		t.Errorf("refresh calls = %d, want 0 when the revoke itself succeeded", refresh)
	}
}

func TestServiceLogoutCorruptFileDeletesWithoutRevoking(t *testing.T) {
	fake := &fakeSessionClient{}
	svc, path := newTestService(t, fake)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if !fileGone(t, path) {
		t.Error("the corrupt session file was left in place")
	}
	if _, _, revoke, _ := fake.counts(); revoke != 0 {
		t.Errorf("revoke calls = %d, want 0: there is no bearer to send", revoke)
	}
}

// TestServiceLogoutDoesNotDeleteASessionSavedWhileItWasRevoking: the revoke
// runs unlocked, so a `mivia login` in another shell can land in between.
// This logout was never about that session.
func TestServiceLogoutDoesNotDeleteASessionSavedWhileItWasRevoking(t *testing.T) {
	svc, path := newTestService(t, nil)
	mustSave(t, path, farFutureToken())

	newer := tokenAt("bearer-from-another-shell", "rt-newer", 24*time.Hour)
	fake := &fakeSessionClient{}
	fake.revokeErr = nil
	svc.client = &loginDuringRevokeClient{fake: fake, path: path, newer: newer, t: t}

	if _, err := svc.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if fileGone(t, path) {
		t.Fatal("logout deleted a session another process saved while it was talking to the server")
	}
	if got := mustLoad(t, path); got.Bearer != "bearer-from-another-shell" {
		t.Errorf("stored Bearer = %q, want the newer session intact", got.Bearer)
	}
}

// loginDuringMeClient simulates another process completing a login while this
// one is waiting on GET /v1/auth/me.
type loginDuringMeClient struct {
	fake  *fakeSessionClient
	path  string
	newer Token
	t     *testing.T
}

func (c *loginDuringMeClient) Login(ctx context.Context, e string, p []byte) (Token, error) {
	return c.fake.Login(ctx, e, p)
}
func (c *loginDuringMeClient) Refresh(ctx context.Context, rt string) (Token, error) {
	return c.fake.Refresh(ctx, rt)
}
func (c *loginDuringMeClient) Revoke(ctx context.Context, b, rt string) error {
	return c.fake.Revoke(ctx, b, rt)
}
func (c *loginDuringMeClient) Me(ctx context.Context, bearer string) (Identity, error) {
	mustSave(c.t, c.path, c.newer)
	return c.fake.Me(ctx, bearer)
}

// loginDuringRevokeClient simulates another process completing a login while
// this one is mid-revoke.
type loginDuringRevokeClient struct {
	fake  *fakeSessionClient
	path  string
	newer Token
	t     *testing.T
}

func (c *loginDuringRevokeClient) Login(ctx context.Context, e string, p []byte) (Token, error) {
	return c.fake.Login(ctx, e, p)
}
func (c *loginDuringRevokeClient) Refresh(ctx context.Context, rt string) (Token, error) {
	return c.fake.Refresh(ctx, rt)
}
func (c *loginDuringRevokeClient) Me(ctx context.Context, b string) (Identity, error) {
	return c.fake.Me(ctx, b)
}
func (c *loginDuringRevokeClient) Revoke(ctx context.Context, bearer, refreshToken string) error {
	mustSave(c.t, c.path, c.newer)
	return c.fake.Revoke(ctx, bearer, refreshToken)
}

// --- the lock contract's own trap --------------------------------------
