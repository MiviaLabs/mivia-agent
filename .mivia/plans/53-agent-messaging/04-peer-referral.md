# 53.04 - Peer referral: parent-routed Ask between role agents

**Status:** DESIGN - ADLC Step 0 not run. **Conditional member:** build only
after `02`+`03` usage shows a concrete need; designed now so `01`'s envelope
never has to change.
**Date:** 2026-08-02
**Part of:** program `53` (`00-overview.md`).
**Depends on:** `01`, `02`, `03`.
**Blast radius:** MEDIUM - routing logic in the parent surface; no new child
capabilities beyond a widened `post_message`.

## 1. Goal

Support specialist panels - e.g. a reviewer on one model and an auditor on
another, defined as named agents in config (`internal/config/agents.go`,
per-agent `provider`/`model`/`tools`/`skills` already exist; note
`provider` is honored only from **trusted user-level** agent files
(`~/.mivia/agents/`), never workspace ones, and only together with
`model` - cross-provider panels therefore require user-level definitions) - where one
agent's output can be put in front of another agent **without free-form
chat**. The primitive is `Ask`: a typed, one-shot, parent-routed
question/answer between children.

Design stance from the program evidence: models are not trained for peer
negotiation, and A↔B dialogue anchors B on A's framing. So `Ask` is
**referral, not conversation** - the target sees the question (and any
finding refs) as *data in a fresh brief or framed message*, answers once,
and the exchange is over. Adversarial review ("refute this finding") is a
prompt pattern on top of `Ask`, not a protocol feature. Multi-turn debate,
if ever needed, composes from repeated `Ask`s and remains visible to the
router each hop.

## 2. Verified baseline

- Children hold no orchestration tools and cannot reach each other; the
  only shared substrate is the run ledger/blackboard (`02`) and the parent
  (`registerSessionTool` boundary, `internal/cli/dispatcher.go:364`).
- `02` gives children `post_message`; `03` gives the parent `send_to_task`
  and step-boundary delivery. `Ask` composes exactly these two seams.
- Role identity already exists on events and provenance
  (`agent.EventOrigin`, typed identity stamping in
  `internal/cli/agent_task_handler.go:180`).

## 3. Design

### 3.1 The Ask flow

1. Child A calls `post_message{kind:"ask", to_role:"auditor", body, refs}`.
   Persisted like any `01` message; A may block on the reply
   (`wait_seconds`, same machinery as `02` questions, same
   `max_pending_questions` pot) or continue and pick the answer up at a
   later step boundary.
2. **The router decides.** The router is parent-side policy code (not the
   parent LLM by default): it resolves `to_role` against the run's live
   tasks and configured agents, applies policy (§3.2), and either
   - delivers: a framed `ask` message to the target via `03` delivery
     (running target) or spawns the target agent with the ask as its brief
     (no live instance - the referral-as-spawn case, reusing
     `buildSpawnTasks`);
   - declines: A receives a structured `declined{reason}` result.
3. Target's reply: a `post_message{kind:"answer", in_reply_to}` routed back
   to A the same way. One answer per ask; further replies are refused.
4. Every hop is a ledger message; `run_messages` shows the full referral
   chain to the parent model and the user.

### 3.2 Router policy

Config, per run or per agent pair:

```toml
[subagents.messaging.routing]
mode = "policy"        # "policy" (rules below) | "parent" (parent LLM approves each ask)
max_asks_per_task = 4
max_referral_depth = 2 # A→B→C allowed, A→B→C→D refused
allow = ["reviewer->auditor", "auditor->reviewer"]  # empty = same-run any-role
```

`mode="parent"` surfaces each ask to the parent model as a pending item in
`inspect_agents`/`run_messages` for explicit approval - available where
oversight matters more than latency. Latency reality: the parent is a
pull-only turn-taker (00 §7.2), so a *blocking* ask under `mode="parent"`
waits for the parent to happen to poll and will usually hit
`wait_seconds`; parent-approved asks are effectively non-blocking-only.
Cycle guard: an ask chain carries its ancestor message IDs; a repeat
participant beyond `max_referral_depth` is declined. Enabled by default
like the rest of messaging (program decision 2026-08-02), bounded by
these quotas.

Because the router acts under the parent principal without a parent-LLM
decision in `mode="policy"`, referral-as-spawn is the one place a child's
action causes a spawn - a capability children are otherwise denied. Two
hard rules contain it: **(1) a blocking ask is never satisfied by
spawning** - if the target is not running, a blocking ask is declined with
`declined{reason:"target_not_running"}` and the child may re-ask
non-blocking; only non-blocking asks may trigger referral-as-spawn. This
also removes the worker-slot deadlock where a parked asker holds the slot
its target's spawn needs (02 §6.2). **(2) Referral-as-spawn requires an
explicit `allow` pair** - the empty-list "same-run any-role" default
applies only to live targets.

### 3.3 What panels look like in practice

The intended usage shape (documented, not enforced):

- Parent dispatches reviewer + auditor detached (`spawn_agent wait=none`)
  with briefs pointing at the blackboard.
- Reviewer posts `finding`s (`02`). Where the reviewer wants a second
  opinion, it `ask`s the auditor with the finding ref; the auditor answers
  after reading the ref via its own tools.
- Parent joins, reads the result envelopes plus `run_messages`, and
  synthesizes. Single-writer rule stands: panel agents stay read-only on
  the worktree; only the parent (or one designated writer task) edits files.

## 4. Invariants

- No child→child channel exists even now: delivery always transits the
  parent-side router and the ledger; a network trace of the process shows
  no new sockets, a code trace shows no new registry exposure.
- INV-AG-9/10, fingerprint pin, framing, and budget rules from `01`-`03`
  apply unchanged; asks spend the same quotas.
- Referral-as-spawn is bounded **explicitly, not by pool limits**:
  `max_fanout` is enforced per orchestration batch (`Pool.validate`,
  `internal/subagents/subagents.go:121-124`) and a referral spawn is a
  batch of one; spawn depth is `caller.Depth + 1` from the parent-side
  router (`buildSpawnTasks`,
  `internal/cli/orchestrate_spawn_tasks.go:70`), so referral spawns are
  siblings and coordinator `MaxDepth` never constrains ask chains.
  Therefore: referral spawns count against a new per-run cumulative cap
  `max_referral_spawns_per_run` (default 4); the ask-chain ancestor list
  is the depth authority; and the §3.2 rules (non-blocking only, explicit
  allow pair) apply. (Config note: actual pool defaults are 32/10/1000
  with `-1` = unlimited, `subagents.New`; the `mivia.toml.example` comment
  claiming "0 = unlimited" is wrong - fix tracked outside this plan.)

## 5. Verification

- Unit: routing table (allow list, depth, cycles, quotas, no-such-role);
  decline paths; one-answer enforcement.
- Integration: reviewer/auditor fixture pair end-to-end incl.
  referral-as-spawn; `mode="parent"` approval flow; chain visibility in
  `run_messages`.
- Adversarial: hostile ask bodies through framing; role spoof attempt
  (child claiming `from` role) blocked by server-side stamping.

## 6. Open decisions

1. **Does referral-as-spawn belong in v1 of this member,** or is
   live-target-only enough? Position: include it - panels are usually
   dispatched detached, and "auditor not running yet" will be the common
   case; it reuses existing spawn machinery.
2. Whether `mode="parent"` is worth building before someone asks. Position:
   design the seam (router is an interface), implement `policy` only,
   leave `parent` as a follow-up flagged in the config as unimplemented.
3. Cross-run asks (agent in run X asking a role in run Y): out of scope,
   likely forever - runs are the isolation boundary.
