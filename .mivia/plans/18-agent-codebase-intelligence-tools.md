# 18 — Structured code intelligence tools

**Status:** Design-ready — **two open decisions (§5, §6)**. Supersedes the
2026-07-30 proposal draft, which was written without reading `60`.
**Date:** 2026-07-30
**Depends on:** nothing. **Blocks:** nothing. **Composes with:** `19` (both add
tools to the same session-built registry; §8 change #6 is shared).
**Blast radius:** MEDIUM — adds a new model-facing tool surface to every agent,
and (under A or B) the first `golang.org/x/tools` dependency in the module.
§3 and §5 are the load-bearing sections.

---

## 1. The gap

An agent auditing or implementing a change must first establish the shape of the
codebase. The only structural tools it has today are `grep` (`internal/tools/search.go:30`,
capped at 50 matches) and `glob` (`search.go:160`, capped at 200). Both return
*text locations*. Neither resolves a symbol.

The consequence is not the call count — it is that **textual search cannot answer
the questions that actually matter**, so the agent substitutes a chain of
approximations and then reasons over the result as if it were exact:

| Question | What `grep` can actually do | Failure mode |
|---|---|---|
| Who implements `storage.Store`? | Match method names, guess | Misses embedders; hits every unrelated `Append` |
| Who calls `ClaimRun`? | Match the name | Cannot distinguish declaration, call, comment, or a same-named method on a different type |
| Where is `ErrClaimHeld` checked? | Match the name | Cannot tell `storage.ErrClaimHeld` from `ledger.ErrClaimHeld` — the exact distinction that matters at `internal/ledger/storage_claims.go:25-27` |
| What symbols changed in `HEAD~1..HEAD`? | Nothing | Requires reading every hunk |

The third row is the load-bearing one. Two distinct sentinels share a name across
`internal/storage` and `internal/ledger`, and the translation between them is the
correctness-critical seam. A name-matching tool cannot see that seam at all.

> The gap is **symbol identity**, not search throughput.

## 2. Three corrections to the original proposal

**Correction 1 — there is no "orchestrator-side helper" layer.** The draft's §3
asserted these should be "orchestrator-side helpers, not subagent tools." No such
layer exists. Every executable capability in this codebase is a `tools.Tool` on
the one `runtime.Dispatcher` (`internal/runtime/dispatcher.go:19-25`); there is no
fourth kind and no non-dispatcher execution path. The only real tier distinction
is `tools.PrivilegedTool` (`internal/tools/tools.go:29-32`), which restricts a
tool to the root agent. **Decided out of scope:** these tools ship unprivileged
and available to every agent, sub-agents included. Restriction and configuration
are separate later phases.

**Correction 2 — rule `60` binds this plan, and the draft violated it.** The
draft's tool names, parameters, and example output were Go-only (`go doc`,
"Go symbol name", `*.go`). `.mivia/rules/60-tools-project-language-generic.md` is
a non-negotiable product rule: model-facing `Description()` and schema
`description` prose must be project- and language-generic, enforced by
`internal/tools/generic_surface_test.go:44-80`, which fails CI on exactly the
strings the draft used. mivia is a generic coding-agent host; a tool called
`search_references` whose description says "Go" is a defect, not a shortcut.

**Correction 3 — the performance claim was asserted, not measured; the real
number depends on a cache the draft never mentioned.** See §4.

## 3. Invariant to establish

> A code-intelligence tool returns **resolved symbols or nothing**. It never
> returns a name match that it could not resolve, and it never reports absence
> it did not verify.

Three corollaries, each of which rejects a plausible cheaper design:

- **No degrading to grep.** If the analyzer cannot run (no toolchain, unsupported
  language, unparseable workspace), the tool returns an explicit
  `analysis unavailable: <reason>`. It must not silently fall back to textual
  matching, because the caller's whole reason for using it is that grep's answer
  is untrustworthy.
- **Partial results are labelled partial.** Type-checking succeeds on code that
  does not build (§4), but the result set may be incomplete. Any response derived
  from a package with `Errors` carries `"complete": false` and the error count.
- **"Not found" and "not analyzed" are different answers.** Collapsing them is
  how a tool teaches an agent that dead code is safe to delete.

## 4. Measured feasibility

Benchmarked against this repo at HEAD (329 files, 68,766 LOC, 21 packages) with
Go 1.26 and `golang.org/x/tools` v0.47.0. These are measurements, not estimates.

| `packages.Load` mode | warm cache | with `Tests:true` |
|---|---|---|
| `NeedName\|NeedFiles` | 26 ms | 52 ms |
| `+NeedSyntax` (no types) | 64 ms | 93 ms |
| `+NeedTypes\|NeedTypesInfo`, **`NeedDeps` omitted** | **123 ms** | **244 ms** |
| `LoadAllSyntax` (`+NeedDeps`) | 1,395 ms | 1,575 ms |

**Omitting `NeedDeps` is the entire performance story** — same type information,
11x apart. Dependency types come from compiler export data instead of being
re-type-checked from source. All five target queries answered end-to-end in
**235 ms**.

Three findings that constrain the design:

**The tradeoff inverts on a cold build cache.** Export-data mode must compile
dependencies to produce `.a` files: **7,360 ms** cold versus 123 ms warm.
`LoadAllSyntax` is cache-independent at ~1,400 ms either way. So the honest claim
is *sub-second on a warm cache, ~7s on the first run after a clean*. `go build ./...`
is the warming step. The draft's flat "<1s, else too slow" budget would have
rejected the correct design on its first invocation.

**Type-directed queries work on code that does not compile.** Verified by
deliberately breaking a package: `go build` failed, but `Load` still returned
`Types.Complete() == true` and retained valid types for 5 of 6 declarations, with
the failure surfaced in `pkg.Errors`. This is what makes §3's "labelled partial"
corollary implementable rather than aspirational.

**Do not build a callgraph.** Measured on this repo: `cha` 229 ms / 908,321 edges,
`vta` 1,229 ms / 172,821 edges — CHA produces 5.3x the edges of VTA, and that
ratio is its false-positive rate. Spurious callers are worse than no answer under
§3. A direct `TypesInfo.Uses` scan answered "who calls F" exactly in **11.8 ms**.
Callgraph construction is rejected for the default path.

Rejected alternatives, briefly: **gopls as a library** is impossible (v0.23.0 has
only `main.go` at module root; everything is `internal/`). **gopls CLI** measured
0.62–1.40 s per invocation, is documented as not "efficient, complete, flexible,
or officially supported", and v0.23.0 removed three subcommands mid-flight;
`-remote=auto` daemon mode measured *slower* (4.05 s). **`guru`** is deleted.
**semgrep OSS** is single-file only (cross-file is a paid tier) so it structurally
cannot answer interface satisfaction. **ast-grep / tree-sitter** have no type
resolution — same ceiling as textual search.

## 5. Options — DECISION REQUIRED

### A. Go-only tools, with a rule `60` exception

Name and describe the tools in Go terms; amend rule `60` to carve out an
exception for language-analysis tools.

*For:* Simplest descriptions; no abstraction over a single implementation.
*Against:* Requires amending a rule explicitly marked non-negotiable, to benefit
the one workspace that is this repo. Every user running mivia on a TypeScript
project sees a permanently-failing Go tool in their surface. Rejects itself.

### B. Capability-named tools, one analyzer backend, generic prose

Tools are named and described by capability (`find_references`, `changed_symbols`).
Descriptions never name a language. A single internal `analyzer` interface has one
implementation (Go); workspaces it cannot handle get §3's explicit
`analysis unavailable`.

*For:* Satisfies `60` without weakening it. The generic surface is honest — the
capability genuinely is language-independent, only the backend is not. Adding a
second backend later needs no tool-surface change.
*Against:* One interface with one implementation is speculative structure, which
`engineering-working-contract` warns against. Mitigated by keeping the interface
to exactly the two methods the two tools need, and by *not* building a registry,
plugin system, or config surface for backends.

### C. Structured search only — no type information

Keep grep's engine; add structured output and role classification (declaration
vs. call vs. comment) from the AST alone. No `go/packages`, no new dependency.

*For:* No dependency, no toolchain requirement, works on any language with a
parser, 34 ms.
*Against:* Cannot answer interface satisfaction (needs method sets), cannot
resolve `x.Close()` (needs the type of `x`), and **cannot distinguish
`storage.ErrClaimHeld` from `ledger.ErrClaimHeld`** — §1's load-bearing case. It
is a better grep, not symbol identity. Fails §3.

**Recommended: B.** C fails the invariant that motivates the plan. A buys
simplicity by breaking a non-negotiable rule for a single-workspace benefit. B
pays one small structural cost — an interface with one implementation — to keep
the model-facing surface honest for the polyglot workspaces this product exists to
serve. The cost is bounded by refusing to generalise further than two methods.

**Open decision 5a — the dependency.** B (and A) add `golang.org/x/tools`, which
pulls `golang.org/x/mod` and `golang.org/x/sync`. Rule `30` says avoid new
third-party dependencies and prefer stdlib; this module's direct dependency set is
currently five packages. `x/tools` is Go-team maintained and versioned with the
toolchain, which is the strongest case any non-stdlib dependency can make — but it
is still a decision to take deliberately, not by implication. Vendoring a subset is
not viable; `go/packages` is not separable from its `gcexportdata` and `go/internal`
support. **If this dependency is refused, the plan collapses to C and should be
closed rather than half-built.**

## 6. Open decision: the tool surface (assuming B)

The draft proposed four tools. Two of them should not be built.

**Fold `trace_value` into `find_references`.** The draft's Tool A (implementations
and callers) and Tool B (where a sentinel is returned, translated, and checked)
are the same query: *references to a symbol, classified by role*. A sentinel is
just a symbol whose interesting roles are `returned` and `compared`. Two tools
here would be two schemas, two descriptions, and two rule-60 surfaces for one
capability.

**Do not build `find_untested_code`.** The draft's method — "grep function names
in `_test.go` files" — cannot support the claim in its own example output
(`PutContent: UNTESTED`). A name appearing in a test file does not mean the
behaviour is tested, and a name absent from one does not mean it is not — it may
be exercised through an interface, a table, or a helper. The tool would emit a
confident `UNTESTED` verdict that is wrong in both directions, which is precisely
the §3 failure mode. The same question is answerable, honestly, as
`find_references(symbol, roles=["caller"])` filtered by the workspace's test-file
convention — with the agent seeing the actual call sites and drawing its own
conclusion. **Recommend: cut it.**

**Open decision 6a:** whether `changed_symbols` ships in the first phase at all.
It is the cheapest of the four (34 ms, pure AST, no type information, no
`NeedDeps` question) and the only one that needs no analyzer backend — but it is
also the one whose absence is least painful, since `git diff` is already reachable
through `run_command`. Shipping it separately would let phase one be a single tool
with a single schema.

That leaves, for phase one:

```
find_references(symbol, roles?, limit?)
  → definition | implementations | callers | returns | comparisons
changed_symbols(base, head)          # pending 6a
  → per file: added / removed / modified top-level declarations
```

Both descriptions must pass `generic_surface_test.go`: no `go`, no `*.go`, no
module path, examples drawn from more than one ecosystem.

## 7. Security: loading packages runs the toolchain

`go/packages` invokes `go list` as a subprocess in the workspace. That has two
consequences the draft did not consider, and both are rule `10` surfaces:

- **Network.** Module resolution can fetch from the network. Rule `10` is
  deny-by-default for network access. The analyzer must run with `GOPROXY=off`
  and `GOFLAGS=-mod=readonly`, and surface an `analysis unavailable` when
  resolution fails, rather than downloading.
- **Execution in an untrusted workspace.** The tool causes a toolchain subprocess
  to run against workspace-controlled files — reached without passing
  `run_command`'s allowlist (`internal/tools/run.go:47`), which is the mechanism
  that otherwise governs process execution. This is a genuine widening of the
  execution surface and must be stated as such, with the environment scrubbed the
  same way `run_command` scrubs it.

Neither is a blocker. Both need a negative test (`secure-change` requires ≥1 per
new guard).

## 8. Changes (assuming B, phase one)

| # | File | Change |
|---|---|---|
| 1 | `internal/codeintel/analyzer.go` (new) | `Analyzer` interface — exactly two methods, `References` and `ChangedSymbols`. No registry, no plugin surface. |
| 2 | `internal/codeintel/goanalyzer.go` (new) | `go/packages` backend. `NeedName\|NeedFiles\|NeedSyntax\|NeedTypes\|NeedTypesInfo`, **no `NeedDeps`**, `Tests:true`. `GOPROXY=off`, `GOFLAGS=-mod=readonly` per §7. Dedup keyed on `(PkgPath, Name)` — see §9. |
| 3 | `internal/codeintel/roles.go` (new) | Classify each `TypesInfo.Uses` position as definition / caller / return / comparison / implementation. |
| 4 | `internal/tools/find_references.go` (new) | The tool. `Capability{Class: ExecutionRead, MaxResultBytes: …}`. Language-generic prose. |
| 5 | `internal/tools/default_registry.go:94-111` | Register it, honouring `DisableTools`. |
| 6 | `internal/tools/generic_surface_test.go` | **Shared with `19`.** Extend the guard to cover the session-built registry, not just `NewDefaultRegistry` — see §9. |
| 7 | `.mivia/invariants.md` + `Makefile:131` | Register the new invariant test names in both places. |
| 8 | `go.mod` | `golang.org/x/tools` (+ `x/mod`, `x/sync` indirect) — pending 5a. |

**New package, not a new file in `internal/tools`.** `.mivia/policy/go-structure.json`
caps production files at 500 soft / 800 hard lines; the analyzer does not belong in
the tools package regardless.

**No config surface in phase one.** Per the scope decision in §2, no
`ToolsConfig` field, no `DefaultOptions` entry beyond what registration needs.
`DisableTools` already provides the off switch.

## 9. Verification

```bash
go build ./... && go vet ./...
go test ./internal/codeintel/ ./internal/tools/ ./internal/cli/ -race -count=1
go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**Tests:**

- `TestFindReferencesDistinguishesSameNamedSentinels` — **the load-bearing one.**
  Must resolve `storage.ErrClaimHeld` and `ledger.ErrClaimHeld` as distinct
  symbols and must not report one's references under the other. This is the test
  that fails if anyone reimplements the tool on textual matching.
- `TestFindReferencesReportsPartialOnBrokenPackage` — fixture that does not
  compile; result carries `complete: false` and a non-zero error count, and still
  returns the resolvable references (§3, §4).
- `TestFindReferencesRefusesWithoutAnalyzer` — unsupported workspace returns
  `analysis unavailable`, **not** an empty result set and not a grep fallback.
- `TestAnalyzerDoesNotReachNetwork` — asserts `GOPROXY=off` in the subprocess
  environment (§7). Negative security test required by `secure-change`.
- `TestFindReferencesDedupsTestVariants` — with `Tests:true`, `go list` returns a
  package up to three times (`p`, `p [p.test]`, `p.test`) and the same symbol
  resolves to **distinct `types.Object` pointers**. Pointer-identity dedup
  silently double-counts. Keys on `(PkgPath, Name)`.
- `TestSessionToolSurfaceIsProjectAndLanguageGeneric` — see below.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| 1 | Resolve symbols by name string instead of `types.Object` | `TestFindReferencesDistinguishesSameNamedSentinels` |
| 2 | Return an empty result instead of `analysis unavailable` | `TestFindReferencesRefusesWithoutAnalyzer` |
| 3 | Drop `complete:false` when `pkg.Errors` is non-empty | `TestFindReferencesReportsPartialOnBrokenPackage` |
| 4 | Remove `GOPROXY=off` from the analyzer environment | `TestAnalyzerDoesNotReachNetwork` |
| 5 | Dedup on `types.Object` pointer identity | `TestFindReferencesDedupsTestVariants` |
| 6 | Put `go test ./...` in the tool description | `TestSessionToolSurfaceIsProjectAndLanguageGeneric` |

**The existing generic-surface guard does not cover this plan.** `generic_surface_test.go:44-80`
walks `NewDefaultRegistry(...)`. Tools registered later onto the session registry
are invisible to it — the same vacuous-check trap documented in `16` §5 for
`dispatch_tasks` / `spawn_agent`. Mutation proof #6 fails today for any
session-registered tool. Change #6 must land **with** the first tool, not after,
or rule `60` is unenforced for everything this plan and `19` add.

**Performance gate:** assert the warm-cache path stays under 1s on this repo, and
record the cold number rather than gating on it (§4).

**Docs:** `docs/product/agent.md` — describe the capability in multi-ecosystem
terms, and state plainly that structural analysis is available only where an
analyzer backend exists.

## 10. Rollback criterion

Kill this plan if any of these hold:

- **5a is refused.** Without `golang.org/x/tools` the only reachable design is C,
  which fails §3. Close the plan; do not ship a better grep under a name that
  promises symbol resolution.
- **The warm-cache path exceeds ~1s on a repo this size.** The agent will fall
  back to grep by habit, and an unused tool still costs context in every prompt.
- **`analysis unavailable` becomes the common case** for real workspaces. A tool
  that mostly refuses is worse than no tool — it consumes surface area and
  teaches the agent to distrust the result.
- **The first correctness bug is a false positive**, not a false negative. Under
  §3 a missed reference is a bug; an invented one is a reason to withdraw the
  tool, because it silently corrupts the reasoning it was built to support.

In all four cases the correct action is to delete the tool and the package, not
to add fallbacks. The value here is exactness; a code-intelligence tool that
hedges is a slower grep.
