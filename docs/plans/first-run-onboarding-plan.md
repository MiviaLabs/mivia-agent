# First-Run Onboarding Plan

**Status:** In Progress
**Author:** mivia / MiviaLabs
**Date:** August 2026
**Topic:** First-time user experience (FTUE) after install — automatic config
bootstrap, automatic provider key setup, and install-script guidance.

---

## 1. Executive Summary

### 1.1 Context

A new user installs `mivia` with `scripts/install.sh` (or `install.ps1`).
The installer places the binary only. It writes no config and no key.

When the user then runs `mivia chat` in any directory, config loading finds
no file at any of `DefaultConfigCandidates()` (`$MIVIA_CONFIG`,
`./.mivia/mivia.toml`, `~/.mivia/mivia.toml`) and fails with:

```
no configured provider models available
```

This message names no fix. The user must already know to run `mivia setup`
from reading the README. Nothing in the binary itself says so — not the bare
`mivia` usage text, not the error, not the installer.

### 1.2 Goal

Remove the manual step. A user who installs `mivia` and runs `mivia chat`
should reach a working chat session, or a clear one-line fix, without first
reading documentation.

---

## 2. Design

### 2.1 Silent config bootstrap

`mivia chat` gains an opt-in `LoadOptions.AutoBootstrapUserConfig` flag on
`internal/config.Load`. When set, and no config file exists anywhere, and no
explicit `--config`/`$MIVIA_CONFIG` was given, the loader writes a minimal
default config to `UserConfigPath()` (`~/.mivia/mivia.toml`): the shipped
default provider, `openrouter`, with its default model. The write is silent
— no prompt, no output — and the loader then proceeds as if that file had
existed.

This flag is opt-in and wired only from `mivia chat`. Every other caller of
`config.Load` (diagnostics, MCP resolution, memory config, hook scope, and
the existing test suite) keeps today's behavior unchanged. `config.Load` is
called from many read-only and test paths; giving all of them a
file-writing side effect would be a surprising, hard-to-review behavior
change and would break test isolation.

An explicit `--config`/`$MIVIA_CONFIG` that points at a missing file still
fails loudly. Auto-bootstrap only fires on the "nothing configured
anywhere" case, never on a broken explicit pointer.

### 2.2 Automatic key setup on first `mivia chat`

A bootstrapped config still has no API key. `mivia chat` checks
`Resolved.APIKeySet` after config load. When it is false:

- **Interactive session** (stdin and stdout are both a TTY): run the same
  prompt used by `mivia setup` — masked input, written to
  `~/.mivia/.env` (0600) — then continue into the chat session with the
  freshly written key. The user never has to know the `setup` subcommand
  exists or run `mivia chat` a second time.
- **Non-interactive session** (scripted, `-p` one-shot, piped, CI): never
  prompt — blocking on stdin in a non-interactive process is a footgun.
  Fail with a clear, actionable error naming `mivia setup` and the exact
  env var (e.g. `OPENROUTER_API_KEY`) to set instead.

`mivia setup` remains available as an explicit command for scripted
provisioning, non-default providers, and re-keying.

### 2.3 Install-script guidance

`scripts/install.sh` and `install.ps1` print a one-line next step after a
successful install: run `mivia chat` to get started. This replaces needing
to read the README's "First run" section to discover the entry point.

### 2.4 Registry-driven default config

The minimal config written by both `mivia setup` and the new bootstrap path
is generated from `internal/providerregistry`'s descriptors (`DefaultModel`,
`DefaultURL`, `DefaultAPIKeyEnv`) rather than a hand-maintained string
constant. Today only `openrouter` (the shipped default) has this
auto-written path; any other provider `mivia setup --provider <name>`
selects gets the same treatment instead of being left for the user to hand-
edit `mivia.toml`. This keeps one source of truth instead of two constants
drifting apart, the same problem `scripts/check_provider_docs.py` already
guards against for documentation.

---

## 3. Scope boundaries

- Only `mivia chat` gets the automatic bootstrap and key prompt. `mivia
  doctor`, `mivia config`, `mivia agents`, and other subcommands keep
  today's explicit-error behavior. Expanding auto-bootstrap to more
  commands is a future decision, not part of this plan.
- Project-level config (`<repo>/.mivia/mivia.toml`) is never auto-created.
  Bootstrap only ever targets the user-level file (`~/.mivia/mivia.toml`),
  matching the trust model already documented in `.mivia/mivia.toml.example`
  around lifecycle hooks: a project's own config is something a user reads
  before trusting, not something mivia writes on a project's behalf.
- No API key is ever written to `mivia.toml`. Keys stay in the user env
  file, unchanged from today's model.

---

## 4. Verification

| Area | Test target |
| :--- | :--- |
| Config bootstrap | `internal/config`: missing config + flag → file created, `found=true`; flag unset → unchanged; explicit missing `--config` → still a hard error; existing config → untouched |
| Chat key prompt | `internal/clichat`: key already set → no prompt; TTY + no key → prompts and writes; non-TTY + no key → clear error, no hang |
| Install scripts | Manual read of the printed next-steps line |

Scoped `go test` runs only, per this repo's convention — no `make verify` or
whole-suite runs from an interactive session.

---

## 5. Status

Sections 2.1, 2.2, and 2.3 are implemented in a background task as of this
plan's date; this document is the design record for that change and the
basis for review before commit. Section 2.4 was already agreed and is
in scope for the same change.
