# 33.8 - Docs, `mivia.toml.example`, and `INV-AG-29`

**Status:** DESIGN - ready. **Contains one correction to `00-overview.md` §15.**
**Date:** 2026-08-01
**Parent:** [`00-overview.md`](00-overview.md) §12, §13, §15
**Depends on:** `01`–`07`.
**Blast radius:** LOW.

---

## 1. Correction - the docs path in `00-overview.md` §15 collides

`00-overview.md` §15 step 8 says "Docs: `docs/development/hooks.md`". **That file
already exists and is about something else.** It documents Git hooks - `make
install-hooks`, `core.hooksPath=.githooks`, pre-commit/pre-push, the agent hook guard,
and bypass policy. It is also a **required path**
(`.mivia/policy/required-paths.json`) and is subject to the unique-H1 check in `make
docs-check`.

Writing lifecycle-hook documentation into it would collide with a required, gated,
unrelated file, and would fuse the two most confusable meanings of "hook" in this repo
into one page.

**Resolution:** lifecycle hooks are documented at **`docs/development/lifecycle-hooks.md`**,
with:

- an entry in `docs/OWNERS.yaml` (required by `make docs-check`);
- a unique H1 (`# Lifecycle Hooks`), distinct from `# Development Hooks`;
- a cross-reference from `docs/development/hooks.md` disambiguating the two - *that*
  page is about Git hooks the agent must not bypass, *this* page is about mivia's own
  lifecycle layer. `00-overview.md` §6 already notes the two run in opposite
  directions; the docs should say so where a reader will hit it.

Whether `docs/development/lifecycle-hooks.md` should itself be added to
`required-paths.json` is a judgement call for implementation: it is a new
user-facing surface, which argues yes, but every required path is a permanent
maintenance obligation. Decide explicitly, do not default.

## 2. Docs content

The page must cover, at minimum, the things a reader will otherwise get wrong:

- the three v1 events, and that `PreToolUse` is the only one that can block;
- that `argv` is an array and there is **no shell** - with the "why" (`00-overview.md`
  §3c), because the first thing users will try is a shell string;
- that context arrives as `MIVIA_*` env vars and stdin JSON, never spliced into argv;
- the exit-code and structured-output contract, including that `PreToolUse` uses
  `hookSpecificOutput.permissionDecision` while other events use flat `decision` - and
  that this mirrors Claude Code, so scripts port;
- **what trust does and does not cover**: the hook *definition* is hashed, the script
  body at `argv[0]` is not (slice `04` §2). A reader who assumes otherwise has a wrong
  threat model, and the docs are where that gets corrected;
- `on_timeout`, and that `PreToolUse` defaults to `block` - a hung gate is a closed
  gate;
- `--bypass-hook-trust`, documented as dangerous;
- that workspace `mivia.toml` hooks are ignored, and why (slice `01`).

## 3. `mivia.toml.example`

Add a commented `[hooks]` section carrying the §3a shape from `00-overview.md`. Every
example must be one that actually loads - an example using the rejected `run =` string
form or a `trust =` key would be a config that slice `02` refuses, shipped in our own
example file.

## 4. `INV-AG-29`

Register the invariant from `00-overview.md` §13, split into its testable clauses.
Confirm `29` is still free at implementation time: `invariants.md` runs contiguously
to `INV-AG-28`, and plans `35`, `36`, `37`, `40` have reserved `30`–`33`.

Each clause needs at least one named test from slices `01`–`07`:

| Clause | Proven by (slice) |
|---|---|
| hooks load only from user config; workspace never supplies one | `01` |
| trust is derived, never declared; editing a confirmed hook revokes it | `02`, `04` |
| fresh install and headless-without-bypass run zero non-managed hooks | `04`, `05` |
| argv-only, no shell, no `PATH`, no interpolation; never re-enters `Invoke` | `03`, `06` |
| `PreToolUse` is the only blocking event, `Kind == Tool` only, propagates to subagents, blocks on timeout, returns `blocked` with a reason | `06` |
| hook output is separately bounded; ceilings and audit hashes stay exact | `03`, `06` |

An invariant row whose clauses are not each backed by a named test is a claim, not an
invariant - that is what `.mivia/invariants.md`'s test column is for.

## 5. `make verify` gate

Every `[[hooks]]` event in any config the gate can see is a known v1 event; unknown
fails the build. This is the same shape as the existing config-validation gates in
`scripts/verify_agent_config.py`.

## 6. `.mivia/INDEX.md`

Add the row for this plan directory. The Plans table currently has no entry for `33`
at all.

## 7. Verification

- `make docs-check` passes with the new page and its `OWNERS.yaml` entry
- `make verify` passes, including the new event-name gate
- `make validate-invariants` accepts `INV-AG-29` with no duplicate ID
- every test named in the `INV-AG-29` row exists and passes

## 8. Done when

Someone who has never read this plan can write a working `PreToolUse` hook from the
docs alone, and cannot write one that silently fails open.
