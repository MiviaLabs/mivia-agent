# 33.5 — Headless gating and `--bypass-hook-trust`

**Status:** DESIGN — ready.
**Date:** 2026-08-01
**Parent:** [`00-overview.md`](00-overview.md) §6a, §6b
**Depends on:** `04`.
**Blocks:** `06`.
**Blast radius:** MEDIUM — this is the second half of the gate. Landing `06` without
it means CI and `-p` runs execute hooks nobody confirmed.

---

## 1. The rule

With no TTY there is no one to confirm, so `-p` / headless runs execute **zero
non-managed hooks** unless `--bypass-hook-trust` is passed. Managed hooks
(`~/.mivia/managed.toml`) still run — an operator placed them there out-of-band, which
is the whole point of the tier.

This is fail-closed by default, and it is the correct default even though it means a
CI job that expects hooks silently gets none until someone passes the flag. The
alternative — headless implies trusted — makes a cloned repo's hooks execute on any
build machine that ever runs mivia non-interactively.

## 2. One flag, and it says `bypass`

`--bypass-hook-trust`, mirroring Codex's `--dangerously-bypass-hook-trust`.

`00-overview.md` §6a drops the first draft's `--trust-project-hooks` /
`--trust-user-hooks`. The reasoning is worth restating because it is the whole design
of this slice: **a flag whose name reads as a feature gets pasted into CI configs
unexamined.** "Trust my hooks" sounds like configuration. "Bypass hook trust" sounds
like what it is. One flag, greppable, and it never reads as a feature.

Requirements:

- documented as dangerous, in `--help` and in the docs (slice `08`);
- when passed, the run **logs which hooks it executed without confirmation**, at
  startup, by event and argv. A bypass that leaves no record is indistinguishable from
  no gate at all;
- it bypasses *trust*, not the other guarantees: argv-only execution, no shell, output
  bounds, timeouts, and the `Kind == Tool` filter all still apply.

## 3. Interaction with `disableAllHooks`

Claude Code has a global kill switch that managed hooks survive (`00-overview.md` §1b).
v1 does **not** need one — with zero-by-default trust, "disable everything" is the
state you start in, and `/hooks` can leave hooks unpromoted. Note it as a deliberate
omission rather than an oversight, so a later reviewer does not read the gap as a
missing feature.

## 4. Verification

`go test ./internal/cli/...`:

- headless with no flag runs zero non-managed hooks, including hooks that **are**
  confirmed in the trust store — headless does not inherit an interactive confirmation
- headless with managed hooks present runs those, and only those
- `--bypass-hook-trust` runs unconfirmed hooks **and** logs each one it ran
- the flag does not relax argv-only execution, timeouts, or the output bound
- interactive runs are unaffected by the flag's absence

Manual: a CI invocation without the flag reports why hooks did not run, in a message
that names the flag — a silent no-op here is the failure mode that produces
"hooks are broken" bug reports.

## 5. Done when

There is exactly one way to run an unconfirmed hook, it is named `bypass`, and taking
it leaves a record.
