# 33.4 — Hash-keyed trust store and the `/hooks` command

**Status:** DESIGN — ready.
**Date:** 2026-08-01
**Parent:** [`00-overview.md`](00-overview.md) §6, §6a, §9
**Depends on:** `02` (definition hash), `03` (something to gate).
**Blocks:** `06` — the dispatcher is not wired to call a hook until this exists.
**Blast radius:** MEDIUM — this slice is the gate; a defect here is a defect in every
other slice's safety argument.

---

## 1. Trust is derived, never declared

`00-overview.md` §6a records the blocker: the first draft put `trust = "managed"`
inside the `[[hooks]]` table, i.e. inside the file whose trustworthiness is the
question. Plan `05` §5 already closed this class — *a floor the agent can lower is not
a floor.* There is no `trust` key (slice `02` rejects it). Tier comes from two things
the config cannot influence:

1. **Which file the hook came from**, resolved at a fixed path by the loader (slice
   `01`) — not a path the file can name.
2. **The content hash of the hook definition** (slice `02` §4), matched against the
   trust store.

| Tier | Source | Behaviour |
|---|---|---|
| `managed` | `~/.mivia/managed.toml`, a separate operator file, absent by default | always runs; not user-disableable |
| `user` | `[[hooks]]` in `~/.mivia/mivia.toml` at its fixed path | runs iff `(source, hash)` is confirmed; else prompts once, interactively |
| `workspace` | workspace `mivia.toml` | not loaded at all (slice `01`) |
| `untrusted` | user hook whose hash is unconfirmed or changed | does not run; listed as pending |

## 2. Why hash, not name

Codex tracks hook definitions by hash (`00-overview.md` §1c), and it buys a property a
name-keyed store cannot: **editing a confirmed hook revokes its trust automatically.**
A name-keyed store lets `hooks/fmt.sh` be confirmed once in week one and rewritten
freely in week six — the confirmation would then attest to a script that no longer
exists. This slice's central test is that mutation revokes.

Open question this slice must answer, not leave implicit: the hash covers the hook
**definition** (event, matcher, argv, timeout), which is what the config declares. It
does **not** cover the *contents of the script at `argv[0]`*. Editing the script body
therefore does not revoke trust. That is the same boundary Codex draws, and it is
defensible — the user confirmed "run this script on this event," and the script is
their own file under their own version control — but it must be **stated in the
`/hooks` display and in the docs (slice `08`)**, because a user who assumes otherwise
has a wrong threat model. If a future slice wants script-content hashing, it is an
additive change to the same store.

## 3. The store

`~/.mivia/hook-trust.json`, located via `workspace.NamespacePath`. This is the one
JSON file the plan introduces, and the distinction that keeps "all config is TOML"
true: it is runtime **state** (what the user confirmed), not configuration
(`00-overview.md` §9).

- Records `(source path, definition hash, confirmed-at)`.
- Written atomically; a corrupt or unreadable store means **zero hooks run**, not "run
  them all" — fail closed on the gate's own storage, or the gate is deletable.
- Never records a hook the user declined; a decline is not persisted as a "no", it
  simply leaves the hash unconfirmed, so the next session asks again. Persisting
  declines would let one mis-click permanently disable a hook the user later wants.

## 4. `/hooks`

A slash command in `internal/cli`, mirroring Codex's and Claude's browsers:

- list each discovered hook: event, matcher, resolved `timeout`/`on_timeout`, derived
  tier, and status (active / pending / hash-changed);
- **`hash-changed` is displayed distinctly from `pending`** — "this was trusted and has
  since been edited" is a materially different message from "this is new," and
  collapsing them trains re-confirmation reflex;
- `/hooks trust <id>` promotes, as an explicit user action;
- managed hooks are listed as operator-set and cannot be promoted or disabled here.

## 5. Confirmation UX

Interactive only. The prompt shows the event, matcher, and the **full resolved argv**
— not a truncated command name, because the argv is the thing being authorized. First
confirmation happens at load, before any tool call, so the user is not asked mid-turn
while blocked on a gate.

Headless has no one to ask; slice `05` owns that.

## 6. Verification

`go test ./internal/hooks/...`:

- confirmed `(source, hash)` runs; unconfirmed does not
- **editing a confirmed hook definition revokes trust** — the central test
- reordering handlers revokes; reformatting the TOML does not (pairs with `02` §4)
- a corrupt/unreadable/absent trust store yields zero hooks, never all hooks
- a declined hook is not persisted as denied and is offered again next session
- managed hooks run without appearing in the store

`go test ./internal/cli/...`:

- `/hooks` distinguishes active / pending / hash-changed
- `/hooks trust` promotes exactly one hook and writes exactly one record

## 7. Done when

A fresh install runs zero hooks; a confirmed hook runs; an edited confirmed hook stops
running until re-confirmed — all three proven by test.
