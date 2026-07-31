# 27 — Align the user config path: `~/.mivia/mivia.toml`

**Status:** Design-ready — §4 decided (hard cutover + a stat-only notice); §7 hands one
constraint to `05`.
**Date:** 2026-07-31
**Commits:** *(none — this plan changes no production code)*
**Depends on:** `04` (workspace namespace, shipped). **Blocks:** `05` — ship this first (§7).
**Blast radius:** LOW for the binary (two candidate slices, no new file). **MEDIUM for
`05`'s privilege surface** — §7 names a collision this change *creates* and `05` must close.

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
  silently changes which file wins, with nothing to notice it. Every option in §4 is
  therefore a one-line-per-slice change, and the only thing that can be wrong is order.

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
knows `.mivia/mivia.toml` now knows both locations — but it is a small one, and it is the
reason §4 refuses to pay for it with a permanent second code path.

## 4. Backward compatibility — DECIDED: **hard cutover, plus a stat-only notice**

### The population, measured

| Fact | Evidence |
|---|---|
| No tagged release exists | `git tag` is empty at HEAD |
| No release pipeline exists | `.github/workflows/` contains only `ci.yml`; no goreleaser, no `release` target in `Makefile` |
| The binary self-reports as pre-release | `version.Version = "0.0.0-dev"` (`internal/version/version.go`), overridden only by release builds that have never run |
| `~/.config/mivia/` does not exist on the only known user's machine | checked 2026-07-31; `13` §1.9 recorded the same finding independently at `:915` |
| mivia never creates the file | there is no `config init` command (`docs/product/config.md:107`); every user-level config was hand-created by following `docs/product/config.md:47-54` |

`13` rev 4 (`.mivia/plans/13-provider-model-arrays.md:913-915`, dated 2026-07-31) already
decided this exact compatibility question for the config *schema*: **"mivia is unpublished
with one user, so no deprecation window is owed."** It deleted the `model` key outright — no
shim, no alias, no rename guard. Deciding differently here for the config *path*, three days
later, on the same evidence, would mean two contradictory compatibility postures in one
config system.

### Decision

**The legacy paths stop being read the moment this plan lands.** `paths.go:40` and `:53` are
replaced, not appended to. `~/.config/mivia/config.toml` and `~/.config/mivia/.env` are
ordinary files that mivia has no knowledge of, exactly as `04` §3 made `.ai/` ordinary
workspace content.

**Plus one thing `04` deliberately refused: a notice.** `04` §4 rejected a deprecation
notice because detecting the old location meant compiling `.ai` into the binary, and `.ai`
was a *squatted generic namespace* that the plan existed to stop naming. That argument does
not transfer: `~/.config/mivia/` is mivia's own directory, no rule forbids naming it, and
there is a failure mode here that `04` did not have — see below. So:

> `LegacyUserPaths()` in `internal/config` returns the two `.config/mivia` paths. Nothing
> reads them, opens them, or parses them; they are `os.Stat`-ed and named in one stderr
> line. This is **not** a fallback: there is no second source of truth, no precedence, and
> `05` still has exactly one fixed user path to read (§7).

### Why a notice is needed here and was not needed in `04`

`04`'s break was self-announcing: a user whose `.ai/agent-prompt.md` stopped being read lost
their prompt and their workspace skills on the next `mivia chat`, visibly.

This break can be **silent and wrong**, not merely absent. `chat` and `doctor` both pass
`AllowMissingConfig: true` (`internal/cli/chat_command.go:46`, `doctor.go:16-19`), so a
config that vanishes is not an error — `Load` returns built-in defaults and sets
`ConfigPath = "(defaults)"` (`load.go:97-99`). A user whose `~/.config/mivia/config.toml`
selected `provider = "zai"` with a custom `base_url` and a `models` allowlist, and who has
`DEEPSEEK_API_KEY` in their process environment, would silently start chatting to
`deepseek-v4-flash` — a different provider, a different model, real spend — with no error at
all. That is the one outcome a config system must never produce, and one `os.Stat` closes it.

### When the notice fires, and when it does not

| Situation | Notice? |
|---|---|
| Legacy file exists, new counterpart does not | **yes** — one line to stderr |
| New counterpart exists (migrated by `mv` **or** `cp`, so the legacy file may remain) | no — it self-extinguishes |
| Neither exists | no |
| `$MIVIA_CONFIG` or `--config` points *at* the legacy file | **no** — it is being read, so "no longer read" would be a lie (§6) |

Config and env are keyed independently: migrating one does not silence the other.

Exact text, naming both paths and the action:

```text
notice: ~/.config/mivia/config.toml is no longer read — move it to ~/.mivia/mivia.toml
```

Stderr, never stdout: `config show` and `doctor` have parseable stdout
(`doctor.go:24-42`). The house pattern is already there —
`fmt.Fprintf(os.Stderr, "warning: …")` at `chat_command.go:87,93`. Emitted immediately after
`config.Load` in all three commands, which for `chat` is before any TUI paint.

### When the notice itself is deleted

**In the same change that cuts the first tagged release.** It exists for a pre-release
population that a released binary cannot contain: a fresh install has never had
`~/.config/mivia/`, and advising it to migrate from a path mivia never wrote is a bug.
`LegacyUserPaths()` and its CLI caller are deleted as a pair — a stat helper left with zero
callers is exactly `25` §4's failure mode.

### Rejected

- **Fallback (new path first, legacy path still read, deprecation warning).** Two lines to
  add, and genuinely cheap under `FirstExisting`. Rejected on two grounds. (a) It buys
  nothing over the notice for the measured population of §4, and pays a permanent second
  code path for a change that §3 admits buys the user nothing. (b) It hands `05` **two**
  possible user-config files at fixed paths. `05` §5 reads `load_workspace_roles` and
  `[agents.guardrails]` from the user file at a fixed path *specifically* so a workspace
  cannot lower its own floor; "which of the two fixed paths is the floor" is a question with
  no safe default — read the first that exists and a stale legacy file silently overrides a
  migrated one; read the new one only and the legacy path is a fallback for config but not
  for privilege, which is worse than either. A cosmetic change must not add a branch to a
  privilege surface.
- **Auto-migrate (copy or move on first run).** Rejected. `04` §4 already refused to move
  files in a user's tree unasked, and `121ee0b`/`f439686` removed the last auto-write mivia
  had. It also has real failure modes the other two do not: a partial copy under a full
  disk, a destination that already exists with different content, a symlinked
  `~/.config/mivia`, and two mivia processes racing on first run. Writing to a user's home
  directory to save them one `mv` is not a trade this repo makes.

## 5. `.env` moves too — in scope, not scoped out

`~/.config/mivia/.env` → `~/.mivia/.env` (`paths.go:53`), same notice, same cutover.

Leaving it behind would reproduce this plan's own defect in a worse form: config at
`~/.mivia/mivia.toml` and credentials at `~/.config/mivia/.env` is *more* split than today,
where at least the two live together. It is the same one-line change.

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
(`chat_command.go:32`, `doctor.go:12`, `config_cmd.go`). One consequence, handled in §4: a
`$MIVIA_CONFIG` pointing at the legacy file keeps working, and must therefore suppress the
notice.

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
machine where mivia was run from `$HOME` already has `~/.mivia/sessions/`. Two consequences,
both mirroring `04` §3's note about `.mivia/` already being occupied:

1. The §4 notice must key on **the file being absent**, never on "`~/.mivia/` does not
   exist", or it never fires for exactly the users who need it.
2. The directory is already mivia's on those machines, which is a small point in the
   decision's favour: `~/.mivia/` is not a fresh claim on the user's home directory.

## 8. Exact file list

**Files to create: none.** `04` needed a new resolver; this reuses it (`04` created
`internal/workspace/namespace.go`, and `NamespacePath` already takes an arbitrary root).

### Modify — Go

| File | Change |
|---|---|
| `internal/config/paths.go` | `:40` → `workspace.NamespacePath(home, "mivia.toml")`; `:53` → `workspace.NamespacePath(home, ".env")`. Candidate **order unchanged**: `$MIVIA_CONFIG` → workspace → user. Add `UserConfigPath()` / `UserEnvPath()` (fixed paths, no filesystem access — §7a) and `LegacyUserPaths()` (stat-only, with a comment naming its deletion trigger — §4) |
| `internal/workspace/namespace.go` | Widen the doc comment at `:5-16`: the namespace directory now also resolves under the home directory. No signature change — `NamespacePath` (`:21-24`) already takes an arbitrary root. `:14-16`'s standing rule ("Nothing outside this file may name a namespace directory") is what forbids a second `.mivia` literal appearing in `paths.go` |
| `internal/cli/doctor.go` | Emit the §4 notice after `config.Load` (`:16-22`). Place the shared unexported helper here beside `displayPath` (`:56-61`) |
| `internal/cli/chat_command.go` | Call the helper after `:46`, before any TUI paint |
| `internal/cli/config_cmd.go` | Call the helper after `:27` |
| `internal/cli/root.go` | `:63` usage text → `Config: $MIVIA_CONFIG \| ./.mivia/mivia.toml \| ~/.mivia/mivia.toml` |

### Modify — shipped examples and docs

| File | Change |
|---|---|
| `docs/product/config.md` | The canonical doc (`docs/OWNERS.yaml` topic `product-config`); rule 40 — edit in place, create nothing. Search orders `:12-14` and `:16-19`; the setup block `:47-54`; the `env_file` example `:59`; the installed-binary section `:102-107`. Add §5's explanation of why the workspace `.env` stays at the repo root, and the §4 migration line |
| `.mivia/mivia.toml.example` | `:1` and `:10` name `~/.config/mivia/…` |
| `.mivia/mivia.toml` | `:2`, `:5`, `:16`, `:17`. **This file has unrelated uncommitted modifications at the time of writing — rebase before editing** |
| `.env.example` | `:1` — `Copy to ./.env or ~/.config/mivia/.env` |
| `typescript` (repo root, **tracked**) | A committed `script(1)` capture. It carries a stale usage dump (`./mivia.toml`, `~/.config/mivia/config.toml`) that is already wrong at HEAD — it predates `04`. **Delete it; do not update it.** A terminal recording is not a doc and cannot be kept correct |

### Modify — plans

| File | Change |
|---|---|
| `.mivia/plans/05-role-model-core.md` | Seven path references (§7a) plus the new §7b paragraph and its two test names. Same pattern as `25` §7 Wave 4 amending `05` §6 |
| `.mivia/plans/13-provider-model-arrays.md` | `:191` (§1.9) lists the candidate paths inside a live implementation-ready plan — update. **`:915` is dated evidence inside a revision log — leave it.** Do not rewrite a finding that was true when it was recorded |
| `.mivia/INDEX.md` | Registry row (lands with this plan's own commit) |

**Not changed:** `load.go:235`'s "no config file found (tried %s)" already interpolates the
live candidate list and stays correct by construction.

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
| `TestLegacyUserPathsAreStatOnly` | `config` | **load-bearing.** With a valid legacy config present, a non-`$HOME` cwd, and no other candidate, `Load` reports `(defaults)` and none of the legacy file's values appear in `Resolved` |
| `TestDefaultConfigCandidatesHonorsEnvOverrideFirst` (existing) | `config` | unchanged; re-run for §6 |
| `TestNoticeFiresWhenOnlyLegacyConfigExists` | `cli` | one stderr line naming both paths |
| `TestNoticeSilentAfterMigration` | `cli` | new file present **and legacy file still present** (the `cp` case) ⇒ silence |
| `TestNoticeSilentWhenConfigFlagSelectsTheLegacyFile` | `cli` | `$MIVIA_CONFIG`/`--config` at the legacy path: it loads, and no "no longer read" line is printed (§6) |
| `TestNoticeGoesToStderrNotStdout` | `cli` | `doctor` and `config show` stdout stays parseable |
| `TestNoticeCoversConfigAndEnvIndependently` | `cli` | migrating one does not silence the other (§5) |

All filesystem tests use `t.TempDir()` with `HOME` redirected, per rule 20.

### Mutation proofs (rule 20 — required for every guard)

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Keep `~/.config/mivia/config.toml` as a fourth candidate | `TestDefaultConfigCandidatesHasNoLegacyUserPath` |
| M2 | Make `LegacyUserPaths` open and parse the file instead of `os.Stat`-ing it | `TestLegacyUserPathsAreStatOnly` |
| M3 | Put the user candidate before the workspace candidate | `TestCandidateOrderPrefersWorkspaceOverUser` |
| M4 | Fire the notice whenever the legacy file exists, ignoring the new one | `TestNoticeSilentAfterMigration` |
| M5 | Print the notice on stdout | `TestNoticeGoesToStderrNotStdout` |
| M6 | Suppress the notice whenever `--config` is set at all, rather than only when it selects the legacy file | `TestNoticeFiresWhenOnlyLegacyConfigExists` (run with `--config` pointing elsewhere) |
| M7 | Restore `TestDefaultConfigCandidatesUsesNamespace` to its HEAD form | **nothing fails.** Recorded per rule 20 as the reason the rewrite is a deliverable and not a cleanup: at HEAD that test is a shape assertion, not a guard |
| M8 | *(`05`, not built here)* Drop the home-equals-workspace refusal | `TestWorkspaceRolesRefusedWhenWorkspaceIsHome` — handed over in §7b |

### Verification

```bash
go build ./... && go vet ./...
go test ./internal/config/... ./internal/cli/... ./internal/workspace/... -race
make verify && make invariants
```

Plus one manual pass, because §4's whole justification is a user-visible failure mode:
create `~/.config/mivia/config.toml` selecting a non-default provider, run `mivia doctor`,
and confirm the notice appears and `config:` reads `(defaults)` rather than silently
reporting the legacy file's provider.

## 10. Rollback criterion

If `~/.mivia/` proves to collide in the home directory — another tool claiming the name, or
`05` unable to close §7b without contorting the role model — the fix is **not** to re-add
`~/.config/mivia/config.toml` as a second candidate. That restores the split this plan
exists to close and leaves the codebase worse than before it. Per `04` §7: choose one
location and compile in exactly one. If §7b cannot be closed, this plan is wrong and
`~/.config/mivia/` should be kept — reopen §3 rather than ship a hybrid.

The §4 notice is separately reversible at zero cost: delete two symbols.

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
| `internal/cli` | LOW — one helper, three call sites, one usage string |
| `internal/workspace` | NONE functionally — comment only |
| User-visible | MEDIUM in kind, LOW in measure — a config location changes, for a measured population of one pre-release machine (§4) |
| `05`'s privilege surface | **MEDIUM** — §7b creates a collision that did not exist and must be closed there |

## 12. Plan scorecard

| Criterion | Score |
|---|---|
| Compiles | PASS — two replaced expressions, two added pure functions |
| No cycles | PASS — `internal/config` already imports `internal/workspace` (`paths.go:8`) |
| No breaking Go API | PASS — additive; `DefaultConfigCandidates`/`DefaultEnvCandidates` keep their signatures |
| Testable in isolation | PASS — candidate builders are pure given `HOME` and cwd |
| Backward-compatible config | **FAIL, intentionally** — §4, hard cutover, notice-covered, evidence-backed |
| Every function has a test | PASS by §9 |

## 13. What this does NOT solve

- The cache path still honours XDG while config does not (§3). Named, not fixed.
- There is still **no layering** between workspace and user config. This plan aligns the
  paths; it does not merge them. `05` §3's whole fixed-path apparatus exists because of that
  and stays necessary afterwards.
- `05` §5's four pre-existing ungated workspace-to-system-prompt paths are untouched.
- The stray tracked `typescript` capture is deleted here as a drive-by (§8) because it names
  a path this plan changes. Nothing prevents another one from being committed; if that
  recurs, it wants a gate, not a second deletion.
