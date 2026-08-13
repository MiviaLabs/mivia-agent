# Recurring Defect Taxonomy

This document lists the defect classes that this repository produces again and again.
Each class comes from the `fix(...)` commit history, not from theory.

Source: 405 `fix` commits out of 1103 total commits (2026-07-27 to 2026-08-08).
More than one in three commits in this repository is a fix. The same classes
return in chains: 35 fixes for workflow resume and claim handoff, 45 for history
retention, 43 for bounds and budgets, 26 for workflow delivery.

Use this document as a probe list, not as a severity table. The severity rules stay
in `bug-audit` and `secure-change`.

## How to use it

- **Step 0 (plan)**: read the classes that touch the planned surface. Record in the
  plan which class each design decision closes.
- **Step 5 (bug audit)**: run every probe for the classes the diff touches.
- **Pre-merge verification**: `verify-change` requires a probe result for each class
  in scope.
- **After a fix**: run the sweep in "Chain control" below. One fix per class, not one
  fix per report.

Each class has an identifier `DC-n`. Cite the identifier in findings and in plans.

---

## DC-1 Terminal state with no return edge

**Mechanism.** A state machine marks a state terminal. A recoverable condition then
routes to that state. The run can never recover, because no edge leaves the state.

**Evidence.** `b977729` made `delivery_failed` terminal, so any commit to `master`
during a run produced a permanent refusal. `31e857f` accepted success edges from
reserved terminals. `3fcaa7c` settled pre-flight refusals into a terminal state.

**Probes.**
- List every terminal state. For each one, name the conditions that reach it.
- For each condition, decide: is it permanent, or is it transient?
- A transient condition must not reach a terminal state, or the terminal state must
  have an explicit re-entry edge with a single enforcing compare-and-set.
- Check that the terminal invariant test still holds after you add a re-entry edge.

## DC-2 Claim, lease, and fence for distributed exclusion

**Mechanism.** Two coordinators, engines, or hosts act on the same run. The code uses
a boolean claim flag instead of a fenced lease. A stale owner keeps writing after
another owner takes over.

**Evidence.** 74 commits. `5474470` fenced claims across coordinators, engines, and
delivery. `a32c270` stopped a stale DAG executor from dispatching after claim theft.
`f8ec525` admitted same-key workflows once. `7e7f5c6` reconciled concurrent admission.
`b977729` replaced force-release with a lease-based takeover to stop cross-host double
publish.

**Probes.**
- Name the owner of each mutable run record. Name the mechanism that proves ownership.
- A claim without a monotonic fence token is a defect. A stale owner must fail its
  next write, not win it.
- Test the takeover path: owner A stalls, owner B takes over, owner A resumes and
  writes. Owner A must be refused.
- Test the admission path: two callers with the same key start at the same time.
  Exactly one run must exist.
- Release the claim on every failure path, including the pre-flight refusal path.

## DC-3 Compare-and-set against a stale version

**Mechanism.** Code reads a version, does work, then writes with the version it read
or with a hardcoded constant. A concurrent writer bumps the version. The set fails
silently, or a later set overwrites the concurrent write.

**Evidence.** `7418278`: `executeRun` used a hardcoded version 1. A racing `Cancel`
bumped the version, the set failed silently, and the task stayed queued while the pool
executed it. The post-pool set then overwrote `cancel_requested` with `completed`.

**Probes.**
- Find every compare-and-set. Confirm the version comes from a live read, never a
  constant.
- Confirm the code checks the result of the set. A failed set must not fall through
  into the success path.
- Confirm a failed set does not leave the caller executing work the state no longer
  authorizes.

## DC-4 Crash, resume, and replay

**Mechanism.** The process stops between two durable writes. On restart, the recovery
path reconstructs state from an incomplete record. In-flight work is lost, duplicated,
or misreported.

**Evidence.** 99 commits. `47dad1a` recovered interrupted runs. `2f11c6f` preserved
in-flight attempt status in the resume plan. `80e0cc2` refused resume after worktree
loss. `011f9a6` centralized resume recovery after five separate partial fixes.

**Probes.**
- For each durable write sequence, name the state after a crash between each pair of
  writes. Confirm the recovery path handles every one.
- Resume must be idempotent. Run resume twice and compare the result.
- Resume must restore work, never authority. Check that the resumed principal comes
  from the live session, not from the stored record.
- A resource the run depends on can disappear while the run is stopped. Resume must
  refuse, not proceed on a missing resource.

## DC-5 Zero value means unlimited, guard reads it as zero

**Mechanism.** A bound uses `0` for "no limit". A guard of the form
`len(x) >= max` then treats `0` as "already at the limit" and returns nothing.
The same class covers a config key placed at the wrong table level, which the
parser drops without an error.

**Evidence.** `9475bee`, `b8e2a20`, and `ac01ccc` closed the zero-means-unlimited
contract across tool defaults. A separate fix found `max_steps=0` at TOML top level
instead of under `[chat]`, silently dropped, plus three layers that each replaced a
zero value with a hardcoded default.

**Probes.**
- For every numeric bound, state what `0` means. Test that meaning.
- Find every place that replaces a zero value with a default. Count the layers.
  More than one layer means the user value cannot reach the runtime.
- Confirm the parser rejects unknown keys and misplaced keys. A silent drop is a
  defect.
- Confirm the resolved value is written back, so downstream code reads the same
  number the resolver computed.

## DC-6 Bound, cap, budget, and truncation

**Mechanism.** Two independent limits get mapped onto one variable. A page end
overflows. A truncation cuts a multi-byte character or a structural delimiter.

**Evidence.** 113 commits. `d25e81c` computed a page end overflow-safely.
`fbc3f82` fixed a title truncation off-by-one. `1c448ba` decoupled the evidence render
cap from the binding cap. `7ddb07c` stopped mapping a step timeout onto a pool budget.
A separate fix kept grep output valid UTF-8 on truncation.

**Probes.**
- List each limit and its owner. Two limits with different owners must not share a
  variable.
- Test each bound at `0`, `1`, `max-1`, `max`, and `max+1`.
- Test paging at the last page and past the last page. Use a sum that can overflow.
- Truncation must produce a valid value of its own type: valid text encoding, valid
  structure, valid identifier length.
- The caller must learn that truncation happened. A silent cut is DC-9.

## DC-7 Timeout and deadline mapping

**Mechanism.** A step-level timeout, a run-level deadline, and a pool budget are three
different numbers. Code derives one from another and the run stops early or hangs.

**Evidence.** `b8d2d2d` let workflow steps run to the run deadline instead of a fixed
ten minutes. `7ddb07c` stopped mapping a step timeout onto a pool budget. A separate
fix applied a twelve-hour timeout floor on resume.

**Probes.**
- Draw the deadline hierarchy. A child deadline must be shorter than or equal to its
  parent, and must be derived from it explicitly.
- No fixed duration constant may govern work whose real bound is a caller deadline.
- On resume, the original deadline may have passed. State the rule and test it.
- The context deadline must reach outbound network calls.

## DC-8 Retry, backoff, and storm control

**Mechanism.** A retry path retries a permanent failure, or retries without a bound,
or loses state between attempts.

**Evidence.** 43 commits. `41b46c8` hardened workflows against retry storms and hangs.
`20c8932` and `a0903ff` added and then widened transient-provider retry inside steps.
A separate fix honored a bounded `Retry-After`, and another closed the staged retry
body when a backoff was cancelled.

**Probes.**
- Classify each failure as transient or permanent before you retry. Retry only
  transient failures.
- Bound the attempt count and the total elapsed time. Both, not one.
- A server-supplied delay must be honored and must itself be bounded.
- Each attempt must restate the full contract. `49e721e` restated the output schema in
  validation retries, because the retry lost it.
- Cancellation during backoff must release every resource the staged attempt holds.

## DC-9 Silent failure and dishonest status

**Mechanism.** An error is swallowed, a truncation is not reported, or a status string
claims more than the code did.

**Evidence.** 95 commits. `30b5643` surfaced workflow file read errors. `51f04e7`
removed a fake workflow runner and made interrupt and resume honest. `1fb54f4`
reported the enforced read bound on oversized lines. `820817a` refused a delivery start
when the origin was unresolved instead of proceeding.

**Probes.**
- Find every discarded error value. Each one needs a stated reason.
- A partial result must be labelled partial in the value the caller receives, not only
  in a log line.
- A status must be derived from what happened, never from what was requested.
  `report the effort that results, not the one requested` is the same defect.
- A missing precondition must refuse the operation, not select a default and continue.

## DC-10 Path, environment, and isolation escape

**Mechanism.** A path check runs before a symlink resolve. An inherited environment
variable redirects a child process out of its sandbox. A worktree path resolves
against the wrong root.

**Evidence.** 27 commits. `d6a7faf` stopped worktree marker and lifecycle lock symlink
escapes. A separate fix blocked `GIT_DIR` and `GIT_WORK_TREE` in worktree
`run_command` environments. `e8ae25a` isolated the verification Git environment.
`63fc747` resolved the main repository root from inside worktrees. `be2ec04` allowed
template paths whose names contain a literal double dot, which an over-broad traversal
check rejected.

**Probes.**
- Resolve symlinks first, then check containment. The reverse order is a defect.
- List the environment variables a child process inherits. Deny the ones that redirect
  its root, its configuration, or its credentials.
- A traversal check must reject the `..` path segment, not the `..` substring.
- Inside a linked worktree, the repository root is not the working directory. Resolve
  it explicitly.

## DC-11 Identity and comparison

**Mechanism.** Two values that name the same thing compare as different, or two
different things compare as equal.

**Evidence.** `ac484fd` compared pull-request head owners case-insensitively.
`ee43bc9` made `containsIdent` walk subtrees instead of checking the top level only.
`eaf7978` replaced a `%p` pointer identity with a real identity.

**Probes.**
- For each identity comparison, state the canonical form. Normalize both sides before
  you compare.
- Case, trailing separators, and host spelling are the usual sources.
- A pointer or a formatted address is not an identity. Use a declared key.
- A structural search must walk the whole structure, not the first level.

## DC-12 History and state retention across interrupt

**Mechanism.** A turn, an attempt, or a session ends early. The partial record is
discarded, so the user loses visible work and the operator loses the failure reason.

**Evidence.** 124 commits in the persistence group. A chain of fixes retained
preparation on cancellation, retained canceled transport history, preserved
force-sent canceled history, preserved interrupted preparation errors, and retained
workflow retry execution history (`13a1f1b`). `67d3fdd` persisted failed-attempt
errors.

**Probes.**
- For every early-exit path, name what the user sees afterwards. A cancel must keep
  the partial answer and add a cancelled marker.
- A failed attempt must persist its error before the state moves on.
- Retry must not erase the record of the attempt it replaces.
- Test the interrupt at each phase: before first activity, mid-tool, mid-stream, and
  after the answer.

## DC-13 Authority, scope, and routing

**Mechanism.** A component receives a wider tool scope, a wider principal, or a
different route than its declaration allows.

**Evidence.** `b982c2f` aligned workflow agent authority. `153409d` added restricted
workflow engineers. `ed07bc9` scoped the registry before the dispatcher. `2089538`
enforced the source boundary and root-scope denials. `101e3d1` routed session workflow
deliver and cancel through the CLI paths instead of a parallel path.

**Probes.**
- Scope the registry before the dispatcher reads it, never after.
- One operation, one path. A second path for the same operation drifts.
- A nested component inherits work, never authority.
- Test the denial, not only the grant.

## DC-14 External interface tolerance

**Mechanism.** The code assumes a well-formed response from an interface it does not
own. The real interface omits a field, sends a usage-only chunk, returns arguments that
do not parse, or rejects a field the code asks for.

The boundary is not only a model provider. A local external tool is the same
mechanism: `ad1e38c` requested a `baseRefOID` field that `gh` has never exposed, so
every workflow delivery died after all gates had passed, and the fake `gh` in the tests
accepted the field that real `gh` rejects. A fake that is more tolerant than the real
interface hides this class instead of catching it.

**Evidence.** 71 commits. Fixes assigned identifiers when the provider omitted tool
call identifiers, treated usage-only chunks as completed turns, rejected invalid stream
tool arguments, stopped a provider message leak in error text, and compacted and
retried once on a prompt-too-long response.

**Probes.**
- For each field the code reads from an external interface, ask what happens when it is
  absent. For each field the code *requests*, confirm the interface actually exposes it
  in the versions in use.
- A fake must refuse what the real interface refuses. A permissive fake proves nothing.
- Every tool call must stay pairable with its result, including skipped and failed
  calls.
- External error text may carry request content. Do not put it into a user-facing
  error without redaction.
- Test the malformed response, not only the good one.

---

## DC-15 Static allowlist over a dynamically-named set

**Mechanism.** A hand-authored list enumerates known names to grant a property (core
tier, an allowlist, a denylist). A separate mechanism can add members to the domain
that list ranges over, but those members get a name only at runtime (a hash, a remote
identifier, a generated ID) that could not have been written into the list when it was
authored. Membership defaults to false for anything the list cannot name, so every
runtime-named member silently gets the "unlisted" outcome forever, with no error and no
signal that the list is incomplete.

**Evidence.** `388ed35`'s companion fix: `internal/cli/tool_tiers.go`'s `[tools] core`
list is a hand-authored allowlist of compiled-in tool names. An MCP tool's name
(`mcp__<server>__x<hex>`, `internal/mcp.EncodeToolName`) is a runtime hash of whatever
the remote server reports, unknowable when `core` is written. Configuring `core` at all
silently moved every MCP tool into the deferred tier - the tool an operator connected a
server for became unreachable, because the model had to actively discover a tool it had
no a-priori reason to know existed. `authorizedAgentTools` (`internal/cli/mcp_scope.go`)
had already solved the identical problem for tool AUTHORIZATION, by granting membership
through the parent domain (the selected server) instead of the individual name - the
core-tier split was a second, independent path over the same tools that had not adopted
that rule (DC-13's "one operation, one path" applies here too).

**Probes.**
- For each hand-authored list (allowlist, denylist, core/priority tier, routing table),
  name the domain it ranges over. Can every member of that domain be named in advance,
  or does anything in it get a name only at runtime?
- If a runtime-named member exists, the list needs an explicit rule for it (grant
  through a parent/selector the list CAN name, or refuse to silently exclude) - not
  silent exclusion by omission.
- When two mechanisms decide a related property over the same set (authorization vs.
  visibility, admission vs. priority), grep for every other decision point over that
  set and confirm each applies the same runtime-named-member rule, not just the first
  one fixed.
- A config change that "restricts" a static surface (naming a core tier, narrowing an
  allowlist) must not have the side effect of silently blocking a dynamic surface that
  shares the same authorization gate.

## DC-16 A live-content path exists but only some producers feed it

**Mechanism.** A live/streaming signal (an event bus, a push channel) exists and has
real consumers, but only some of the code paths that produce the underlying content
actually publish to it. A path added later, or one that takes a structurally different
route to the same user-visible output, writes straight to its own local sink (a
terminal, a buffer) and never reaches the shared signal. Every direct consumer of that
path's own output still works, so the gap is invisible locally - it only shows up to a
cross-cutting observer that depends on the shared signal instead.

**Evidence.** `9809597d`: `internal/agent/loop.go`'s agent-loop path published exactly
one aggregate `EventAssistant` after a reply finished streaming to `FinalWriter` -
`teeWriter` captured bytes for interrupted-turn recovery but never republished them
live, so a cross-process observer (`internal/hub`'s relay to mivia-agent-desktop) saw
nothing during generation, then the whole answer at once right before "done". Its own
sweep flagged, and did not fix, the sibling case a follow-up fix closed the same day:
`internal/chat/context_plain_send.go`'s plain (`--no-tools`) chat path streams straight
to the caller's `io.Writer` via `Completer.ChatStream` and never reaches
`internal/agent`, so it never touched `EventBus` at all - not even the one aggregate
event the tool-enabled path had. Two different code paths to "the model's reply
streamed to the user," one shared live-signal consumer, only one path (then, after the
first fix, still only one of two) actually fed it.

**Probes.**
- For every live/streaming signal (event bus, push channel, subscription), list EVERY
  producer path that generates the content it's supposed to carry - not just the one
  the signal was originally built against. A provider-facing `Completer.ChatStream`/
  `Completer.Chat` split, a tools-on/tools-off branch, a legacy/context-enabled branch:
  each is a separate producer path until proven otherwise.
- For each producer path found, confirm it actually publishes to the signal, not just
  that it writes correctly to its own direct caller's writer. A path that streams
  correctly to the terminal in front of it can still be fully silent to every other
  consumer of the shared signal.
- A fix that adds publishing to one producer path is not complete until every other
  producer path feeding the same user-visible signal is checked against the same gap
  (this is DC-16 restating the Chain control rule below, but the "site" here is a code
  path, not a name in a list - it's easy to fix one path, ship it, and miss the sibling
  because the two never look like duplicated code).
- Test the signal from EVERY producer path, not just the one the bug report named -
  a passing test suite that only exercises one path (e.g. the tools-on path) will not
  catch a sibling path's identical gap.

## Chain control

The history shows chains: one class produced 35, 45, or 26 separate fixes. A chain
means the first fix closed one site and left the others.

After you confirm a defect, and before you commit the fix:

1. Name the class from this list. If none matches, add a new class here.
2. Search the repository for every other site of the same class. Use the probe list,
   not the symptom text.
3. Fix all reachable sites in one change, or record the ones you leave and why.
4. Add one regression test per site, not one test for the class.
5. When the class has a matching row in `.mivia/invariants.md`, update that row.
   When it has none and the class is now load-bearing, add a row.

A fix report that closes one site of a known class, with no sweep result, is
incomplete.

## Maintenance

Update this document when a `fix` commit does not match any class, or when a class
produces a new mechanism. Cite the commit. Do not remove a class because it stopped
appearing; the probe is what keeps it away.
