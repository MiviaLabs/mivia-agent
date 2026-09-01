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
fix applied a twelve-hour timeout floor on resume. `938f4784`/`779378e0`/`aeca04d3`
saturated seconds-to-Duration conversions: an unbounded seconds count overflows
`time.Duration(n) * time.Second` to a negative Duration, which arms an
already-expired deadline or expires retention immediately.

**Probes.**
- Draw the deadline hierarchy. A child deadline must be shorter than or equal to its
  parent, and must be derived from it explicitly.
- No fixed duration constant may govern work whose real bound is a caller deadline.
- On resume, the original deadline may have passed. State the rule and test it.
- The context deadline must reach outbound network calls.
- Every runtime seconds-to-Duration conversion must be bounded BEFORE the
  multiply (`config.SaturatingSeconds` or an upstream clamp). Gate:
  `scripts/check_timeout_saturation.py` + `.mivia/policy/timeout-saturation.json`
  (`make timeout-saturation-check`).
- Every outbound request must carry a deadline of its own, or an explicitly
  named context deadline armed by its caller. One send path setting the
  field while a sibling omits it is the recurring shape: a conformance
  suite tests implementations and never executes the call sites. Gate:
  `scripts/check_request_deadline.py` + `.mivia/policy/request-deadline.json`
  (`make request-deadline-check`).

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
when the origin was unresolved instead of proceeding. `b151445e` accepted and
normalized a `[context.summary] provider/model` override that no wiring consumed - every
compaction kept billing the session's (expensive) model while the config claimed a cheap
one - and `1befc767` rewired it through a fail-closed completer with the misconfig
promoted to a load error.

**Probes.**
- Find every discarded error value. Each one needs a stated reason.
- A partial result must be labelled partial in the value the caller receives, not only
  in a log line.
- A status must be derived from what happened, never from what was requested.
  `report the effort that results, not the one requested` is the same defect.
- A missing precondition must refuse the operation, not select a default and continue.
- A config key that load resolves and normalizes must have a runtime consumer.
  Normalization is the tell: a key that parse, normalize, and doc but no wiring reads is
  a silent default-keep - the operator configures the override and the runtime does the
  default thing with no error, which is exactly how `b151445e`'s summary override
  shipped dead.
- **Empty-body event rendering.** A typed event body (`ErrorBody`,
  `NoticeBody`, `TextEndBody`) with an empty string payload must
  either be suppressed at the renderer (defensive guard) or fail at
  the producer (fix the producer). A renderer that prints "  error"
  for `ErrorBody{Text:""}` is dishonest status: the kind says an
  error happened, the body says nothing did, and the user sees
  one line with no actionable content. The canonical guard is on
  `TextEndBody` in `internal/ui/stream/stream.go:35-37`; mirror it
  for `ErrorBody` and any future Body type. Defensive guards on
  the renderer are NOT a fix for the producer — the producer is
  still wrong — but they prevent a noise line from reaching the
  transcript and surface the producer defect as a "garbled line
  is missing" instead of a "garbled line is misleading".

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
`b346f9d7` keyed the recovered-path message lookup by the stripped RawID
against an index keyed by the full namespaced TaskID; gated by
`TestTaskResultProducerConformance`.

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
deliver and cancel through the CLI paths instead of a parallel path. `0378ac65` found
two independent implementations of `/model <name>` resolution - classic REPL
(`internal/clichat`) and the new TUI (`internal/uiadapter`) - that had diverged: the
TUI's `resolveProviderAndModel` silently picked the first provider whose catalog
happened to contain a requested model name, with no ambiguity check, while the REPL
path never searched other providers at all. Neither path routed through a shared
lookup. Fixed by introducing one shared `(*config.Resolved).OtherProvidersWithModel`
both surfaces now call. `34cfac2f` found `reasoning_dialect = "anthropic_adaptive"` -
Anthropic's native wire format, a request shape only two provider clients can
actually deliver - reachable from config validation on ANY provider, with no
capability gate; a model entry naming it on a provider whose client only speaks
OpenAI-compatible chat/completions would have loaded successfully and then sent a
malformed request at runtime. Fixed with `reasoning.CanCarryDialect`, an explicit
allow-list `internal/config`'s `checkReasoningIsDeliverable` now consults before
accepting the dialect.

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
- An operating system is an external interface. POSIX behavior a platform does
  not honor (atomic rename over an open target, permission bits) is this class:
  `44b5ad24` shipped a concurrent marker publish that Windows rejects with
  ERROR_ACCESS_DENIED. A platform-conditional code path needs a deterministic
  test that forces the condition on every platform
  (`TestMarkerPublishRetriesTransientRenameContention`), not a wait for the
  one CI runner where it happens intermittently.

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
- **End-to-end smoke test parity.** A user-visible pipeline that has
  both a unit-test surface (scripted fixtures, one-shot canned
  responses) AND a real-world surface (live provider, real event
  sequence under the agent loop) is exactly the kind of split this
  DC is about. When the unit-test surface passes but the real
  surface breaks, the regression is in the un-tested path. For every
  UI-ship phase, list the path the user takes and the path the
  unit test takes; if they are not the same path, add an offline
  smoke test that bridges them. The smoke test must run in CI
  without live credentials and must drive a realistic event
  sequence (turn.start -> text.delta -> text.end -> notices ->
  turn.end), with the test asserting per-kind event counts. The
  canonical shape is `TestSend_FullTurn_ExactlyOneOfEach` in
  `internal/uiadapter/conversation_test.go` plus
  `TestRenderSmoke_RealisticOneUserInput` in
  `internal/ui/stream/stream_test.go`. The two together cover both
  halves of the channel-to-renderer pipeline.
- A fix that adds publishing to one producer path is not complete until every other
  producer path feeding the same user-visible signal is checked against the same gap
  (this is DC-16 restating the Chain control rule below, but the "site" here is a code
  path, not a name in a list - it's easy to fix one path, ship it, and miss the sibling
  because the two never look like duplicated code).
- Test the signal from EVERY producer path, not just the one the bug report named -
  a passing test suite that only exercises one path (e.g. the tools-on path) will not
  catch a sibling path's identical gap.

## DC-17 Two producers write one cached display value with no precedence rule

**Mechanism.** A UI or status value has two producers: an exact, event-driven update
and a periodic, time-throttled recompute that falls back to a cheaper or staler
source. Both write the same cache field. The throttle's only test is "has enough wall
time passed since the last write", not "is the pending write more or less authoritative
than what is already there." Once the throttle window elapses, the stale recompute
overwrites a fresher exact value with no comparison, and the display visibly regresses
mid-turn even though better data already arrived and nothing asked for a refresh.

**Evidence.** mivia-agent TUI status bar: the "ctx N%" indicator had two writers to
`tuiModel.cachedCtxPercent` - `updateFromDrain`, fed by the agent loop's per-step
`EventTokenUsage` (exact, provider-reported), and `liveCtxPercent`'s own 500ms-throttle
fallback, which recomputed from `session.ContextUsage()` (an estimate over
`Session.Messages`, itself frozen at the turn's start until the whole turn commits).
The throttle had no way to know a step's exact sample had already landed, so any tool
call running longer than 500ms let the next redraw's fallback stomp the exact value
with the stale pre-turn estimate - the number would jump to the right value once, then
visibly fall back down mid-turn. Fixed by adding an explicit precedence latch
(`liveCtxSampled`): once the exact producer writes this turn, the fallback producer is
locked out for the rest of the turn instead of being allowed to overwrite on a timer.

**Probes.**
- For every cache/display field with more than one writer, name each writer's
  precision (exact/measured vs. estimated/derived) and the condition under which it
  writes.
- A time-only throttle ("have N ms passed") is not a precedence rule. If a lower-
  precision writer can fire after a higher-precision one, the cache needs an explicit
  flag or generation counter recording which producer wrote last, not just when.
- Test the specific failure shape: exact write, then enough time passes for the
  throttle to expire with no new exact write, then a redraw. The stale writer must not
  fire, or must not be allowed to overwrite.
- Check every place the same field is read, not just the one render path a bug report
  named - a fix that adds the latch to one read site and misses a sibling read
  (idle-vs-busy render branches are a common split) leaves the regression reachable
  from the other site.

## DC-18 Terminal status enumeration drift

**Mechanism.** A domain adds a new terminal status value (a status no further
transition may move a task or run out of) alongside existing ones. The status is not
declared in one shared source of truth; instead, every place that must treat "terminal"
specially - a reconciler's short-circuit, a grant-pause predicate, a progress-halt
check, a failure classifier - hand-lists the terminal set as a literal switch or map.
The new status is added to the domain but not to every hand-list. Any site the addition
missed treats the new status as non-terminal and applies its normal in-flight rule to
it, so a task that should never move again gets re-admitted, reopened, or re-marked by
whichever site was missed.

This is DC-1's mirror image: DC-1 is a recoverable condition routed into a terminal
state with no way out; DC-18 is a genuinely terminal state that keeps an unintended way
out because one enumeration site did not learn about it.

**Evidence.** A stack drive's `cancelStackDependents` (added to halt the dependents of
a terminally failed chunk) wrote a new `"canceled"` status that
`internal/workflows/stacking` did not declare and that the reconciler's terminal
short-circuit (`merged, failed, skipped`) did not name. A canceled dependent whose run
row read failed/canceled/timed_out fell to the reconciler's reopen path and was
re-admitted by the next drive wave - the exact resurrection the cancel was written to
prevent. The same gap disabled a grant-pause exit and let a canceled chunk's branch be
re-marked merged. Fixed by declaring `stacking.StatusCanceled` and a single
`stacking.TerminalStatuses`/`StatusIsTerminal` source of truth, then routing every
enumeration site through it instead of leaving each with its own list.

**Probes.**
- When a fix adds a new terminal status, grep for every existing enumeration of the
  statuses that were already terminal (a switch, a map literal, an `AdmissiblePreStatuses`-shaped
  list) in the same domain. Each one is a candidate site the new status must join.
- Prefer one declared set (a slice or map the domain package owns) over repeated
  literal switches. A second literal enumeration of the same status set is the
  precondition for this class, not a stylistic nit.
- For the new status, write one regression test per enumeration site proving the site
  now treats it as terminal - not one test for the status added to the domain.
- The direction matters: confirm the new status has NO outgoing transition anywhere
  (worth checking against DC-1 in the same sweep), then confirm every site that reads
  "is this terminal" agrees.

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

## DC-19 Re-entrant state with no owner to drive it

**Mechanism.** A recoverable failure routes a run back to a non-terminal, re-enterable
state (the state machine is fine: DC-1's return edge exists). But the foreground process
that reached the failure exits after CASing the state, instead of continuing to drive the
run forward. The state is genuinely resumable, and nothing is dishonest about it (DC-9
does not apply: the printed status is accurate) - but no live process is doing the
resuming, so the run sits parked until an operator notices the printed recovery command
and runs it by hand.

**Evidence.** `delivery.ReopenForRepair` (internal/workflows/delivery/repair.go) CASes a
run back to `RunStatusRunning` at its repair step after a repairable delivery rejection,
bounded by `MaxDeliveryRepairs`. `finishWorkflowRunDelivery` (internal/cli/workflow_run.go),
the shared settle point for both `mivia workflow run --allow-publish` and `mivia workflow
resume`, printed the new status and returned - the foreground process then exited, leaving
two live runs parked at `running` for 20+ minutes each with no process touching them,
looking hung. The session engine's periodic recovery sweep already had the correct
pattern for the identical scenario (`reconcileParkedDelivery`,
internal/cli/workflow_tool_engine_reconcile.go: release the execution lock, then re-enter
through `resumeCLI`) - the sweep and the CLI foreground paths were fixed independently and
drifted.

**Probes.**
- A command whose contract is "own this run until it reaches a terminal status" (grep for
  that claim in comments) must not return control to the shell/caller on a re-entrant
  non-terminal status without itself continuing to drive the run.
- When a fix adds a new automatic re-entry to a background or scheduled process (a sweep,
  a session engine), check every foreground/one-shot CLI entry point that reaches the same
  state machine for the identical gap - the two paths are not the same code and do not
  automatically share a fix.
- A single-purpose command whose contract is "act once and report" (e.g. `workflow
  deliver`) is not automatically in scope for this probe; confirm the command's own
  contract (and its tests) before extending "own until terminal" to it.

## DC-20 A declared capability constant drifts stale against the real upstream service

**Mechanism.** A config or catalog entry hardcodes a numeric fact about a third-party
model or service (context window, output ceiling, rate limit) at the time it was
written. The vendor later raises that number - a new model generation, a beta graduating
to general availability, a price-tier change - and nothing in the system re-derives or
re-validates the constant against the vendor's current, real capability. The stale value
is silently smaller than reality, so every computation downstream of it (a budget, a
compaction trigger, a rate limiter) is systematically wrong in the safe-looking direction,
which is exactly what makes it go unnoticed: it never causes a hard failure, only a
program that behaves as if the vendor's service is worse than it actually is.

**Evidence.** `.mivia/mivia.toml` declared `context_window_tokens = 200000` for
`claude-sonnet-5`, `claude-opus-5`, `claude-fable-5`, and `claude-sonnet-4-6`, and
`1000000` (round decimal) for the `gemini-3.*` family. Anthropic's real Claude 5-generation
models ship a 1,000,000-token window as the GA default (no beta header, no price premium -
confirmed via Anthropic's own model documentation), and Google's Gemini 3.x family's real
window is `1,048,576` (2^20), not the round `1,000,000` the catalog entries approximated.
`internal/config.EffectivePromptTokens` derives the session's prompt budget directly from
this declared number, and `internal/contextmgr.Plan` compacts at 80% of that budget - so
the stale 200000 window on Claude 5-generation models capped the real prompt budget at
roughly 1/5 of the model's actual capacity and made compaction fire five times earlier
than the real service allows. A user watching the context gauge saw "31% used" after a
handful of tool-calling steps and read it as a bug in the compounding logic; the
accounting was correct, the declared ceiling it was measured against was wrong.

**Probes.**
- For every catalog/config entry declaring a third-party model's context window, output
  ceiling, or rate limit, verify the number against the vendor's OWN current
  documentation, not against what a previous entry in the same file already claims - a
  copy-pasted stale value looks exactly as authoritative as a correct one.
  `.mivia/mivia.toml` and `.mivia/mivia.toml.example` must agree with each other and with
  reality; a fix to one without the other reintroduces the exact drift for whichever new
  workspace copies the example.
  - Web search results reporting the same number differently ("1 million tokens" vs. an
  exact integer) do not resolve to the same literal - Anthropic's documented models use
  round decimal millions, Google's documented models use binary-power millions
  (1,048,576) as a matter of platform convention; use the vendor's own exact number, not
  a rounded paraphrase from a secondary source.
- A model whose real specification could not be independently confirmed (this fix left
  `claude-sonnet-4-7` and `gemini-pro-agent` untouched: the first did not resolve to a
  real, currently-documented model, the second is an internal proxy alias with no public
  spec) must be left as-is and named as unconfirmed, not guessed by extrapolating from a
  sibling entry's number.
- This class recurs on every future model generation release; it has no code fix, only a
  standing obligation to re-verify declared catalog constants when a vendor ships a new
  model or graduates a capability from beta to GA.

## DC-21 A platform-specific symbol escapes its platform guard

**Mechanism.** Code imports a portable-looking package (typically
`golang.org/x/sys/unix`) and calls a symbol that only some GOOS builds define -
an ioctl constant, a flag, a struct field. The author's own OS carries the
symbol, so local builds, `go vet`, and unit tests all pass; the first cross-OS
build to touch that file fails to compile. Because it is usually a _test_
helper that breaks, most jobs keep passing and the failure surface is one OS
job - or zero, if the runner shell masks it (see evidence). The class is about
the escape, not the symbol: any per-GOOS API reached from an untagged file
qualifies.

**Evidence.** Two commits four days apart each introduced an unguarded pty test
helper in `internal/clichat` calling `unix.TIOCSPTLCK`/`TIOCGPTN` (Linux-only):
`db580256` (`withPtyStdin`) and `6daa46ae` (`openTestPTY`). The darwin job went
red on both (`verify-macos`, run 33040674276); windows showed the same compile
errors yet concluded green because its multi-line pwsh run block only fails on
the last command's exit code - the mask turned one class into a second silent
site. Fixed as a guarded pair in `d5946852`; the pwsh mask fixed separately in
`66bb81f4`.

**Probes.**
- Grep for platform-suspect imports (`golang.org/x/sys/unix`, `syscall`) in
  files with no `//go:build` line. Any hit whose symbols are not defined for
  every GOOS the repo ships (linux, darwin, windows) is this defect.
- Cross-compilation catches the class offline: `GOOS=darwin go vet ./...` and
  `GOOS=windows go vet ./...` type-check every build-tag variant of the touched
  packages without needing runners. Run it whenever a change touches a
  platform-specific import.
- When quarantining such code behind a `<name>_<goos>.go` / `<name>_other.go`
  pair, both halves must share one signature contract, and the non-native half
  must skip with the same posture the capability-missing case already uses -
  never fail.
- CI honesty is a precondition: if a runner shell only propagates the last
  command's exit status (pwsh run blocks), a compile break in this class hides
  entirely. Split such blocks into single-command steps first.

## DC-22 A refactor moves code but leaves its non-Go wiring pointing at the old path

**Mechanism.** A package or directory moves, merges, or is deleted. Go-level
references get rewritten mechanically because the compiler enforces them - but
references outside the compile graph survive untouched: Makefile targets,
`.github/workflows` run commands, scripts, docs snippets, release tooling. They
rot invisibly until the next invocation either fails loudly (`directory not
found`) or quietly runs less than intended (a `-run` filter that matches no
renamed test is still green).

**Evidence.** `491c7789` collapsed five `internal/workflows` subpackages into
`internal/workflows/definition` and rewrote 119 Go call sites, but left the
Makefile target `verifier-integration` pointing at the deleted
`./internal/workflows/verifier`. The dedicated CI job
`verify-main-verifier-integration` then failed with `[setup failed] directory
not found` on every main push (runs 33036787729, 33040674276) until fixed by
`86e7fe1d`.

**Probes.**
- Any diff that renames, merges, or deletes a directory must grep the whole
  tree for the old path string restricted to non-`*.go` files (Makefile,
  `.github/`, `scripts/`, `docs/`) and reconcile every hit in the same change.
- Prefer targeting whole packages over name-based `-run` filters in gates that
  exist for one specific test; a filter that silently matches nothing after a
  rename turns the gate vacuous instead of loud. If a filter is unavoidable,
  assert match-count > 0 before trusting the result.
- After any path-affecting refactor, verify every package path referenced by a
  Makefile target resolves on disk (one pass over `\./(internal|cmd)...`
  occurrences suffices today).

## DC-23 Host-authored placeholder is replayed to the model as its own output

**Mechanism.** A host pass rewrites a field of a stored assistant turn -
eliding it, capping it, redacting it, or replacing it with a notice - and a
later request replays that field verbatim to the provider as the assistant's
own prior output. The model is then told it said or thought something it never
produced. The damage is worst in fields the provider treats as authoritative
model state rather than as conversation text: a preserved-thinking dialect
feeds a replayed `reasoning_content` back into the model's own reasoning
context, so one fabricated sentence stays in scope for the rest of the session.
It is invisible in short sessions because no rewrite pass has run yet, and it
survives a restart because the rewrite is what got persisted.

**Evidence.** Context compaction replaced elided assistant reasoning with
`[reasoning elided by context compaction]`, and `toAPIMessages` replayed that
sentence as `reasoning_content` to every provider declaring
`RequiresReasoningReplay` - z.ai's GLM entries, which run
`reasoning_dialect = thinking_preserved` (`clear_thinking:false`). Only long or
resumed sessions reached it, which is where empty GLM turns were observed.

**Probes.**
- For every host pass that WRITES a field of a stored assistant turn, ask what
  the wire serializer does with that field. A rewrite is only safe if the field
  is either never replayed, or replayed to a provider that reads it as
  conversation text rather than as its own state.
- Distinguish "the field is gone" from "the field is a placeholder". Absent is
  honest; fabricated is not. Send the field absent unless a provider documents
  a hard rejection for the absent case - and if one does, keep the placeholder
  for that provider ONLY, gated on the same flag that documents the rejection.
- Content-role notices are not this class: a tool-result notice is labelled as
  a notice and the model reads it as such. The class is about fields the
  provider attributes to the model.
- A partial rewrite (a redaction placeholder substituted into otherwise genuine
  text) is a weaker instance: the block is still mostly the model's own output.
  Weigh dropping it against losing real context, and state the disposition.

## DC-24 A line-oriented writer emits a record-boundary artifact its parser reads as content

**Mechanism.** A producer writes a line-oriented format. Its per-record writer
ends every line with a newline. The producer then adds one more separator or
terminator byte between records, and the text carries a blank line that no
format rule declares. A parser in the same system maps every input line to one
content row. The blank line becomes a phantom content row, and the renderer
prints an empty row where no content exists. Row counts, scroll windows, and
paging budgets computed from the parsed rows are wrong by the same amount.

**Evidence.** `internal/diff.FormatUnifiedAt` wrote an extra newline between
hunks on top of `writeHunk`'s per-line newline, and kept the trailing newline
after a final +/- line. `internal/uiadapter.parseDiffHunks` maps an empty
input line to an empty context row, so the TUI showed one empty row after
every hunk. The parser's empty-line tolerance exists for external tools that
trim trailing whitespace, which made the producer's own artifact parse as
content. Fixed in the change that added this class (regression tests
`TestFormatUnifiedAt_NoBlankLineBetweenHunks` and
`TestParseDiffHunks_DropsSeparatorAndTerminatorEmptyLines`): the writer
stopped emitting the separator, and the parser drops empty lines only at
record boundaries - just before a record header, or at end of output.

**Probes.**
- For every writer of a format that an in-repo parser reads line by line,
  list what it writes BETWEEN records and at END of output, on top of the
  per-line newline. Anything beyond the per-line newline is this defect.
- For every such parser, state how it classifies an empty line, and test the
  three positions apart: mid-record (content, keep), just before a record
  header (artifact, drop), end of input (artifact, drop).
- Round-trip test: run the producer's real output through the parser and
  assert no parsed row carries empty content the input did not carry as
  content.
- Sweep every sibling writer that feeds the same parser. A format with two
  writers needs the fix in both.

## DC-25 Exported stream or event channel is never closed upon worker termination

**Mechanism.** A background worker, poller, or subscription manager exposes an
asynchronous receive-only channel (`Inputs() <-chan T` or `Events() <-chan E`)
for caller consumers. Callers consume the channel using a standard idiom
`for item := range worker.Inputs()`. When the worker's `Stop(ctx)` method is
called, the worker closes internal loop control channels (`stopCh`) and exits,
but forgets to close the exported output channel (`defer close(w.inputCh)`).
Callers iterating over the channel block indefinitely on the range loop, leaking
consumer goroutines, holding references to enclosing session pools, and failing
graceful shutdown deadlines.

**Evidence.** `internal/chatsync.InputPoller.Stop` terminated `p.loop` but
left `p.inputCh` unclosed. `internal/uiadapter.SessionPool.forwardRemoteInputs`
blocked permanently on `range syncSess.Inputs()`, leaking a goroutine per
pooled session. Caught by architectural bug review finding [AR-2] and fixed in
commit `ac410387` (regression tests `TestInputPoller_ChannelClosedOnStop` and
`TestSessionPool_ForwardRemoteInputsGoroutineTerminatesOnStop`).

**Probes.**
- For every worker with an exported channel accessor, trace the termination
  branch of its background loop. Ensure `defer close(ch)` runs on all exit paths.
- Conformance test: Start worker, invoke `worker.Stop()`, and assert that
  reading from `<-worker.Channel()` immediately returns `(zero, false)` without
  timeout.

## DC-26 Top-level JSON array sent where upstream API schema requires object envelope

**Mechanism.** An HTTP client method accepts a slice parameter (`events []EventItem`)
and serializes it directly to `POST /v1/...` as a top-level JSON array (`[...]`).
The upstream REST API schema requires an object envelope with a named field
(`{"events": [...]}`). Unit tests with handwritten mock servers mirror the
client author's mistaken assumption by decoding `json.NewDecoder(r.Body).Decode(&slice)`
directly, resulting in 100% test pass rates in mock suites while 100% of live
requests fail with HTTP 400 Bad Request.

**Evidence.** `internal/chatsync.Client.AppendEvents` sent `events []EventItem`
directly, causing live sync batch append requests to fail against the API's
ValidationPipe. Mock servers in `client_test.go`, `attach_test.go`, and
`session_test.go` shared the defect. Caught in correctness bug review and fixed
in commit `ac410387` (regression test `TestClientAppendEvents_MatchesWireEnvelope`).

**Probes.**
- Compare every client `POST`/`PUT`/`PATCH` payload against frozen schema contracts
  in `api/contracts/` or upstream OpenAPI/JSONSchema definitions.
- Unit test mock servers must decode request payloads into typed structs or maps
  that assert the required envelope properties, rather than decoding bare slice
  types.

## Maintenance

Update this document when a `fix` commit does not match any class, or when a class
produces a new mechanism. Cite the commit. Do not remove a class because it stopped
appearing; the probe is what keeps it away.

## DC-27 An injected option's zero value is indistinguishable from a deliberate policy

**Mechanism.** A package is made a leaf (or otherwise decoupled) by replacing a direct
import with an injected option field: instead of calling `otherpkg.Policy()`, the type
takes `Policy bool` or `Classify func(error) string` and the host supplies it. The
decoupling is correct and the gate that motivated it passes. But the field's ZERO value
is a legal, plausible policy - `false` reads as "the operator did not ask for this",
`nil` reads as "use the default" - so a wiring site that never sets it is
indistinguishable from one that deliberately chose that value.

Every test of the decoupled package passes, because the package is doing exactly what it
was told. The defect lives entirely in the caller, in a line that was never written.

This is the sibling of DC-16: there the producer path exists and does not publish; here
the consumer field exists and nobody supplies it. Both are absences, and an absence is
what a test asserting present behaviour cannot see.

**Why it recurs.** The refactor that creates the seam and the wiring that fills it are
naturally two different commits, often two different authors. The first is reviewable and
self-contained; the second looks like mechanical follow-up and is the one that gets
dropped. A concurrent-agent tree makes this near-certain: the author who created the
requirement does not own the files that must satisfy it.

**Where it has appeared.** `internal/chatsync.ProjectorOptions` produced FOUR instances in
one package: `WriterID` (adopt/fork dead in production while tests made it look live),
`StreamAssistant` (contract T2's default, unset), and `ErrorMessage` + `RedactToolArgs`
(introduced by the leaf refactor 3d1076ce, unwired until the commit citing this class).

**Control.**

- Where the zero value is not a safe default, do not accept one. Take the value as a
  required positional argument to the constructor, so an unwired site is a COMPILE error
  rather than a silent policy. This is what `NewClient(tokens TokenProvider, ...)` does
  for the token provider, and it is strictly stronger than any test.
- When the zero value must remain legal, test the PRODUCTION constructor, not a
  hand-built options literal. A test that builds its own struct asserts the author's
  intent; a test that calls `cliSyncOptions`/`poolSyncOptions` asserts the wiring.
- Table the test over BOTH values of a boolean. A site that hardcodes either constant
  passes one case and fails the other, so the assertion cannot be satisfied by a literal.
- On any commit that converts an import into an injected option, enumerate the wiring
  sites in the commit body and confirm each one. The seam and its callers are one change,
  even when they are separate commits.

## DC-28 A classification switch enumerates the observed case and lets its neighbours fall through

**Mechanism.** A `switch` maps a value from a FINITE EXTERNAL VOCABULARY - an HTTP
status code, an upstream error code, an enum another system owns - onto a local
policy. The author enumerates the cases the current behaviour produced, and the
remaining members of that vocabulary fall to `default`. The default is correct for
the case under test, so every test passes; it is silently wrong for the neighbours
nobody named.

This is not "an unhandled case crashed". Nothing crashes. The neighbour gets the
default policy, which is a plausible policy, so the defect is a WRONG ACTION rather
than a missing one - and the wrong action is usually the more expensive of the two,
because the default on a classification switch is nearly always "retry" or "carry on".

It is the sibling of DC-27 and DC-16: all three are absences. There the absence is a
field nobody set; here it is a `case` nobody wrote. A test suite that enumerates the
same cases the switch does cannot see any of them - the test and the code share one
list, and the list is the bug.

**Why it recurs.** The switch is written against an OBSERVED response. An author who
has seen the server answer 400 writes `case 400`. The vocabulary the server may draw
from is far larger than the vocabulary it has been seen to use, and it grows on the
server's release schedule, not this repo's. Each later widening ("413 and 422 join
400") adds the case that was just observed and re-freezes the rest.

**Where it has appeared.** `internal/chatsync/client.go`'s `parseErrorResponse`
(commit `6bec2d05`) classifies an error response into poison-stop versus retry. It
names 400, 413 and 422 as poison, 401, 404 and 409 as their own outcomes, and
deliberately leaves 408 and 429 on the retry path (poisoning a "try again later"
would turn a transient slowdown into a permanent stop - that reasoning is correct
and is pinned by `TestTransientStatusesKeepRetrying`).

Every OTHER 4xx still falls to `default`, which returns a generic `server error (%d)`
that `session_flush.go` and `session_badrequest.go` route to `scheduleRetry`. The
still-unguarded members are **402, 403, 405, 406, 407, 410, 415, 421, 423, 424, 428,
431 and 451**. Several of those are as permanent as 400: a 403 or a 451 on an append
is a body or a principal the server will never accept, and 415 is a content type it
will never decode. The session retries each of them for the life of the process while
reporting itself as running. The class is therefore live and open at its own origin
site, which is the evidence that it is a class and not one commit's oversight.

**Control.**

- When a switch classifies a finite external vocabulary, enumerate the WHOLE
  vocabulary, not the observed subset. For each remaining member, confirm one of two
  things and say which in the commit body: it is handled explicitly, or the default is
  provably correct FOR THAT MEMBER. "The default is correct for the case I tested" is
  not one of the two.
- Prefer classifying by RANGE or by property over classifying by value where the
  vocabulary has a structural meaning. `4xx except 408 and 429 is permanent` states a
  rule about the whole vocabulary; `case 400, 413, 422` states a rule about three
  observations. The range form fails safe as the server adds codes.
- Where the default carries the more expensive outcome (retry forever, trust, allow),
  invert it: make the default the cheap outcome and enumerate the members that earn
  the expensive one. A retry-by-default classifier must justify the default for every
  unnamed member; a stop-by-default classifier only has to be wrong once, loudly.
- Table the test over the vocabulary, not over the switch. A test that lists the same
  codes the switch lists proves only that the author wrote the same list twice.

## DC-29 Locked capture, unlocked reuse

**Mechanism.** A mutex-guarded field is correctly read under the lock and captured
into a local: `mu.Lock(); x := p.field; mu.Unlock()`. A few lines later, in the same
function, the field is needed again - and the author reaches for `p.field` a second
time instead of reusing `x`. The lock discipline at the top of the function reads as
proof the whole function is safe; the second, bare read quietly falls outside it.

This is not "forgot to lock" in the usual sense - the author DID lock, once, and
that is exactly what makes the second read easy to miss in review: the function
already looks synchronized. The bug is that the capture's scope of protection ends
at `Unlock()`, and nothing marks the second read as having stepped outside it.

**Why it recurs.** The capture is usually written to satisfy the FIRST use (often a
network call whose signature takes the value once). A second use gets added later -
often for a follow-up request that logically belongs to the same operation - and the
author's fingers reach for the field name they already know (`p.field`), not the
local a few lines up that has scrolled out of view. The two reads then silently
address different points in time if a writer runs between them.

**Evidence.** `internal/chatsync/poller.go`'s `pollOnce` (fixed in `a2e554d7`)
captured `sessID := p.sessionID` under `p.mu` for the `NextInput` call, then called
`ConsumeInput(consumeCtx, p.sessionID, raw.ID)` three lines later - a fresh,
unprotected read of the same field, racing `SetSessionID` under `-race` and, worse,
able to consume an input fetched from one remote session against a different one if
`SetSessionID` ran in between. `SetSessionID` had no production caller at fix time,
so the window was latent rather than reachable, but the shape is the same regardless
of who calls the setter.

**Probes.**
- For every `mu.Lock(); x := p.field; mu.Unlock()` capture, grep every other
  reference to `p.field` in the same function and confirm each one reuses `x`, not a
  fresh read.
- Treat "the function locks somewhere" as a false signal of safety. Check EACH read
  of the guarded field independently; a function can be half-protected.
- Gate: `mivia.go.no-locked-field-reread` (`semgrep/agent-standards.yml`) matches
  this exact shape statically for the common case where the capture and the reread
  sit in the same block. It cannot see a reread in a DIFFERENT function or through
  an intermediate helper - `go test -race` against a concurrency test that actually
  exercises the write path (`TestInputPoller_ConsumeInputUsesTheSameSessionIDAsNextInput`,
  `internal/chatsync/poller_session_race_test.go`) is what catches those; write one
  whenever the field has a setter with any caller, present or planned.

## DC-30 Teardown proceeds without waiting for an async delivery pipeline to finish

**Mechanism.** A producer hands work to a consumer through a call that is
documented as non-blocking or fire-and-forget (a bounded queue, an event bus
`Publish`, a channel send with a `default` case) and returns immediately once
the work is *enqueued*, not once it is *handled*. The caller then tears the
consumer down - Stop, Close, process exit - on the very next line, reasoning
"the producer is done, so the consumer must be too." The teardown path drains
whatever has already reached the consumer's OWN internal buffer, which reads
as complete, but says nothing about work still sitting in the enqueue layer
between the two, waiting for a delivery goroutine that has not been scheduled
yet. Nothing observable distinguishes "already delivered" from "enqueued a
moment ago" at the call site - both look like a function that already
returned.

**Why it recurs.** The non-blocking contract exists precisely so the producer
never stalls, which is correct and desirable; the mistake is assuming that
same non-blocking property also means "and it already happened." A low-volume
manual test never exposes this: a handful of events clear a bounded queue
before a human can even reach for the next command, so the gap is invisible
until real load (many small events instead of a few big ones) inflates the
queue's drain time closer to - or past - the teardown's own budget.

**Evidence.** `internal/clichat/chat_sync.go`'s `attachCLISync` (fixed
alongside the regression test below) called `syncSess.Stop(ctx)` directly from
the detach closure. `Stop` only drains `SyncSession`'s own `eventCh` via a
non-blocking `drainAndFlushFinal`; it has no visibility into
`events.Bus`'s per-subscription queue (`internal/events/bus.go`, default
256-cap, `Publish` "never blocks on a handler") that feeds `HandleEvent` in
front of it. A one-shot turn with `[sync].stream_assistant = true` publishes
5-10x the event volume of an unstreamed turn; `oneShot` returns the instant
the model/tool loop finishes, `defer attachCLISync(...)()` fires on the very
next line, and the still-queued tail (the final `assistant.message`,
`turn.ended`, trailing `tool.ended` events) was silently abandoned when the
process exited moments later. Reproduced live against a real staging session
(all three symptoms - reasoning, tool I/O, and the tail-loss described here -
surfaced together while dogfooding), then pinned by
`TestAttachCLISyncDetach_DeliversTheFullBurstBeforeStopping`.

**Probes.**
- For every teardown call (`Stop`, `Close`, `Shutdown`) that follows a
  non-blocking handoff (`Publish`, a buffered channel send, a queue push),
  check whether the teardown's own drain logic can see ONLY the handoff's
  target buffer, or also the layer feeding it. If the two are different
  buffers owned by different components, the teardown needs an explicit
  synchronization call (a `Flush`, a `Wait`, a barrier) on the UPSTREAM layer
  before it proceeds - draining its own buffer is not enough.
  `events.Bus.Flush()` is exactly this primitive here; look for its
  equivalent (a barrier, drain-then-ack, or explicit join) before trusting
  any other bounded-queue-plus-teardown pair.
  - Volume-scale the test: a burst well under the queue's capacity (so a
    capacity-based drop-oldest cannot be the reason for loss) that runs the
    producer's publish loop immediately followed by teardown, with no sleep
    and no explicit synchronization call. If it can lose events, the fix is
    missing; a correct fix is deterministic here regardless of scheduler
    timing because the synchronization primitive is barrier-based, not a
    race the test has to get lucky to observe.

## DC-31 A fixed short timeout sized for the idle case bounds an operation whose cost scales with backlog

**Mechanism.** A shutdown or final-flush path gives a real network operation
a short, fixed ctx budget - reasonable if that operation always does a small,
constant amount of work. But the operation actually sends WHATEVER has
accumulated since the last successful attempt, as ONE uncapped request (no
pagination, no chunking). Under light load the backlog is always small and
the fixed budget is never noticed. Under real load - a burst that outpaces
how fast the periodic background path can drain it - the backlog grows, and
the SAME fixed budget that was generous for one case is starved for the
other. The timeout was sized for "how long does a network call normally
take," never audited against "how long can this call's PAYLOAD grow."

**Why it recurs.** The budget is usually copied from a nearby precedent (a
different Stop/Close call with a genuinely small, bounded operation) or
picked as "a couple seconds feels responsive" during manual testing, which
only ever exercises the light-load case - a human typing a few messages
never accumulates a real backlog before quitting. The failure mode is also
easy to miss under test: an in-process httptest server answers in
microseconds regardless of backlog size, so nothing in a fast test suite
ever pays the real network cost that exposes the gap.

**Evidence.** `internal/clichat/chat_sync.go`'s `attachCLISync` detach
closure and `internal/newtui/run.go`'s TUI shutdown both gave `Stop`'s final
flush a 2-5 second ctx. `FlushOutbox` (`internal/chatsync/attach.go`) sends
the ENTIRE unflushed backlog in one `AppendEvents` call - no size cap, no
chunking. Once `[sync].stream_assistant = true` raised event volume 5-10x, a
real turn against the real staging API accumulated a backlog periodic
mid-turn flushes could not fully drain (a single-threaded worker loop
blocked on each network round trip cannot also drain new local events
during that call), and the final `flushNow` needed longer than 2 seconds for
a real round trip. `Stop`'s `timedOut` path returned early, handing outbox
close to a goroutine the caller's own process exit then killed - the
backlog was not delayed, it was permanently lost (confirmed via the local
outbox file: all events were correctly projected and durably queued
locally, seq 1-504; only the first 78 were ever uploaded, matching the
server's own stored count exactly).

**Probes.**
- For every operation whose request body is "whatever accumulated," not a
  fixed shape, check what bounds that accumulation (a max batch size? a
  chunking loop?) and what bounds the TIME budget given to send it. If the
  size has no cap and the timeout is a small constant, the timeout is wrong
  for the operation's actual worst case, not merely "a little tight."
  `internal/chatsync.RecommendedStopTimeout` is the fix here: a single named,
  documented constant both callers reference, sized for a real network round
  trip carrying a real backlog, not copied ad-hoc per call site.
  - Test it against a REAL slow server, not a fast local fake: an httptest
    handler that sleeps past the OLD budget but under the NEW one
    (`TestAttachCLISyncDetach_SurvivesASlowFinalFlush`) is the only way to
    observe this - a mock that never blocks cannot fail regardless of what
    the timeout constant says.

## DC-32 An unbounded batch crosses a peer's hard cap, and the rejection reads as unrecoverable

**Mechanism.** A client accumulates work locally with no size limit ("send
whatever's pending") and submits it to a peer as one request. The peer DOES
cap what it accepts, and rejects an oversized submission with a 4xx. Nothing
about that 4xx is actually unrecoverable - splitting the same submission into
two requests would succeed - but the client's error classification was
written for a DIFFERENT kind of 4xx (a genuinely bad, unfixable submission),
and every 4xx that doesn't match the one carved-out RECOVERABLE shape (here:
a sequence-gap complaint) falls into that catch-all "poison, stop
permanently" branch. The client had a bug (no chunking); the peer's correct,
well-behaved rejection of that bug's output gets treated as proof the
CONNECTION is broken, not the SUBMISSION.

**Why it recurs.** The accumulation side and the transport side are written
by different concerns at different times: "collect everything that needs
sending" has no reason to think about a wire-level cap when it's written,
and "send what's pending" often starts life genuinely small (a handful of
events between periodic flushes), so a batch-size cap feels like premature
complexity until real load - here, turning on a feature that multiplies
event volume 5-10x - makes the accumulated backlog cross a limit nobody
had reason to hit before. The peer's cap is also easy to never learn about
until it fires: it's enforced, but not necessarily documented anywhere the
client author would read before shipping.

**Evidence.** `internal/chatsync/attach.go`'s `FlushOutbox` sent the entire
unflushed outbox as one `AppendEvents` request. A direct probe against the
real staging API confirmed the server caps batches at 100 events with a
400 ("events must contain no more than 100 elements"). That message doesn't
match `handleBadRequest`'s `IsSequenceComplaint` check (written for a
different 400 shape entirely - a seq-gap from a crash-window race), so it
fell to `poison()`, which stops the sync session permanently for the rest
of the process. Once `[sync].stream_assistant = true` raised event volume
enough that the local outbox could genuinely exceed 100 unflushed events
(easily, since periodic mid-turn flushes could not always keep pace), the
very next flush attempt - including the final one on Stop - poisoned the
session outright, discarding everything queued after it. The same probe
also surfaced a second, narrower version of this class: a single ~200KB
event payload got a 500 (not a clean 4xx) where a 60KB one succeeded -
flagged as residual, not fixed here (see the Sweep note on the fix commit).

**Probes.**
- For every "accumulate locally, submit as one request" path, ask
  explicitly: does the peer cap what it accepts, and does the client know
  that cap and respect it, or does it submit "whatever's pending" and
  trust the peer to accept any size? If nobody has verified the peer's
  actual limit (probe it directly against the real service, not just its
  docs - `maxAppendBatch` here was set from an empirical probe, not a
  written spec), assume one exists and chunk defensively.
- For every place a 4xx/error response is classified as terminal vs.
  retryable vs. "fix and resend smaller," check whether the classification
  covers ALL of the peer's actual 4xx vocabulary or only the one shape the
  author had in mind. A catch-all "anything else is unrecoverable" branch
  silently absorbs every NEW 4xx meaning the peer ever adds, including ones
  that are trivially fixable client-side.
- Gate: `TestFlushOutboxChunksBatchesAtTheServerCap`
  (`internal/chatsync/attach_test.go`) mocks the server's own 100-cap
  rejection and asserts the client never sends a batch large enough to
  trigger it, and that a 250-event backlog still fully lands across
  multiple chunked requests.

## DC-33 A struct field is nulled by one subsystem's precondition; a sibling subsystem still reads it assuming it stays populated

**Mechanism.** A struct field starts populated and one subsystem depends on
it staying that way. A second, unrelated subsystem later gains the
authority to null the field as a side effect of its OWN state transition
("once X is enabled, this field belongs to X's replacement and is
cleared"). The nulling subsystem has no way to know who else reads the
field - it was written to serve its own concern, not to honor every
reader's assumption. The reading subsystem, in turn, was written before
the nulling subsystem existed (or before the nulling subsystem grew this
behavior), so it has no reason to check for the null case; it just reads
the field and treats an unexpectedly empty value as "legitimately absent"
rather than "cleared out from under me by someone else's precondition." If
the nulling condition is the DEFAULT path in production - not an edge case
- the field is empty on every real run, and a reader that falls back
silently on empty (mint a fresh substitute, skip, no error) makes the
whole failure invisible: nothing crashes, nothing logs, the dependent
feature just never round-trips.

**Why it recurs.** The field's name and original purpose ("the directory
this session lives in") say nothing about the fact that a second subsystem
now owns its lifecycle for the common case. A reader added later, in a
different package, reasonably assumes a plainly-named field on a shared
struct holds what its doc comment says - nothing about the type signature
distinguishes "this is stable" from "this is stable only until some other
subsystem's precondition fires." Because the nulling path is the DEFAULT
one in production, no manual smoke test catches it either: the feature
built on the field "works" in the sense that it runs without error, it
just silently does the wrong (ephemeral, unpersisted) thing every time.

**Evidence.** `internal/chat/context_integration.go`'s `SetContextManager`
unconditionally zeroes `chat.Session.SessionDir` (with `sessionStore`/
`saveManager`) the instant context state is enabled - which every real
`mivia chat` invocation does, CLI or TUI, before chat sync ever attaches.
`internal/clichat/chat_sync.go`'s `cliSyncOptions` and
`internal/uiadapter/session_pool.go`'s `poolSyncOptions` read
`sess.SessionDir` to anchor chat-sync's local identity file.
`chatsync.LoadOrCreateIdentity` treats an empty anchor directory as "no
identity directory available" and mints a fresh, NEVER-PERSISTED identity
rather than erroring - so every chat-sync attach ran against an ephemeral
identity with no `RemoteSessionID`, and `AttachSession` always took the
create-fresh branch instead of re-attaching to an existing remote session.
Every resume of a local chat thread therefore forked a brand-new remote
session with the API. No test caught this: every existing chat-sync
identity test built its `chat.Session` as a hand-rolled struct literal
with `SessionDir` set directly, never through the real `SetContextManager`
path that clears it in production.

**Probes.**
- For every field a reader treats as reliably populated, find every OTHER
  writer of that same field (not just the constructor) and check whether
  that writer's precondition for clearing it is the DEFAULT path in
  production, not a rare edge case.
- A reader that falls back silently on an unexpectedly empty/zero field
  (mint-fresh, skip, treat-as-absent) hides this class completely. Prefer
  either a reader that can distinguish "genuinely never applicable" from
  "cleared by someone else," or a caller-supplied value the reader has no
  way to get wrong instead of a shared mutable field at all.
- Build the object through its REAL production construction path when
  testing a reader of a shared field - not a struct literal that pins the
  field's value before the writer that owns its lifecycle ever runs.
- A narrow, legacy-sounding field name ("SessionDir") on a widely-shared
  struct can accumulate readers from unrelated subsystems that never
  intended to depend on its lifecycle. Grep every reader before repurposing
  or clearing such a field.
- Gate: `TestCLISyncOptionsPersistsIdentityWithoutSessionDir`
  (`internal/clichat/chat_sync_opts_test.go`) and
  `TestPoolSyncOptionsPersistsIdentityWithoutSessionDir`
  (`internal/uiadapter/session_pool_syncopts_test.go`) build a session with
  `SessionDir` explicitly empty - the real production shape - and assert
  identity still round-trips across a simulated resume.

## DC-34 An operation fence is captured after the operation it is meant to fence, not before

**Mechanism.** A staleness/fencing token is meant to answer "did anything
relevant change between when I started this operation and when I'm about to
publish its result?" That guarantee only holds if the token is captured
*before* the operation's own blocking work begins. If it is instead captured
*after* the blocking work returns - because the token-capture line sits
right next to the publish call it feeds, which reads naturally as "capture,
then publish" - the token now only ever compares the session's state against
itself: nothing observes the interval between capture and check, since both
happen back-to-back with no yield in between. A concurrent mutation that
lands *during* the blocking work (another operation racing it, a user action
firing mid-flight) is invisible to the check: by the time the token is
captured, that mutation has already happened and is already reflected as
"current." The operation silently wins over whatever it should have lost to.

**Why it recurs.** The capture-then-check pattern is locally correct-looking
at every call site: `token := s.captureOperationToken(...); return
s.publish(token, ...)` reads like ordinary sequencing, and the function
compiles, passes single-threaded tests, and passes any concurrency test that
only exercises the fast, uncontended path. The bug only shows up under a
timing window that requires a slow or artificially blocked I/O step
*between* the vulnerable capture point and where the naive placement put it
- exactly the case a blocking-store test double is built to create. A
reviewer skimming the diff sees "captures a token, checks it before
publishing" and confirms the fencing pattern is present, without checking
*where in the function* relative to the blocking call the capture happens.

**Evidence.** `internal/chat/context_catalog.go`'s `loadContextCatalog`
called `s.captureOperationToken("catalog-load:"+name)` at each of its four
return sites, all *after* `s.fetchCatalogSessionData(name)` - the function's
one blocking catalog read - had already returned. A concurrent `Clear()` or
`SelectModel()` racing that fetch therefore always lost the race silently: a
slow `Load` could resurrect content a user had already cleared, or overwrite
a live model switch with the stale saved binding, and `tokenCurrentLocked`
would report the token as current every time, because there was never a
window in which it could observe the concurrent change. The legacy
file-backed loader this replaced got this right by construction - its
token was captured as the literal first line of the load function, before
its own blocking read - so the divergence was invisible until two of its
tests were ported to the context-catalog path (`TestLoadCannotResurrectAfterClear`,
`TestLoadCannotOverwriteModelSwitch`) and started failing not with a build
error but with a silently-succeeded `Load` where `ErrStaleOperation` was
expected.

**Probes.**
- For any function whose job is "check whether context changed since we
  started," find the token/fence capture line and the function's blocking
  I/O or slow call, and verify the capture happens strictly BEFORE the slow
  call - not merely before the `publish`/`return` line it happens to sit
  next to in the source.
- A capture-then-immediately-check pattern with no yield between them
  (no channel receive, no lock release/reacquire, no goroutine switch) can
  never observe a concurrent mutation, regardless of how correct the
  comparison logic itself is. If the two lines are adjacent, ask what
  blocking step happened earlier in the same function that the token should
  have spanned instead.
- Test the fence with a deliberately slow/blocked dependency (a test double
  that blocks on a channel until released) and a concurrent mutation
  triggered while it is blocked - a single-threaded or already-fast test
  cannot expose this class no matter how many assertions it has.
- Gate: `TestLoadCannotResurrectAfterClear` (`internal/chat/clear_race_test.go`)
  and `TestLoadCannotOverwriteModelSwitch` (`internal/chat/model_policy_test.go`)
  block a catalog fetch mid-flight, perform a concurrent `Clear`/`SelectModel`,
  then release the fetch and assert the load is rejected as stale rather than
  silently overwriting the concurrent change.
