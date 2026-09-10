# Automations: centralized scheduling for workflows, agents, skills, slash commands, and prompts

Status: approved plan, not yet implemented. All open questions resolved.
Reviewed: planner -> plan-reviewer (5 passes; 6 blocking findings raised and resolved).

## Goal

Ship user-defined automations in a new `internal/automation` package: TOML-defined,
manually or schedule-triggered (interval / at-times / cron+TZ), each fire running as a
background session — optionally in a fresh managed worktree off a selectable base ref —
with durable run records and a fenced single-fire claim. Exposed through a new
`mivia automations` CLI and injected into the Settings -> Automations section **by
interface**, never by a `uiadapter -> automation` import.

An automation's action is an ordered list of steps; each step is a prompt, a skill, an
agent, a slash command, or a workflow. A mixed action is simply a multi-kind step list
executed in order in one session.

## Baseline: what exists today

| Fact | Evidence |
| --- | --- |
| The Automations settings section is a **fake**: in-memory only, never seeded from config, never persisted, and `startRun` fabricates a run record with no execution | `internal/uiadapter/settings_automations.go`; `settings.go:33,135` (`initFromConfig` seeds projects/providers/agents/skills/MCP, not automations) |
| The section is not merely hidden — it is **absent from the nav** | `internal/ui/screen/settings/settings.go:28` (`sectionCount = 6`), `:71` (`sectionNames` has no `"automations"`); `newAutomationsSection` is called only from tests |
| `ports.AutomationSettings` already defines the exact 5-method surface | `internal/uikit/ports/settings_automations.go:157-163` |
| `internal/uikit/ports` is a genuine leaf (first-party imports: `uikit/intent`, `uikit/uievent` only) | `internal/uikit/ports/ports.go:22-23` |
| `internal/cronschedule` is referenced in comments but **does not exist** | `ports/settings_automations.go:21,34,38` |
| Worktree creation already takes a base ref | `cliworktree.CreateManagedWorktree(root, name, baseRef, branchPrefix)`, `internal/cliworktree/context_setup.go:152` |
| Crash-recovery precedent (deferred to v2, see D5) | `sessionWorkflowEngine.ReconcileParkedRuns`, `internal/cliworkflow/workflow_tool_engine_reconcile.go:47`, dispatched at `workflow_tool_service.go:131` + 30s ticker |
| Fenced claims exist and are load-bearing | `internal/storage/sqlite_claims.go` (`run_claims`, `fenced_tokens`, `ClaimRunFenced` / `RefreshClaimFenced` / `TakeoverExpiredClaimFenced`); `internal/ledgercore/claims.go` |
| Slash commands are a first-class catalog | `internal/clichat/slash_catalog.go:28-113`, `SlashKindBuiltin` / `SlashKindSkill` |
| No cron library in the module graph yet | `go.mod`, `go.sum` |
| Atomic-write precedent: open-tmp / write / fsync / close / rename | `internal/chatsync/delivered_ledger.go:30-37` (`worktree_marker.go:81` uses the same shape but omits the fsync) |
| 30 builtin slash commands, most picker- or lifecycle-bound | `internal/clichat/slash_catalog.go:42-76` |

## SDK reuse (R7)

The repo already wraps the SDK at an established adapter boundary (`internal/sdkadapter`
wraps `agentloop`/`tools`/`provider`/`mcp`/`workspace`/`skills`). R7 means *reuse rather
than reinvent* — not *replace working internals*. `internal/workflows` deliberately
imports zero SDK packages today.

| Need | SDK candidate | Decision |
| --- | --- | --- |
| Schedule primitives | `scheduler.Every` / `scheduler.At` | **Accept** as conversion targets from `ports.ScheduleSpec`. `Schedule` is an interface, so `cronschedule.Spec` drops in without touching `Add`. |
| Job scheduling loop | `mivia-ai-sdk/scheduler` | **Reject as source of truth.** `Scheduler.entries` is unexported and unenumerable; a fired one-shot is deleted with no trace (`scheduler/run.go:131`). Durable, listable next-fire state must be ours. |
| Trigger model | `scheduler.Registry` (`Fire(ctx,name)`) | **Reject.** A second name->action map beside the automation store is two sources of truth for automation names. Manual trigger is one direct `Service.RunOnce(id)` call. |
| Run ledger | `sdk/ledger` (+`ledger_sqlite` build tag) | **Reject.** The repo's own fenced claim stack already outperforms it for this use and is load-bearing; the SDK store sits behind a build tag this repo does not enable. |
| Failure text | `scheduler.JobFailedEvent.Data` | **Reject.** Unparsed `fmt.Sprintf` string, tainted. We classify into `ports.RunFailKind`. |
| Step graph / resume | `sdk/flow` + `flow.Resume`/`Checkpoint` | **Reject for v1; borrow the shape.** `flow.Resume` resumes a flow-machine checkpoint; our resumable unit is a saved `chat.Session` plus a step index. Adopting it means authoring a `flow.Definition` per automation and running steps outside the session holding the transcript. We copy the `Checkpoint{Done,Skipped,Failed}` idea into the run record. Revisit if steps ever need branching/panels. |
| Model-facing scheduling | `sdk/subagent` `SchedulerTool`/`TriggerTool`/`FlowTool` | **Reject.** `Privileged()` self-scheduling by the model is a footgun and covers no requirement. Nothing under `internal/` imports `sdk/subagent` today. |
| Workflow execution | `cliworkflow.NewSessionWorkflowEngine` | **Accept** (repo-native). |
| Worktree creation | `cliworktree.CreateManagedWorktree` | **Accept** (repo-native). |

## API

```go
// Package automation owns user-defined automations: their TOML definitions,
// their schedules, and their durable run records. It depends on cli*
// packages; nothing in internal/ui* may import it.
package automation

// StepKind names what one automation step runs. Every kind except
// StepWorkflow executes as one turn in the automation's background session.
type StepKind int

const (
    StepPrompt   StepKind = iota // Prompt sent verbatim
    StepSkill                    // Ref = skill name, rendered like /<skill>
    StepAgent                    // Ref = agent name; selects it, then sends Prompt
    StepSlash                    // Ref = slash command (headless-safe allowlist only)
    StepWorkflow                 // Ref = workflow name; dispatched to the workflow engine
)

// Step is one unit of an automation action. A MIXED action is a Steps slice
// with several kinds: steps run in order in ONE session, and each completed
// index is recorded so a resumed run restarts at index+1.
type Step struct {
    Kind   StepKind
    Ref    string
    Prompt string
    Inputs map[string]string // StepWorkflow only
}

// WorktreeMode selects where a run executes. BaseRef is first-class (R4):
// "HEAD" or any ref cliworktree can resolve.
type WorktreeMode int

const (
    WorktreeNone WorktreeMode = iota // run in the workspace root
    WorktreeNew                      // create a managed worktree off BaseRef
)

type Spec struct {
    ID, Name, Description string
    Enabled               bool
    Trigger               TriggerSpec // manual | every | at | cron+tz
    Steps                 []Step
    Worktree              WorktreeMode
    BaseRef               string
    Unattended            UnattendedPolicy // default deny
}

// Service is the automation backend. It satisfies ports.AutomationSettings,
// which is how the settings UI reaches it without any UI package importing
// this one.
type Service struct{ /* store, spawn, sqlite, clock, cron */ }

func New(root string, db *storage.SQLite, spawn SessionSpawner, cfg Config) (*Service, error)

// SessionSpawner is the one capability the executor needs from the session
// pool. The parameter type is a PLAIN func, never uiadapter.BindFunc: a
// composition-root adapter in internal/newtui converts. See D4.
type SessionSpawner interface {
    CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error)
}

// Compile-time proof of the inverted dependency.
var _ ports.AutomationSettings = (*Service)(nil)

func (s *Service) Automations() []ports.Automation
func (s *Service) Runs(automationID string, limit int) []ports.Run
func (s *Service) Run(runID string) (ports.Run, bool)
func (s *Service) Apply(ctx context.Context, scope ports.Scope, e ports.AutomationEdit) (ports.SaveHandle, error)
func (s *Service) Watch(ctx context.Context, automationID string) (ports.RunHandle, error)

func (s *Service) Serve(ctx context.Context) error                                  // cron loop; missed fires SKIPPED
func (s *Service) RunOnce(ctx context.Context, automationID string, trigger ports.TriggerKind) (ports.Run, error)
func (s *Service) ResumeRun(ctx context.Context, runID string) error                 // restart at step_index+1
func (s *Service) SweepInterrupted(ctx context.Context) (int, error)
```

```go
// in internal/uiadapter/settings_automations.go (NOT settings.go, which is near the LOC cap)

// SetAutomationBackend installs the automation backend the Automations
// settings section delegates to. It is an INTERFACE on purpose:
// internal/automation imports cliworkflow/clichat/cliworktree, and INV-TUI-29
// (AGENTS.md:135-139) requires uiadapter stay isolated from CLI entrypoints.
// The composition root (internal/newtui) constructs the concrete
// *automation.Service and injects it here. nil restores the in-memory
// behaviour existing tests rely on.
func (s *SettingsStore) SetAutomationBackend(b ports.AutomationSettings)
```

Ports changes (`internal/uikit/ports/settings_automations.go`): `ActionRef` gains
`Steps []ActionStep`; `Automation` gains `Worktree WorktreeSpec{Mode, BaseRef}`; the
`AutomationEdit` closed union gains `ResumeAutomationRun{RunID string}`.
`ActionRef.Workflow` stays a compat alias mapping to a single `StepWorkflow`.

## Decisions

**D1. Definitions in TOML, run records in sqlite.** `automations.toml` under the config
dir (project `<workspace>/.mivia/`, user `~/.mivia/`, mirroring existing config
precedence and `ports.Scope`) is the source of truth for definitions — human-editable and
git-committable like `.mivia/workflows/*`. `automation_runs` (new table in the sqlite DB
already holding `run_claims`/`fenced_tokens`) is the source of truth for history:
`(id, automation_id, origin, state, step_index, step_count, session_name, worktree_path,
worktree_branch, claim_token, started_at, ended_at, fail_kind, message)`.

`Apply` writes TOML then reloads, using **atomic rename**: `os.CreateTemp` in the target
directory, `Chmod(0600)`, write, `Sync`, `Close`, then `os.Rename` over the destination.
This is chosen over in-place truncation because a crash mid-write would otherwise leave a
truncated definitions file that fails to parse and disables every automation at once. No
external watcher depends on inode stability: nothing in the repo watches this new path,
and the only `fsnotify` consumer in the tree (`internal/cliagents/memory_reconciler.go`)
watches the Markdown memory store's project/org directories, not this path.

The sequence adds `Sync()`, which the cited precedent does **not** have: `WriteWorktreeMarker`
is `CreateTemp -> Chmod(0600) -> write -> Close -> Rename` with no fsync. The addition is
deliberate — rename is only atomic with respect to *durable* bytes, so without the fsync a
power loss can leave the renamed file present but empty. `internal/chatsync`'s own
ledger writer already uses the stricter open-tmp/write/fsync/close/rename sequence
(`delivered_ledger.go:30-37`) for exactly this reason, and that is the precedent followed
here.

**D2. Execution entry point per step kind (closes prior review finding P1).** One
executor, the background session. `StepPrompt`/`StepSkill`/`StepAgent`/`StepSlash` are
rendered to text and sent through the run's conversation (skills/slash resolved via
`clichat.SlashCommands(surface, *skills.Registry)` / `FindSlashCommand`; a slash command
outside the headless-safe allowlist is rejected at **validation** time, not at 2am; the
executor passes `SlashSurfaceTUI` — see D15 for why that is required for skill lookup).
`StepAgent` selects the agent on the session's own forked `cliagents.AgentSessionState`,
then sends. The headless-safe slash allowlist is D15. `StepWorkflow` is the only step
leaving the session, and it uses the existing **named** path — no in-memory `CompiledWorkflow` is ever synthesized:

```go
eng := cliworkflow.NewSessionWorkflowEngine(root, configPath) // exported: workflow_tool_engine.go:66
req := workflowledger.StartRequest{
    Workflow:      step.Ref,                                  // NOT .Name: agenttools_types.go:68-84
    InvocationKey: runID + ":" + strconv.Itoa(stepIndex),
    Inputs:        inputs,
}
h, err := eng.Start(ctx, req) // Start is exported on an unexported type: legal
```

Inert for non-workflow steps: delivery/publish, sandbox, admission/idempotency ledger,
panels, verifier, checkpointing — those stages live inside the workflow engine and only a
`StepWorkflow` reaches it.

**D3. Worktree per run (R4).** `WorktreeNew` ->
`cliworktree.CreateManagedWorktree(root, "auto-"+automationID+"-"+runID, spec.BaseRef,
config.LoadWorktreeConfig(root).BranchPrefix)`, before the session exists. Creation
failure fails the run with zero side effects (no session, no orphan worktree). Path and
branch are recorded on the run. **No auto-cleanup, ever, in v1** — the branch is the
deliverable; removal stays with `mivia worktree remove` and the TUI worktree picker.

**D4. Dependency direction — the central structural decision.**

The naive shape is a hard compile-breaking cycle: `automation -> uiadapter` (for
`SessionPool.CreateFreshInDir`, `session_pool_worktree.go:36`) **and** `uiadapter ->
automation` (for `settingsAutomations`, `settings.go:24-59,135`). Resolution:

- **The interface already exists.** `ports.AutomationSettings` is exactly the five-method
  surface (`ports/settings_automations.go:157-163`). No new interface is invented.
- **It lives in a verified leaf.** `internal/uikit/ports`'s only first-party imports are
  `uikit/intent` and `uikit/uievent` (`ports.go:22-23`). So `automation -> ports` drags no
  CLI family anywhere, and `uiadapter -> ports` already exists (`settings.go:12`).
  uiadapter's transitive graph is unchanged — this is what satisfies INV-TUI-29.
- **Direction:** `automation` imports `ports` and *implements* `AutomationSettings`;
  `uiadapter` holds it *by interface*. There is never a `uiadapter -> automation` import.
- **Setter:** `SetAutomationBackend(b ports.AutomationSettings)`, a new field guarded by
  the existing `s.mu`, mirroring `SetConversation`/`SetSyncOptsNotifier`
  (`settings.go:81,125`). The five `settings_automations.go` methods delegate when the
  backend is non-nil, else keep current in-memory behaviour so existing test doubles pass.
  Placed in `settings_automations.go`, not `settings.go` (which is at 623 lines, near the
  structure cap).
- **The pool edge is inverted too.** `automation` declares `SessionSpawner` with a
  **plain func parameter**, not `uiadapter.BindFunc`.

  > **Compile hazard, caught in review.** `SessionPool.CreateFreshInDir` takes
  > `bind BindFunc`, a *named* type (`session_pool_worktree.go:16`). Go interface
  > satisfaction requires exact parameter-type identity, and a defined type is never
  > identical to an unnamed func literal with the same underlying type. So
  > `*uiadapter.SessionPool` does **not** structurally satisfy an interface written with a
  > plain func — and naming `uiadapter.BindFunc` in `automation`'s own signature would
  > reintroduce the forbidden edge. The fix is a one-line **adapter closure at the
  > composition root**, which is a second deliberate "mutual non-importing" seam:

  ```go
  // in internal/newtui — the only package already importing both.
  spawn := automationSpawnerFunc(func(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
      return pool.CreateFreshInDir(uiadapter.BindFunc(bind), dir)
  })
  ```

- **Exact wiring**, in `internal/newtui/run.go` immediately after line 243
  (`settingsStore.SetConversation(conv)`), where `pool` (`:233`) and `settingsStore`
  (`:242`) are both in scope:

  ```go
  // Composition root: newtui already imports internal/cli and internal/uiadapter,
  // so it is the one legal place the concrete automation.Service and the
  // SessionPool can meet. The store holds it by ports.AutomationSettings;
  // uiadapter never names this package.
  if autoSvc, err := automation.New(agentState.WorkspaceRoot, store, spawn, automation.Config{}); err == nil {
      settingsStore.SetAutomationBackend(autoSvc)
  } else {
      log.Printf("automations disabled: %v", err) // non-fatal; TUI starts normally
  }
  ```

Net: `automation` and `uiadapter` are mutually non-importing; both point at `ports`.

**D5. Headless turn execution (R5).** A session *can* be driven with no TUI attached, but
only with a mandatory drain. Verified against `conversation.go` and `turn_stream.go`:

```go
conv, err := spawn.CreateFreshInDir(bindFn, worktreeDir)
ctx, cancel := context.WithTimeout(parent, cfg.TurnTimeout) // ALWAYS deadlined
defer cancel()
h, err := conv.Send(ctx, intent.Send{Text: prompt})
for ev := range h.Events() { collect(ev) }                  // drain to CLOSE
```

Why the drain is load-bearing: the per-turn channel is buffered at 32
(`conversation.go:40,206`) and `turnStream.Send` **blocks** when full
(`turn_stream.go:50-62`), deliberately, to avoid dropping transcript content. In the TUI a
`tea.Cmd` drains it. Headless, if we stop reading, the agent-loop goroutine parks inside
the tap while `Conversation.Send` still holds `c.turnMu` (`conversation.go:203`) — every
later `Send` on that conversation blocks forever. Two rules make it safe:

1. **Drain until the channel closes.** `Events()` closes exactly once
   (`turn_stream.Close`, `turn_stream.go:80-100`; the `closed` bool is guarded by `mu`, so
   the close is once regardless of which of Cancel/emitTurnEnd wins). Ranging to close is
   the only correct terminator — breaking on `KindTurnEnd` leaves later tap sends parked.
2. **Always deadline the ctx.** `turnStream.Send`'s `<-s.done` arm (`turn_stream.go:59`)
   is the only thing that unparks a blocked sender. When nothing drains, `cancelTurn()` is
   itself unreachable (it runs downstream of the blocked send), so the parent deadline is
   genuinely the only escape hatch.

Each run uses a fresh conversation and never reuses it, so a wedge cannot cross runs. **No
addition to uiadapter is required.** Recorded assumption: read-verified, not
runtime-verified — chunk 2's wedge test is the mitigation and must land before chunk 6.

**D6. Missed fires are skipped, never caught up.** An interval automation that missed 40
fires overnight must not stampede 40 sessions. On start, next fire is recomputed strictly
in the future from the persisted `next_fire_at`.

**D7. Single-fire dedup.** Every fire — scheduled or manual — must win
`ClaimRunFenced("automation:"+automationID)` before any side effect. A lost claim is a
no-op, not an error. Concurrency cap: one in-flight run per automation; a colliding fire
records a `skipped` run.

**D8. Unattended permissions are deny-by-default.** A scheduled run does not inherit a
live session posture: no interactive approver is attached, so a tool call needing approval
fails the step fast rather than hanging until morning. `allow_publish` is never set for
`StepWorkflow` — delivery stays pending for a human. Opt-in `unattended = "auto"` per
automation, rendered as a warning in the detail view. Manual TUI triggers use the same
policy: a background session with no attached prompt UI cannot approve either.

**D9. The settings section joins the nav unconditionally, rendering "unavailable" when
unbacked.** Caught in review: `settings.go:28` hardcodes `const sectionCount = 6`, `:71`'s
`sectionNames` has no `"automations"`, and the `sections: []section{...}` literal at `:108`
has no Automations entry — `newAutomationsSection` is called only from tests. So the
section is structurally absent, and wiring alone would leave the feature with no UI entry
point.

The fix follows the package's **existing placeholder pattern, not conditional nav length**.
A second review pass established why conditional nav is wrong here: `sectionNames` is a
package-level var read by the free function `SectionIndex(name)` (`settings.go:65-92`),
which is called from `commands.go` *before* any `Screen` or backend exists. A
backend-conditional nav gives `SectionIndex` no way to know whether a backend is wired, so
`/settings automations` with none wired would resolve to an index `New()` then clamps into
the wrong section — violating this package's documented deep-link contract (an unresolved
name must become a `Notice`, never a silent wrong section). Projects and Skills already
sit unconditionally in the nav with no ports-backed section, rendering "unavailable"
(`settings.go:67-70`; `New()`'s doc: "Every store field may be nil; a nil field's section
renders 'unavailable' rather than the screen failing to build").

So chunk 3: bump `sectionCount` to 7, add `"automations"` to `sectionNames` in nav order,
add the section to the `New()` literal, and let it render "unavailable" when the
`ports.AutomationSettings` backend is nil. `settings_test.go:188-189` (which pins
`SectionIndex("automations")` as unresolvable) **changes with the feature**: the name
becomes resolvable, and a new test asserts the unbacked section renders "unavailable"
rather than being absent.

**D10. Cron is the timing authority**, not a display estimate: `internal/cronschedule`
implements `Schedule.Next`, which the scheduler ticks on. `NextFire` is always computed
from the cron/interval spec, never from `LastRun + period`.

`github.com/robfig/cron/v3` is **approved** (operator decision) and is the parser:
`cronschedule.Parse(expr, tz)` wraps `cron.ParseStandard(expr)` for the 5-field form plus
`time.LoadLocation(tz)` for the zone, and returns a `*Spec` whose `Next(after)` satisfies
the SDK `scheduler.Schedule` interface. Rationale for taking the dependency rather than
hand-rolling: DST transitions, the day-of-month/day-of-week OR rule, and range/list/step
parsing are a well-known bug farm; robfig/cron is MIT-licensed, widely deployed, and has
**no transitive dependencies**, so the added surface is one small vendored parser.
`Parse` remains the single entry point, so the implementation can be swapped later without
touching any caller.

Gate compliance (`.agents/rules/30-go-standards.md:71`): the addition lands in its own
`deps`-scoped commit; `make tidy && git diff --exit-code go.mod go.sum` must show exactly
one added module and no other churn (chunk 4 verification).

**D11. ID validation is ours.** `chat.sanitizeSessionName` (`persistence.go:69`) only
strips `..`, `/`, `\`, `:` and nulls — it does not reject reserved prefixes. The save name
`__auto__<id>__<runID>` does not collide with `chat.AutoSaveName` `__last__`
(`persistence.go:16`; `IsAutoSaveName` prefix-checks `__last__` only), so the scheme is
safe — but safety must not rest on the sanitizer. Store validation rejects any
`automationID`/`runID` not matching `^[a-z0-9][a-z0-9_-]{0,63}$`, at TOML load and at run
creation, before the save name is composed.

**D15. Headless-safe slash allowlist (`StepSlash`).** Enforced at TOML **validation**, not
at fire time, so an unusable automation is refused when it is written rather than failing
at 2am. The rule is a closed taxonomy over `builtInSlashCommands()`
(`clichat/slash_catalog.go:42-76`):

**Surface: the executor passes `SlashSurfaceTUI`.** This is load-bearing and was a review
finding: `SlashCommands` returns `SlashKindSkill` entries *only* for that surface
(`slash_catalog.go:95`: `if registry == nil || surface != SlashSurfaceTUI { return commands }`),
so passing the plain surface would make every skill-backed `StepSlash` validate as allowed
and then fail lookup at run time. `SlashSurfaceTUI` is also the only exported surface
constant (`slashSurfacePlain`/`slashSurfaceBoth` are unexported), so it is the sole legal
choice from outside the package. The name is about *catalog breadth*, not about a terminal
being attached — the executor renders no UI. Consequence, stated explicitly: the three
plain-only builtins (`/exit`, `/provider`, `/workspace`) are not in the TUI catalog at all
and so are unresolvable rather than classified; all three would be rejected anyway.

**Resolution is by `FindSlashCommand(Ref, SlashSurfaceTUI, registry)`, not raw-name table
match**, so aliases (`/h`, `/?`, `/quit`, `/q`) inherit their canonical command's
classification automatically.

- **Allowed: every `SlashKindSkill` command.** These are the user-extensible surface and
  the reason `StepSlash` exists as a distinct kind; they are plain skill invocations with
  no UI dependency.
- **Allowed builtins (5), argument form only:** `/compact`, `/model`, `/effort`,
  `/budget`, `/steps`. All are pure session-scoped configuration with no picker rendering
  and no lifecycle side effect. Because a bare `/model`, `/effort`, `/budget`, or `/steps`
  opens a picker that cannot render headless, validation **requires a non-empty argument**
  for those four. `/compact` is `AutoExecute` and safe bare.
- **Rejected, by named class** (the refusal message states the class):
  - *needs an interactive surface* — `/worktrees`, `/sessions`, `/workflows`, `/queue`,
    `/agent`, `/title`, `/new`, `/select`. The first five open a picker; `/new` and
    `/select` are direct actions but presuppose a session-tab/selection UI the executor
    does not have. `/agent` is additionally redundant: `StepAgent` is the dedicated,
    testable path.
  - *session-lifecycle mutation* — `/clear`, `/save`, `/load`, `/delete`, `/resume`,
    `/exit`: each would corrupt or abandon the very session the executor is driving.
    `/resume` is also re-entrant (an automation resuming a run from inside a run).
  - *outbound side effect, unattended* — `/search`: it takes a required `<query>`, is not
    `AutoExecute`, and performs a real network action. Rejected not because it is inert
    but because an unattended web fetch belongs in a prompt or skill step where the intent
    is explicit and reviewable.
  - *informational no-op* — `/help`, `/status`, `/list`, `/session`, `/tools`, `/hooks`,
    `/agents`, `/plain`, `/provider`, `/workspace`: they render output nobody reads at 2am
    and change no state.

Exhaustiveness is a test obligation, not a comment: a table test iterates
`builtInSlashCommands()` and asserts every entry falls in exactly one bucket, so a builtin
added later fails the build rather than silently defaulting to allowed or rejected.

The allowlist is a single table in `internal/automation/store.go` so widening it later is
a one-line change with a test, not a code-path change.

**D12. Import-policy edges.** `internal/automation` needs its own new allow entry in
`.mivia/policy/import-layers.json`: `internal/storage`, `internal/chat`,
`internal/clichat`, `internal/cliworkflow`, `internal/cliworktree`, `internal/config`,
`internal/skills`, `internal/sdkadapter`, `internal/cronschedule`, `internal/uikit/ports`,
`internal/workflows/ledger`. Plus **one** edge on `internal/newtui`:
`internal/automation`. **No change to `internal/uiadapter`'s allow-list at all** — that
absence is the positive proof the cycle is gone. (Note: `uiadapter`'s list already
contains `internal/cliagents`, a pre-existing cli* edge this plan neither uses nor widens.)

**D13. Crash recovery (R6).** v1 is **manual resume** from the TUI and
`mivia automations resume <run-id>`. At service start, any run in `running` whose fenced
claim has expired is marked `interrupted` (via `TakeoverExpiredClaimFenced`) and shown
with a Resume affordance. Resume reopens the saved session and restarts at
`step_index + 1` (a half-finished step re-runs whole). The automatic
`ReconcileParkedRuns`-style loop is explicitly deferred to v2; the schema needs no change
to add it.

**D14. CLI surface.** `internal/cli/root.go` gains
`case "automations": return cliautomations.RunAutomations(args[1:])` plus `usageText()`
lines for `automations list | show <name> | run <name> [--wait] | runs [--automation n]
[--limit n] | resume <run-id> | serve`. `serve` is what a user wires into OS
cron/launchd/systemd if they want firing while the TUI is closed.

## Chunks

Sequenced so the wiring shape is proven before anything expensive is built on it.

1. **Store + validation.** TOML schema, load/save both scopes, `automation_runs` table +
   migration in `internal/storage`, ID charset/length validation (D11). No scheduling, no
   execution.
2. **Wiring shape.** `SetAutomationBackend` + delegation in
   `uiadapter/settings_automations.go`; `automation.Service` skeleton with
   `var _ ports.AutomationSettings = (*Service)(nil)` and `SessionSpawner`; the
   newtui adapter closure + injection (D4); both policy entries (D12); the headless-drive
   helper and its wedge test (D5). **Gate: `make import-layers-check` and
   `make verify-fast` must pass here.**
3. **Settings nav.** Bump `sectionCount` to 7, add `"automations"` to `sectionNames`, add
   the section to `New()`'s literal, render "unavailable" when the backend is nil (D9);
   update `settings_test.go:188-189`; add the unavailable-when-unbacked assertion.
4. **Schedule evaluation.** New `internal/cronschedule` wrapping `robfig/cron/v3`
   (`cron.ParseStandard` + `time.LoadLocation`, D10); `ScheduleSpec` -> next-fire;
   skip-not-catch-up (D6). The dependency addition is its own `deps`-scoped commit.
5. **Fenced claim + run lifecycle.** `ClaimRunFenced`, `TakeoverExpiredClaimFenced`, state
   transitions, `RunFailKind` classification (D7, D13).
6. **Executor.** Worktree creation (D3), `SessionSpawner.CreateFreshInDir`, headless turn
   from chunk 2, per-step dispatch (D2), step-index checkpointing, unattended policy (D8).
7. **Serve loop.** Cron ticker over enabled automations. Implemented as
   `Service.Serve(ctx)` (`internal/automation/serve.go`): a package-private
   `deadlineMap` tracks each enabled scheduled automation's next-fire instant
   independently, keyed by automation ID (not one global "next tick" scalar).
   **DL-1 invariant:** no map entry ever holds the zero `time.Time{}` value,
   since `due()`'s comparison (`!deadline.After(now)`) would treat a stored
   zero as "always due" and fire that automation every tick forever (a
   fire-storm). The map has exactly two guarded write sites — `refresh`'s
   never-seen branch and `advance` — both gated by an explicit `IsZero()`
   check on `NextFire`'s three-outcome contract (real time / exhausted-zero /
   error) before ever assigning. `refresh` never recomputes an
   already-armed deadline (recomputing on every tick is what caused an
   earlier draft's starvation bug — an interval automation's deadline was
   pushed forward every tick and never actually elapsed); only `advance`,
   called right after a real fire, may move a deadline forward, using
   `max(firedDeadline, now)` so a daemon that oversleeps several intervals
   still yields exactly one future fire (D6), never a backlog replay. Each
   tick fires every due automation **sequentially, never concurrently** —
   load-bearing for `cliautomations.HeadlessSpawner` (chunk 9), which tracks
   only one in-flight session at a time and is unsafe under concurrent
   dispatch. `Serve` does not call `sweepInterrupted` (D13's startup sweep
   stays chunk 8's concern). A per-automation `RunOnce` error is logged, not
   fatal — one broken automation must not wedge every other one's schedule.
8. **Resume.** Reopen saved session, restart at `step_index + 1`.
9. **CLI** (D14). `internal/cliautomations` (sibling of `internal/cli`,
   matching `cliworkflow`/`cliworktree`/`clichat`'s shape) implements
   `mivia automations list|show <id>|run <id>|serve`. `run`/`serve` are
   backed by `HeadlessSpawner`, a second, independent
   `automation.SessionSpawner` implementation alongside `internal/newtui`'s
   TUI-bound one (D4): it builds a bare `*chat.Session` directly via
   `composition.BuildSession` and wraps it with `uiadapter.NewConversation`
   — never `uiadapter.SessionPool`, `BindFunc`, or `NewSessionPool` (a TUI
   session pool cannot back a headless daemon: its constructor needs a live
   interactive session/agent-state that a `serve` process never has).
   Because `automation.SessionSpawner`'s two methods give no end-of-run
   signal, and the session ID minted inside `spawnRunSession` never escapes
   `RunOnce` (`ports.Run` carries no session field, and widening it was
   deliberately out of scope), `HeadlessSpawner` tracks a single
   in-flight session's checkpoint store (not an ID-keyed map) and exposes an
   ID-less `CloseLastRun() error` — correct only because `Serve`'s own
   sequential dispatch guarantees at most one session is ever open at a
   time. `Serve`'s loop discovers this capability via an optional type
   assertion (`spawn.(interface{ CloseLastRun() error })`) rather than
   widening `SessionSpawner` itself, so the TUI's spawner (which does not
   implement it) is unaffected. `CreateFreshInDir` defensively closes any
   leftover `current` store before overwriting it (logged, not silent) as a
   safety net against a skipped cleanup — but this net only protects against
   a *dead* prior session; it would actively corrupt a still-*live* one, so
   `HeadlessSpawner` is explicitly not safe under any future
   concurrent-automation-dispatch design without a real redesign.
10. **Docs + `docs/OWNERS.yaml` registration** for this file.

## Tests

- **Cycle proof:** `go list -deps ./internal/uiadapter | grep -c internal/automation` -> 0;
  same for `cliworkflow`, `cliworktree`, `clichat` (guards transitive isolation, not just
  the direct edge).
- **Ports leafness regression:** `go list -deps ./internal/uikit/ports` contains no
  `internal/cli*` and no `internal/uiadapter`.
- **Interface satisfaction:** compile-time `var _ ports.AutomationSettings = (*Service)(nil)`.
- **Spawner adapter:** compile-level test that the newtui closure satisfies
  `automation.SessionSpawner` (pins the `BindFunc` conversion, D4).
- **Headless wedge (negative, the important one):** drain to close, assert a *second*
  `Send` on the same conversation returns promptly; then the inverse — stop draining with
  a 100ms ctx and assert the turn unparks via ctx cancellation rather than hanging.
- **Drain-to-close (negative):** a consumer breaking on `KindTurnEnd` must be caught by an
  assertion that the channel is observed closed.
- **Skip-not-catch-up (negative):** clock jumped 10 periods -> exactly one fire.
- **Dedup (negative):** two concurrent `RunOnce` -> one run record; loser is a no-op with
  nil error.
- **ID validation (negative):** `../../etc`, `__last__`, `__last__x`, 64+ chars,
  uppercase, empty -> all rejected at store load with a named error.
- **Slash allowlist (negative, D15):** `/sessions` (interactive surface), `/delete`
  (lifecycle), `/search` (outbound side effect), `/help` (no-op), and a bare `/model` with
  no argument -> each rejected at TOML load with the class named in the error; `/compact`
  bare and `/model gpt-x` accepted; an arbitrary `SlashKindSkill` command accepted.
- **Slash exhaustiveness (D15):** every entry of `builtInSlashCommands()` falls in exactly
  one bucket — fails when a builtin is added without classification.
- **Slash surface (D15):** resolution uses `SlashSurfaceTUI`, proven by a test that a
  skill-backed `StepSlash` resolves through `FindSlashCommand` and that an alias (`/h`)
  inherits its canonical command's classification.
- **Cron (D10):** DST spring-forward (a schedule anchored inside the skipped hour is
  never returned for the transition day; robfig/cron skips the whole day rather than
  shifting later the same day — verified empirically, not assumed). DST fall-back:
  ground truth, verified against the real `robfig/cron/v3` implementation, is the
  **opposite** of the naive assumption below this bullet's original wording — a
  schedule anchored inside the repeated hour fires **twice** that day, once at each UTC
  offset, because `cron.Schedule.Next` matches local wall-clock fields only with no
  offset awareness and has no "already fired this wall time" state to dedupe against.
  `internal/cronschedule`'s tests assert this real behavior; day-of-month + day-of-week
  invalid expression rejected, `Next` strictly after `after`.
- **Atomic write (D1):** a write interrupted before rename leaves the previous
  `automations.toml` intact and parseable.
- **Save-name non-collision:** `chat.IsAutoSaveName("__auto__a__b")` is false.
- **Mixed action:** `[prompt, skill, agent, workflow]` executes in order in one session;
  `StepWorkflow` passes `InvocationKey` and never sets `allow_publish`.
- **`StartRequest` field:** compile-level — constructing `StartRequest{Workflow: "x"}`
  fails to build if the field regresses to `Name`.
- **Worktree:** `BaseRef="HEAD"` and a named branch both create a worktree and the session
  dir equals it; creation failure -> failed run, no session, no orphan worktree.
- **Resume:** a run at `step_index=1` of 3 restarts at step 2, not step 0; resume of a
  succeeded run is refused.
- **Section nav:** `SectionIndex("automations")` resolves (updating the
  `settings_test.go:188-189` pin); the section renders "unavailable" with a nil backend and
  renders rows with one injected; nav length is 7 in both cases.
- **uiadapter:** `Apply(UpsertAutomation)` writes TOML that reloads identically; `Watch`
  delivers a real run event; no run record is ever fabricated without an execution.

## Verification

```
make verify-fast          # build + unit
make import-layers-check  # must show NO new internal/uiadapter edge
make structure-check      # file-LOC caps
make verify               # full suite, before push
make tidy                 # chunk 4: exactly one added module (robfig/cron/v3), no other churn
go list -deps ./internal/uiadapter | grep internal/automation   # expect NO output
```

## Risks

- The headless drive is read-verified, not runtime-verified. Chunk 2's wedge test is the
  mitigation and must land before chunk 6.
- `internal/uiadapter/settings.go` is already 623 lines and `session_pool.go` is at the
  structure hard cap; the setter and delegation go in `settings_automations.go`.
- The `ports` union change is breaking for `internal/ui/screen/settings` and its mocks;
  keeping `ActionRef.Workflow` as a compat alias limits the blast radius.
- `Start` being an exported method on an unexported type is legal but brittle to refactor;
  the compile-level test pins it.
- Skip-not-catch-up is a real behaviour choice a user may read as a bug ("my 2am job did
  not run because the laptop was closed"). Must be surfaced in the detail view.
- **DST fall-back double-fire (found during chunk 4's implementation, not anticipated at
  design time):** a cron schedule anchored inside a repeated local hour (e.g.
  `America/New_York`'s November fall-back 1:00-1:59am) fires **twice** on that calendar
  day under `robfig/cron/v3`'s wall-clock-only matching (`internal/cronschedule`'s
  `TestDSTFallBackFiresTwiceForWallClockOnlySchedule` asserts this against the real
  library). D7's per-fire `ClaimRunFenced` dedup prevents a *double-admitted run* only if
  the two fires collide on the same claim key within the claim's lifetime; two fires an
  hour apart do not collide and will legitimately produce two runs. Chunk 5/6 must decide
  whether this is accepted (rare, once a year, matches the underlying library's behavior)
  or worth a narrower mitigation (e.g. a per-automation minimum inter-fire spacing) before
  `automations serve` ships.
- Worktree accumulation with no GC is a known, accepted v1 cost.
- Without `automations serve` wired into an OS scheduler, nothing fires while the app is
  closed. This is the accepted v1 boundary; a standalone always-on daemon is deferred
  until a driver OS-cron cannot satisfy (sub-minute intervals, or a containerized
  always-on target).

## Resolved questions

All three prior open questions are closed; see D10 (cron dependency approved), D1 (atomic
rename), and D15 (slash allowlist). No open questions remain.
