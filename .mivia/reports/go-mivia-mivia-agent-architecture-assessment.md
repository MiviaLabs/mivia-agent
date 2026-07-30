# Mivia harness and go-mivia platform assessment

Date: 2026-07-31

## Decision

Do not make an immediate binary choice between "replace Crush in go-mivia" and
"discard go-mivia". Since go-mivia is pre-launch, the preferred direction is a
small, new platform slice whose sole agent runtime is mivia. Reuse proven,
well-bounded go-mivia components only behind explicit contracts. Do not embed
the current mivia CLI, and do not carry the current go-mivia macro-workflow
core into the first release.

The target is not another general orchestration framework. It is a single
governed delivery loop:

```
authenticated request -> admitted work item -> isolated workspace
  -> mivia stage executor -> platform verification -> human approval -> PR/evidence
```

The new control plane owns identity, tenancy, durable run state, workspace
isolation, retries, verification, GitOps, and delivery approval. Mivia owns
only task-local agent execution, including an optional bounded micro-DAG.

## The challenge question

What is the smallest product that can reliably take an authenticated customer
request to a reviewable change while preserving a durable audit trail and an
operator kill switch?

If a proposed component does not directly answer that question for the first
customer workflow, defer it. In particular, free-form web chat, multiple
harnesses, a generic workflow marketplace, code graph/knowledge services,
training-data preparation, and autonomous merge/delivery are not first-slice
requirements.

## Options considered

| Option | Advantages | Main cost / failure mode | Verdict |
| --- | --- | --- | --- |
| 1. Add `mivia` beside Codex and Crush in current go-mivia | Fastest apparent integration; retains existing Temporal and GitOps work | Adds a third special case through closed allowlists and an already oversized workflow core; still inherits pre-launch security and release-baseline problems | Do not start here |
| 2. Replace Crush with mivia in current go-mivia | Own the harness and remove external dependency | Current `mivia` CLI is presentation-oriented and mivia has no public embedding API; a direct replacement creates a second orchestrator and loses typed lifecycle semantics | Viable only after extracting an engine API; not the first product decision |
| 3. Extract a `mivia-agent/engine` package, then add it as a go-mivia runner adapter | Preserves mature platform guards and enables a gradual migration | Requires careful authority split and still couples delivery to the large v2 workflow core | Good migration path if go-mivia components prove valuable |
| 4. Build a thin new platform around mivia, selectively reusing go-mivia packages/contracts | Least accidental complexity; forces a real product boundary; avoids inheriting unfinished architecture | Requires intentional selection of the minimum durable and security foundations | **Recommended** |
| 5. Ship mivia CLI alone first | Fastest route to user learning and validates the harness | Does not supply hosted tenancy, work queues, team approval, durable delivery evidence, or safe remote execution | Good companion/local product, not a hosted platform replacement |
| 6. Make mivia itself the full cloud control plane | One codebase in theory | Rebuilds tenancy, durable orchestration, isolation, GitOps, scheduling, and operator surfaces inside a runtime designed as a local CLI | Reject for now |

## Why a thin new platform is credible

Go-mivia already demonstrates useful platform concepts: immutable stage inputs,
dependency-aware workflow execution, workspace fingerprinting, post-run path
enforcement, and structured runner results. For example, an agent stage binds a
workspace invocation, selects a harness, and applies read-only workspace
fingerprinting before and after execution in
`../go-mivia/internal/v2/runner/activity/agent_stage.go:232-300`; write stages
independently list changed files and enforce path scope at lines 303-334.

But the current platform contains a very broad product surface. The workflow
package includes large final-delivery and final-review files, and the repository
itself records an unfinished decomposition plan in
`../go-mivia/docs/plans/orchestrator-workflow-decomposition.md`. This is not a
reason to discard its ideas. It is a reason not to make it the minimum viable
architecture merely because it exists.

The v2 security model is explicitly local/pre-authentication. Chat resolves the
organization from a caller-provided query parameter
(`../go-mivia/internal/v2/chat/httpapi/handler.go:100-118`), and its lifecycle
handler states that authentication does not yet exist
(`../go-mivia/internal/v2/chat/httpapi/resume_handler.go:424-441`). The dashboard
session is a hard-coded local operator
(`../go-mivia/internal/v2/dashboard/apphttp/session.go:8-45`). These are valid
pre-launch/local constraints, not evidence that the product can safely be
promoted to a hosted multi-tenant foundation unchanged.

## Required mivia extraction

An interface is the right direction, but it must be a new public,
dependency-inverted engine API. `go-mivia` cannot import mivia's current
`internal/...` packages from a sibling module, and `mivia` currently exposes
only CLI commands (`chat`, `config`, `doctor`, `version`) in
`internal/cli/root.go:12-65`.

The boundary should resemble this, with the platform adapting its own sealed
task record to the request:

```go
type StageExecutor interface {
    Execute(context.Context, ExecutionRequest, EventSink) (ExecutionResult, error)
}
```

`ExecutionRequest` must contain a protocol version, immutable attempt and
idempotency IDs, an already-created workspace grant, fixed read/write scope,
tool and environment grants, time/output/budget limits, and task criteria.
`ExecutionResult` must contain only a terminal category, bounded usage,
validated child summaries, and safe artifact references. It must not carry
raw prompts, model transcripts, credentials, or delivery authority.

Mivia's current local defaults support a bounded in-process pool (four workers,
depth three, fanout 16) in `internal/config/defaults.go:13-26`. That is useful
inside one admitted stage, but it is not the platform scheduler. Concurrent
write-capable child tasks against one workspace are unsafe: the first write
slice must either use one worker or allocate one platform-owned worktree per
child and merge through a platform verifier.

## Non-negotiable authority split

| Concern | Owner |
| --- | --- |
| User authentication, organization/project membership and authorization | New platform |
| Admission, policy snapshot, parent DAG, retry budget, cancellation and durable run state | New platform |
| Workspace creation, container/OS isolation, credential injection and egress policy | New platform |
| Task-local reasoning, tool calls and bounded child-task execution | Mivia engine |
| Diff inventory, scope enforcement, verification, review approval, PR/push | New platform |
| Mivia's local ledger/session data | Ephemeral attempt detail only; never delivery authority |

In-process embedding is not an OS security boundary. The mivia engine must run
inside a worker process/container that has no delivery credentials, cannot
broaden its workspace or tool grants, and is cancelled and joined before the
parent attempt returns. The platform independently inventories the resulting
Git state; it never trusts a self-reported changed-file list.

## First product slice

Build only the following, in this order:

1. Authenticated single-tenant or single-organization deployment with explicit
   role checks. Do not use request-supplied organization keys as identity.
2. Create/list/cancel one durable work item with a single admitted repository
   and immutable base SHA.
3. A worker obtains a fresh isolated worktree and invokes mivia through the
   public engine API for a **read-only research or review** stage.
4. Persist schema-validated heartbeats and safe evidence references; stream
   them to the operator UI without storing raw prompt/model output by default.
5. Add one serial workspace-write implementation stage, then independently run
   diff/path scope checks and the project's verifier.
6. Require human approval before creating a draft PR. Autonomous merge, broad
   retries, and multi-stage micro-DAG writes remain out of scope.

The read-only stage first is deliberate: it validates engine integration,
cancellation, events, retention, and tenant ownership without granting an
unproven embedded runtime write access to customer code.

## Questions that must be answered before implementation

1. Who is the first paying/operator user: one internal engineering team, a
   managed service, or external organizations? This determines whether
   single-tenant deployment is an acceptable first release.
2. Is the first job research/review, a narrowly scoped code change, or
   end-to-end PR delivery? Do not support all three in v1.
3. What credentials may the worker receive, and which actions remain outside
   the agent process? A default should be no Git provider token in the agent
   worker.
4. What evidence must be retained, for how long, and who may read it? Raw
   prompts and transcripts should be opt-in, encrypted, and separately
   authorized.
5. What constitutes a successful change: test pass, reviewer approval, draft
   PR creation, or merge? Encode only that terminal condition in v1.

## Migration rule

Do not copy current go-mivia packages wholesale. Extract or reproduce one
tested capability at a time behind a new product-owned contract: identity and
tenant authorization first, then work-item storage, workspace isolation, mivia
adapter, verification, and draft-PR delivery. Preserve the useful runner
postconditions, not the platform's present package topology.

## Evidence and verification

ReportFormat: mivia-report/v1
Skill: architecture-review
Result: PARTIAL
Scope: mivia-agent and ../go-mivia runner, orchestration, chat, dashboard, and security boundaries
Summary: A thin new platform around a public mivia engine is preferable to a direct current-platform harness swap, subject to product and tenancy decisions.
Evidence:
- `./scripts/check-v2-boundaries.sh` in ../go-mivia: PASS
- `go test ./internal/v2/runner ./internal/v2/agentinvocation/... ./internal/v2/orchestrator/harnessselect ./internal/v2/executioncontext/...` in ../go-mivia: PASS
- `go test ./internal/v2/orchestrator/decomposition ./internal/v2/orchestrator/harnessselect` in ../go-mivia: PASS (independent review execution)
- `go test ./internal/v2/orchestrator/chain/workflow` in ../go-mivia: FAIL (independent review execution reported deterministic completed-versus-blocked failures; independently reproduce before treating it as baseline)
Findings:
- Current mivia lacks a public machine/engine API and cannot be directly imported from go-mivia.
- Current go-mivia v2 is not ready to serve as a hosted multi-tenant baseline because request authentication and tenant authorization are not implemented.
- Current go-mivia runner guards are worth preserving, but the workflow core is too broad to be the default first-product foundation.
ResidualRisk: The first user, tenancy model, credential model, evidence-retention policy, and exact terminal delivery condition are product decisions not inferable from source.
NextAction: Choose the first customer workflow and tenancy model, then design the versioned mivia engine contract and thin-platform boundary before implementation.

## Review coverage note

The requested ten concurrent agents could not be allocated: this environment
permits four total active agents, including the coordinator. Three independent
read-only reviews (runner, orchestration, and web/security) plus a second
runner-interface pass were completed; the coordinator performed the direct
boundary and selected-package verification above. No production files were
modified.
