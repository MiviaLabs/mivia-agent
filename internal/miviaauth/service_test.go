package miviaauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeSessionClient is a hand-written test double for sessionClient. It lets
// tests control each call's outcome and count invocations, without any
// network round trip.
type fakeSessionClient struct {
	loginToken Token
	loginErr   error
	loginCalls int

	refreshToken Token
	refreshErr   error
	refreshCalls int

	revokeErr   error
	revokeCalls int
}

func (f *fakeSessionClient) Login(_ context.Context, _ string, _ []byte) (Token, error) {
	f.loginCalls++
	return f.loginToken, f.loginErr
}

func (f *fakeSessionClient) Refresh(_ context.Context, _ string) (Token, error) {
	f.refreshCalls++
	return f.refreshToken, f.refreshErr
}

func (f *fakeSessionClient) Revoke(_ context.Context, _ string) error {
	f.revokeCalls++
	return f.revokeErr
}

func farFutureToken() Token {
	return Token{
		Bearer:         "far-future-bearer",
		ExpiresAt:      time.Now().Add(24 * time.Hour),
		OrganizationID: "org-1",
		Role:           "admin",
	}
}

func nearExpiryToken() Token {
	return Token{
		Bearer:         "near-expiry-bearer",
		ExpiresAt:      time.Now().Add(1 * time.Minute),
		OrganizationID: "org-1",
		Role:           "admin",
	}
}

func expiredToken() Token {
	return Token{
		Bearer:         "expired-bearer",
		ExpiresAt:      time.Now().Add(-1 * time.Hour),
		OrganizationID: "org-1",
		Role:           "admin",
	}
}

func TestServiceLoginSuccessSavesToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	fake := &fakeSessionClient{loginToken: farFutureToken()}
	svc := NewService(fake, path)

	if err := svc.Login(context.Background(), "user@example.com", []byte("pw")); err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() after Login() error = %v", err)
	}
	if got.Bearer != farFutureToken().Bearer {
		t.Errorf("Load().Bearer = %q, want %q", got.Bearer, farFutureToken().Bearer)
	}
}

func TestServiceLoginErrorDoesNotSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	fake := &fakeSessionClient{loginErr: errors.New("bad credentials")}
	svc := NewService(fake, path)

	if err := svc.Login(context.Background(), "user@example.com", []byte("pw")); err == nil {
		t.Fatal("Login() error = nil, want an error")
	}

	if _, err := Load(path); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() after failed Login() error = %v, want ErrNotFound", err)
	}
}

func TestServiceLogoutRevokesAndDeletes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := Save(path, farFutureToken()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	fake := &fakeSessionClient{}
	svc := NewService(fake, path)

	if err := svc.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if fake.revokeCalls != 1 {
		t.Errorf("revokeCalls = %d, want 1", fake.revokeCalls)
	}
	if _, err := Load(path); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() after Logout() error = %v, want ErrNotFound", err)
	}
}

func TestServiceLogoutRevokeErrorStillDeletesAndReturnsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := Save(path, farFutureToken()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	fake := &fakeSessionClient{revokeErr: errors.New("offline")}
	svc := NewService(fake, path)

	if err := svc.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v, want nil (offline-safe)", err)
	}
	if _, err := Load(path); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() after Logout() error = %v, want ErrNotFound", err)
	}
}

func TestServiceLogoutNothingStoredReturnsNilWithoutRevoke(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	fake := &fakeSessionClient{}
	svc := NewService(fake, path)

	if err := svc.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v, want nil", err)
	}
	if fake.revokeCalls != 0 {
		t.Errorf("revokeCalls = %d, want 0", fake.revokeCalls)
	}
}

func TestServiceEnsureFastPathNoRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	tok := farFutureToken()
	if err := Save(path, tok); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	fake := &fakeSessionClient{}
	svc := NewService(fake, path)

	got, err := svc.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if got != tok.Bearer {
		t.Errorf("Ensure() = %q, want %q", got, tok.Bearer)
	}
	if fake.refreshCalls != 0 {
		t.Errorf("refreshCalls = %d, want 0", fake.refreshCalls)
	}
}

func TestServiceEnsureNearExpiryRefreshesAndSaves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := Save(path, nearExpiryToken()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	newTok := farFutureToken()
	newTok.Bearer = "refreshed-bearer"
	fake := &fakeSessionClient{refreshToken: newTok}
	svc := NewService(fake, path)

	got, err := svc.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if got != newTok.Bearer {
		t.Errorf("Ensure() = %q, want %q", got, newTok.Bearer)
	}
	if fake.refreshCalls != 1 {
		t.Errorf("refreshCalls = %d, want 1", fake.refreshCalls)
	}

	stored, err := Load(path)
	if err != nil {
		t.Fatalf("Load() after Ensure() error = %v", err)
	}
	if stored.Bearer != newTok.Bearer {
		t.Errorf("stored Bearer = %q, want %q", stored.Bearer, newTok.Bearer)
	}
}

func TestServiceEnsureAlreadyExpiredNeverCallsRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := Save(path, expiredToken()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	fake := &fakeSessionClient{}
	svc := NewService(fake, path)

	_, err := svc.Ensure(context.Background())
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("Ensure() error = %v, want ErrReauthRequired", err)
	}
	if fake.refreshCalls != 0 {
		t.Errorf("refreshCalls = %d, want 0", fake.refreshCalls)
	}
	if _, err := Load(path); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() after Ensure() error = %v, want ErrNotFound", err)
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
	fake := &fakeSessionClient{refreshErr: &StatusError{StatusCode: 401}}
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
	fake := &fakeSessionClient{refreshErr: &StatusError{StatusCode: 503}}
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

	stored, loadErr := Load(path)
	if loadErr != nil {
		t.Fatalf("Load() after Ensure() error = %v, want file untouched", loadErr)
	}
	if stored.Bearer != tok.Bearer {
		t.Errorf("stored Bearer = %q, want unchanged %q", stored.Bearer, tok.Bearer)
	}
}

func TestServiceEnsureRefreshNetworkErrorLeavesTokenUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	tok := nearExpiryToken()
	if err := Save(path, tok); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
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

	stored, loadErr := Load(path)
	if loadErr != nil {
		t.Fatalf("Load() after Ensure() error = %v, want file untouched", loadErr)
	}
	if stored.Bearer != tok.Bearer {
		t.Errorf("stored Bearer = %q, want unchanged %q", stored.Bearer, tok.Bearer)
	}
}

func TestServiceEnsureRefreshSucceedsButSaveFailsReturnsNewBearerAndError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based write failures are not portable to windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := Save(path, nearExpiryToken()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	// Remove write permission on the parent dir so the subsequent Save
	// inside Ensure's refresh path fails (os.CreateTemp cannot create the
	// temp file), while Load (read-only) still succeeds.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	newTok := farFutureToken()
	newTok.Bearer = "rotated-bearer"
	fake := &fakeSessionClient{refreshToken: newTok}
	svc := NewService(fake, path)

	got, err := svc.Ensure(context.Background())
	if err == nil {
		t.Fatal("Ensure() error = nil, want a wrapped save error")
	}
	if got != newTok.Bearer {
		t.Errorf("Ensure() bearer = %q, want new bearer %q alongside the error", got, newTok.Bearer)
	}
}
