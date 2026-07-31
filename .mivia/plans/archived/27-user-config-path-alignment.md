# 27 — Align the user config path: `~/.mivia/mivia.toml`

**Status:** ✅ IMPLEMENTED 2026-07-31 — hard cutover only; no legacy fallback, migration,
or notice. §7 hands one constraint to `05`.
**Date:** 2026-07-31
**Commits:** `1d3fe08` (partial path cutover); implementation completed in `bd5f1c7`.
**Depends on:** `04` (workspace namespace, shipped). **Blocks:** `05` — ship this first (§7).
**Blast radius:** LOW for the binary (two candidate slices, two pure path helpers, no new
file). **MEDIUM for `05`'s privilege surface** — §7 names a collision this change creates
and `05` must close.

> **Anchors verified at HEAD `c329a5f` (2026-07-31).** Per `00`'s standing note they will
> drift; grep the symbol, not the number.

---

## 1. Goal

Move the user-level config from `~/.config/mivia/config.toml` to `~/.mivia/mivia.toml`, and
the user-level env file from `~/.config/mivia/.env` to `~/.mivia/.env`, so the user and
workspace conventions are the same directory name and the same filename.

## 2. What is inconsistent today

`DefaultConfigCandidates()` (`internal/config/paths.go:31-43`) resolves three candidates,
and the third disagrees with the second on **both** axes:

| # | Candidate | Directory convention | Filename | Anchor |
|---|---|---|---|---|
| 1 | `$MIVIA_CONFIG` | — (explicit path) | — | `paths.go:33` |
| 2 | `<cwd>/.mivia/mivia.toml` | `.mivia/` namespace | `mivia.toml` | `paths.go:37` |
| 3 | `~/.config/mivia/config.toml` | `.config/mivia/` | `config.toml` | `paths.go:40` |

`DefaultEnvCandidates()` (`:46-55`) repeats it: `<cwd>/.env`, then
`~/.config/mivia/.env` (`:53`).

Two consequences worth stating before the tradeoff:

- **There is no layering.** `loadFile` takes `FirstExisting(DefaultConfigCandidates())`
  (`load.go:231`; `FirstExisting` at `paths.go:57-69` returns the first *regular file*), so
  exactly one config file is ever parsed. A workspace file shadows the user file entirely.
  This is why `05` §3 reads both files at fixed paths instead of through `config.Load`.
- **Adding a candidate is cheap; ordering is the entire semantics.** There is no merge to
  get wrong and no precedence UI to design — but a candidate inserted in the wrong position
  silently changes which file wins. Every option in §4 is therefore a one-line-per-slice
  change, and the only thing that can be wrong is order.

## 3. The XDG tradeoff — recorded, not re-litigated

`~/.config/` is the XDG Base Directory convention. `~/.mivia/` abandons it. **The owner has
decided in favour of symmetry with `.mivia/mivia.toml`**; this section records the cost, and
one fact that makes the cost smaller than it looks.

**`$XDG_CONFIG_HOME` is not honoured anywhere in this codebase.** `paths.go:40` and `:53`
hardcode `filepath.Join(home, ".config", "mivia", …)`. A user who sets
`XDG_CONFIG_HOME=~/.dotfiles/config` gets `~/.config/mivia/config.toml` regardless — the
spec's central mechanism. mivia is XDG-*shaped*, not XDG-compliant, so this change abandons
a convention that was never implemented, not one that was working.

`XDG_*` does appear twice, and neither is this: `internal/tools/run_command_test.go:191-193`
and the `env_allowlist` entries in `.mivia/mivia.toml:184` / `.mivia/mivia.toml.example:105`
govern which variables are passed to **child processes**, which is unrelated to where mivia
reads its own config.

**One residual inconsistency, deliberately left open.** `defaultStorePath()` uses
`os.UserCacheDir()` (`internal/config/defaults.go:83-84`), which *does* honour
`$XDG_CACHE_HOME` on Linux because Go's standard library implements it. After this change
mivia honours XDG for its cache and not for its config. That is worse than either pure
position, and it is out of scope here: moving the ledger database is a data-migration
question about a file mivia creates and owns, not a config-path question about a file the
user writes. It is named so the next reader does not discover it as a surprise.

**What this change buys the user: nothing they can do.** No capability, no fix, no
performance. It buys one convention instead of two. That is a real benefit — a user who
knows `.mivia/mivia.toml` now knows both locations — and the pre-release status makes a
hard cutover the appropriate compatibility posture.

## 4. Compatibility — DECIDED: **hard cutover, no legacy support**

### The population, measured

| Fact | Evidence |
|---|---|
| No tagged release exists | `git tag` is empty at HEAD |
| No release pipeline exists | `.github/workflows/` contains only `ci.yml`; no goreleaser, no `release` target in `Makefile` |
| The binary self-reports as pre-release | `version.Version = "0.0.0-dev"` (`internal/version/version.go`), overridden only by release builds that have never run |
| `~/.config/mivia/` does not exist on the only known user's machine | checked 2026-07-31 |
| mivia never creates the file | there is no `config init` command (`docs/product/config.md:107`); every user-level config was hand-created by following `docs/product/config.md:47-54` |

`13` rev 4 already decided the same compatibility posture for the config schema: mivia is
unpublished with one user, so no deprecation window is owed. The path change follows that
decision.

### Decision

**The legacy paths stop being read the moment this plan lands.** `paths.go:40` and `:53` are
replaced, not appended to. `~/.config/mivia/config.toml` and `~/.config/mivia/.env` are
ordinary files that mivia has no knowledge of. There is no fallback, stat probe, warning,
auto-migration, or compatibility shim.

An explicit `$MIVIA_CONFIG` or `--config` path remains supported as a generic user-supplied
file override. It is not a legacy candidate and receives no special handling.

### Rejected

- **Legacy fallback or migration notice.** Rejected because this CLI is unpublished and has
  no user population to preserve. Adding a second candidate or a detector would create
  permanent compatibility surface without benefit.
- **Auto-migrate.** Rejected because mivia must not write into a user's home directory to
  save them a manual copy or move.

## 5. `.env` moves too — in scope, not scoped out

`~/.config/mivia/.env` → `~/.mivia/.env` (`paths.go:53`), same hard cutover.

Leaving it behind would preserve the split this plan closes: config at
`~/.mivia/mivia.toml` and credentials at `~/.config/mivia/.env`.

**The workspace `.env` stays at `<cwd>/.env` and does not move into `<cwd>/.mivia/`**
(`paths.go:49`). The symmetry argument runs out here on purpose: a repo-root `.env` is a
convention owned by direnv, docker compose, and every dotenv library in existence, and mivia
reading a file other tools already write is the point. `~/.mivia/.env` has no such prior
claim. The resulting asymmetry — user env inside the namespace, workspace env beside it — is
the correct one and the docs must say why.

**Not changed:** the explicit `env_file` key is a user-supplied path expanded by
`ExpandPath` (`paths.go:11-28`, consumed at `load.go:251-257`) and is unaffected. The example
value in `docs/product/config.md:59` is documentation and moves with §8. Nothing in this plan
creates `~/.mivia/.env` or sets its mode; the docs recommend `chmod 600`, as today.

## 6. `$MIVIA_CONFIG` — unchanged

Verified: `paths.go:33` reads it with `os.Getenv`, expands it through `ExpandPath`, and
places it **first**. Not `envfile.Lookup`. This matters beyond this plan — `05` §5's
rejection of an env-var gate rests on exactly this property, because `envfile.Lookup` would
resolve through `DefaultEnvCandidates()` whose first entry is `<cwd>/.env`, handing a
workspace its own gate. `$MIVIA_CONFIG` is safe *because* it is `os.Getenv`, and it selects a
config **file**, so it is orthogonal to any directory convention.

No change to the variable, its name, its position, or how it is read. `--config` likewise
(`chat_command.go:32`, `doctor.go:12`, `config_cmd.go`). Explicit paths remain generic
overrides; no legacy-path behavior is added.

## 7. Interaction with `05` — this plan ships first, and hands `05` a new guard

### 7a. Ordering

**Neither blocks the other in Go** — this plan touches `internal/config/paths.go`; `05`
touches `internal/cli/`. But `05` writes new code that names the user config path at a fixed
location (§3, §5), so the order decides whether a HIGH-blast-radius privilege surface is
edited once or twice.

**Ship `27` first.** It is LOW risk, self-contained, and `05` sits at position 5 in the
`INDEX` triage. Shipping it after `05` means re-touching `internal/cli/agent_roles.go` — the
file that resolves privilege grants — for a cosmetic path change.

**This plan must therefore export the fixed path as a symbol** so `05` calls it instead of
re-deriving `filepath.Join(home, …)`: `config.UserConfigPath()` and `config.UserEnvPath()`,
returning the fixed home-relative paths without consulting candidates or the filesystem.
That is precisely what `05` §3 needs — "read at fixed paths, not through `config.Load`" —
and it keeps one place that knows where user config lives.

Every user-config reference in `05` moves: §3 (`:42`, `:47`), §4 (`:123`, `:156`), §5
(`:165`, `:167`, `:169`), and the error-message example at `:200`. While editing, note that
`05` cites `config/load.go:155` for `FirstExisting` in two places; at HEAD it is `load.go:231`.

### 7b. The collision this change creates — `05` must close it

> **When the workspace root is the home directory, `<cwd>/.mivia/mivia.toml` and
> `~/.mivia/mivia.toml` are the same file.**

Under today's paths this is impossible: `~/.config/mivia/config.toml` is not
`<any cwd>/.mivia/mivia.toml` for any cwd. After this change the two candidates collapse
whenever `cwd` (or `--workspace`) is `$HOME` — which is not an exotic way to run a CLI agent.

**For `config.Load` today this is harmless.** `FirstExisting` returns the same path either
way; the file is parsed once and nothing changes. No guard is needed and none is built here.

**For `05` it is an escalation.** `05` §3 ranks the user file *trusted, always loads* and the
workspace file *untrusted, gated off by default*; §5 reads `load_workspace_roles` from the
user file at its fixed path so that a hostile repo cannot authorize itself. If the two paths
are one file, the gate's own file becomes workspace-writable — and `write_file` is confined
to the workspace root (`internal/cli/root.go:51`), so with root `$HOME` the agent is already
inside it. `04` §5's rule applies verbatim: **a floor the agent can lower is not a floor.**

The rule `05` must adopt, stated so it cannot be implemented backwards:

> When the resolved user namespace directory and the resolved workspace namespace directory
> are the same directory, the file is **user config only**: workspace-sourced
> `[[agents.roles]]` are refused and the gate keeps its user-config meaning. Drop the
> untrusted reading, never the trusted one — refusing the trusted reading instead would make
> a user lose their own roles by running mivia in `$HOME`, while loading the untrusted one is
> the escalation. Compare resolved absolute paths (`filepath.EvalSymlinks` then
> `filepath.Clean`), not strings, or a symlinked `$HOME` defeats it.

Pin in `05` with `TestWorkspaceRolesRefusedWhenWorkspaceIsHome` and
`TestGateKeepsUserMeaningWhenWorkspaceIsHome`.

**This plan deliberately does not build that guard.** Nothing reads workspace-sourced roles
today, so a refusal now would be a guard with no principal — `25` §4's and
`architecture-review` step 2's exact finding. The sequencing in §7a is the mechanism that
keeps the constraint attached to a plan that is still open: if `05` ships first and `27`
follows, this hazard arrives with no plan owning it.

### 7c. `~/.mivia/` may already exist

`chat` does `os.MkdirAll(workspace.SessionsDir(wsRoot))` (`chat_command.go:85-88`), so any
machine where mivia was run from `$HOME` already has `~/.mivia/sessions/`. Path resolution
must use the fixed file path regardless of whether the directory already exists; this plan
does not create the directory or any config file.

## 8. Exact file list

**Files to create: none.** `04` needed a new resolver; this reuses it (`04` created
`internal/workspace/namespace.go`, and `NamespacePath` already takes an arbitrary root).

### Modify — Go

| File | Change |
|---|---|
| `internal/config/paths.go` | `:40` → `workspace.NamespacePath(home, "mivia.toml")`; `:53` → `workspace.NamespacePath(home, ".env")`. Candidate **order unchanged**: `$MIVIA_CONFIG` → workspace → user. Add `UserConfigPath()` / `UserEnvPath()` as fixed paths with no filesystem access (§7a) |
| `internal/workspace/namespace.go` | Widen the doc comment at `:5-16`: the namespace directory now also resolves under the home directory. No signature change — `NamespacePath` (`:21-24`) already takes an arbitrary root. `:14-16`'s standing rule ("Nothing outside this file may name a namespace directory") is what forbids a second `.mivia` literal appearing in `paths.go` |
| `internal/cli/root.go` | `:63` usage text → `Config: $MIVIA_CONFIG \| ./.mivia/mivia.toml \| ~/.mivia/mivia.toml` |

### Modify — shipped examples and docs

| File | Change |
|---|---|
| `docs/product/config.md` | The canonical doc (`docs/OWNERS.yaml` topic `product-config`); rule 40 — edit in place, create nothing. Update search orders, setup commands, the `env_file` example, and the installed-binary section. Explain why workspace `.env` stays at the repo root. Do not add legacy migration or compatibility guidance |
| `.mivia/mivia.toml.example` | `:1` uses `~/.mivia/mivia.toml`; `:10` uses `./.env` for the workspace example |
| `.mivia/mivia.toml` | `:2`, `:5`, `:16`, `:17`. **This file has unrelated uncommitted modifications at the time of writing — rebase before editing** |
| `.env.example` | `:1` — `Copy to ./.env or ~/.mivia/.env` |
| `typescript` (repo root, **tracked**) | A committed `script(1)` capture. It carries a stale usage dump (`./mivia.toml`, `~/.config/mivia/config.toml`) that is already wrong at HEAD — it predates `04`. **Delete it; do not update it.** A terminal recording is not a doc and cannot be kept correct |

### Modify — plans

| File | Change |
|---|---|
| `.mivia/plans/05-role-model-core.md` | Confirm §7a uses `config.UserConfigPath()` and §7b retains the home-equals-workspace guard; do not add legacy compatibility behavior |
| `.mivia/INDEX.md` | Registry row (lands with this plan's own commit) |

**Not changed:** `load.go:235`'s "no config file found (tried %s)" already interpolates the
live candidate list and stays correct by construction. Explicit `--config` and
`$MIVIA_CONFIG` remain generic path overrides.

## 9. Test strategy

`internal/config/paths_test.go` exists (`:12-30`, `:32-38`) and one of its two tests must be
rewritten, not merely extended.

> **`TestDefaultConfigCandidatesUsesNamespace` stops proving anything after this change.**
> Its assertion (`:22-25`) fails any candidate ending in `/mivia.toml` that does not end in
> `.mivia/mivia.toml`. The new user candidate ends in `.mivia/mivia.toml`, so it passes —
> while the test can no longer distinguish the workspace candidate from the user one, which
> is the only thing its comment claims it checks. Rewrite it to assert both candidates by
> exact expected value (`filepath.Join(cwd, ".mivia", "mivia.toml")`,
> `filepath.Join(home, ".mivia", "mivia.toml")`) **and their order**.

| Test | Package | Asserts |
|---|---|---|
| `TestDefaultConfigCandidatesUsesNamespace` (rewritten) | `config` | both candidates by exact value, and that neither is the repo-root `mivia.toml` the original guarded |
| `TestDefaultConfigCandidatesHasNoLegacyUserPath` | `config` | no candidate contains `.config` + `mivia` — the cutover as behaviour, not as a diff |
| `TestCandidateOrderPrefersWorkspaceOverUser` | `config` | with both files present, `FirstExisting` returns the workspace one. Order is the whole semantics (§2) |
| `TestDefaultEnvCandidatesUsesNamespace` | `config` | `<cwd>/.env` first, `~/.mivia/.env` second — and that the workspace `.env` did **not** move into `.mivia/` (§5) |
| `TestUserConfigPath` / `TestUserEnvPath` | `config` | fixed home-relative paths, independent of filesystem state |
| `TestDefaultConfigCandidatesHonorsEnvOverrideFirst` (existing) | `config` | unchanged; re-run for §6 |

All filesystem tests use `t.TempDir()` with `HOME` redirected, per rule 20.

### Mutation proofs (rule 20 — required for every guard)

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Keep `~/.config/mivia/config.toml` as a fourth candidate | `TestDefaultConfigCandidatesHasNoLegacyUserPath` |
| M2 | Put the user candidate before the workspace candidate | `TestCandidateOrderPrefersWorkspaceOverUser` |
| M3 | Restore `TestDefaultConfigCandidatesUsesNamespace` to its HEAD form | **nothing fails.** Recorded per rule 20 as the reason the rewrite is a deliverable and not a cleanup: at HEAD that test is a shape assertion, not a guard |
| M4 | *(05, not built here)* Drop the home-equals-workspace refusal | `TestWorkspaceRolesRefusedWhenWorkspaceIsHome` — handed over in §7b |

### Verification

```bash
go build ./... && go vet ./...
go test ./internal/config/... ./internal/cli/... ./internal/workspace/... -race
make verify && make invariants
```

No legacy-path fixture or migration probe is required. The focused tests must prove that
only the new default candidates are searched and that explicit overrides remain first.

## 10. Rollback criterion

If `~/.mivia/` proves to collide in the home directory — another tool claiming the name, or
`05` unable to close §7b without contorting the role model — do not add the legacy path back
as a second candidate. That restores the split this plan exists to close and leaves the
codebase worse than before it. Per `04` §7: choose one location and compile in exactly one.
If §7b cannot be closed, reopen §3 rather than ship a hybrid.

## 11. Blast radius and invariants

**No row in `.mivia/invariants.md` is affected.** The manifest contains no invariant about
config candidate paths (checked at HEAD; `INV-SEC-1` and `INV-SEC-2` concern config-only
*defaults* for secret patterns and redaction, not where config is found). This plan
**allocates no invariant id**: the cutover is a path change with no guard standing behind it,
and a manifest row asserting a path is not an invariant — it is a restatement of the code.

`05` §7b's refusal **is** a guard and needs a row, allocated at `05`'s landing time, lowest
free id per prefix. `INDEX` records `INV-AG-28` as lowest free at HEAD; do not reserve it
here.

Blast radius by surface:

| Surface | Radius |
|---|---|
| `internal/config` | LOW — two candidate entries, two new pure functions |
| `internal/cli` | LOW — one usage string |
| `internal/workspace` | NONE functionally — comment only |
| User-visible | MEDIUM in kind, LOW in measure — a config location changes for an unpublished CLI |
| `05`'s privilege surface | **MEDIUM** — §7b creates a collision that did not exist and must be closed there |

## 12. Plan scorecard

| Criterion | Score |
|---|---|
| Compiles | PASS — two replaced expressions, two added pure functions |
| No cycles | PASS — `internal/config` already imports `internal/workspace` (`paths.go:8`) |
| No breaking Go API | PASS — additive; `DefaultConfigCandidates`/`DefaultEnvCandidates` keep their signatures |
| Testable in isolation | PASS — candidate builders are pure given `HOME` and cwd |
| Backward-compatible config | **FAIL, intentionally** — §4 accepts a hard cutover because the CLI is unpublished |
| Every function has a test | PASS by §9 |

## 13. What this does NOT solve

- The cache path still honours XDG while config does not (§3). Named, not fixed.
- There is still **no layering** between workspace and user config. This plan aligns the
  paths; it does not merge them. `05` §3's whole fixed-path apparatus exists because of that
  and stays necessary afterwards.
- `05` §5's four pre-existing ungated workspace-to-system-prompt paths are untouched.
- The stray tracked `typescript` capture is deleted here as a drive-by (§8) because it names
  a path this plan changes. Nothing prevents another stale capture from being committed; if
  that recurs, it wants a gate, not compatibility code.
