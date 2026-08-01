# 33.1 - Hook config scope: user-only load, workspace strip

**Status:** DESIGN - ready. This slice unblocks the rest of `33`.
**Date:** 2026-08-01
**Parent:** [`00-overview.md`](00-overview.md) §3b, §6a
**Depends on:** nothing. Deliberately first - it is the slice that resolves the
`00-overview.md` §3b blocker, and every later slice assumes its outcome.
**Blocks:** `02`, `04`, `06`.
**Blast radius:** LOW as shipped (it *removes* a loading path), HIGH if skipped -
every other slice inherits code execution from workspace config without it.

---

## 1. Why this is slice one

`00-overview.md` §3b establishes that mivia config does not merge:
`config.DefaultConfigCandidates()` (`internal/config/paths.go:47-60`) returns
`$MIVIA_CONFIG`, then `<cwd>/.mivia/mivia.toml`, then `~/.mivia/mivia.toml`, and
`Load` takes `FirstExisting` (`internal/config/load.go:308`). Exactly one file is
read. A workspace file does not *add to* user config - it *replaces* it.

So `[[hooks]]` in a workspace `mivia.toml` is not "a project hook alongside mine."
It is the only hook config that exists, supplied by a cloned repo, executing on the
user's machine. This slice closes that before any code can execute a hook.

## 2. Scope

1. `[[hooks]]` is read **only** from `config.UserConfigPath()` (`~/.mivia/mivia.toml`),
   opened at its fixed path - never via `config.Load`, whose result depends on cwd.
   This is the same mechanism plan `05` §5 specifies for `load_workspace_config`, for
   the same stated reason: *a floor the agent can lower is not a floor.*
2. When the file resolved by `config.Load` is a **workspace** file and it contains a
   `[[hooks]]` table, that table is **stripped and a warning emitted** naming the file
   and the count of hooks ignored. Mirrors `05` §5's treatment of workspace
   `[chat].system_prompt` / `[subagents].system_prompt`.
3. The warning is user-visible at startup, not debug-only. A silently ignored hook is
   how someone concludes hooks are broken and reaches for `--bypass-hook-trust`.

## 3. Out of scope

- Any hook *parsing* beyond detecting the table's presence - that is slice `02`.
- Any hook *execution* - slice `03`.
- A multi-file config merge layer. Project-supplied hooks are deferred with it
  (`00-overview.md` §3b). If that decision is reopened, this slice is void and a
  config-merge plan comes first.

## 4. Design note - do not "fix" this by reordering candidates

An attractive-looking shortcut is to make `DefaultConfigCandidates` prefer user
config over workspace config. **Do not.** That silently changes which config every
existing install loads - providers, tools, privacy - to fix a hooks problem. The
shadowing behaviour is a real defect, but it is a defect of the config layer with its
own blast radius, and it deserves its own plan rather than riding in on this one.

## 5. Verification

`go test ./internal/config/...`:

- `[[hooks]]` in a workspace `mivia.toml` is stripped; a warning names the path and
  the ignored count - the direct analogue of `05`'s `TestGate_IgnoredInWorkspaceConfig`
- the same table in `~/.mivia/mivia.toml` survives load
- hooks are read at the fixed user path even when `$MIVIA_CONFIG` points elsewhere -
  the env var selects the *general* config, it does not relocate the hook source
- a workspace file with no `[[hooks]]` emits no warning (no false positives on the
  common case)

Manual: a repo containing `.mivia/mivia.toml` with `[[hooks]]` warns and runs nothing.

## 6. Done when

A workspace-supplied `[[hooks]]` table can be shown to be unreachable by construction,
not by convention, and the test that proves it is named in the invariant row (`08`).
