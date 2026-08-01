# 18 - Structured code intelligence tools

**Status:** ✅ IMPLEMENTED 2026-07-31 - shipped, then bug-audited twice and
fixed both times. All decisions from the original plan confirmed; no
correction here required reopening §5's dependency decision or §6's
one-tool scope.
**Date:** 2026-07-30
**Depends on:** nothing. **Blocks:** nothing.
**Blast radius:** MEDIUM - adds one model-facing tool to every agent, and the
first `golang.org/x/tools` direct dependency in the module.
§3 and §7 are the load-bearing sections.

| Wave | Shipped in |
|---|---|
| Waves 1–4: dependency, analyzer, role classification, tool + registration (§10) | `af2ee4d` |
| First bug audit - `findImplementations` wiring + interface-type guard | `b2efa38` |
| First bug audit - `containsIdent` must walk subtrees, not just top-level | `ee43bc9` |
| First bug audit - `findImplementations`/tool-surface follow-ups | `a4e7806` |
| Second bug audit (3 parallel agents) - 6 confirmed findings, see below | `783a0c2`* |
| Perf follow-up - O(n²) truncation loop found while testing the byte-budget fix | `e4db387` |

\* `783a0c2`'s commit message is `test(cli): guard the chat viewport height
against layout drift` - a concurrent session's unrelated `internal/cli` work.
The two sessions shared one working tree; a plain `git commit` swept this
plan's staged codeintel/tools changes into that commit alongside its own. The
diff is real and verified (`git show 783a0c2 --stat` shows the six files
below), just filed under a commit message that describes something else.

**Second bug audit - 6 confirmed findings, all fixed (`783a0c2`):**

1. `sameObject` compared only `Pkg().Path()+Name()`, so a struct field, method,
   or local variable sharing a name with an unrelated package-level
   declaration was misreported as a reference to it. Fixed by restricting the
   comparison to package-scope objects (`isPackageScopeObject`).
2. Bare-name and ambiguous-qualifier symbol queries silently resolved to
   whichever same-named package-level symbol `packages.Load` iterated last -
   this repo alone has 5 top-level `New` funcs. Fixed: `loadPackages` now
   collects all candidates and returns an explicit ambiguity error instead of
   picking one.
3. `errors.Is`/`errors.As` sentinel checks classified as `RoleCaller` instead
   of `RoleComparison`, so `roles=["comparison"]` returned nothing for this
   plan's own §1 motivating example (`storage.ErrClaimHeld` is checked
   exclusively via `errors.Is` in this repo). Fixed in `roles.go`.
4. `Truncated` was set whenever the result hit the cap, even when that was
   the exact, complete total. Fixed: `addLoc` now only flags truncation when
   a genuine match is dropped beyond the cap.
5. `packages.Load` never received the caller's `ctx`, so cancellation/timeout
   had no effect during the load itself (up to ~7s cold per §4). Fixed by
   setting `packages.Config.Context`.
6. The tool's byte-budget enforcement did nothing on the error path and
   didn't converge on the success path once `Locations` was emptied. Fixed:
   both flow through one `marshalBudgeted` helper. Testing this exposed a
   7th issue - the helper's truncation loop was O(n²) (73s at 10,000
   locations); replaced with an O(log n) binary search over the kept-prefix
   length, fixed in `e4db387`.

All six findings had regression tests added (`internal/codeintel/analyzer_test.go`,
`internal/codeintel/bug_audit_test.go`, `internal/codeintel/roles_test.go`,
`internal/tools/find_references_test.go`). `go build`, `go vet`, and
`go test ./internal/codeintel/... ./internal/tools/... -race -count=1` are
clean.

---

## Corrections found during validation

Every empirical claim in this plan was independently re-verified against the
code. Most held. These did not. Where a citation drifted by a few lines it is
noted inline.

- **C1 - Line number drift does not invalidate any substantive claim, but
  several citations are stale.** `internal/tools/search.go:160` (glob) is now
  ~line 157 (Name) / 158 (Description) - 2-line drift. `search.go:30` (grep) is
  still accurate at line 30. `internal/runtime/dispatcher.go:19-25` points at the
  imports and Kind constants; the `Dispatcher` struct itself is at line 72. The
  conceptual claim (one dispatcher holds all Tool handlers) is true; only the
  line number is wrong. All other citations (`tools.go:29-32`, `default_registry.go:12-20`,
  `default_registry.go:94-111`, `generic_surface_test.go:44-80`, `generic_surface_test.go:49`)
  are accurate within 1–2 lines.

- **C2 - `golang.org/x/tools` is ALREADY a transitive dependency, so the
  "first dependency" framing overstates the cost.** `go mod graph` confirms
  `modernc.org/libc@v1.74.1 → golang.org/x/tools@v0.47.0`. The binary is
  unchanged when the packages are not imported (19.7 MB with or without the
  dependency in go.mod). Making it a direct dependency adds no download, no
  cache pressure, no cgo requirement, and no cross-compilation barrier. Only the
  sub-packages we import (`go/packages`, `go/types`, `go/ast`) get linked.

- **C3 - The environment-scrubbing mechanism for the `go/packages` subprocess
  is underspecified.** The plan says "on the same terms `run_command` uses", but
  `filterEnv` is a method on `*runCommandTool` (gated by that tool's allowlist
  configuration), not a standalone function. The `Analyzer` must either accept
  an env allowlist directly or import the tool config. This is implementable but
  the plan does not mention the config plumbing needed.

- **C4 - The session-tool surface test covers the new tool automatically, but
  for a different reason than stated.** The plan says "No guard extension is
  needed here. (`19` still needs one for its own tools.)" This is correct - the
  session test (`TestSessionToolSurfaceIsProjectAndLanguageGeneric`) creates a
  `NewDefaultRegistry` and iterates all tools, so a default-registry tool is
  covered. The parenthetical about `19` is misleading: 19's tools are session
  tools registered after the default registry, and `TestSessionToolSurfaceIsProjectAndLanguageGeneric`
  already covers them too. No correction is needed to the code - just to the
  reasoning.

- **C5 - Rule 60 tension is resolved: BUILD.** The tool CAN be described
  without naming Go. Name `find_references`, parameters `symbol`/`roles`/`limit`,
  roles as `definition`/`implementation`/`caller`/`return`/`comparison` - all
  language-generic. In non-Go workspaces, returns explicit `analysis unavailable`.
  One tool-description slot in every prompt is a real routing-accuracy cost;
  §11's rollback criterion acknowledges it. Decision stands.

- **C6 - The plan does not mention the `internal/cli` pre-existing build
  failure from concurrent work.** A sibling agent is working in `internal/cli`
  (sessions_dialog_test.go fails to compile with undefined `m.sessionsDlg` and
  `confirmDeleteOne`). This is not caused by the plan and must be excluded from
  verification gates or worked around. The plan's `go test ./internal/cli/` will
  fail for reasons unrelated to this change.

- **C7 - The second parameter in the plan's mutation proof table (mutation #7)
  is `go test ./...` in the description, but the test it names is
  `TestToolSurfaceIsProjectAndLanguageGeneric`. This test checks the DEFAULT
  REGISTRY tools; the session tool test is `TestSessionToolSurfaceIsProjectAndLanguageGeneric`.
  Since the tool registers in the default registry, both tests apply. No test
  change needed - both already scan all tools.

- **C8 - The warm-cache performance gate (§11) cannot be verified before
  implementation and is relaxed to a "record and report" requirement rather
  than a hard ≤1 s pass/fail.** The plan's own §4 reports ~1,400 ms for
  `LoadAllSyntax` (cache-independent), which is already above the 1 s gate. The
  plan's original warm-cache claim (<1 s) applies to a `TypesInfo.Uses` scan
  (11.8 ms) - which is the actual query, not the load time. The rollback
  criterion is adjusted to apply to the query path only (the 11.8 ms scan), not
  the one-time load cost. The first-invocation cold load is recorded rather than
  gated.
**Commits:** `af2ee4d` (feat: resolve symbol references with find_references
tool, includes the `golang.org/x/tools` dependency), `b2efa38`, `ee43bc9`,
`a4e7806` (first bug audit), `783a0c2` (second bug audit - see the findings
list in the status header above), `e4db387` (perf follow-up).

---

## 1. The gap

An agent auditing or implementing a change must first establish the shape of the
codebase. The only structural tools it has are `grep` (`internal/tools/search.go:30`,
capped at 50 matches) and `glob` (`search.go:160`, capped at 200). Both return
*text locations*. Neither resolves a symbol.

The consequence is not the call count - it is that **textual search cannot answer
the questions that matter**, so the agent substitutes approximations and then
reasons over the result as if it were exact:

| Question | What `grep` can do | Failure mode |
|---|---|---|
| Who implements `storage.Store`? | Match method names, guess | Misses embedders; hits every unrelated `Append` |
| Who calls `ClaimRun`? | Match the name | Cannot separate declaration, call, comment, or a same-named method on another type |
| Where is `ErrClaimHeld` checked? | Match the name | Cannot tell `storage.ErrClaimHeld` from `ledger.ErrClaimHeld` - the exact distinction that matters at `internal/ledger/storage_claims.go:25-27` |

The third row is load-bearing. Two distinct sentinels share a name across
`internal/storage` and `internal/ledger`, and the translation between them is the
correctness-critical seam. A name-matching tool cannot see that seam at all.

> The gap is **symbol identity**, not search throughput.

## 2. Three corrections to the original proposal

**Correction 1 - there is no "orchestrator-side helper" layer.** The draft's §3
asserted these should be "orchestrator-side helpers, not subagent tools." No such
layer exists: every executable capability is a `tools.Tool` on the one
`runtime.Dispatcher` (`internal/runtime/dispatcher.go:19-25`). The only real tier
distinction is `tools.PrivilegedTool` (`internal/tools/tools.go:29-32`), which
restricts a tool to the root agent. **Decided:** this tool ships unprivileged and
available to every agent, sub-agents included. Restriction and configuration are
later phases.

**Correction 2 - rule `60` binds this plan, and the draft violated it.** The
draft's names, parameters, and examples were Go-only (`go doc`, "Go symbol name",
`*.go`). `.mivia/rules/60-tools-project-language-generic.md` is non-negotiable:
model-facing `Description()` and schema prose must be project- and
language-generic, enforced by `internal/tools/generic_surface_test.go:44-80`,
which fails CI on exactly the strings the draft used. Note the rule's own table
permits Go-specific *host implementation* - so `internal/codeintel` may be
Go-specific; only the tool surface may not be.

**Correction 3 - the performance claim was asserted, not measured, and the real
number depends on a cache the draft never mentioned.** See §4.

## 3. Invariant to establish

> A code-intelligence tool returns **resolved symbols or nothing**. It never
> returns a name match it could not resolve, and never reports absence it did not
> verify.

Three corollaries, each rejecting a plausible cheaper design:

- **No degrading to grep.** If analysis cannot run (no toolchain, unsupported
  language, unparseable workspace), the tool returns an explicit
  `analysis unavailable: <reason>`. It must not fall back to textual matching -
  the caller's reason for using it is that grep's answer is untrustworthy.
- **Partial results are labelled partial.** Type-checking succeeds on code that
  does not build (§4), but the result may be incomplete. Any response derived
  from a package with `Errors` carries `"complete": false` and the error count.
- **"Not found" and "not analyzed" are different answers.** Collapsing them is
  how a tool teaches an agent that dead code is safe to delete.

## 4. Measured feasibility

Benchmarked against this repo at HEAD (329 files, 68,766 LOC, 21 packages) with
Go 1.26 and `golang.org/x/tools` v0.47.0. Measurements, not estimates.

| `packages.Load` mode | warm cache | with `Tests:true` |
|---|---|---|
| `NeedName\|NeedFiles` | 26 ms | 52 ms |
| `+NeedSyntax` (no types) | 64 ms | 93 ms |
| `+NeedTypes\|NeedTypesInfo`, **`NeedDeps` omitted** | **123 ms** | **244 ms** |
| `LoadAllSyntax` (`+NeedDeps`) | 1,395 ms | 1,575 ms |

**Omitting `NeedDeps` is the entire performance story** - same type information,
11x apart. Dependency types come from compiler export data instead of being
re-type-checked from source. All target queries answered end-to-end in 235 ms.

Three findings that constrain the design:

**The tradeoff inverts on a cold build cache.** Export-data mode must compile
dependencies to produce `.a` files: **7,360 ms** cold versus 123 ms warm.
`LoadAllSyntax` is cache-independent at ~1,400 ms either way. The honest claim is
*sub-second warm, ~7 s on the first run after a clean*. `go build ./...` is the
warming step. The draft's flat "<1s, else too slow" budget would have rejected the
correct design on its first invocation.

**Type-directed queries work on code that does not compile.** Verified by
deliberately breaking a package: `go build` failed, but `Load` still returned
`Types.Complete() == true` and retained valid types for 5 of 6 declarations, with
the failure surfaced in `pkg.Errors`. This makes §3's "labelled partial"
corollary implementable rather than aspirational.

**Do not build a callgraph.** Measured here: `cha` 229 ms / 908,321 edges, `vta`
1,229 ms / 172,821 edges - CHA produces 5.3x the edges of VTA, and that ratio is
its false-positive rate. Spurious callers are worse than no answer under §3. A
direct `TypesInfo.Uses` scan answered "who calls F" exactly in **11.8 ms**.
Callgraph construction is rejected.

Rejected alternatives: **gopls as a library** is impossible (v0.23.0 has only
`main.go` at module root; everything else is `internal/`). **gopls CLI** measured
0.62–1.40 s per invocation, is documented as not "efficient, complete, flexible,
or officially supported", and v0.23.0 removed three subcommands mid-flight;
`-remote=auto` measured *slower* (4.05 s). **`guru`** is deleted. **semgrep OSS**
is single-file only (cross-file is a paid tier) so it structurally cannot answer
interface satisfaction. **ast-grep / tree-sitter** have no type resolution.

## 5. Decision: capability-named tool, Go-only backend

Three options were considered.

### A. Go-only tool, with a rule `60` exception

*For:* Simplest description; no abstraction over one implementation.
*Against:* Requires amending a rule marked non-negotiable, to benefit the one
workspace that is this repo. Every user on a TypeScript project sees a
permanently-failing Go tool. Rejects itself.

### B. Capability-named tool, generic prose, single Go backend

Named and described by capability. Descriptions never name a language.
Unsupported workspaces get §3's explicit `analysis unavailable`.

*For:* Satisfies `60` without weakening it. The generic surface is honest - the
capability genuinely is language-independent, only the backend is not.
*Against:* Risks speculative structure if built as an interface with one
implementation.

### C. Structured search only - no type information

*For:* No dependency, no toolchain requirement, 34 ms, any language.
*Against:* Cannot answer interface satisfaction (needs method sets), cannot
resolve `x.Close()` (needs the type of `x`), and **cannot distinguish
`storage.ErrClaimHeld` from `ledger.ErrClaimHeld`** - §1's load-bearing case.
A better grep, not symbol identity. Fails §3.

**DECIDED: B.** C fails the invariant that motivates the plan. A buys simplicity
by breaking a non-negotiable rule for single-workspace benefit.

**B's stated cost is avoided, not paid.** The draft of this section proposed an
`Analyzer` interface with one implementation, which `engineering-working-contract`
rightly warns against. Not needed: `internal/codeintel` exports a **concrete**
`*Analyzer`, and the test seam goes at the *consumer* - `internal/tools` declares
a private one-method interface for fakes. Standard Go, no speculative structure,
and a second backend later needs no tool-surface change.

**DECIDED 5a - accept the dependency.** `golang.org/x/tools` pulls
`golang.org/x/mod` and `golang.org/x/sync`. Rule `30` says avoid new third-party
dependencies and prefer stdlib, and this module's direct set is currently five
packages. `x/tools` is Go-team maintained and versioned with the toolchain, which
is the strongest case a non-stdlib dependency can make; `go/packages` is not
separable from its `gcexportdata` support, so vendoring a subset is not viable.
The alternative is C, which fails §3. **This is the one decision here worth
re-litigating if the dependency budget is tighter than it appears** - if it is
refused, close the plan rather than half-build it (§11).

## 6. Decision: one tool in phase one

The draft proposed four tools. Two should never be built, and one is deferred.

**`trace_value` folds into `find_references`.** The draft's Tool A
(implementations, callers) and Tool B (where a sentinel is returned, translated,
checked) are the same query: *references to a symbol, classified by role*. A
sentinel is a symbol whose interesting roles are `return` and `comparison`. Two
tools would be two schemas, two descriptions, two rule-60 surfaces, one
capability.

**`find_untested_code` is cut.** Its method - "grep function names in `_test.go`"
- cannot support the claim in its own example output (`PutContent: UNTESTED`). A
name in a test file does not mean the behaviour is tested; a name absent from one
does not mean it is not - it may be exercised through an interface, a table, or a
helper. The tool would emit a confident verdict wrong in both directions, which is
exactly §3's failure mode. The question is answerable honestly as
`find_references(symbol, roles=["caller"])` filtered by the workspace's test-file
convention, with the agent seeing real call sites.

**DECIDED 6a - `changed_symbols` is deferred to a later phase.** It is the
cheapest of the four (34 ms, pure AST, no type information) but the least painful
absence, since `git diff` is already reachable through `run_command`. Phase one
ships one tool with one schema. Adding it later touches no phase-one code.

Phase one surface:

```
find_references(symbol, roles?, limit?)
  → definition | implementations | callers | returns | comparisons
```

## 7. Security: loading packages runs the toolchain

`go/packages` invokes `go list` as a subprocess in the workspace. Two consequences
the draft did not consider, both rule `10` surfaces:

- **Network.** Module resolution can fetch from the network; rule `10` is
  deny-by-default for network access. The analyzer runs with `GOPROXY=off` and
  `GOFLAGS=-mod=readonly`, and surfaces `analysis unavailable` when resolution
  fails rather than downloading.
- **Execution in an untrusted workspace.** The tool causes a toolchain subprocess
  to run against workspace-controlled files, reached **without** passing
  `run_command`'s allowlist (`internal/tools/run.go:47`) - the mechanism that
  otherwise governs process execution. This is a genuine widening of the execution
  surface and must be stated as such. The subprocess environment is scrubbed on
  the same terms `run_command` uses.

Neither is a blocker; both need a negative test (`secure-change` requires ≥1 per
new guard).

## 8. API surface

`internal/codeintel/codeintel.go` - types:

```go
// Role classifies how a source location uses a symbol.
type Role string

const (
	RoleDefinition     Role = "definition"
	RoleImplementation Role = "implementation"
	RoleCaller         Role = "caller"
	RoleReturn         Role = "return"
	RoleComparison     Role = "comparison"
)

// Location is one classified reference to a symbol.
type Location struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol"`
	Role   Role   `json:"role"`
}

// Result is the outcome of a reference query.
type Result struct {
	Symbol    string     `json:"symbol"`
	Locations []Location `json:"locations"`
	Complete  bool       `json:"complete"`
	Errors    int        `json:"errors,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// ErrUnavailable reports that analysis could not run at all.
var ErrUnavailable = errors.New("analysis unavailable")
```

`internal/codeintel/analyzer.go` - the concrete backend:

```go
// Analyzer resolves symbol references by type-checking a workspace.
type Analyzer struct{ root string }

// NewAnalyzer returns an Analyzer rooted at dir.
func NewAnalyzer(dir string) *Analyzer

// References returns classified references to symbol, capped at limit.
// It returns ErrUnavailable when the workspace cannot be analyzed.
func (a *Analyzer) References(ctx context.Context, symbol string, roles []Role, limit int) (Result, error)
```

`internal/tools/find_references.go` - the tool and its consumer-side seam:

```go
// referenceFinder is the analyzer capability this tool needs. Declared here so
// tests can substitute a fake without an interface in the analyzer package.
type referenceFinder interface {
	References(ctx context.Context, symbol string, roles []codeintel.Role, limit int) (codeintel.Result, error)
}

type findReferencesTool struct {
	finder   referenceFinder
	maxBytes int
	limit    int
}

type findReferencesArgs struct {
	Symbol string   `json:"symbol"`
	Roles  []string `json:"roles,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}
```

`Execute` returns marshalled `codeintel.Result`. `Capability` returns
`Capability{Class: ExecutionRead, MaxResultBytes: t.maxBytes}`.

## 9. Changes

| # | File | Change |
|---|---|---|
| 1 | `internal/codeintel/codeintel.go` (new) | `Role`, `Location`, `Result`, `ErrUnavailable` per §8. |
| 2 | `internal/codeintel/analyzer.go` (new) | `Analyzer`, `NewAnalyzer`, `References`. `packages.Load` with `NeedName\|NeedFiles\|NeedSyntax\|NeedTypes\|NeedTypesInfo`, **no `NeedDeps`**, `Tests:true`. `GOPROXY=off`, `GOFLAGS=-mod=readonly` (§7). Dedup on `(PkgPath, Name)` - see §10. |
| 3 | `internal/codeintel/roles.go` (new) | Classify each `TypesInfo.Uses`/`Defs` position into a `Role`. Implementations via `types.Implements` on both `T` and `*T`. |
| 4 | `internal/tools/find_references.go` (new) | The tool per §8. Language-generic prose. |
| 5 | `internal/tools/default_registry.go:94-111` | `register(&findReferencesTool{...})`, honouring `DisableTools`. |
| 6 | `go.mod` / `go.sum` | `golang.org/x/tools` (+ `x/mod`, `x/sync` indirect). |
| 7 | `.mivia/invariants.md` + `Makefile:131` | Register new invariant test names in both. |
| 8 | `docs/product/agent.md` | Document the capability in multi-ecosystem terms. |

**The tool registers in the default registry, not the session path.** It needs
only the workspace, which `DefaultOptions` already carries
(`internal/tools/default_registry.go:12-20`) - unlike `19`'s tools, which need the
ledger repository and therefore must register later. Consequence: this tool is
**automatically covered** by the existing rule-60 guard, since
`generic_surface_test.go:49` walks `NewDefaultRegistry`. No guard extension is
needed here. (`19` still needs one for its own tools.)

**New package, not a new file in `internal/tools`.**
`.mivia/policy/go-structure.json` caps production files at 500 soft / 800 hard
lines, and the analyzer does not belong in the tools package regardless.

**No config surface in phase one.** Per §2, no `ToolsConfig` field beyond what
registration needs. `DisableTools` already provides the off switch.

## 10. Implementation waves

Per `.mivia/rules/05` Step 1: one file per task, test task precedes each
production task, reviewer every 2–3 tasks.

**Wave 1 - dependency and types** (no behaviour)
1. Add `golang.org/x/tools` to `go.mod`; `go mod tidy`; confirm `go build ./...`.
2. `internal/codeintel/codeintel_test.go` - `Role` validity, `Result` JSON shape.
3. `internal/codeintel/codeintel.go` - change #1.
   *Reviewer checkpoint.*

**Wave 2 - the analyzer** (depends on Wave 1)
4. `internal/codeintel/analyzer_test.go` - load this repo; assert
   `TestAnalyzerDoesNotReachNetwork`, `TestAnalyzerReportsPartialOnBrokenPackage`,
   `TestAnalyzerUnavailableWithoutToolchain`.
5. `internal/codeintel/analyzer.go` - change #2.
   *Reviewer checkpoint.*

**Wave 3 - role classification** (depends on Wave 2)
6. `internal/codeintel/roles_test.go` - `TestReferencesDistinguishesSameNamedSentinels`,
   `TestReferencesFindsImplementations`, `TestReferencesDedupsTestVariants`.
7. `internal/codeintel/roles.go` - change #3.
   *Reviewer checkpoint.*

**Wave 4 - the tool** (depends on Wave 3)
8. `internal/tools/find_references_test.go` - fake `referenceFinder`;
   `TestFindReferencesRefusesWithoutAnalyzer`, schema validation, result cap.
9. `internal/tools/find_references.go` - change #4.
10. `internal/tools/default_registry.go` - change #5; confirm
    `TestToolSurfaceIsProjectAndLanguageGeneric` still passes.
    *Reviewer checkpoint.*

**Wave 5 - close out** (depends on Wave 4)
11. `.mivia/invariants.md` + `Makefile:131` - change #7.
12. `docs/product/agent.md` - change #8.

## 11. Verification

```bash
go build ./... && go vet ./...
go test ./internal/codeintel/ ./internal/tools/ -race -count=1
go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**Tests:**

- `TestReferencesDistinguishesSameNamedSentinels` - **the load-bearing one.**
  Must resolve `storage.ErrClaimHeld` and `ledger.ErrClaimHeld` as distinct
  symbols and must not report one's references under the other. Fails if anyone
  reimplements this on textual matching.
- `TestAnalyzerReportsPartialOnBrokenPackage` - fixture that does not compile;
  result carries `complete:false` and a non-zero error count, and still returns
  resolvable references (§3, §4).
- `TestFindReferencesRefusesWithoutAnalyzer` - unsupported workspace returns
  `analysis unavailable`, **not** an empty result and not a grep fallback.
- `TestAnalyzerDoesNotReachNetwork` - asserts `GOPROXY=off` in the subprocess
  environment (§7). Negative security test required by `secure-change`.
- `TestReferencesDedupsTestVariants` - with `Tests:true`, `go list` returns a
  package up to three times (`p`, `p [p.test]`, `p.test`) and the same symbol
  resolves to **distinct `types.Object` pointers**. Pointer-identity dedup
  silently double-counts; key on `(PkgPath, Name)`.
- `TestReferencesFindsImplementations` - `types.Implements` on both `T` and `*T`;
  must find `storage.Memory` and `storage.SQLite` for `storage.Store`.
- `TestToolSurfaceIsProjectAndLanguageGeneric` - existing test, must still pass.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| 1 | Resolve symbols by name string instead of `types.Object` | `TestReferencesDistinguishesSameNamedSentinels` |
| 2 | Return an empty result instead of `analysis unavailable` | `TestFindReferencesRefusesWithoutAnalyzer` |
| 3 | Drop `complete:false` when `pkg.Errors` is non-empty | `TestAnalyzerReportsPartialOnBrokenPackage` |
| 4 | Remove `GOPROXY=off` from the analyzer environment | `TestAnalyzerDoesNotReachNetwork` |
| 5 | Dedup on `types.Object` pointer identity | `TestReferencesDedupsTestVariants` |
| 6 | Test only `T`, not `*T`, in the implements check | `TestReferencesFindsImplementations` |
| 7 | Put `go test ./...` in the tool description | `TestToolSurfaceIsProjectAndLanguageGeneric` |

**Performance gate:** assert the warm-cache path stays under 1 s on this repo;
record the cold number rather than gating on it (§4).

**Docs:** `docs/product/agent.md` - describe the capability in multi-ecosystem
terms, and state plainly that structural analysis is available only where an
analyzer backend exists.

## 12. Plan scorecard

| Criterion | Verdict |
|---|---|
| Compiles (no import cycles) | PASS - `internal/codeintel` imports only stdlib + `x/tools`; `internal/tools` → `codeintel` is a new edge with no return path |
| No breaking API change | PASS - additive; one new `register(...)` line |
| Testable in isolation | PASS - `referenceFinder` seam at the consumer; analyzer tested against this repo |
| Backward-compatible config | PASS - no new config; `DisableTools` is the off switch |
| Every function has a test | PASS - Waves 1–4 pair each production task with a preceding test task |
| New dependency justified | **CONDITIONAL** - §5a; the one decision worth re-litigating |
| Rule `60` satisfied | PASS - default-registry registration means the existing guard covers it (§9) |

## 13. Rollback criterion

Kill this plan if:

- **§5a is reversed.** Without `golang.org/x/tools` the only reachable design is
  C, which fails §3. Close the plan; do not ship a better grep under a name that
  promises symbol resolution.
- **The warm-cache path exceeds ~1 s on a repo this size.** The agent will fall
  back to grep by habit, and an unused tool still costs context in every prompt.
- **`analysis unavailable` becomes the common case** for real workspaces. A tool
  that mostly refuses is worse than none - it consumes surface area and teaches
  the agent to distrust the result.
- **The first correctness bug is a false positive**, not a false negative. Under
  §3 a missed reference is a bug; an invented one is a reason to withdraw the
  tool, because it silently corrupts the reasoning it was built to support.

In all four cases the correct action is to delete the tool and the package, not to
add fallbacks. The value here is exactness; a code-intelligence tool that hedges
is a slower grep.
