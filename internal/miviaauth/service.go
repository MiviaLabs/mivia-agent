package miviaauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrReauthRequired means no usable local session exists — the caller
// should prompt the user to run `mivia login` again. It covers: no stored
// token, a corrupt stored token, an already-expired stored token (refresh
// never works past expiry per go-mivia's contract), and a definitive 401
// from the refresh endpoint (the stored bearer was revoked/invalid).
var ErrReauthRequired = errors.New("not logged in; run `mivia login`")

// refreshSkew is how far ahead of actual expiry Ensure proactively refreshes.
const refreshSkew = 10 * time.Minute

// sessionClient is the subset of Client's behavior Service depends on, so
// tests can substitute a fake instead of a real HTTP round trip.
type sessionClient interface {
	Login(ctx context.Context, email string, password []byte) (Token, error)
	Refresh(ctx context.Context, bearer string) (Token, error)
	Revoke(ctx context.Context, bearer string) error
}

// Service manages the local CLI session lifecycle: login, logout, and
// keeping a stored token usable across calls.
type Service struct {
	client sessionClient
	path   string
}

// NewService builds a Service. client is typically *Client from NewClient;
// path is typically config.UserAuthPath().
func NewService(client sessionClient, path string) *Service {
	return &Service{client: client, path: path}
}

// Login authenticates and persists the resulting Token to path.
func (s *Service) Login(ctx context.Context, email string, password []byte) error {
	tok, err := s.client.Login(ctx, email, password)
	if err != nil {
		return fmt.Errorf("miviaauth: login: %w", err)
	}
	if err := Save(s.path, tok); err != nil {
		return fmt.Errorf("miviaauth: save token after login: %w", err)
	}
	return nil
}

// Logout revokes the stored bearer server-side on a best-effort basis (a
// network failure here must NOT prevent the local token file from being
// deleted — logout must always succeed locally, including offline) and
// deletes the local token file.
func (s *Service) Logout(ctx context.Context) error {
	tok, err := Load(s.path)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		// A corrupt file: nothing to revoke server-side (no bearer to send),
		// but clear it so a stale/unreadable file doesn't linger.
		return Delete(s.path)
	}

	// Best-effort revoke: ignore the error, since Logout must succeed
	// locally even when offline or the server is unreachable.
	_ = s.client.Revoke(ctx, tok.Bearer)

	return Delete(s.path)
}

// Ensure returns a currently-valid bearer, refreshing the stored token if
// it is within refreshSkew of expiry (or already expired -> ErrReauthRequired,
// since refresh never works on an already-expired bearer). A definitive 401
// from Refresh also yields ErrReauthRequired (and clears the stale local
// token). A transient failure (429/503/network) leaves the stored token file
// untouched and returns a wrapped error with an EMPTY bearer string -- Go
// convention, never return a non-empty value alongside a non-nil error --
// so the caller must retry Ensure() later; the file is untouched specifically
// so that retry can succeed once the transient condition clears.
func (s *Service) Ensure(ctx context.Context) (string, error) {
	tok, err := Load(s.path)
	if err != nil {
		_ = Delete(s.path)
		return "", ErrReauthRequired
	}

	now := time.Now()
	if tok.Expired(now) {
		_ = Delete(s.path)
		return "", ErrReauthRequired
	}

	if !tok.NeedsRefresh(now, refreshSkew) {
		return tok.Bearer, nil
	}

	return s.refresh(ctx, tok)
}

// refresh performs the actual refresh round trip for a token that needs
// one, and applies the resulting save/error/clear rules. Split out of
// Ensure to keep that function short and each branch easy to name.
func (s *Service) refresh(ctx context.Context, tok Token) (string, error) {
	newTok, err := s.client.Refresh(ctx, tok.Bearer)
	if err != nil {
		var statusErr *StatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusUnauthorized {
			_ = Delete(s.path)
			return "", ErrReauthRequired
		}
		return "", fmt.Errorf("miviaauth: refresh: %w", err)
	}

	if err := Save(s.path, newTok); err != nil {
		// The refresh already succeeded and rotated the bearer server-side,
		// so returning the old (now-superseded) bearer would be strictly
		// worse than handing back the new one: the caller gets a working
		// bearer for this one call even though persistence failed, and the
		// wrapped error tells them persistence needs attention. This is the
		// one place Ensure deliberately breaks the "never return a non-empty
		// value alongside a non-nil error" convention, and this comment is
		// why.
		return newTok.Bearer, fmt.Errorf("miviaauth: save refreshed token: %w", err)
	}

	return newTok.Bearer, nil
}
