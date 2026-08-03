# 51.09 - STOPPED: supersede stale tool results

**Status:** **STOPPED - DO NOT BUILD.** ADLC Step 0 ran 2026-08-03 and hit
this plan's own declared stop condition. §6.1 said "resolve the ResourceKey
question first; if keys are window-insensitive, stop and redesign the key."
They are, and worse than feared.
**Date:** 2026-08-02, stopped 2026-08-03
**Part of:** program `51` (`00-overview.md`).

## 0. Step 0 disposition

Panel verdict: **DO-NOT-BUILD.** Three independent BLOCKs, any one fatal.

### 0.1 The stop condition fired

`pathCapabilityKey` (`internal/tools/tools.go:483`) unmarshals **only**
`{"path": string}` and returns `"path:" + abs`. Independently verified.

- `read_file`'s `offset`/`limit` are not in the key (`read.go:44-52`), so
  two **disjoint windows** of one file collide. Superseding would destroy
  context the model still needs - exactly the risk §6.1 named.
- `grep`'s **`pattern` is not in the key either** (`search.go:55`), which
  §6.1 did not anticipate. A search for `bar` would supersede an earlier
  search for `foo` over the same tree: same class, same key, unrelated
  content.
- A path-less `grep`/`glob`/`list_dir` falls through to the **constant**
  `"workspace:read"` (`tools.go:487`), collapsing every path-less call in a
  session onto one key.

### 0.2 Findings

| Finding | Severity | Disposition |
|---------|----------|-------------|
| ResourceKey is path-only; disjoint windows and unrelated greps collide; path-less calls collapse to a constant | BLOCK | **Accepted.** Stop condition met. |
| **The design has no seam.** The planner cannot obtain a resource key: `PlanInput` carries no workspace root (`contextmgr/contracts.go:15-31`) and `pathCapabilityKey` needs `ws.Resolve`, which performs `EvalSymlinks` (`internal/workspace/root.go:58`). Re-deriving in-planner breaks `Plan`'s purity and makes the key disk-state-dependent | BLOCK | **Accepted.** A stamped-at-insert key would be needed - a different plan. |
| **The benefit is already collected.** Shipped `49` elides *every* non-mandatory prior tool body over 2048 B regardless of resource identity (`planner_elision.go:98-124`, floor at `:60`). Residual benefit is bodies ≤2 KiB | BLOCK | **Accepted.** The cost argument is spent. MEDIUM blast radius for a trivial saving. |
| INV-CE-09-A ("always exactly one live copy") is **already false at HEAD**: only the latest tool unit is protected (`planner_elision.go:73-83`), so a resource whose newest result sits outside that unit and exceeds 2 KiB has its only copy elided today | BLOCK | **Accepted.** The invariant describes a property the system does not have. |
| No evaluation harness exists (`Makefile` has test/race/vet/invariants/coverage only), so §6.4's quality claim - the plan's actual justification - cannot be measured | BLOCK | **Accepted.** §6.4 called this out itself: "a cost optimisation wearing a correctness argument". With the cost gone, only the unmeasurable argument remains. |
| INV-CE-09-D ("never inspects content") holds only in weakened form: class and raw args are structurally available, but deriving a key needs JSON parsing and path canonicalisation | MEDIUM | Moot given the above. |
| Interaction with the seen-ledger: a substituted body is a short `ref:` line; `49`'s 2 KiB floor spares it, but `09` has no size floor and would overwrite the ref with a reference-free notice | MEDIUM | **Accepted.** Recorded as a constraint on plan `10` §5. |
| Baseline errors: `markLatestToolUnit` moved to `planner.go:216`, mandatory-set building moved to `planner_elision.go:73`; `search.go:49` should be `:55`; "`49` already **specifies**" is stale - `49` shipped, and its notice carries **no** recoverable reference | Confirmed | Corrected in the overview's re-read baseline. |

### 0.3 Locked conclusion

The plan rests on a key that does not identify what it claims to identify,
needs a seam that does not exist, targets a saving another shipped plan
already takes, and asserts an invariant the system already violates. Its
one remaining justification is a quality claim this repo cannot measure.

Stop. The prior art that motivated it (arXiv 2606.10209 - superseded tool
state degrades task success, not just cost) is still credible; it is the
*implementation path* that is unavailable, and re-opening requires an
evaluation harness first.

## 1. Salvage

Two items, neither of which is this plan, both independently worthwhile.

| # | Item | Why it stands alone |
|---|------|---------------------|
| S1 | Make `Capability.ResourceKey` span- and query-sensitive (`tools.go:483`) | It is a **live correctness/performance defect**: the key feeds the concurrency scheduler (`internal/agent/loop_tools.go:408`), so disjoint reads and unrelated path-less greps are falsely serialised today. Overview §8.1. Fixing it is worth doing whether or not superseding is ever revisited - and it is a precondition if it is. |
| S2 | Give `49`'s elision notice a recoverable `contentref` handle (`planner_elision.go:127-131`) | INV-CE-C is violated by shipped code. Overview §8.2. Also the hard blocker under plan `10`. |

## 2. Reopening criteria

Revisit only when **all three** hold:

1. S1 has landed, so a resource key identifies a resource.
2. An evaluation harness exists that can measure task-level quality, not
   just token count.
3. A measurement shows residual superseded-state cost **below** `49`'s
   2 KiB elision floor is material. If the mass is above the floor, `49`
   already has it and there is nothing left to win.
