# Lifecycle Hooks

mivia's deterministic control and observation layer: scripts the **runtime** runs
at lifecycle events, every time, whether or not the model wants it.

> **Not Git hooks.** [`hooks.md`](hooks.md) is about the repository's Git hooks -
> `make install-hooks`, pre-commit, pre-push - which the agent must never bypass.
> This page is about mivia's own layer, which runs *your* scripts around *the
> agent's* tool calls. The two run in opposite directions and share only a word.

> **Not skill triggers.** A `triggers:` phrase in `SKILL.md` influences which
> skill the *model picks* - probabilistic, and the model may ignore it. A hook
> fires on a lifecycle event - deterministic, and the model has no say. They
> compose; neither replaces the other.

## The three v1 events

| Event | When | Can block? |
|---|---|---|
| `PreToolUse` | after the invocation is reserved, before the tool runs | **Yes** |
| `PostToolUse` | after the tool returns | No - the tool already ran |
| `Stop` | the root turn ended | No |

`PreToolUse` is the only event that can stop anything. `PostToolUse` is for
reacting - format, lint, run tests. `Stop` is for observing - log a turn's cost.

Events other harnesses have (`SessionStart`, `SessionEnd`, `UserPromptSubmit`,
`SubagentStart`, …) are **deferred, not unknown**, and the config parser says so
by name rather than failing as a typo.

## Where hooks are configured

**`~/.mivia/mivia.toml` only.** A `[[hooks]]` table in a workspace
`.mivia/mivia.toml` is ignored, and mivia warns at startup naming the file and
how many hooks it skipped.

This is not conservatism. mivia's config does not merge across layers - exactly
one file is read - so a workspace config *replaces* your user config rather than
adding to it. A `[[hooks]]` table in a cloned repository would therefore be the
only hook config that exists, running arbitrary commands on your machine the
moment you `cd` into that repository. Project-supplied hooks wait on a real
config merge layer, which is its own change.

`$MIVIA_CONFIG` selects the *general* config. It does not relocate the hook
source, and a hook table in the file it points at is ignored with a warning.

## Shape

```toml
# ~/.mivia/mivia.toml

[[hooks]]
event   = "PreToolUse"
matcher = "run_command"        # regex on the tool name; absent or "" = every tool

  [[hooks.handlers]]
  type       = "command"       # command is the only type in v1
  argv       = ["./hooks/block-bypass.sh"]
  timeout    = 10              # seconds, 1-600
  on_timeout = "block"         # block | allow

[[hooks]]
event   = "PostToolUse"
matcher = "write_file|search_replace"

  [[hooks.handlers]]
  type = "command"
  argv = ["./hooks/format-changed.sh"]
```

`argv` is an **array**, and there is **no shell**. The first thing most people
try is a single command string, so it is worth saying why it does not exist: a
string that has to be split into arguments is a shell-shaped field that only
pretends not to be one, and it brings back every quoting bug it looks like it
avoids. mivia runs `argv[0]` directly with `argv[1:]` as arguments. A `;`, `&&`,
backtick or `$(…)` inside any element arrives at your script as a literal
argument.

`argv[0]` is a **path**, resolved against the directory of the config file that
declared the hook; absolute paths work too. There is **no `PATH` lookup**, so a
hook cannot silently become a different binary because `PATH` changed - and a
bare name like `gofmt` resolves to `~/.mivia/gofmt`, not to the one on your
`PATH`.

Everything is validated at load. An unknown key, an unknown event, a matcher
that is not a valid regular expression, a timeout outside 1-600, an
`on_timeout` that is neither `block` nor `allow` - each is an error naming the
file and the `[[hooks]]` index. Nothing is ever quietly coerced to the
permissive option.

## What your hook receives

Context arrives two ways, and **never** as command-line syntax.

Environment variables:

| Variable | Value |
|---|---|
| `MIVIA_HOOK_EVENT` | `PreToolUse`, `PostToolUse`, or `Stop` |
| `MIVIA_TOOL` | the tool name (empty for `Stop`) |
| `MIVIA_FILE` | the tool's top-level `path` argument, when it has one |
| `MIVIA_SESSION_ID` | the session id |
| `MIVIA_WORKSPACE_ROOT` | the workspace root, which is also the working directory |

And one JSON object on stdin:

```json
{
  "event": "PreToolUse",
  "tool": "run_command",
  "input": { "argv": ["git", "commit", "-m", "x"] },
  "session_id": "...",
  "turn_id": "...",
  "tool_call_id": "..."
}
```

A filename containing shell syntax is inert through both paths: a value passed
in the environment or in JSON is never re-parsed as syntax.

Hooks inherit your environment, unlike `run_command`, which runs under a
filtered one. The difference is who chose the program: `run_command` executes an
argv the *model* composed, while a hook is a program *you* wrote and confirmed.

## What your hook returns

Control is by exit code:

| Exit | Meaning |
|---|---|
| `0` | success. stdout is parsed as JSON; anything else is treated as plain output |
| `2` | **block** (`PreToolUse` only). stdout JSON is ignored; stderr is the reason |
| other | non-blocking warning; stderr is surfaced to you, execution continues |

**Only exit 0 parses stdout as JSON.** That is deliberate and matches Claude Code
and Codex: a hook cannot block *and* return a body that says it allowed.

Structured output, mirroring Claude Code so scripts port between harnesses:

```jsonc
// PreToolUse - nested
{ "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "deny",
    "permissionDecisionReason": "commit uses a hook-bypass flag forbidden by policy" } }

// PostToolUse and Stop - flat
{ "decision": "block", "reason": "…", "additionalContext": "…" }
```

Note that `PreToolUse` uses a **different shape** from the other events. This
trips people up, so mivia fails closed on it: a `PreToolUse` hook that returns
the flat `{"decision": …}` shape is **denied**, not read as an allow. Likewise
`permissionDecision` values other than `allow` and `deny` - including `ask` and
`defer`, which mivia has no dispatcher-layer prompt to escalate to - deny rather
than fall through to permission.

`updatedInput` (rewriting the tool's arguments) is **not supported** and is
rejected. mivia computes the invocation's input hash and dedup fingerprint
before the hook runs, so a rewrite afterwards would produce an audit record
describing input that was never executed.

Stdout that is not decision-shaped is ordinary output and becomes advisory
context attached to the tool result, in its own attributed block, under its own
8 KiB bound. It is never spliced into the tool's own result, so per-tool output
ceilings and audit hashes keep describing the tool's bytes.

## Timeouts: a hung gate is a closed gate

| Event | Default timeout | Default `on_timeout` |
|---|---|---|
| `PreToolUse` | 10s | **`block`** |
| `PostToolUse` | 10s | `allow` |
| `Stop` | 5s | `allow` |

`PreToolUse` defaults to `block` because the alternative is that **hanging the
gate disables the gate**. A hook wedged on a lock, waiting on stdin, or merely
slow would otherwise let the call through unchecked - and an attacker who can
make your hook hang would have defeated it, as would an ordinary flaky script.

A handler that cannot start at all - a typo in `argv`, a missing file - is the
same situation, and resolves the same way. Set `on_timeout = "allow"` if you
want a fail-open gate; you will have chosen it knowingly.

## Trust

Hooks run arbitrary commands, so **a fresh install runs zero hooks.**

There is no `trust` key. A file cannot declare its own trust level - a hostile
config would simply write the one that always runs. Trust is *derived* from two
things the config cannot influence: which fixed path the hook loaded from, and
the content hash of the hook definition.

Run `/hooks` to list what mivia found, then `/hooks trust <number>` to confirm
one. The listing distinguishes:

- **active** - confirmed, and running
- **pending** - never confirmed
- **hash-changed** - confirmed once, and edited since

Declining is not recorded. mivia asks again next session, so one mis-click
cannot permanently disable a hook you later want.

### What trust does and does not cover

Trust is keyed on the **hook definition**: event, matcher, argv, timeout,
`on_timeout`. Editing any of them revokes the confirmation automatically, which
is the property a name-keyed store could not give - it would let `fmt.sh` be
confirmed once and its definition rewritten freely.

**It does not cover the contents of the script at `argv[0]`.** Editing the
script body does **not** revoke your confirmation. This is the same boundary
Codex draws, and it is defensible - you confirmed "run this program on this
event", and the program is your own file under your own version control - but if
you assumed otherwise you had the wrong threat model. Reformatting your TOML
does not revoke trust; reordering handlers does, because it changes behaviour.

### Non-interactive runs

With no terminal there is nobody to confirm anything, so `-p` and any run
without a TTY execute **zero** non-managed hooks - including ones you already
confirmed interactively. A headless run deliberately does not inherit an
interactive confirmation, because "headless implies trusted" would make a cloned
repository's hooks execute on any build machine that ever runs mivia
non-interactively.

`--bypass-hook-trust` is the only way to run an unconfirmed hook, and it is
**dangerous**. It is meant for automation that has already vetted its hook
sources. It logs every hook it ran without review at startup, because a bypass
that leaves no record is indistinguishable from having no gate at all. It
bypasses *trust* and nothing else: argv-only execution, timeouts, `on_timeout`,
and the output bound all still apply.

The flag is named `bypass` on purpose. A flag that reads as a feature ("trust my
hooks") is what gets pasted into a CI config unexamined.

### Managed hooks

An operator can install hooks a user cannot disable, at `/etc/mivia/managed.toml`.
mivia loads it only if the filesystem itself vouches for it: a root-owned regular
file, with no group or world write bit, in a directory holding the same
properties, with no symbolic link on either. A root-owned file inside a
user-writable directory can simply be replaced, so checking the file alone would
check nothing.

The path is deliberately **not** under `~/.mivia`. A file in your own home is
writable by you - and by the agent running as you - so auto-trusting it would be
self-authorization one directory over, which is precisely what refusing a `trust`
key inside the config prevents. On a platform where mivia cannot verify that
boundary there is no managed tier at all, rather than an unverified one.

Managed hooks run in non-interactive sessions, and `/hooks trust` refuses them:
they are the operator's, not yours.

## Blocked is not failed

A tool a `PreToolUse` hook denied returns status `blocked`, distinct from
`failed`. A policy block and a broken tool are different events and must not be
indistinguishable in the audit log or the transcript. The reason your hook gave
reaches the model verbatim - that is the entire point of blocking rather than
silently dropping the call.

## Scope and limits

- Hooks fire for **tool** invocations only, never for skill or subagent
  dispatch. An event named `PreToolUse` that fired on subagent dispatch would be
  a lie in a security-relevant name.
- A **deduplicated** invocation fires no hook: the tool did not run.
- Hooks **propagate to subagents**. A gate a subagent escapes is not a gate.
- Hooks never re-enter mivia's dispatcher, so a `PreToolUse` hook matching
  `run_command` cannot dispatch `run_command` and recurse.
- `Stop` currently fires in the interactive TUI only. The classic `--plain` REPL
  and the `-p` one-shot have no turn-end publish point for it to hang from.
- `SKILL.md` frontmatter hooks, `http`/`mcp_tool`/`prompt`/`agent` handler
  types, and a global kill switch are all out of v1. With zero-by-default trust,
  "disable everything" is the state you start in.

## Example: refuse a commit that bypasses Git hooks

```toml
[[hooks]]
event   = "PreToolUse"
matcher = "^run_command$"

  [[hooks.handlers]]
  type       = "command"
  argv       = ["./hooks/no-verify-guard.sh"]
  timeout    = 5
  on_timeout = "block"
```

```sh
#!/bin/sh
# ~/.mivia/hooks/no-verify-guard.sh
if grep -q -- '--no-verify' ; then
  printf 'commit uses --no-verify, forbidden by policy\n' >&2
  exit 2
fi
exit 0
```

The script reads the invocation JSON on stdin, so `grep` sees the whole tool
call including its `argv`. Exit 2 blocks, and the message on stderr is what the
model is told.
