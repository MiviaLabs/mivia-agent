//go:build liveauth

// This file is compiled only under the `liveauth` build tag, and running it
// talks to a real deployment with real credentials. That is why it is tagged
// rather than env-gated with t.Skip: AGENTS.md forbids running a live e2e
// without an explicit ask, and a tag cannot be triggered by accident the way a
// skipped test can be by an env var that happens to be set.
//
// The tag also keeps the file out of .mivia/policy/test-skips.json. Everything
// here fails rather than skips: if you set the tag, you asked for a live run,
// so missing credentials are an error and not a quiet pass.
//
// Run it with:
//
//	make live-auth-smoke
//
// WHAT THIS IS FOR
//
// api/contracts/auth.v1.json is maintained by hand. Its README says plainly
// that it cannot see the live server drift: it pins what THIS package models,
// so it catches our edits, never theirs. This file is the other half. It is the
// only thing in the repo that compares the modelled contract against the
// deployment, and until it runs, "the CLI matches the API" is an assumption.

package miviaauth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const (
	envBaseURL  = "MIVIA_LIVE_API_BASE_URL"
	envEmail    = "MIVIA_LIVE_EMAIL"
	envPassword = "MIVIA_LIVE_PASSWORD"
)

// liveTimeout bounds the whole run. Each individual request already has
// Client's own requestTimeout; this stops a wedged deployment from hanging a
// developer's terminal.
const liveTimeout = 90 * time.Second

// maxPlausibleAccessTokenTTL is a drift tripwire, not a contract assertion.
// The API signs access tokens for one hour (DEFAULT_TOKEN_TTL_SECONDS in
// apps/api's auth.service.ts). A value far outside that means the token
// lifetime changed, which is exactly what the refresh-longevity work could do,
// and this package's 10-minute refreshSkew is calibrated against the old one.
const maxPlausibleAccessTokenTTL = 24 * time.Hour

// liveEnv is the credential set for a live run. The password stays []byte to
// match Client.Login, which converts it to a string only at the marshal site.
type liveEnv struct {
	baseURL  string
	email    string
	password []byte
}

// readLiveEnv collects the credentials, failing loudly when any is absent.
// It reports every missing variable at once: discovering them one run at a
// time against a rate-limited login endpoint is needlessly slow.
func readLiveEnv(t *testing.T) liveEnv {
	t.Helper()
	var missing []string
	get := func(name string) string {
		value := os.Getenv(name)
		if value == "" {
			missing = append(missing, name)
		}
		return value
	}
	env := liveEnv{baseURL: get(envBaseURL), email: get(envEmail)}
	password := get(envPassword)
	if len(missing) > 0 {
		t.Fatalf("live auth smoke needs %v in the environment; set them for a throwaway test account, never a real user's", missing)
	}
	env.password = []byte(password)
	return env
}

// liveContext bounds the run and is cancelled when the test ends.
func liveContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	t.Cleanup(cancel)
	return ctx
}

// liveClient builds a Client against the configured deployment.
func liveClient(t *testing.T, env liveEnv) *Client {
	t.Helper()
	client, err := NewClient(env.baseURL)
	if err != nil {
		t.Fatalf("NewClient(%q) = %v; the base URL must be https, or http on loopback", env.baseURL, err)
	}
	return client
}

// liveLogin authenticates and returns the session. Token values are never
// logged: a failure message reports whether a field was present, never what it
// contained.
func liveLogin(t *testing.T, ctx context.Context, client *Client, env liveEnv) Token {
	t.Helper()
	tok, err := client.Login(ctx, env.email, env.password)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return tok
}

// TestLiveAuthContract walks one session through its whole life, in order,
// against the deployment.
//
// The subtests share a session and must run in order. That is deliberate: the
// refresh token is one-time use, so each step consumes the previous step's
// output, and a fresh login per subtest would both break that chain and spend
// the login rate-limit budget (the planned limit is 10 logins / 5 min per IP)
// on setup rather than on coverage.
func TestLiveAuthContract(t *testing.T) {
	env := readLiveEnv(t)
	ctx := liveContext(t)
	client := liveClient(t, env)

	session := liveLogin(t, ctx, client, env)

	t.Run("login returns the modelled session", func(t *testing.T) {
		checkSessionShape(t, session)
	})

	t.Run("me matches the login identity", func(t *testing.T) {
		checkIdentity(t, ctx, client, session, env)
	})

	t.Run("refresh rotates the refresh token", func(t *testing.T) {
		session = checkRotation(t, ctx, client, session)
	})

	t.Run("Ensure refreshes and persists", func(t *testing.T) {
		session = checkEnsurePersists(t, ctx, client, session)
	})

	t.Run("revoke ends the session", func(t *testing.T) {
		checkRevoke(t, ctx, client, session)
	})
}

// checkSessionShape asserts the three fields tokenFromSession requires, plus
// the expiry semantics refreshSkew depends on.
func checkSessionShape(t *testing.T, tok Token) {
	t.Helper()
	if tok.Bearer == "" {
		t.Error("login response had no access token")
	}
	if tok.RefreshToken == "" {
		t.Error("login response had no refresh token; the CLI treats a session without one as a pre-/v1 file and forces reauth")
	}
	if tok.ExpiresAt.IsZero() {
		t.Fatal("login response had no expiresAt")
	}

	ttl := time.Until(tok.ExpiresAt)
	if ttl <= 0 {
		t.Fatalf("access token expired %v ago at issue; every Ensure would refresh immediately", -ttl)
	}
	if ttl > maxPlausibleAccessTokenTTL {
		t.Errorf("access token TTL = %v, which is far past the modelled one hour; if the token lifetime changed, refreshSkew (%v) needs recalibrating", ttl, refreshSkew)
	}
	if ttl <= refreshSkew {
		t.Errorf("access token TTL = %v is inside refreshSkew (%v); every command would refresh on every call", ttl, refreshSkew)
	}
}

// checkIdentity proves /v1/auth/me answers for the bearer just issued, and
// that it describes the account that logged in.
func checkIdentity(t *testing.T, ctx context.Context, client *Client, tok Token, env liveEnv) {
	t.Helper()
	id, err := client.Me(ctx, tok.Bearer)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if id.Email != env.email {
		t.Errorf("Me().Email = %q, want %q", id.Email, env.email)
	}
	if id.ID == "" {
		t.Error("Me() returned no user id")
	}
	if id.OrganizationID == "" {
		t.Error("Me() returned no organization id")
	}
	if id.Role == "" {
		t.Error("Me() returned no role")
	}
}

// checkRotation proves the refresh token is genuinely one-time use: the server
// must hand back a different one. If it ever returns the same value, this
// package's whole save-or-lose-the-session design is solving a problem that no
// longer exists, and the theft-detection path would misfire.
//
// It deliberately does NOT assert that the access token changed. The JWT
// payload is {sub, organizationId, role, jti} plus second-granularity iat/exp,
// and jti is the session id, which is stable across rotation by design. A
// refresh in the same wall-clock second as the previous issue therefore
// produces a byte-identical token. Measured against the deployment on
// 2026-08-31: login and refresh 0.47s apart returned the identical string,
// while a pair straddling a second boundary did not.
//
// That is not a defect and nothing here depends on it: the comparisons that
// decide whether a session is still ours (deleteIfUnchanged, clearIfStillMine)
// use the refresh token, which is 32 random bytes and always changes. An
// inequality assertion on the bearer would just be a coin flip on timing.
func checkRotation(t *testing.T, ctx context.Context, client *Client, old Token) Token {
	t.Helper()
	fresh, err := client.Refresh(ctx, old.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	checkSessionShape(t, fresh)

	if fresh.RefreshToken == old.RefreshToken {
		t.Error("refresh returned the same refresh token; the contract says it rotates")
	}
	if fresh.ExpiresAt.Before(old.ExpiresAt) {
		t.Errorf("refreshed expiry %v is BEFORE the previous %v; a refresh must never shorten the session", fresh.ExpiresAt, old.ExpiresAt)
	}
	// The useful property is that the new bearer works, which is what the
	// discarded inequality check was reaching for.
	if _, err := client.Me(ctx, fresh.Bearer); err != nil {
		t.Errorf("the refreshed access token does not authenticate: %v", err)
	}
	return fresh
}

// checkEnsurePersists drives the real Service against the real server. This is
// the part no httptest can prove: a token that needs refreshing is renewed
// under the lock, the rotated value reaches disk, and the file the next process
// reads is the new one.
func checkEnsurePersists(t *testing.T, ctx context.Context, client *Client, tok Token) Token {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")

	// Seed a session that is live server-side but due for refresh locally.
	// Backdating the expiry is what makes resolveToken take the refresh branch
	// without waiting an hour for it to happen naturally.
	stale := tok
	stale.ExpiresAt = time.Now()
	if err := Save(path, stale); err != nil {
		t.Fatalf("Save: %v", err)
	}

	bearer, err := NewService(client, path).Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Whether the bearer STRING changed proves nothing - see checkRotation on
	// why a same-second refresh returns an identical JWT. That it authenticates,
	// and that the rotated refresh token below reached disk, is the real proof
	// the refresh happened and was persisted.
	if _, err := client.Me(ctx, bearer); err != nil {
		t.Errorf("the access token Ensure returned does not authenticate: %v", err)
	}

	stored, err := Load(path)
	if err != nil {
		t.Fatalf("Load after Ensure: %v", err)
	}
	if stored.RefreshToken == tok.RefreshToken {
		t.Fatal("Ensure did not persist the rotated refresh token; the next run would present a dead one, which the server reads as theft and answers by revoking the session")
	}
	if stored.Bearer != bearer {
		t.Error("the persisted access token differs from the one Ensure returned")
	}
	checkFilePermissions(t, path)
	return stored
}

// checkFilePermissions asserts the 0600 the store promises. Unix only: Windows
// does not carry these mode bits, and an `if` keeps this file free of t.Skip.
func checkFilePermissions(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("auth file mode = %v, want 0600", perm)
	}
}

// checkRevoke ends the session and proves it is actually dead, rather than
// trusting the {ok:true} acknowledgement.
func checkRevoke(t *testing.T, ctx context.Context, client *Client, tok Token) {
	t.Helper()
	if err := client.Revoke(ctx, tok.Bearer, tok.RefreshToken); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, err := client.Refresh(ctx, tok.RefreshToken)
	if err == nil {
		t.Fatal("refresh succeeded after revoke; the session outlived its revocation")
	}
	requireAPIStatus(t, err, http.StatusUnauthorized)
}

// TestLiveReusedRefreshTokenIsRejected proves the server's theft detection.
//
// It uses its own login because the check destroys the session: presenting a
// rotated-away token is the signal that revokes it outright. That cost is the
// point -- Service.refresh deletes the local file on a definitive 401 from this
// exact path, and that behavior is only correct if the server really answers
// this way.
func TestLiveReusedRefreshTokenIsRejected(t *testing.T) {
	env := readLiveEnv(t)
	ctx := liveContext(t)
	client := liveClient(t, env)

	first := liveLogin(t, ctx, client, env)
	if _, err := client.Refresh(ctx, first.RefreshToken); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	_, err := client.Refresh(ctx, first.RefreshToken)
	if err == nil {
		t.Fatal("the rotated-away refresh token was accepted a second time; it must be one-time use")
	}
	requireAPIStatus(t, err, http.StatusUnauthorized)
}

// requireAPIStatus asserts the error is a StatusError the API itself produced.
//
// FromAPI is the load-bearing half. Service.refresh destroys a session only on
// a 401 it can attribute to this API, so that a captive portal or an
// intercepting proxy answering 401 cannot burn a 30-day refresh token. A test
// that checked only the status code would pass against such a proxy.
func requireAPIStatus(t *testing.T, err error, want int) {
	t.Helper()
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %v (%T); want a *StatusError", err, err)
	}
	if statusErr.StatusCode != want {
		t.Errorf("status = %d, want %d", statusErr.StatusCode, want)
	}
	if !statusErr.FromAPI {
		t.Errorf("status %d was not attributed to the API; Service.refresh will not treat it as definitive, so the session would survive when it should not", statusErr.StatusCode)
	}
}
