# 35 — Import Grok Build subscription session

**Status:** DESIGN — not yet implemented.
**Date:** 2026-08-02
**Depends on:** plan 34 (`xai` provider descriptor + factory), the OAuth research in
this plan's appendix.
**Blocks:** nothing. **Amends:** plan 34 §6 (the "no subscription auth" carve-out —
this plan fills it).
**Blast radius:** MEDIUM — reads a credential file we do not own (`~/.grok/auth.json`),
validates a JWT, and sends a bearer token to xAI's inference proxy. No new privilege
surface beyond an existing API call; the risk is credential handling and token
refresh correctness.

---

## 1. Goal

Let a user who has a **Grok Build subscription** (SuperGrok / X Premium+) use their
existing subscription from mivia, **without** reimplementing xAI's OAuth client and
**without** an API key. The user runs `grok login` once (in the official `grok` CLI),
and mivia reads the resulting session token to authenticate against xAI's inference
proxy. mivia becomes a consumer of a credential another tool produced — not a
credential issuer.

This is the "import, don't reimplement" plan. It is explicitly **not** mivia's own
OAuth flow (plan 36). It exists because:

1. Many mivia users will already have `grok login` working.
2. Reimplementing xAI's PKCE + scope + client_id is brittle — xAI changes scopes
   (they added `conversations:read`/`write` mid-2026) and the client_id is obfuscated
   in the grok binary. Importing sidesteps all of it.
3. It ships value fast: no OAuth client, no loopback server, no browser flow.

## 2. How the grok subscription credential works (research summary)

Full source analysis is in §11. The essential facts:

- `grok login` (browser OIDC, default) or `grok login --device-auth` (RFC 8628)
  produces an OAuth2 token set stored at **`~/.grok/auth.json`**.
- The file is a `BTreeMap<String, GrokAuth>` keyed by OAuth scope. The default key is
  `https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828` (the obfuscated client_id).
- The relevant struct fields:
  - `key` — the `access_token` (a JWT); this is what we send as `Bearer`.
  - `refresh_token` — for silent renewal.
  - `expires_at` — when the access_token expires.
  - `oidc_issuer` / `oidc_client_id` — needed to refresh via OIDC discovery.
- Tokens are sent to the **inference proxy** `cli-chat-proxy.grok.com`, **not**
  `api.x.ai`. The subscription entitlement lives in the JWT's team principal, not in
  an account balance.
- Refresh is `POST {issuer}/oauth2/token` with `grant_type=refresh_token`, up to 3
  retries with backoff. Terminal errors (`invalid_grant`) require re-login.

## 3. The import model

mivia reads `~/.grok/auth.json`, extracts the freshest valid token, and uses it as a
bearer. Two refresh strategies, in priority order:

### 3a. Strategy A — read-only import (v1, this plan)

mivia reads the file and uses the token as-is. **mivia does not refresh.** When the
token expires, mivia tells the user to run `grok login` again (which refreshes the
file), then retry.

- **Pro:** zero coupling to xAI's OAuth internals. If xAI changes scopes, client_id,
  or the refresh endpoint, mivia is unaffected — the grok CLI handles it.
- **Con:** a long mivia session that outlives the token's lifetime fails until the
  user re-runs `grok login`. The access_token lifetime is typically ~1 hour.
- **Mitigation:** detect expiry proactively, warn early, and document the workflow.
  A `mivia login --refresh-grok` shim (§5) can invoke `grok login` non-interactively
  if the grok CLI is present, hiding the manual step.

### 3b. Strategy B — refresh-aware import (deferred)

mivia reads the file **and** refreshes the token itself using the stored
`refresh_token` + OIDC discovery, writing the refreshed token back to `auth.json`.
This is what every "Grok OAuth" third-party tool does (`pi-xai-oauth`, `oh-my-pi`).

- **Pro:** seamless long sessions.
- **Con:** mivia now owns OAuth refresh correctness against an xAI API that is "not a
  stable public surface" (per `pi-xai-oauth`'s own warning). Scope/client_id drift
  breaks us. We compete with the grok CLI for refresh — two processes refreshing the
  same `auth.json` is a race.
- **Decision:** **defer.** Ship A first; only build B if the manual-refresh UX proves
  unacceptable. When B lands, it **must** use single-flight refresh and file-locking
  to avoid clobbering a concurrent grok CLI refresh.

## 4. Credential resolution and precedence

A new resolution step in the `xai` provider path:

```
1. XAI_API_KEY env / model.api_key            ← plan 34 (static key)
2. ~/.grok/auth.json (subscription session)   ← this plan (imported token)
3. (error: no credential)
```

This matches grok's own precedence (session token > API key) inverted for our
purposes: we prefer an explicit API key when present (it's unambiguous and
self-contained) and fall back to the imported session. A user who sets both gets the
key path; a subscription user who sets neither key nor `XAI_API_KEY` gets the import.

**Resolution is opt-in via a provider flag**, not automatic. Scanning `~/.grok/` by
default is surprising. The user declares intent:

```toml
[providers.xai]
# ... models, default_model ...
api_key_env = "XAI_API_KEY"        # still works (plan 34)
base_url = "https://api.x.ai/v1"   # default; overridden when using subscription

[providers.xai.subscription]
import_from = "~/.grok/auth.json"  # opt into session import
# base_url override is automatic when import is active (see §6)
```

When `[providers.xai.subscription]` is present and `XAI_API_KEY` is unset, mivia
resolves the credential from `auth.json` and points the provider at the inference
proxy.

## 5. The `mivia login` shim (optional convenience)

A `mivia login --grok` command that shells out to the grok CLI:

```
mivia login --grok
  → exec("grok", "login")   # or "grok login --device-auth" if no browser
  → on success, mivia re-reads ~/.grok/auth.json
```

This is **not** mivia's own login flow (plan 36). It delegates to the grok CLI, which
owns the OAuth client. It exists purely so the user doesn't context-switch tools when
a token expires. If `grok` is not on PATH, the command prints install instructions and
exits. The shim never reimplements the OAuth flow.

## 6. Endpoint routing — the inference proxy

When using an imported subscription token, the base URL must change from
`api.x.ai/v1` (plan 34) to the inference proxy. Research identified the host as
`cli-chat-proxy.grok.com`. The path and request shape are OpenAI-compatible.

This is wired by making `base_url` in `[providers.xai.subscription]` default to the
proxy when `import_from` is set, overridable for users behind a corporate proxy
(mirroring grok's `GROK_CLI_CHAT_PROXY_BASE_URL`). The factory receives the resolved
base URL via `Options.BaseURL` exactly as it does today — no factory change needed.

## 7. Token validation and expiry

On resolve, mivia:

1. Reads `~/.grok/auth.json`. If absent → clear error: "run `grok login` first, or
   set `XAI_API_KEY`".
2. Selects the entry whose key matches the default grok client_id scope. If multiple,
   pick the one with the latest `expires_at`.
3. Decodes the JWT **without verifying signature** (we are not the issuer; the proxy
   validates) to read `exp` and the team principal for diagnostics.
4. If `exp` is past → error: "session expired; run `grok login` (or `mivia login
   --grok`) and retry". Do not attempt refresh in v1 (§3a).
5. If `exp` is within the early-warning window (5 min) → warn at startup, proceed.

JWT decoding is claims-only (base64-decode the payload segment). No signature
verification — mivia is a consumer, not the resource server. A token the proxy
rejects returns a clear 401 that we surface as "session invalid; re-login".

## 8. Security considerations

| Concern | Mitigation |
|---|---|
| Reading another tool's credential file | Opt-in only (§4); never scan `~/.grok/` without explicit config |
| Token plaintext on disk (grok's choice, not ours) | Document that `auth.json` is `0600`; we neither weaken nor improve grok's storage model |
| Token in mivia's process memory | Same exposure as any API key; the existing `APIKey` handling in `ProviderRuntime` applies. Do not log the token; `INV-AG-5` (redaction opt-in) covers previews |
| Two tools refreshing the same file | v1 doesn't write the file (Strategy A). When Strategy B lands, file-lock + single-flight |
| Wrong-endpoint token use (proxy vs api.x.ai) | The `[providers.xai.subscription]` block routes to the proxy automatically; a bare key stays on `api.x.ai`. Never mix |

## 9. Verification

- `go test ./internal/provider/...` — new `xai_subscription_test.go`: resolve from a
  fixture `auth.json`, pick latest-expiring entry, decode `exp`, fail-closed on
  expired/missing/malformed
- `go test ./internal/config/...` — `[providers.xai.subscription]` parses; base_url
  resolves to the proxy when import is active
- `go build ./... && go vet ./...`
- Manual: `grok login` → `mivia --provider xai` (with subscription block) completes a
  chat turn against the inference proxy

## 10. Invariant

A new row (next free `INV-AG-30`): *Subscription session import is opt-in via
`[providers.xai.subscription].import_from`; mivia never scans `~/.grok/` without it.
An imported token is read-only in v1 — mivia does not write `auth.json`. An expired
token fails closed with a re-login instruction, never silently falls back to an
unrelated credential.*

## 11. Appendix — grok subscription auth (source analysis)

From `xai-org/grok-build` Rust source (`crates/codegen/xai-grok-shell/src/auth/`):

**Storage** — `auth.json` is a `BTreeMap<String, GrokAuth>`. Key format:
`"{issuer}::{client_id}"`, default `"https://auth.x.ai::b1a00492-..."`. Permissions
`0600`. No OS keyring.

**Token struct** (`model.rs`):
```rust
pub struct GrokAuth {
    pub key: String,                        // access_token (JWT bearer)
    pub auth_mode: AuthMode,                // OAuth2 | DeviceCode | ApiKey | ExternalProvider
    pub user_id: String,
    pub refresh_token: Option<String>,
    pub expires_at: Option<DateTime<Utc>>,
    pub oidc_issuer: Option<String>,
    pub oidc_client_id: Option<String>,
}
```

**Acquisition** — OAuth 2.1 Authorization Code + PKCE (S256), loopback redirect
`http://127.0.0.1:{port}/callback`, or RFC 8628 device code. Scopes (frozen):
`openid profile email offline_access grok-cli:access api:access
conversations:read conversations:write workspaces:read workspaces:write`.

**Refresh** — `POST {token_endpoint}` with `grant_type=refresh_token`; up to 3 retries
with jittered backoff; terminal errors (`invalid_grant`) not retried;
`single_flight.rs` coalesces concurrent refreshes.

**Wire** — `AuthRetryMiddleware` stamps `Authorization: Bearer {token}` on every
request; subscription tokens additionally set `X-XAI-Token-Auth`. Target host is the
inference proxy `cli-chat-proxy.grok.com`, not `api.x.ai`.

**Enforcement** — Enterprise `disable_api_key_auth` replaces first-party xAI keys
with the session token at request time; `force_login_team_uuid` pins login to a team.
The token's team principal carries the subscription entitlement.

## 12. Rollback

If the imported-token path proves unreliable (proxy shape changes, scope drift), the
fallback is plan 34 (API key). The subscription import is additive config — removing
the `[providers.xai.subscription]` block returns the user to the key path with no code
change. Strategy B (refresh-aware) is the only piece whose rollback is non-trivial,
and it is deferred.

## 13. Sequencing

1. `internal/provider/xai_auth.go` (new) — `resolveGrokSession(path)` reads
   `auth.json`, selects entry, decodes JWT claims, returns `(token, baseURL, error)`
2. `internal/config/types.go` — `SubscriptionConfig{ImportFrom, BaseURL}` on the xai
   provider section
3. `internal/config/load.go` — when subscription import is active and no API key,
   resolve the token and override base_url to the proxy
4. `internal/provider/xai.go` — accept a resolved token (not just an API key) via
   `Options`; if the token came from import, set the subscription headers
5. `internal/provider/xai_subscription_test.go` — fixture-based resolve + expiry
6. `mivia login --grok` shim (optional, §5)
7. Docs + `mivia.toml.example` subscription block
8. Invariant `INV-AG-30`
