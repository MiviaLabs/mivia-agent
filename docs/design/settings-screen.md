# `/settings` full-screen — implementation plan

Reviewed by a planner pass and a hostile pass, both against the real tree.
Product directive: **everything wired in the UI — full CRUD, save, read,
async state — against a ports knob, integrated later.** That is also what
`docs/design/ui-isolation.md` already requires of this phase.

## 1. Layout

```
 <  mivia                                    settings > models   [ demo/mivia-fast ] [ 1% ]

   General      │  Providers                                            3 configured
 > Models       │
   MCP          │  > openrouter                       active   key set        4 models
   Agents       │    ollama                                    loopback       2 models
   Automations  │    deepseek                                  key missing    0 models
                │
                │  ─ openrouter ─────────────────────────────────────────────────
                │  base url   https://openrouter.ai/api/v1
                │  api key    OPENROUTER_API_KEY               set
                │  models     anthropic/claude-opus-5          200k ctx   high
                │             openai/gpt-5                     128k ctx   —
                │
 up/down move  left/right pane  enter edit  n new  x delete  / filter  esc back
```

Top bar and status row are kept. Nav is left, hard-capped. No composer on
this screen — that is what frees bare `n`/`x`/`/`/`space` (ux rule 6.2) and
makes `/` unambiguous as filter.

## 2. Frame

New package `internal/ui/screen/settings`.

- Push/pop, `ViewFlags{AltScreen:true}`, own keymap context: pattern from
  `screen/transcript`.
- Topbar + `reservedRows` + `gutter` + `FillBG`: pattern from
  `conversation.go:401,457,477`. (Draft 1 wrongly named `transcript` for
  both; the pager has no topbar and no statusline.)
- `topbar.SetBreadcrumb([]string{"settings", section})` on every nav move;
  the model capsule stays — the Models section is *about* the active model.

Body height = `height - top.Height() - 1 - 1`. Property tests: `View()` emits
exactly `height` rows and no row exceeds `width`, across a width sweep, plus
`Height()`/row-count agreement. These are the frame's contract and ship with
the frame, not as a later slice.

## 3. Render primitive

`render.Split` puts nav on the right (`split.go:70-74`) and so does
`SplitDialog` (`split.go:82-83`) — draft 1 proposed reusing `SplitDialog` for
the editor and missed that. Both have exactly **one** consumer
(`filespanel.go:558,569`), so generalising is cheap.

Take the planner's signature over my 8-arg one — pure geometry, no policy,
no two adjacent `Side` values meaning different things:

```go
func SplitAt(t theme.Theme, tier theme.Tier, width, height, leftWidth int,
             focus Side, left, right string) string
```

`Split` and `SplitDialog` become one-line callers, byte-identical output.
Nav width is *settings* policy and lives in `internal/uikit/config`:
`SettingsNavMin = 14`, `SettingsNavMax = 24` (`render.SplitNavMax = 60` is a
file-list cap, far too wide for five words).

## 4. Ports — narrow, nil-able, persistence-shaped

The planner is right that one 20-method interface blocks independent slices:
the fake would have to implement all of it before the first section compiles.
Per-section interfaces in a struct of nil-able fields, matching the nil
`CommandRunner` convention (`commands.go:38`):

```go
type Settings struct {   // any field may be nil -> "unavailable"
    General     GeneralSettings
    Providers   ProviderSettings
    MCP         MCPSettings
    Agents      AgentSettings
    Automations AutomationSettings
}
```

Each section interface is read + apply:

```go
type ProviderSettings interface {
    Providers() []ProviderView
    Apply(ctx context.Context, scope Scope, e ProviderEdit) (SaveHandle, error)
}
type ProviderEdit interface{ isProviderEdit() }
type UpsertProvider struct{ Provider ProviderView }
type RemoveProvider struct{ Name string }
type UpsertModel    struct{ Provider string; Model ModelView }
type RemoveModel     struct{ Provider, Model string }
type ActivateModel   struct{ Provider, Model string }
```

Three things make integration wiring rather than redesign:

- **`Scope` is a parameter, not a struct field.** `internal/config` layers
  user/project/overlay (`load.go:361`) with separate `[mcp]`/`[hooks]`/
  `[verifiers]` loaders, so every future write must name its target file. A
  scope smuggled inside a value changes every signature at integration.
- **Typed `Edit` unions, no stringly-typed setter.** A typo is a compile
  error and the adapter can switch exhaustively.
- **Save is async and observable** — the "streaming state" ask:

```go
type SaveHandle interface {
    ID() string
    Events() <-chan SaveEvent   // Pending -> Validating -> Saved | Failed
    Cancel()
}
type SaveEvent struct{ State SaveState; Field, Message string }
```

Same shape as `ports.TurnHandle`, so the UI has one async convention. A plain
channel is not bubbletea, so semgrep `mivia.go.uikit-no-bubbletea-lipgloss`
is satisfied — **no `tea.Cmd` may appear in any port signature.**

View field sets are copied verbatim from `internal/cli/config_cmd.go:61-83`
(`providerModelGroupJSON`, `modelSpecJSON`) so the adapter is a field copy.
Per its own comment, **`ReasoningDialect` is dropped** — an internal wire
detail a picker does not need.

`demoharness` implements all five over per-`Harness` state seeded from JSON
via an explicit constructor arg (semgrep forbids `init()` and package-level
state in `uikit`).

## 5. Secrets — the hostile pass killed my draft-1 claim

I claimed the read model "carries no secret field, so a mistake is a compile
error". **That was wrong.** Verified against `internal/config/types.go:48-65`:
`Args` (`npx -y srv --token=sk-live-…`), `URL`
(`https://user:pw@host?api_key=…`), `Command`, and any transport
`FailReason` all hold secrets under innocuous names. My proposed
`key|token|secret|password` name test catches none of them — false assurance,
worse than no test.

`internal/redact` cannot rescue it: `Policy`'s zero value redacts nothing by
design (`redact.go:28-33`), it is compiled from config (`Compile`, `:43`), and
`internal/uikit` has no policy source. Adding one inverts the dependency this
phase exists to avoid.

Full CRUD makes exposure unavoidable — you cannot edit an argument list you
may not see. So: containment, five parts.

1. **Masked by default, revealed on intent.** List and detail render
   `Args`/`URL` through a masker eliding after `--token=`, `--api-key=`,
   `key=`, and URL userinfo/query. `ctrl+r` reveals the focused field only.
2. **`URL` splits at projection.** The list view gets `Endpoint` =
   `scheme://host/path`. The full URL lives only in the edit form's value.
3. **`FailReason` is an enum**, not a raw error: `FailKind` ∈ `{Spawn,
   Connect, TLS, Timeout, Protocol, Auth}` plus a short adapter-authored
   message. Transport strings never reach the UI.
4. **`SaveEvent.Message` names the field, never the content.** On the type.
5. **A taint test, not a name test.** Seed every string field with
   `sk-test-not-real-CANARY`, render every section at every size and tier,
   assert the canary appears in no frame and no golden. Plus a seed lint:
   `scripts/secret_scan.py` is high-confidence-only by design and will not
   flag a realistic-looking fake.

`AgentView.SystemPromptChars` (a length, not the text) stays.

## 6. Keys

- **Global key is `f2`.** No function key is bound anywhere in the new UI
  (checked f1-f12: zero), and ux rule 1.1 permits function keys. `ctrl+g` was
  the obvious candidate and is not free: it is `IDCollapseAll` in
  `ContextTranscript` (`keymap.go:174`), live and implemented
  (`keys.go:402`). It is only *partially* shadowed — `ContextTranscript` is
  consulted only while a block holds focus (`keys.go:82`) — but a key that
  works in the resting state and silently collapses blocks otherwise fails ux
  rule 1.4 ("the hint must state the complete truth for the current state").
  `ctrl+t/o/n/r` are taken; `ctrl+p`/`ctrl+l` are reserved by
  `wireframes-panes.md` §15. `f2` conflicts with nothing and contradicts no
  spec. `ctrl+,` can be added later as a Kitty-protocol bonus, the pattern ux
  rule 4.4 already establishes for `shift+enter`.
  Note: `internal/uikit/keymap` is imported only by `internal/ui/**`;
  `internal/cli` does not use it. These bindings are the new UI's own, not
  stale copies from the old TUI, so they are ours to change if we choose to.
- **Delete is `x`, not `d`** — `d` means "diff or source" in the adjacent
  files list (`keymap.go:240`); rebinding it to a destructive action next
  door is a muscle-memory hazard `Collisions()` cannot flag.
- `up/down j/k` move, `left/right h/l` pane, `enter` edit, `n` new, `space`
  toggle, `/` filter, `ctrl+r` reveal, `esc` back (confirm if dirty), `?` help.
  **No `tab`** (ux rule 6.1).
- `ContextSettings` must be added to `Map.Help()`'s `order` — the planner
  corrected me here: this is **test-caught**, not silent
  (`TestHelpCoversEveryBindingAndContext`, `keymap_test.go:134`). Do not copy
  `TestPagerHelpIsGeneratedFromTheTable`'s hardcoded count.
- Glyphs: `>` and reverse video only. `wireframes-panes.md` §3 — the Unicode
  and ASCII sets are the same set. No new symbols, no tier branch.
- Focus shown by rule colour **and** row marker + `RoleBGSelection`, never
  colour alone (6.3/6.4).

## 7. Components

Planner's cut, accepted: **`component/field` only**, two kinds —
`KindText` (the only one wrapping `textinput`, with the `SetTheme` restyle
`composer.go:79-96` proves mandatory) and `KindChoice` (an inline cycler
covering bool and enum, no textinput, so invalid values are unreachable).
Validation is an injected `Validate func(string) error`, not a `Kind` variant.

**`component/form` is dropped.** The ordered-field cursor is ~40 LOC inside
the first section that needs an editor; promote it when a second one does.

Fields are constructed **eagerly**, not on `n`: the theme walk
(`staleThemes`, `router_integration_test.go:249`) only reaches values live on
`app.Model`, so a lazily-built field would keep bubbles' hard-coded styles
while the walk stayed green.

## 8. Section abstraction

One small interface, one file per section, so five sections cannot each
re-derive the list/detail/keys plumbing:

```go
type section interface {
    Title() string
    SetSize(w, h int)
    SetTheme(t theme.Theme, tier theme.Tier)
    Update(msg tea.Msg) (section, tea.Cmd)
    View() string          // detail body only, never the frame
    Hints() []keymap.ID
}
```

`Screen` owns frame, nav, focus, notice, save state — nothing else. Models is
pre-split into `models.go` (providers + activate) and `models_detail.go`
(models within a provider) from the first commit, not retro-split under a
failing gate.

## 9. Slices — six, each independently landable

| # | Content | Ships |
|---|---|---|
| S1 | `render.SplitAt`; `Split`/`SplitDialog` become callers; nav-width constants | no user change |
| S2 | ports types + five interfaces + `SaveHandle`; demoharness fake + seeds; taint test; seed lint; drift guard | no user change |
| S3 | frame, nav, keymap, `/settings` command, property tests, docs | `/settings` opens; panes say "unavailable" |
| S4 | General + Automations (flat toggle lists) | two sections live |
| S5 | Models + CRUD (pre-split) | the section with real demand |
| S6 | MCP + Agents + CRUD | complete |

Docs ride with S3 and are amended by S4–S6. `docs/design/settings-screen.md`
needs no `OWNERS.yaml` edit — `ui-design-phase0` owns the directory — but it
must pass `check_prose.py` (ASD-STE100) and `check_names.py`.

## 10. Gates the draft missed

- `.mivia/policy/go-structure.json`: **`commentBlockLines` soft 25 / hard 30**,
  baseline closed to new files. This repo's house style is dense WHY comments;
  it is the gate most likely to bite a new UI package. Files 500/800, funcs
  80/120, tests 800/1200.
- semgrep `uikit-no-bubbletea-lipgloss` (:373), `uikit-ui-no-init` (:411),
  `uikit-ui-no-package-level-sync-state` (:424), `ui-no-raw-print` (:396).
- The genericity rules (`generic_surface_test.go`, `prompt_generic_test.go`)
  do **not** apply — they govern `internal/tools` and `internal/cli`.
- `TestSelectingThemeLeavesNoValueOnTheOldTheme` needs a **fixture** change
  (push settings before the walk), not a test-body change.
- `cmd/mivia-ui/main_test.go:294 TestMockCommandsAreRealCandidates` requires a
  non-empty `Desc` on the new `/settings` entry.
- ADLC: 7 steps per slice. Six slices, not ten — the slice count is itself a
  cost.

## 11. Decisions carried, with their caveats

- **Activate = "takes effect on next start"** for a provider. `/provider`
  cannot switch at runtime (`chat_slash_handlers.go:147`); model-within-
  provider *is* live. The UI must say which, not pretend.
- **MCP enable/disable has no runtime API** (`Failures()` is the only status
  source). The knob defines the intent, the fake honours it, the state badge
  reads `Unknown`. Growing `internal/mcp` is the adapter's first job.
- **Automations** — see §12. The SDK scheduler is real; the automation
  domain model is not, and we define it.


## 12. Automations — against the real SDK

Verified in `/home/mac/projects/mivialabs/mivia-ai-sdk`. Both halves of the
directive check out, with one large caveat.

**What exists.** `scheduler.Scheduler` (`scheduler/scheduler.go:43`) with
`Add(id, Schedule, Job) error`, `Remove(id) bool`, and `Run(ctx, *events.Bus)
error`; `Job` is `func(ctx) error` (`:14`). `trigger.Registry`
(`trigger/registry.go:51`) with `Add(name, Condition, Action) error`,
`Remove`, and `Fire(ctx, name) error`. Both are shipped, tested, API-locked.

**What does not exist — at all.** No `Automation` entity. No run record, run
state, run history, run detail, duration, attempt count, or log. No trigger
kind enum (`MANUAL`/`SCHEDULED` appear nowhere). No cron parser. No
persistence of jobs. No way to *list* what is registered — `entries` is
unexported and there is no `List()` or `NextFor(id)`.

So "view run details" has nothing behind it today. **We define the domain
model.** That is the correct outcome for a knob-first phase, and the SDK's own
plan invites it: the trigger package shipped over an architecture review that
recommended holding it "until a caller scoped it". This screen is that caller.

### The division of labour

The ports store is the **source of truth** for the automation list and run
history. The SDK scheduler is only the firing mechanism — it cannot be
enumerated, and a one-shot schedule is *deleted* from its map once it fires
(`scheduler/run.go:131`), leaving no evidence it ever existed. Reading state
back out of the scheduler is not possible and the design must not assume it.

### Ports types

```go
type Automation struct {
    ID, Name, Description string
    Enabled  bool
    Trigger  TriggerSpec
    Action   ActionRef        // what it runs: a workflow name, for now
    LastRun  *RunSummary
    NextFire *time.Time       // computed by the adapter, nil if never
    Scope    Scope
}

type TriggerKind int          // TriggerManual | TriggerScheduled  (extensible)
type TriggerSpec struct {
    Kind     TriggerKind
    Schedule *ScheduleSpec    // set when Kind == TriggerScheduled
}

// ScheduleSpec is SERIALISABLE, unlike scheduler.Schedule, which is an
// opaque Go interface with unexported implementations and no JSON shape.
// The adapter converts it to scheduler.Every / scheduler.At / the new
// cross-platform recurring schedule (see §14) - all three satisfy the same
// scheduler.Schedule interface, so Scheduler.Add never changes.
type ScheduleSpec struct {
    Kind     ScheduleKind     // ScheduleInterval | ScheduleAt | ScheduleRecurring
    Every    time.Duration    // Kind == ScheduleInterval
    At       []time.Time      // Kind == ScheduleAt
    Cron     string           // Kind == ScheduleRecurring, 5-field cron expression -
                               // kept as plain text here, not internal/cronschedule.Spec,
                               // so ports stays free of the SDK-shaped dependency; the
                               // adapter calls cronschedule.Parse(cron, loc) (see §14)
    TZ       string           // IANA zone; Kind == ScheduleRecurring, required
}

type Run struct {
    ID, AutomationID string
    Trigger    TriggerKind    // how this run started
    State      RunState       // RunPending|RunRunning|RunSucceeded|RunFailed|RunCancelled
    StartedAt  time.Time
    EndedAt    *time.Time
    FailKind   FailKind       // enum, not a raw error (see §5)
    Message    string         // adapter-sanitised
}
type RunSummary struct{ ID string; State RunState; StartedAt time.Time }
```

`RunCancelled` is ours: the SDK has no cancellation and the `ledger`'s status
set has no `cancelled` or `timed_out` either. Including it now is cheap;
retrofitting a state into a persisted enum is not.

### Interface

```go
type AutomationSettings interface {
    Automations() []Automation
    Runs(automationID string, limit int) []Run   // newest first
    Run(runID string) (Run, bool)
    Apply(ctx context.Context, scope Scope, e AutomationEdit) (SaveHandle, error)
    Watch(ctx context.Context, automationID string) (RunHandle, error)
}
type RunHandle interface { Events() <-chan Run; Cancel() }

type UpsertAutomation struct{ Automation Automation }
type RemoveAutomation struct{ ID string }
type SetAutomationEnabled struct{ ID string; On bool }
type TriggerAutomation   struct{ ID string }   // manual fire
```

`Watch` reuses the channel convention `TurnHandle` and `SaveHandle` already
set, so a live run streams into the run-detail pane with no third async shape.

### Constraints the SDK forces on the UI

1. **Reject non-positive intervals.** `scheduler.Every(d)` with `d <= 0`
   returns a never-firing schedule and `Add` accepts it
   (`scheduler/schedule.go:29-37`) — a silently dead automation with no error.
   The field validator rejects it before it reaches the port.
2. **Warn on an all-past `At` set** — it registers, never fires, and is then
   dropped from the scheduler.
3. **Cron IS in scope** (weekday/time-of-day schedules — "weekdays at 9am" —
   are a hard requirement). The SDK declined a parser: "A caller who wants
   cron syntax writes their own type that satisfies `Schedule`." We are that
   caller. This is deliberately **not** host cron (no crontab/launchd/Task
   Scheduler) — it is an embedded, cross-platform `Schedule` implementation
   that fires only while the mivia harness process is running, matching the
   SDK's own non-durable, in-process stance. It lives outside `internal/ui`
   and `internal/uikit` (harness-side, not UI code) and is being planned as
   its own sub-effort in parallel — see §14.
4. **No timezone handling anywhere** in the SDK — `Every` is plain duration
   arithmetic, so there is no DST correctness to inherit or rely on. The UI
   shows local time and says so.
5. **Only failures are observable, as a flat string.** `JobFailedEvent`'s
   `Data` is `fmt.Sprintf("job %s failed: %v", id, err)` — no run id, no
   structure, and a raw error. It is **tainted** by §5's rule: the adapter
   parses and sanitises it into `FailKind` + message; the UI never sees it.
   There is no success event at all — run completion is the adapter's to
   record.
6. **Manual fire returns only `error`** — no run id. The adapter mints the run
   id before calling `Fire`.

### Section UX

Two panes inside the detail pane: the automation list, and — on `enter` — its
detail with schedule, trigger, next fire, and run history. `t` triggers a
manual run and switches to the live run view. `space` toggles enabled.

## 13. Revised slices

| # | Content | Ships |
|---|---|---|
| S1 | `render.SplitAt`; nav-width constants | no user change |
| S2 | ports types + interfaces + `SaveHandle`/`RunHandle`; demoharness fake + seeds; taint test; seed lint; drift guard | no user change |
| S3 | frame, nav, keymap, `/settings`, `f2`, property tests, docs | `/settings` opens |
| S4 | General | one section live |
| S5 | Models + CRUD (pre-split) | the section with real demand |
| S6 | MCP + Agents + CRUD | |
| S7 | Automations + CRUD + run history + live run | the largest section |

Automations is its own slice, not a toggle list bolted onto General: it is the
only section with a second-level detail view, a history list, and a streaming
state.

### Delivery status: S1-S7 shipped

All seven slices landed. Two scope notes, both explicit at the call site
(a user-facing notice, not a silent no-op) rather than left implicit:

- **Projects and Skills are placeholders.** Neither has a ports-backed
  domain model - no research or design has scoped one, unlike Automations
  (§12), which got exactly that treatment before it was built. They render
  "unavailable" like any nil-backed section and occupy their planned nav
  positions so the ordering (`General, Projects, Automations, Agents,
  Skills, Models, MCP`) does not shift when they are defined.
- **Creation ("n") is not wired for Models, MCP, or Agents.** Browse,
  activate, toggle, and remove are real, store-backed CRUD verbs.
  Creating a provider, an MCP server, or an agent needs a multi-field
  entry flow this pass did not build (`component/form` was cut in §7 for
  the same reason); "n" reports a clear notice rather than doing nothing
  or crashing. General's fields are all pre-set choices, so General has
  no such gap.

A real bug worth recording: `Screen.Update`'s original switch handled only
`WindowSizeMsg`/`ThemeChangedMsg`/`KeyPressMsg` and dropped everything
else, so a section's own async save-result message (`generalSavedMsg`,
`agentsFailedMsg`, and so on) never reached it - the write still landed in
the store, but the section never learned whether it succeeded, and a
`SaveFailed` notice never surfaced. Caught by
`TestRemovingTheDefaultAgentFailsAndKeepsIt` failing with an empty notice
despite the demoharness's guard actually firing. Fixed by forwarding any
message `Update`'s switch does not otherwise recognise to the nav-selected
section, unconditionally (not gated on which pane has focus, since the
user may already have pressed `esc` back to the nav pane while a save from
that section was still in flight).

## 14. Cross-platform recurring schedule ("weekdays at 9am")

Harness-side, not UI-side: a `scheduler.Schedule` implementation living
outside `internal/ui`/`internal/uikit`, called by the eventual real adapter,
never imported by the UI directly. Planned separately because it has its own
dependency decision, its own correctness burden (DST), and its own ADLC
cycle — folding it into a UI slice would blur all three.

### Decision: hand-roll, do not vendor `robfig/cron`

`.agents/rules/30-go-standards.md` ("avoid new third-party dependencies
unless justified by risk, maintenance, and security review; prefer stdlib")
and `.agents/doctrines/engineering-working-contract.md:49` ("avoid
speculative frameworks... unnecessary dependencies") both cut the same way,
and `mivia-ai-sdk/docs/plans/scheduler.md:33-37` already named the
alternative explicitly: "a caller who wants cron syntax writes their own type
that satisfies `Schedule`." That caller is us. The 5-field grammar plus a
weekday/time-of-day case is a small, closed problem — a ~400-600 LOC package,
not a reason to pull in a dependency and its transitive `go.sum` for
capability we'd use a fraction of. Only the *interface shape* and the
*recompute technique* (below) are borrowed conceptually from `robfig/cron`,
same as the SDK plan itself did for `Schedule`.

### Package: `internal/cronschedule`

Leaf package, stdlib only plus `mivia-ai-sdk/scheduler` for the `Schedule`
type it satisfies. Never imports `internal/ui`/`internal/uikit`; the
dependency runs one way — settings/ports → `cronschedule` → SDK scheduler.

```go
type Spec struct {
    Minute, Hour, DayOfMonth, Month, DayOfWeek Field
    Location *time.Location   // mandatory; no silent server-local default
}
type Field struct{ /* sorted deduped values in [min,max], or wildcard */ }

func FieldSet(min, max int, values ...int) (Field, error)
func FieldAny(min, max int) Field
func Weekdays(loc *time.Location, hour, minute int, days ...time.Weekday) (Spec, error)
func Parse(expr string, loc *time.Location) (Spec, error)   // standard 5-field cron text
func (s Spec) Validate() error
func (s Spec) String() string                                 // canonical cron text, round-trips through Parse
func NewSchedule(s Spec) (scheduler.Schedule, error)           // validates, then builds the Next() implementation

type ParseError struct{ Field, Token string; Err error }      // "hour: '25' is out of range 0-23"
```

DOM/DOW combine with **OR**, not AND, when both are restricted — standard
POSIX cron semantics and the rule most commonly gotten wrong, pinned by a
named test (`"0 0 1,15 * MON"` fires on the 1st, the 15th, *and* every
Monday).

### DST/timezone correctness

The one decision that matters: **`Next` recomputes the candidate instant via
`time.Date(year, month, day, hour, minute, 0, 0, loc)` on each candidate day
and never advances by `Add(duration)`.** Duration math is exactly wrong here
— `Add(24*time.Hour)` drifts by an hour across every DST transition, firing
"daily at 9am" at 8am or 10am. This is `robfig/cron`'s own technique, reused
without vendoring the library.

Verified against `time.Date`'s documented contract (go1.26): a skipped hour
(spring-forward) normalizes forward by the gap length for a schedule that
falls inside it; a repeated hour (fall-back) resolves deterministically to
one specific offset, so a schedule fires exactly once, never twice. "Weekdays
at 9am" itself never touches either transition window and is unaffected by
construction — but the test suite pins it explicitly anyway
(`TestNextAcrossDSTWeekdaysAt9am`, run across a real spring-forward and a
real fall-back Monday) because it's the flagship scenario and the test most
likely to catch a duration-math shortcut, so it's written **first**, before
the easy cases, as the red test that keeps the implementation honest.
Feb-29-only schedules are tested for correct skip-to-next-leap-year, since
`time.Date` silently normalizes a non-leap Feb 29 into Mar 1 rather than
erroring — the search loop must explicitly reject that, not trust the
returned time's fields. A second IANA zone with an inverted-hemisphere
transition calendar (`Pacific/Auckland`) catches any hardcoded
northern-hemisphere assumption. An unsatisfiable spec (Feb 30) degrades to
the zero `time.Time` after a bounded search cap — reusing
`scheduler.Schedule`'s own "zero time means never fires" contract rather than
inventing a new one.

### Missed-fire policy: skip, not catch-up — stated, not defaulted into

`Next` is a pure function of `after`; it has no memory of what should have
fired while the process was down. On restart, `Next(time.Now())` searches
only forward, so a missed fire is simply never returned — no special-casing
needed, and none is possible, since the pure function has no side channel to
a persisted last-fired timestamp. This matches the SDK's own non-durable
`Scheduler` (in-memory only, explicitly does not survive a restart) and is
the right default for an agent-automation feature: firing a missed 9am job at
2pm because the laptop was asleep is a surprise, not a convenience. The
settings screen states this in copy ("automations only run while mivia is
running; a missed time is skipped, not caught up"), and catch-up, if ever
wanted, is a persistence-layer feature entirely outside this package's scope.

### Delivery — five slices, each independently red→green

1. `Field`/`Spec`/`Validate` — the data model and validation rules, no
   parsing or `Next` yet.
2. `Parse` — the cron-string grammar, plus a `FuzzParse` target (a hand-rolled
   parser over user-typed or config-synced text is attacker-adjacent input).
   Pre-split into `parse.go` (5-field entrypoint) + `parse_field.go`
   (range/step/list grammar) if the combined file threatens the 500-LOC soft
   cap, rather than splitting retroactively under a failing gate.
3. `Next`/`Schedule`/`NewSchedule` — `TestNextAcrossDSTWeekdaysAt9am` written
   first, before the non-DST cases.
4. DST/timezone hardening — the two-zone, leap-day, fall-back-ambiguity cases,
   kept as its own slice since these are the cases most likely to need a
   second implementation pass.
5. `String()` + docs + the one-line cross-reference to the settings screen's
   `ScheduleSpec` (comment only — no code coupling in either direction beyond
   `NewSchedule`'s existing signature).

Each slice: `make verify`, `go test -race ./internal/cronschedule/...`, the
Step 5 hostile-bug-audit loop before commit. Coverage target 85%+, matching
the SDK scheduler package's own floor, since this repo does not publish a
single per-package number beyond `make verify`'s gates.
