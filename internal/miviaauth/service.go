package miviaauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrReauthRequired means no usable local session exists — the caller should
// prompt the user to run `mivia login` again. It covers: no stored token, a
// corrupt stored token, a stored token written before the /v1 contract (no
// refresh token, and a bearer this API cannot authenticate), and a definitive
// 401 from the server.
var ErrReauthRequired = errors.New("not logged in; run `mivia login`")

// ErrSessionLost means the server rotated the session but the replacement
// could not be written to disk. The refresh token that was on disk is now
// dead server-side and its replacement exists nowhere durable, so the session
// is gone even though nothing failed on the wire.
var ErrSessionLost = errors.New("the session was renewed but could not be saved; run `mivia login`")

// ErrRefreshBusy means another mivia process held the session file for the
// whole lock budget while this one needed to refresh. It is TRANSIENT: the
// stored token is untouched and retrying is the right response.
//
// This deliberately refuses rather than proceeding unlocked. Refreshing
// anyway is precisely the double-refresh the lock exists to prevent, and its
// penalty is the server revoking the session for both processes.
var ErrRefreshBusy = errors.New("another mivia process is refreshing the session; try again in a moment")

// refreshSkew is how far ahead of actual expiry Ensure proactively refreshes.
const refreshSkew = 10 * time.Minute

// sessionClient is the subset of Client's behavior Service depends on, so
// tests can substitute a fake instead of a real HTTP round trip.
type sessionClient interface {
	Login(ctx context.Context, email string, password []byte) (Token, error)
	Refresh(ctx context.Context, refreshToken string) (Token, error)
	Revoke(ctx context.Context, bearer, refreshToken string) error
	Me(ctx context.Context, bearer string) (Identity, error)
}

// Service manages the local CLI session lifecycle: login, logout, keeping a
// stored token usable across calls, and reporting who is logged in.
type Service struct {
	client sessionClient
	path   string

	// acquire takes the refresh lock. It is a field so tests can drive the
	// unavailable and busy branches deterministically, instead of racing a
	// real second process or waiting out the budget.
	acquire acquireFunc
}

// NewService builds a Service. client is typically *Client from NewClient;
// path is typically config.UserAuthPath().
func NewService(client sessionClient, path string) *Service {
	return &Service{client: client, path: path, acquire: withRefreshLock}
}

// lockPath is where this Service's refresh lock lives.
func (s *Service) lockPath() string { return lockPathFor(s.path) }

// runSerialized runs fn under the refresh lock, and runs it anyway if another
// process holds the lock for the whole budget.
//
// This is for the operations where blocking is worse than racing: the user
// asked to log in, or to log out, and a wedged neighbour must not make either
// impossible. Only ensureToken needs the strict form, because only it can
// spend a one-time-use refresh token, which is the thing that must never
// happen twice.
func (s *Service) runSerialized(fn func() error) error {
	result, err := s.acquire(s.lockPath(), fn)
	if err != nil {
		return err
	}
	if result == lockBusy {
		return fn()
	}
	return nil
}

// Login authenticates and persists the resulting Token to path.
//
// The lock covers only the write. The credential exchange itself needs no
// serialization, and holding the lock across it would put a network round
// trip inside a span that Ensure's wait budget is derived from.
func (s *Service) Login(ctx context.Context, email string, password []byte) error {
	tok, err := s.client.Login(ctx, email, password)
	if err != nil {
		return fmt.Errorf("miviaauth: login: %w", err)
	}
	var saveErr error
	if err := s.runSerialized(func() error {
		saveErr = Save(s.path, tok)
		return nil
	}); err != nil {
		return fmt.Errorf("miviaauth: save token after login: %w", err)
	}
	if saveErr != nil {
		return fmt.Errorf("miviaauth: save token after login: %w", saveErr)
	}
	return nil
}

// LogoutOutcome reports what logout managed to do server-side. Logout always
// succeeds locally, so this is how a caller learns that the local file is
// gone but the server-side session may not be.
type LogoutOutcome struct {
	// ServerRevoked reports that the server acknowledged the revocation.
	ServerRevoked bool

	// RevokeErr is why the server-side revocation did not happen. It is
	// reported, not returned as the function's error: the user asked to log
	// out, and they are logged out locally either way.
	RevokeErr error
}

// Logout revokes the stored session server-side on a best-effort basis and
// deletes the local token file. It always succeeds locally, including
// offline, and reports the server-side result through LogoutOutcome so the
// caller can warn rather than fail.
//
// The revoke runs OUTSIDE the lock. It is bearer-authenticated and can take
// two round trips, and nothing about it is a read-modify-write of the session
// file; only the Load and the Delete need serializing.
func (s *Service) Logout(ctx context.Context) (LogoutOutcome, error) {
	var (
		tok     Token
		loadErr error
		outcome LogoutOutcome
	)
	if err := s.runSerialized(func() error {
		tok, loadErr = Load(s.path)
		return nil
	}); err != nil {
		return outcome, fmt.Errorf("miviaauth: logout: %w", err)
	}

	if loadErr != nil {
		if errors.Is(loadErr, ErrNotFound) {
			return outcome, nil
		}
		// A corrupt file: nothing to revoke server-side (no bearer to send),
		// but clear it so a stale/unreadable file doesn't linger.
		return outcome, s.deleteSession(tok, false)
	}

	outcome.RevokeErr = s.revokeSession(ctx, tok)
	outcome.ServerRevoked = outcome.RevokeErr == nil

	return outcome, s.deleteSession(tok, true)
}

// revokeSession ends the session server-side, refreshing once if the stored
// bearer has already expired.
//
// Revoke is bearer-gated, so a bearer older than an hour cannot revoke
// anything, and without the retry every logout after an idle hour would leave
// a 30-day refresh token alive server-side. The refresh is limited to that
// case on purpose: rotating resets the session's expiry to a fresh 30 days,
// so refreshing unconditionally and then failing the revoke would leave a
// LONGER-lived orphan than doing nothing.
//
// The session id survives rotation and revoke matches the previous token hash
// as well as the current one, so a token another process rotated away in the
// meantime still names the right session.
func (s *Service) revokeSession(ctx context.Context, tok Token) error {
	err := s.client.Revoke(ctx, tok.Bearer, tok.RefreshToken)
	if err == nil {
		return nil
	}
	if !isDefinitive401(err) || tok.RefreshToken == "" {
		return err
	}

	fresh, refreshErr := s.client.Refresh(ctx, tok.RefreshToken)
	if refreshErr != nil {
		if isDefinitive401(refreshErr) {
			// The server refused the refresh token. Either it was already
			// revoked or this was a reuse, which the server answers by
			// revoking the session. Either way the session is not alive.
			return nil
		}
		return refreshErr
	}
	return s.client.Revoke(ctx, fresh.Bearer, fresh.RefreshToken)
}

// deleteSession removes the local token file under the lock.
//
// When expected is non-zero it is a compare-and-delete: if another process
// saved a different session while this logout was talking to the server, that
// newer session is left alone rather than destroyed by a logout that was
// never about it.
//
// A busy lock does NOT stop the delete. Logout's contract is that it always
// succeeds locally, and that outranks the guard here: the worst case is the
// race the lock exists to narrow, while the alternative is telling a user
// they are logged out when they are not.
func (s *Service) deleteSession(expected Token, compare bool) error {
	var delErr error
	if err := s.runSerialized(func() error {
		delErr = s.deleteIfUnchanged(expected, compare)
		return nil
	}); err != nil {
		return fmt.Errorf("miviaauth: logout: %w", err)
	}
	return delErr
}

func (s *Service) deleteIfUnchanged(expected Token, compare bool) error {
	if compare {
		if current, err := Load(s.path); err == nil && current.RefreshToken != expected.RefreshToken {
			return nil
		}
	}
	return Delete(s.path)
}

// Ensure returns a currently-valid bearer, refreshing the stored session when
// it is within refreshSkew of expiry or already past it.
func (s *Service) Ensure(ctx context.Context) (string, error) {
	tok, err := s.ensureToken(ctx)
	if err != nil {
		return "", err
	}
	return tok.Bearer, nil
}

// ensureToken is Ensure's real body, returning the whole Token so Whoami can
// report the expiry without a second read of a file that may have just been
// rewritten.
//
// The lock spans Load through Save, which is what lets a caller that lost the
// race re-read inside the lock and discover it no longer needs to refresh at
// all. Every invocation pays one lock acquisition even on the fast path; that
// is the cost of making the decision and the write atomic.
func (s *Service) ensureToken(ctx context.Context) (Token, error) {
	var (
		tok    Token
		tokErr error
	)
	result, err := s.acquire(s.lockPath(), func() error {
		tok, tokErr = s.resolveToken(ctx)
		return nil
	})
	if err != nil {
		return Token{}, fmt.Errorf("miviaauth: ensure: %w", err)
	}

	if result == lockBusy {
		// resolveToken did not run: the lock contract does not let a caller
		// barge past a lock another process is holding.
		return s.resolveAfterBusyLock()
	}
	return tok, tokErr
}

// resolveAfterBusyLock decides what to do when another process held the lock
// for the whole budget.
//
// It re-reads rather than refreshing blind. If the holder finished its work,
// the stored token is now usable and this caller is simply done. Only when
// the token still needs a refresh is contention reported, because refreshing
// alongside the holder is the exact double-refresh that costs both processes
// the session.
func (s *Service) resolveAfterBusyLock() (Token, error) {
	tok, err := Load(s.path)
	if err != nil {
		// The holder was a logout, or a refresh the server refused. The user
		// is not logged in; saying "busy, try again" would be a lie. Nothing
		// is deleted here: this caller never held the lock, so it is in no
		// position to destroy a file it did not read under one.
		return Token{}, ErrReauthRequired
	}
	if tok.RefreshToken != "" && !tok.NeedsRefresh(time.Now(), refreshSkew) {
		return tok, nil
	}
	return Token{}, ErrRefreshBusy
}

// resolveToken is the locked decision: read the stored session and either use
// it or renew it. It must only run while the refresh lock is held (or while
// no lock is available at all).
func (s *Service) resolveToken(ctx context.Context) (Token, error) {
	tok, err := Load(s.path)
	if err != nil {
		_ = Delete(s.path)
		return Token{}, ErrReauthRequired
	}

	if tok.RefreshToken == "" {
		// Written before the /v1 contract. Its bearer is a token from the
		// previous backend and cannot authenticate here at all, so there is
		// nothing to salvage regardless of how much of its lifetime is left.
		_ = Delete(s.path)
		return Token{}, ErrReauthRequired
	}

	if !tok.NeedsRefresh(time.Now(), refreshSkew) {
		return tok, nil
	}
	return s.refresh(ctx, tok)
}

// refresh exchanges the stored refresh token for a new session and persists
// the result.
func (s *Service) refresh(ctx context.Context, tok Token) (Token, error) {
	newTok, err := s.client.Refresh(ctx, tok.RefreshToken)
	if err != nil {
		if isDefinitive401(err) {
			_ = Delete(s.path)
			return Token{}, ErrReauthRequired
		}
		// Transient: a 5xx, a rate limit, or no network at all. The stored
		// file is left exactly as it was, specifically so a later retry can
		// succeed once the condition clears.
		return Token{}, fmt.Errorf("miviaauth: refresh: %w", err)
	}

	if saveErr := Save(s.path, newTok); saveErr != nil {
		// The rotation already happened server-side, so the token still on
		// disk is dead. Leaving it would be worse than removing it: the next
		// refresh would present it, and a rotated-away token is the server's
		// theft signal, which revokes the session outright.
		//
		// There is no retry here. The write failed for a reason that does not
		// heal in a millisecond -- a full disk, a read-only mount, a
		// permission change -- and no ordering on this side could have
		// avoided the loss, because the server commits the rotation before it
		// serializes the response.
		_ = Delete(s.path)
		return Token{}, fmt.Errorf("miviaauth: %w: %v", ErrSessionLost, saveErr)
	}

	return newTok, nil
}

// WhoamiResult is the authenticated identity plus the expiry of the access
// token that proved it.
type WhoamiResult struct {
	Identity  Identity
	ExpiresAt time.Time
}

// Whoami reports who the stored session belongs to, refreshing it first if
// needed.
func (s *Service) Whoami(ctx context.Context) (WhoamiResult, error) {
	tok, err := s.ensureToken(ctx)
	if err != nil {
		return WhoamiResult{}, err
	}

	id, err := s.client.Me(ctx, tok.Bearer)
	if err != nil {
		if isDefinitive401(err) {
			// The bearer was accepted by ensureToken's own checks but the
			// server rejects it: the user was deleted, or the session was
			// revoked between the two calls. Clear the local file so the next
			// command asks for a login instead of repeating this.
			s.clearIfStillMine(tok)
			return WhoamiResult{}, ErrReauthRequired
		}
		return WhoamiResult{}, fmt.Errorf("miviaauth: whoami: %w", err)
	}
	return WhoamiResult{Identity: id, ExpiresAt: tok.ExpiresAt}, nil
}

// clearIfStillMine removes the session file only if it still holds the token
// that was just rejected.
//
// The comparison, not the lock, is what makes this safe: ensureToken released
// the lock before the Me call, so a `mivia login` in another shell may have
// saved a perfectly good session in between, and an unconditional delete
// would destroy it on the strength of a 401 about a different token. A busy
// lock skips the delete entirely -- failing to clean up is harmless, deleting
// the wrong session is not.
func (s *Service) clearIfStillMine(rejected Token) {
	_, _ = s.acquire(s.lockPath(), func() error {
		current, err := Load(s.path)
		if err != nil {
			return nil
		}
		if current.Bearer == rejected.Bearer {
			_ = Delete(s.path)
		}
		return nil
	})
}

// isDefinitive401 reports whether err is a 401 the mivia API demonstrably
// sent, as opposed to one injected by whatever sits between the CLI and the
// API. Only a definitive 401 may destroy a stored session: see
// StatusError.FromAPI.
func isDefinitive401(err error) bool {
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode == http.StatusUnauthorized && statusErr.FromAPI
}
