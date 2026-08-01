# 33.2 - `internal/hooks/config.go`: parse, validate, reject

**Status:** DESIGN - ready.
**Date:** 2026-08-01
**Parent:** [`00-overview.md`](00-overview.md) §3a, §4, §5, §8a
**Depends on:** `01` (the hook table only ever arrives from user config).
**Blocks:** `03`, `04`, `06`.
**Blast radius:** LOW - pure parsing, no execution, no dispatcher contact.

---

## 1. Scope

A new package `internal/hooks` owning the TOML shape from `00-overview.md` §3a and
nothing else. It produces a validated `[]HookGroup`; it does not execute, does not
consult trust, and does not import `internal/runtime` or `internal/tools` (§11a of
the overview - pin the import boundary here, in the slice that creates the package).

Accepted shape:

```toml
[[hooks]]
event   = "PreToolUse"
matcher = "run_command"          # regex on tool name; "" or absent = match all

  [[hooks.handlers]]
  type       = "command"
  argv       = ["./hooks/block-bypass.sh"]
  timeout    = 10
  on_timeout = "block"           # PreToolUse default; "allow" elsewhere
```

## 2. What must be rejected, and with what message

Rejection quality is the deliverable here. Every one of these is a config a user will
plausibly write by copying from Claude Code or Codex docs, and each must fail loudly
rather than no-op. Codex's "parse and silently skip" is the behaviour we are
deliberately not copying (`00-overview.md` §5).

| Input | Verdict | Message must say |
|---|---|---|
| `event = "SessionStart"` (or any deferred event) | reject | **deferred**, not "unknown" - and why (no publish site, `00-overview.md` §4) |
| `event = "PreToolUsee"` | reject | unknown event, plus the list of v1 events |
| `type = "prompt"` / `"agent"` / `"http"` / `"mcp_tool"` | reject | v1 is `command` only; name the type |
| `trust = "..."` | reject | trust is derived, never declared (`00-overview.md` §6a) |
| `run = "gofmt -w $FILE"` | reject | `run` was removed; use `argv` - and say why (no shell, no interpolation) |
| `updatedInput` anywhere in config | reject | not supported; `00-overview.md` §8a |
| unknown key | reject | matches plan `25` §6's unknown-key rejection |
| `argv` empty / absent | reject | a handler with nothing to run |
| `matcher` that does not compile as a regex | reject | at load, not at first tool call |
| `timeout` <= 0 or absurdly large | reject | name the accepted range |
| `on_timeout` other than `block`/`allow` | reject | never coerce to a default |

Two rules that shape all of the above:

- **No value is ever coerced to the permissive branch.** An unrecognised `on_timeout`
  is an error, not `allow`; an unrecognised event is an error, not a skip. Coercion
  toward permissive is how the wire-shape defect in `00-overview.md` §8 would have
  failed open, and the same discipline belongs in the parser.
- **Errors name the file and the table index**, because `[[hooks]]` arrays give no
  other handle to point at.

## 3. Defaults

- `matcher` absent or `""` → match all tool names.
- `timeout` absent → per-event default (`00-overview.md` §7): 10s PreToolUse, 10s
  PostToolUse, 5s Stop.
- `on_timeout` absent → **`block`** for `PreToolUse`, `allow` for the rest. The
  default is computed from the event, so a config author who omits it gets the safe
  one, and `/hooks` (slice `04`) displays the resolved value rather than the blank.

## 4. Definition hash

Slice `04` keys trust on the content hash of a hook definition, so this slice must
produce it: a stable, canonical hash over the **normalised** group (event, matcher,
and each handler's type/argv/timeout/on_timeout), not over the raw TOML bytes.
Whitespace and key order must not change the hash - otherwise reformatting a config
revokes trust and trains the user to re-confirm without reading. Reordering handlers
*does* change behaviour and so *must* change the hash.

## 5. Out of scope

Execution (`03`), trust resolution (`04`), dispatcher wiring (`06`).

## 6. Verification

`go test ./internal/hooks/...`:

- each row of §2 rejected, asserting on the message content, not just on `err != nil`
- accepted shape round-trips to the expected struct
- per-event defaults resolve as §3, including `on_timeout` differing by event
- hash is stable across whitespace/key-order changes and unstable across handler
  reordering, argv changes, and timeout changes
- import-graph assertion: `internal/hooks` imports neither `internal/runtime` nor
  `internal/tools` (`00-overview.md` §11a)

## 7. Done when

A config copied verbatim from the Claude Code or Codex hooks docs fails with a message
that tells the author what mivia supports instead - no silent skips, no partial loads.
