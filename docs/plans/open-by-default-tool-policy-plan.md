# Open-by-Default Tool Policy Plan

**Status:** Proposed
**Author:** mivia / MiviaLabs
**Date:** August 2026
**Topic:** Flip `run_command`'s shipped default from closed (nothing runs)
to open (common dev tools run out of the box), restrictable via config.

---

## 1. Context

Today, `[tools] run_allowlist` has no compiled-in default
(`internal/tools/default_registry.go:174-183` builds the effective
allowlist only from `run_allowlist`/`run_allowlist_only` in config; nothing
is unioned in from the binary). Verified empirically: a first-time user with
the new auto-bootstrapped config (`docs/plans/first-run-onboarding-plan.md`)
gets a working chat session that can read, write, and edit files, but
`run_command` refuses every program — `git status`, `make build`, `npm test`,
anything. `.mivia/mivia.toml.example` documents this as deliberate:

> Nothing is compiled into the binary: with this unset, run_command
> executes no program at all.

This is a closed-by-default, opt-in-to-open security posture. The product
owner's judgment is that this reads as broken rather than safe: a user who
installs `mivia`, asks it to run tests, and gets silently refused will
conclude the tool does not work, not that it is being careful. The direction
is the opposite policy: open by default, users who want restriction set it
themselves in config.

This mirrors the write-path-blocklist change landing alongside this plan
(`internal/config/defaults.go`'s `DefaultWritePathBlocklist` moving from
`[".git", ".mivia/mivia.toml"]` to empty) — both move the same direction,
from closed-with-opt-out to open-with-opt-in.

## 2. Proposed design

Add a compiled-in `BuiltinRunAllowlist` in `internal/tools` (or
`internal/config`, wherever `configuredRunAllowlist`
(`internal/tools/default_registry.go:174`) can reach it), containing the
same multi-ecosystem program list already hand-maintained in
`.mivia/mivia.toml.example`'s `run_allowlist` (`git`, `make`, `go`, `node`,
`npm`, `python`, `cargo`, common Unix utilities, etc. — roughly 140 entries
across languages/ecosystems, see the example file's `[tools] run_allowlist`
block for the current list).

`configuredRunAllowlist` changes from "only what config declares" to "union
of the built-in list with config's `run_allowlist`, unless
`run_allowlist_only` is set" — matching the doc comment that already exists
on `ToolsConfig.RunAllowlist` today ("extends the built-in default
allowlist (union)") but which is currently untrue (there is nothing to
union with). `run_allowlist_only` keeps working exactly as today: it
replaces the effective list entirely, built-in or not — this is already the
mechanism for a project that wants a closed allowlist, so restriction stays
possible without any new config key. `run_blocklist` still subtracts, so a
project can keep the open baseline and remove a handful of programs (e.g.
`rm`, `sudo` if present) without going to a full allowlist.

## 3. Open questions to resolve before implementing

1. **Exact list.** Reuse `.mivia/mivia.toml.example`'s `run_allowlist`
   verbatim, or trim it? That file's list includes some higher-risk entries
   (`ssh`, `scp`, `curl`, `wget`, `docker`, `kubectl`, `terraform`) alongside
   ordinary dev tools. Shipping all of them by default is a bigger exposure
   than shipping just compilers/test runners/`git`. Needs an explicit
   decision, not a silent copy.
2. **Interactive chat vs. workflow steps.** Workflow evidence-gate/verifier
   commands already run inside a `bwrap` sandbox (isolated filesystem +
   network namespace, `.mivia/mivia.toml.example` `[harness]` section) — a
   broad default there is lower-risk than for `mivia chat`'s `run_command`,
   which executes directly against the real host. The two surfaces may
   warrant different defaults rather than one shared list.
3. **Interaction with approval policy.** With approval `default_mode`
   unset resolving to `auto` (accept-always, confirmed in
   `internal/config/approvals_config.go:96-108` and reinforced by this same
   change's explicit `[approvals] default_mode = "always"` in the
   bootstrapped config), an open `run_allowlist` plus auto-approve means a
   first-run agent can execute any built-in-listed program with zero
   confirmation. That combination is the whole point (no friction), but it
   is worth stating plainly rather than arriving at it as a side effect of
   two separate changes landing close together.
4. **Existing pinned tests.** `internal/tools/default_registry_test.go` and
   related config tests likely assert today's "nothing runs unconfigured"
   behavior and will need rewriting, not just extending.

## 4. Recommendation

Ship a deliberately small, reviewed built-in list — compilers/interpreters,
their package managers, `git`, and safe read-only Unix utilities — rather
than the full example file's list verbatim. Leave networking tools (`ssh`,
`scp`, `curl`, `wget`), container/infra tools (`docker`, `kubectl`,
`terraform`, `helm`), and destructive-by-nature utilities (`rm` is
debatable; it is already common in the example list) for a project to add
explicitly via `run_allowlist`. This keeps "clone a repo and start coding"
working out of the box while keeping the higher-blast-radius programs an
explicit opt-in, consistent with the spirit of the request (fix the "why
isn't this working" problem) without maximizing exposure in one move.

This plan does not implement the change; it records the design and open
questions for review before an implementation pass.
