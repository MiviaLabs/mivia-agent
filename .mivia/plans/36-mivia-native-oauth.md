# 36 — mivia native OAuth flow (loopback + device code)

**Status:** DESIGN — not yet implemented. Long-horizon; depends on an auth server
that does not exist yet.
**Date:** 2026-08-02
**Depends on:** plan 34 (xai descriptor), plan 35 (session-token provider path), an
external **mivia auth server** (out of scope to build here).
**Blocks:** nothing in-tree. **Amends:** nothing.
**Blast radius:** HIGH — introduces mivia's first credential-issuing surface (an OAuth
client), a loopback HTTP server, browser/device-code flows, a credential store, and a
token refresh background task. The trust model is the load-bearing part.

---

## 1. Goal

Give mivia its **own** authentication flow — `mivia login` — so a user authenticates
once and mivia obtains, stores, and refreshes tokens from a **mivia-operated auth
server**. This is the foundation for a future mivia subscription, account, and
entitlement system. It is the long-term answer to "how does a user authenticate to
mivia" that does not depend on xAI's grok CLI (plan 35) or a static key (plan 34).

This plan is the **client side** only: the OAuth client in the mivia CLI. The
**server side** (mivia auth server, issuer, token endpoint, account database) is a
separate project not covered here. This plan assumes that server exists and conforms
to OAuth 2.1 / OIDC.

## 2. Why this is plan 36 and not plan 34 or 35

The three plans form a deliberate ladder:

| Plan | Auth model | Credential issuer | Effort | When |
|---|---|---|---|---|
| 34 | Static API key | xAI (console) | Trivial | Now |
| 35 | Imported session | xAI (grok CLI) | Low-Medium | Soon |
| **36** | **Native OAuth** | **mivia (own server)** | **High** | **Later** |

Plan 34 serves the user who has an xAI key today. Plan 35 serves the user who has a
grok subscription but wants to use mivia. Plan 36 serves the user who has a **mivia**
account — which requires mivia to run an auth server, issue tokens, and define
entitlements. Plan 36 is gated on that server existing.

**Do not start plan 36 before the auth server's `.well-known/openid-configuration`
is reachable.** Building a client against a hypothetical server is speculative
generality.

## 3. Research grounding — what every OAuth CLI client does

Surveyed three production CLI OAuth clients (grok-build, Claude Code, Codex). The
shape converges completely:

| Aspect | grok-build | Claude Code | Codex | **mivia (this plan)** |
|---|---|---|---|---|
| Flow | Auth Code + PKCE | Auth Code + PKCE | Auth Code + PKCE | **Auth Code + PKCE** |
| PKCE method | S256 | S256 | S256 | **S256** |
| Loopback redirect | `127.0.0.1:{rand}` | `127.0.0.1:{rand}` | `127.0.0.1:{rand}` | **`127.0.0.1:{rand}`** |
| Device code (RFC 8628) | Yes | Yes | Yes | **Yes** |
| Token storage | `auth.json` (plaintext, 0600) | keychain-dependent | `~/.codex/` | **`~/.mivia/auth.json` (0600) + OS keyring** |
| Refresh | background, single-flight | background | background | **background, single-flight** |
| Client secret | none (PKCE public client) | none | none | **none** |

**The decisions are already made by precedent.** A native CLI OAuth client is Auth
Code + PKCE over loopback, with a device-code alternative for headless, background
refresh with single-flight, and plaintext-file or keychain storage. This plan follows
that shape exactly, because diverging without reason is the real risk.

## 4. The loopback server

The browser flow needs a local HTTP server to receive the authorization code
redirect. Design, mirroring grok-build's `oidc/login.rs`:

1. **Bind** a `TcpListener` on `127.0.0.1` with an OS-assigned random port (port 0).
   Loopback only — never `0.0.0.0`. The random port is registered with the issuer at
   authorization time (per RFC 8252, loopback redirects are port-agnostic).
2. **Route** — a single `/callback` handler that accepts `?code=...&state=...`,
   validates `state` (CSRF), exchanges the code for tokens, writes a success page,
   and signals the waiting CLI.
3. **Race** — between (a) the loopback callback and (b) manual paste of the code or
   full callback URL via stdin/TUI. Remote/SSH users cannot reach `127.0.0.1`, so
   paste is the fallback. Timeout: 10 minutes (matches grok).
4. **Shutdown** the listener immediately after the first resolution (callback or
   paste). Never leave it open.

**Implementation in Go:** `net/http.Server` with a `Shutdown(ctx)` on resolution.
The handler writes a minimal HTML success page and closes the server. The server
runs in a goroutine; the main flow selects on `<-callback | <-paste | <-time.After`.

### 4a. Security of the loopback server

- **Bind loopback only** (`127.0.0.1`, not `0.0.0.0`) — no off-machine access.
- **PKCE** — the `code_verifier` is generated per-login, never sent to the browser,
  and required at token exchange. This is what makes a public client (no secret)
  safe even if the code is intercepted.
- **`state` parameter** — random per-login, validated at callback, prevents CSRF.
- **Single-use** — the listener shuts down after one resolution. No persistent port.
- **No TLS** — loopback HTTP is the standard (RFC 8252 §7.3); the OS local-only
  routing is the transport security. The PKCE+state pair protects the code.

## 5. PKCE

Per RFC 7636, S256 method:

```go
verifierBytes := make([]byte, 32)
rand.Read(verifierBytes)
codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)  // 43 chars
h := sha256.Sum256([]byte(codeVerifier))
codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])
// code_challenge_method = "S256"
```

The verifier is held in memory only for the duration of the login attempt and sent
once, at token exchange. It is never persisted.

## 6. Device code flow (RFC 8628)

For headless / SSH / CI environments without a browser:

1. `POST {issuer}/oauth2/device/code` with `client_id` + `scope` → receive
   `device_code`, `user_code`, `verification_uri`.
2. Print `verification_uri` + `user_code` to the terminal. The user opens the URL on
   any device and enters the code.
3. Poll `POST {issuer}/oauth2/token` with `grant_type=urn:ietf:params:oauth:grant-type:device_code`
   every `interval` seconds. Handle `authorization_pending` (keep polling),
   `slow_down` (increase interval by 5s), `access_denied` / `expired_token` (abort).
4. **Validate** `verification_uri` is HTTPS before printing it — defends against a
   compromised issuer injecting `javascript:` or control characters (grok does this).

## 7. Token storage

Two options, in order of preference:

### 7a. OS keyring (preferred)

Use `github.com/zalando/go-keyring` (cross-platform: macOS Keychain, Windows
Credential Manager, Linux SecretService/KWallet). Token never touches disk in
plaintext.

- **Pro:** strongest default; tokens survive only in the OS-encrypted store.
- **Con:** Linux without a SecretService daemon falls back to file (§7b); CI
  containers usually have no keyring; the dependency adds ~small surface.

### 7b. Plaintext file fallback (`~/.mivia/auth.json`)

Mirrors grok-build's model exactly: JSON file, `0600`, `$GROK_HOME`-equivalent
(`$MIVIA_HOME` or `~/.mivia`). Structure:

```json
{
  "access_token": "...",
  "refresh_token": "...",
  "expires_at": "2026-08-02T12:00:00Z",
  "issuer": "https://auth.mivia.app",
  "client_id": "..."
}
```

- **Pro:** works everywhere, including CI.
- **Con:** plaintext; relies on filesystem perms and full-disk encryption.

**Resolution:** try keyring first; fall back to file with a warning ("keyring
unavailable; storing credentials in ~/.mivia/auth.json with 0600 perms"). A config
flag `[auth].store = "keyring" | "file" | "auto"` (default `auto`) lets the user pin.

### 7c. What is NOT stored

- The `code_verifier` (per-login, ephemeral).
- The `client_secret` (there is none — public client).
- Anything in `mivia.toml` (config is for static settings, not credentials).

## 8. Token refresh

A background refresh that mirrors grok-build's robust design:

| Trigger | Behavior |
|---|---|
| **Proactive** (before expiry) | `time.AfterFunc` armed at `expires_at - 5min`; refreshes silently |
| **Reactive** (on 401) | On a 401 from the API, refresh once, retry the request |
| **Single-flight** | concurrent 401s coalesce into one refresh (a `sync.Once`/`singleflight.Group`) |
| **Terminal errors** | `invalid_grant` → stop background refresh, prompt re-login; do not retry |
| **Backoff** | up to 3 retries with jittered exponential backoff |

Refresh request: `POST {token_endpoint}` with `grant_type=refresh_token`. The new
token set (access + refresh + expiry) replaces the stored one atomically.

**Refresh writes back to the same store** (keyring or file). For file storage, write
to a temp file and `os.Rename` for atomicity, then `chmod 0600`.

## 9. The `mivia login` command surface

```
mivia login                    # browser flow (default)
mivia login --device-auth      # RFC 8628 device code (headless/SSH)
mivia login --print-url        # print the authorize URL, don't open browser (CI)
mivia logout                   # clear stored credentials
mivia whoami                   # show current user, expiry, issuer (debug)
```

`mivia login` opens the browser to `{issuer}/authorize?...` and starts the loopback
server. `mivia login --print-url` is the CI-friendly variant: print the URL, wait for
the code on stdin. `mivia whoami` decodes the JWT (no verification — for display
only) and shows `sub`, `exp`, `iss`.

## 10. Integration with the provider layer

This plan produces a credential (`access_token`) that the provider layer consumes
exactly as plan 35's imported token does. The `xai` factory (plan 34) already accepts
an `Options.APIKey`; the subscription path (plan 35) already routes a bearer token to
the right endpoint. Native mivia auth reuses that path:

```
mivia auth server token  →  resolveGrokSession-equivalent  →  Options  →  factory
```

When mivia has its **own** auth server, the token's `aud` is a mivia entitlement, not
an xAI team principal — but the wire shape (Bearer header) is identical. The provider
factory doesn't care where the token came from. This is why plans 34/35/36 layer
cleanly: they differ in *credential issuance*, not in *credential consumption*.

## 11. The mivia auth server (out of scope — stated for context)

This plan is the client. The server must provide:

- `GET /.well-known/openid-configuration` — discovery (issuer, authorize, token, JWKS endpoints)
- `GET /authorize` — authorization endpoint (browser)
- `POST /oauth2/token` — token + refresh + device-code grant
- `POST /oauth2/device/code` — device code issuance
- `GET/.well-known/jwks.json` — JWKS for id_token validation
- Account + entitlement database (the real work)

**Client_id**: a stable public client id shipped in the mivia binary (like grok's
`b1a00492-...`). Public client, no secret (PKCE). Can be obfuscated (`obfstr`) to
discourage casual misuse, though it is not truly secret.

**Scopes**: `openid profile email offline_access mivia:api` at minimum. Add
entitlement scopes (`mivia:pro`, etc.) when the entitlement system exists.

## 12. What this does NOT do

- **Does not build the auth server.** Server is a separate project.
- **Does not replace plan 34/35.** A user with an xAI key (34) or grok session (35)
  keeps working. Native auth is additive — a `[auth]` section in `mivia.toml`, not a
  replacement for `[providers.*]`.
- **Does not store the client_secret.** There is none.
- **Does not verify the access_token signature client-side.** The resource server
  (the API mivia calls) validates; the client decodes claims for display/expiry only.
- **Does not ship before the server exists.** §2.

## 13. Security considerations

| Threat | Mitigation |
|---|---|
| Loopback server reachable off-host | Bind `127.0.0.1` only, never `0.0.0.0`; random port; single-use |
| Code interception | PKCE S256 — verifier never leaves the client; code is useless without it |
| CSRF on callback | `state` parameter, random per-login, validated at callback |
| Token plaintext on disk | Prefer keyring (§7a); file fallback is `0600` with a warning |
| Token in process memory | Same as any secret; redaction policies (INV-AG-5) cover previews; never log |
| Refresh storm | Single-flight coalescing; backoff; terminal-error classification |
| Malicious `verification_uri` | Validate HTTPS before printing (§6) |
| Stolen refresh_token | Server-side rotation + revocation (server's job); client detects `invalid_grant` and prompts re-login |

## 14. Verification

- `go test ./internal/auth/...` — PKCE generation, loopback server lifecycle (bind,
  receive callback, validate state, shutdown), device-code poll loop with mock issuer,
  token store read/write (keyring mock + file), refresh single-flight, terminal-error
  classification, expiry logic
- `go test ./internal/provider/...` — native token flows through the factory like plan 35
- `go test -race ./...` — the loopback server + refresh goroutine are concurrency-heavy
- `go build ./... && go vet ./...`
- Manual (requires staging auth server): full `mivia login` browser flow; device-code
  flow; expiry → auto-refresh; `mivia logout` clears store

## 15. Invariant

A new row (next free `INV-AG-31`): *The loopback OAuth server binds `127.0.0.1` only
and shuts down after one resolution. PKCE S256 is mandatory for every authorization
code exchange. The `state` parameter is random per-login and validated at callback.
Credential storage prefers the OS keyring and falls back to a `0600` file with a
warning. Token refresh is single-flight and classifies `invalid_grant` as terminal.*

## 16. Rollback

If the keyring dependency proves problematic on a target platform, fall back to
file-only storage (§7b) by defaulting `store = "file"`. If the loopback server is
blocked by a firewall/AV heuristic, `mivia login --print-url` (stdin paste) is the
fallback path and needs no server. If the auth server is delayed, this entire plan is
deferred — plans 34 and 35 cover users in the meantime.

## 17. Sequencing

1. `internal/auth/pkce.go` + tests — S256 verifier/challenge
2. `internal/auth/loopback.go` + tests — bind, callback handler, state validation, shutdown, paste-race
3. `internal/auth/devicecode.go` + tests — RFC 8628 poll loop, HTTPS validation
4. `internal/auth/store.go` + tests — keyring + file fallback, atomic write, 0600
5. `internal/auth/refresh.go` + tests — background refresh, single-flight, terminal errors
6. `internal/auth/flow.go` — orchestrate discovery → authorize → exchange → store
7. `cmd/mivia` — `login`, `login --device-auth`, `login --print-url`, `logout`, `whoami`
8. `internal/config` — `[auth]` section (issuer, client_id, store)
9. Provider integration — native token → `Options` → factory (reuses plan 35 path)
10. Docs + invariant `INV-AG-31`

Each step is independently testable. Steps 1–5 are pure mechanism; 6–9 are
integration; 10 closes the loop. **Do not start until the auth server is reachable**
(§2).

---

## Relationship summary

```
Plan 34 (API key)        ── trivial, ships now, xAI console users
   └─ Plan 35 (import)   ── low-medium, ships soon, grok subscription users
        └─ Plan 36 (native OAuth) ── high effort, gated on mivia auth server
```

Each plan is consumable independently. A user picks the credential they have. The
provider factory doesn't care which path produced the bearer token — that's the design
property that lets the three plans compose without coupling.
