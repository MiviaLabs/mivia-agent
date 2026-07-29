# 09 — Role docs, examples, and program closeout

**Status:** Design-ready.
**Date:** 2026-07-29
**Commits:** `docs: document agent roles`, `feat(cli): ship role examples in mivia.toml.example`
**Depends on:** `02`, `08`.
**Blast radius:** MODERATE — a wrong `mivia.toml.example` is a shipped bug, not "just docs."

---

## 1. Docs to update

All OWNERS-registered; edit **in place** per rule 40 (`updateInPlaceOnly: true`).

| Doc | OWNERS topic | Why |
|---|---|---|
| `docs/product/agent.md` | `product-agent` | roles/team section |
| `docs/product/config.md` | `product-config` | `[agents]` schema |
| `docs/security/overview.md` | `security` | **the security-posture change** — deny/allow semantics, the narrowing rule, what "read-only role" does and does not mean |
| `docs/architecture/overview.md` | `architecture-overview` | the new `internal/roles/` package boundary |
| `docs/architecture/concurrency.md` | `concurrency` | only if `01`'s depth follow-up lands |

> **Rule 00 requires the canonical doc to ship *with* the behavior change**, so `docs/product/agent.md` moves into plan `07` rather than waiting here. `09` covers the remaining docs, examples, and closeout. The predecessor plan deferred every doc to a final phase, which violates documentation-first for the tool-contract change in `07`.

The predecessor plan omitted `docs/security/overview.md` and `docs/architecture/overview.md` entirely — a security feature with no security doc.

## 2. What the docs must say plainly

These are the honesty requirements. Each corrects an overclaim in the predecessor plan.

1. **"Read-only role" means read-only *tool exposure*, nothing more.** v1 provides **no state isolation** between roles: shared workspace root (`workspace.Root` has no read-only mode), shared process globals (`tools.RedactToolArgs()` is a process-global `atomic.Bool` at `internal/tools/privacy.go:16`; `handleRetentionDuration` at `cli/orchestration_state.go:33,128`), and one shared coordinator + ledger (`coordinators` is keyed on the dispatcher, which all roles share).

2. **`run_command` is total privilege.** The global run allowlist (`internal/tools/default_registry.go:24-35`) includes `sh`, `bash`, `rm`, `chmod`, `curl`, `wget`, `ssh`, `docker`, `python`, `node`. A role allowlisting `run_command` therefore has arbitrary file write, delete, network egress, and program execution — a **superset** of `write_file` + `search_replace` + `fetch_url`. Per-role argv scoping does not exist (see §3).

   Consequence to state explicitly: **tool-name set inclusion is not a privilege ordering.** `{run_command}` is not a subset of `{read_file, write_file, grep}` but is strictly more powerful. Anyone reasoning about role privilege by comparing tool lists will be wrong.

   The predecessor plan claimed in §7.9 that "the tool axis is the sole authority axis in v1, so narrowing it is sufficient." That is false, and the docs must not repeat it.

3. **`skills` is enforced for the root orchestrator only** (`06` §2).

4. **Renaming a role breaks resume of in-flight runs** — `requestFingerprint` hashes `Task.Name` (`coordinator/spawn.go:82-89`) and `ResumeInterruptedRun` replays the persisted handler name (`recovery.go:96-100`).

5. **`.mivia/` vs `.ai/` fallback** and the deprecation path (`04`).

## 3. Known limitations to document, not hide

| Limitation | Detail |
|---|---|
| No per-role `run_command` argv scoping | `runCommandTool.allowlist` is a struct field baked at registry build (`default_registry.go:121`, computed `:79-96`) and consumed via `resolveCommand`/`allowed` (`run.go:188-219`). `ScopedRegistry` re-exposes the **same instance** and cannot reconfigure it. Fixing it needs one registry per role, or a per-request policy AND-intersected with the global allowlist. |
| No per-role provider/model credentials | Spawned roles run on the spawner's completer. |
| No `permission_mode` | `runtime.Request.Permission` **is** live — enforced at `internal/skills/skills.go:85` — but only as a *skill-handler* gate. It has no meaning for `Kind=Subagent`/`Tool`, so there is no hook for a per-role mode. (The predecessor plan called it dead code; that was wrong, and the corrected premise still supports the same conclusion.) |
| No delegation graph | `can_spawn` is cut — see `00` §1. |
| ~~Run handles are guessable~~ | **Fixed by plan `02`** (run-handle ownership + unguessable IDs). If `02` has not shipped when this plan runs, restore this row and document the exposure. |

Add a pinning test for the first row — `TestRunCommand_GlobalAllowlistAppliesToAllRoles` — so the documented limitation is asserted rather than merely written down.

## 4. Examples

`mivia.toml.example` (repo root, 3151 bytes) gains a commented `[agents]` section: `researcher`, `engineer`, `reviewer`.

> **Do not ship a `test-runner` role with `tools = ["run_command"]`** as the predecessor plan did. Under the global allowlist that example role is more privileged than `engineer`, which teaches exactly the wrong mental model.

Ship one `.mivia/agents/researcher.md` example demonstrating the markdown form.

**Commit scope:** `mivia.toml.example` is at the repo root, **not** under `docs/**`, so per `commit-message.json` `scopeGuide` it belongs in a `cli` or `build` commit — not `docs`. The predecessor plan placed it under `docs` in two phases.

**Semgrep:** `mivia.generic.no-unresolved-drift-markers` forbids committed `TODO`/`FIXME`/`HACK`/`XXX`. "RESERVED"/"DEFERRED" comments are safe; `TODO` is not.

## 5. Program closeout

- Confirm/refresh plan statuses in `.ai/INDEX.md` §Plans (already registered; the predecessor was never registered — INDEX.md §Plans permits residency only *until* the Step-0 challenge completes, and this program's has).
- Archive `00`–`09` to `.ai/plans/archived/` on completion (the directory does not exist yet; create it).
- Confirm `INV-AG-7` (`01`) and `INV-AG-8` (`05`) resolve under `make validate-invariants`, and that the `make invariants` `-run` regex actually matches the new tests — it is a **hardcoded regex**, so tests not matching an existing prefix are silently skipped.
- Final `mivia-report/v1` per `AGENTS.md`, blast radius HIGH.

## 6. Verification

```bash
make docs-check      # OWNERS + in-place enforcement
make secret-scan     # committed role prompts must carry no secrets/PII
make verify && make test && make race && make invariants && make validate-invariants
```

**Manual smoke (real keys, not committed):**

```text
mivia chat --agent researcher "edit README.md"   # expect: write_file unavailable / refused
mivia chat --agent engineer  "edit README.md"    # expect: succeeds
mivia agents list --explain researcher           # effective tools match config
mivia doctor                                     # role table renders
```

**Runs a ≥1-round `bug-audit` on the docs/TOML diff.** A wrong `mivia.toml.example` ships a broken config to every new user; this is not "just docs."

**Rollback criterion:** if any §2 honesty requirement cannot be stated truthfully because the underlying behavior is worse than described, stop and fix the behavior — do not soften the doc.
