# Automations

Automations run scheduled or manually triggered background work: prompts, skills, agents, slash commands, and workflows, executed in order in a headless session.

## Overview

An automation executes an ordered list of steps in a background session. The session can run directly in the workspace or in a fresh managed Git worktree.

Automations use two storage backends:
- Definitions are stored in TOML files on disk.
- Execution history is stored in a SQLite database.

The feature provides:
- Multiple schedule types: fixed intervals, explicit timestamps, and standard cron expressions with timezone support.
- Single-fire fenced claims that prevent duplicate concurrent runs.
- Headless execution with strict unattended tool approval policies.
- Step-level checkpointing and manual resume for failed or interrupted runs.
- CLI subcommands under `mivia automations`.
- An interactive management interface in the Settings screen, including the ability to cancel a run in progress.

## Configuration and Storage

### File Locations

Automation definitions are stored in `automations.toml` files at two configuration scopes:

- **Project scope**: `<workspaceRoot>/.mivia/automations.toml`
- **User scope**: `~/.mivia/automations.toml`

The TOML document contains a table of automation specifications keyed by automation ID:

```toml
[automations.<id>]
# Automation fields
```

Run history and fenced claims (see [Run History Schema](#run-history-schema)) live in a SQLite store, not `automations.toml`. It is the same store a chat session uses for its own context checkpoints, so the CLI (`mivia automations` list/show/run/runs/resume/serve) and the TUI's Automations settings panel always see the same run history: a run triggered from one surface is immediately visible to the other, and a `serve` sweep can find and mark interrupted a run either surface left running.

The store resolves through the same rule a chat session uses: an explicit `[subagents] store_path` in `mivia.toml` (a relative path resolves against the workspace root), or otherwise the shared, install-wide default `~/.mivia/context.db` used by every workspace on the machine.

A worktree run's session history is not namespaced by its worktree directory - it shares the same store as every other run, so it appears alongside the rest of that automation's history rather than isolated per worktree.

### Atomic Writes

When writing `automations.toml`, the store uses an atomic write sequence:
1. Create a temporary file with mode `0600` in the target directory.
2. Encode the TOML document and write the data.
3. Flush and synchronize bytes to disk with `Sync()`.
4. Close the temporary file.
5. Rename the temporary file over the target file path.

This sequence ensures a process crash never leaves a truncated or corrupt configuration file.

### Identification Rules

Automation IDs and run IDs must match the regular expression:

```text
^[a-z0-9][a-z0-9_-]{0,63}$
```

An ID must:
- Start with a lowercase alphanumeric character.
- Contain only lowercase letters, digits, underscores, and hyphens.
- Have a total length between 1 and 64 characters.

The store validates IDs at load time and rejects invalid identifiers.

### Run History Schema

Run history is persisted in the `automation_runs` SQLite table:

| Column | Type | Description |
| --- | --- | --- |
| `id` | TEXT PRIMARY KEY | Unique run identifier (for example `run-1a2b3c...`) |
| `automation_id` | TEXT NOT NULL | Identifier of the parent automation |
| `origin` | TEXT NOT NULL | Trigger origin: `manual` or `scheduled` |
| `state` | TEXT NOT NULL | Run state: `pending`, `running`, `succeeded`, `failed`, `interrupted`, `cancelled`, or `skipped` |
| `step_index` | INTEGER NOT NULL | Current or next step index to execute |
| `step_count` | INTEGER NOT NULL | Total number of steps in the action |
| `session_name` | TEXT NOT NULL | Saved background chat session's own catalog id (legacy rows used `__auto__<automation_id>__<run_id>`) |
| `worktree_path` | TEXT NOT NULL | Filesystem path of the managed worktree |
| `worktree_branch` | TEXT NOT NULL | Git branch name for the managed worktree |
| `claim_token` | TEXT NOT NULL | Active fenced claim token |
| `started_at` | TEXT NOT NULL | RFC3339 start timestamp |
| `ended_at` | TEXT | RFC3339 completion or failure timestamp |
| `fail_kind` | TEXT NOT NULL | Failure classification category |
| `message` | TEXT NOT NULL | Status, error, or cancellation message |

An index on `automation_id` accelerates history queries.

## Defining an Automation

### Specification Fields

An automation definition in `automations.toml` supports the following fields:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string | Yes | Unique identifier matching `^[a-z0-9][a-z0-9_-]{0,63}$`. |
| `name` | string | Yes | Human-readable display name. |
| `description` | string | No | Description of the automation purpose. |
| `enabled` | boolean | Yes | Enables or disables scheduled execution. |
| `worktree` | integer | No | Worktree mode: `0` (workspace root) or `1` (fresh managed worktree). Default is `0`. |
| `base_ref` | string | Conditional | Git base ref (such as `HEAD` or a branch name). Required when `worktree = 1`. Forbidden when `worktree = 0`. |
| `unattended` | string | No | Approval policy for tool calls: `"deny"` (default) or `"auto"`. |
| `trigger` | table | Yes | Trigger definition table. |
| `steps` | array | Yes | Non-empty ordered list of step tables. |

### Trigger Table (`trigger`)

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `kind` | integer | Yes | Trigger kind: `0` (manual only) or `1` (scheduled). |
| `schedule` | table | Conditional | Schedule configuration table. Required when `kind = 1`. |

### Schedule Table (`trigger.schedule`)

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `kind` | integer | Yes | Schedule kind: `0` (interval), `1` (specific times), `2` (recurring cron). |
| `every_seconds` | integer | Conditional | Period in seconds. Required and must be positive when `kind = 0`. |
| `at_times` | array of strings | Conditional | List of RFC3339 timestamp strings. Used when `kind = 1`. |
| `cron` | string | Conditional | Standard 5-field cron expression. Required when `kind = 2`. |
| `tz` | string | No | IANA timezone name (for example `America/New_York`). Defaults to `UTC` when empty. |

### Step Table (`steps`)

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `kind` | string | Yes | Step kind: `"prompt"`, `"skill"`, `"agent"`, `"slash"`, or `"workflow"`. |
| `ref` | string | Conditional | Reference target: skill name, agent name, slash command, or workflow name. |
| `prompt` | string | Conditional | Prompt text sent to the model or agent. |
| `inputs` | table | No | String key-value input parameters for workflow steps (`kind = "workflow"`). |

### Step Kinds

The executor processes steps in sequential order within a single session:

- **`prompt`**: Sends `prompt` text directly to the active conversation turn.
- **`skill`**: Resolves `ref` against the workspace's skill catalog and sends that skill's full rendered instructions, the same way an interactive `/<skill>` invocation does. Only the short command form (`/<skill> <args>`) is kept in the session's persisted history; the full instructions body is sent to the model for that one turn and not replayed on later turns. `ref` may be given with or without a leading slash. A `ref` that does not resolve to a user-invocable skill in the configured registry fails the step, and fails spec validation up front when a registry is available at save time.
- **`agent`**: Selects the agent named in `ref` on the session state, then sends `prompt`.
- **`slash`**: Executes the slash command in `ref`. The command must satisfy the headless allowlist.
- **`workflow`**: Dispatches the workflow named in `ref` to the workflow engine with `inputs`.

### Configuration Examples

#### Scheduled Nightly Summary (Cron + Worktree)

```toml
[automations.nightly-summary]
id = "nightly-summary"
name = "Nightly Summary"
description = "Summarizes git changes in a fresh worktree"
enabled = true
worktree = 1
base_ref = "HEAD"
unattended = "deny"

[automations.nightly-summary.trigger]
kind = 1

[automations.nightly-summary.trigger.schedule]
kind = 2
cron = "0 2 * * *"
tz = "America/New_York"
every_seconds = 0

[[automations.nightly-summary.steps]]
kind = "prompt"
prompt = "Review commits from the last 24 hours and draft release notes."

[[automations.nightly-summary.steps]]
kind = "workflow"
ref = "publish-notes"
[automations.nightly-summary.steps.inputs]
channel = "internal"
```

#### Interval Health Check (Workspace Root)

```toml
[automations.health-check]
id = "health-check"
name = "Hourly Health Check"
description = "Runs lint and diagnostic checks every hour"
enabled = true
worktree = 0
unattended = "auto"

[automations.health-check.trigger]
kind = 1

[automations.health-check.trigger.schedule]
kind = 0
every_seconds = 3600
cron = ""
tz = ""

[[automations.health-check.steps]]
kind = "slash"
ref = "/compact"

[[automations.health-check.steps]]
kind = "prompt"
prompt = "Check repository diagnostics and report any compilation errors."
```

#### Manual Multi-Step Action

```toml
[automations.release-prep]
id = "release-prep"
name = "Release Preparation"
description = "Prepares release artifacts manually"
enabled = true
worktree = 1
base_ref = "main"
unattended = "deny"

[automations.release-prep.trigger]
kind = 0

[[automations.release-prep.steps]]
kind = "agent"
ref = "code-reviewer"
prompt = "Audit open pull requests for merge readiness."

[[automations.release-prep.steps]]
kind = "skill"
ref = "generate-changelog"
```

## Scheduling

### Schedule Types

1. **Interval (`kind = 0`)**: Executes periodically based on `every_seconds`. Each new deadline adds `every_seconds` to the current time.
2. **Specific Times (`kind = 1`)**: Executes at explicit RFC3339 timestamps listed in `at_times`. When all timestamps lie in the past, the schedule is exhausted and produces no further fires.
3. **Recurring Cron (`kind = 2`)**: Evaluates a 5-field cron expression in the configured timezone.

### Cron Grammar and Timezones

The cron parser evaluates standard 5-field cron expressions:

```text
┌───────────── minute (0 - 59)
│ ┌───────────── hour (0 - 23)
│ │ ┌───────────── day of month (1 - 31)
│ │ │ ┌───────────── month (1 - 12)
│ │ │ │ ┌───────────── day of week (0 - 6) (Sunday to Saturday)
│ │ │ │ │
* * * * *
```

Timezone evaluation uses IANA timezone identifiers (such as `America/Chicago` or `Europe/London`). An empty `tz` string defaults to `UTC`.

### Skip-Not-Catch-Up Policy

The scheduler never catches up missed runs. If a machine sleeps or the daemon stops, missed fire intervals are dropped.

When the scheduler wakes, it evaluates the next fire timestamp strictly after the current wall-clock time. This rule prevents fire storms when a system resumes after downtime.

### Daylight Saving Time Transitions

Cron expressions handle Daylight Saving Time (DST) changes as follows:
- **Spring Forward**: If a scheduled local time falls inside a skipped hour, the scheduler skips that day.
- **Fall Back**: Standard 5-field cron matches local wall-clock fields. If a schedule falls inside the repeated hour, it fires once at each UTC offset during that transition day.

## Execution Model

### Triggering and Watching a Run

Triggering an automation, whether from the CLI or the Settings screen, admits the run immediately (spec lookup, claim, and the initial run row) and then executes its steps in the background. From the TUI, this means starting a run never blocks the interface: the screen stays responsive while the run executes, and the automation's detail panel streams the run's state - pending, running, and its terminal outcome - as it happens.

A run in progress can be cancelled from the Settings screen while it is pending or running. A cancelled run is recorded distinctly from one that was interrupted by a shutdown: cancelling is a deliberate operator action and is not resumable in the same way an interrupted run is.

### Fenced Single-Fire Claims

The engine uses fenced claims to ensure exactly one run executes per automation at any time:
- Each automation uses the claim key `automation:<automation_id>`.
- The executor attempts to acquire a fenced claim token before starting execution.
- If a scheduled fire loses the claim race, the fire is skipped. The engine records a `skipped` run row with a nil error.
- If a manual trigger or resume operation loses the claim race, the operation fails with `ErrRunAlreadyActive`.
- The claim releases automatically when the run completes, fails, is cancelled, or is interrupted.
- While a run executes, its claim is periodically refreshed so a long-running step does not make the run look abandoned to a concurrent interrupted-run sweep.

### Managed Worktrees

When an automation configures `worktree = 1`, the executor isolates execution:
1. Resolves `base_ref` against the repository.
2. Calls the worktree manager to create a directory named `auto-<automation_id>-<run_id>`.
3. Creates a dedicated Git branch with the configured worktree prefix.
4. Initializes the background session inside the new worktree directory.
5. Persists the worktree path and branch in the `automation_runs` table.

If worktree creation fails, the run terminates immediately with zero side effects.

Worktrees are not automatically removed after a run finishes. The resulting Git branch remains available for user inspection and manual cleanup.

### Headless Session Safety

Automations run in headless background sessions without an interactive terminal.

To prevent goroutine leaks and stalled turns, the headless runner enforces two rules:
1. **Drain to Close**: The runner reads the conversation turn event channel until the channel closes. It never terminates early on intermediate turn events.
2. **Context Deadlines**: Every headless turn executes under a strict context timeout. The default timeout is 10 minutes when unset in configuration.

Each run spawns an independent session. Session wedges cannot propagate across runs.

An automation's session in the TUI is isolated from whatever the operator is doing in the foreground: its turns never take over the foreground's live subagent-progress display, and its startup never plants a tool-adoption warning meant for a later, unrelated foreground action. Dispatched subagent threads from a background run are still visible in the same subagent registry the foreground session reads, so a background run's own subagent activity can appear in the foreground's subagent panel.

### Unattended Approval Policy

Automations run unattended and cannot prompt a human operator for tool call approvals:

- **`unattended = "deny"` (default)**: Any tool call requiring interactive approval fails immediately. The failure records the error message `automation "<id>": unattended run denies all tool approvals`.
- **`unattended = "auto"`**: The engine automatically approves all tool calls requiring approval. Auto-approval settings apply only to the active run session and do not persist standing operator permissions.
- **Workflow Steps**: Workflow steps never enable publication delivery. Deliveries remain pending for human review.

### Slash Command Allowlist

Step definitions with `kind = "slash"` validate against a strict headless allowlist at load time. Unsafe slash commands are rejected when reading the configuration.

#### Allowed Commands

- **All Skill Commands**: Any custom skill registered in the workspace or user skill catalog.
- **`/compact`**: Safe to run bare without arguments.
- **`/model <value>`**: Allowed with an explicit model argument.
- **`/effort <value>`**: Allowed with an explicit reasoning effort argument.
- **`/budget <value>`**: Allowed with an explicit token budget argument.
- **`/steps <value>`**: Allowed with an explicit max steps argument.

#### Rejected Command Classes

The validator rejects built-in commands that require interactive capabilities:

- **Interactive UI Pickers**: `/worktrees`, `/sessions`, `/workflows`, `/queue`, `/agent`, `/title`, `/new`, `/select`.
- **Session Lifecycle Mutations**: `/clear`, `/save`, `/load`, `/delete`, `/resume`, `/exit`.
- **Unattended Outbound Effects**: `/search`.
- **Informational Output**: `/help`, `/status`, `/list`, `/session`, `/tools`, `/hooks`, `/agents`, `/plain`, `/provider`, `/workspace`.

## Resuming Runs

A failed or interrupted run can resume from its last completed step, picking up its full conversation history up to that point.

### Resumability Rules

- Only runs in `interrupted` or `failed` state can resume.
- The run record must contain a valid `session_name`: the saved background chat session's own catalog id.
- The automation definition must still exist in configuration.

A run spawned before this scheme existed saved its session under a reserved `__auto__<automation_id>__<run_id>` name. That scheme was chosen because the composed name could never collide with the auto-save name (`__last__`), and because automation and run ids are validated before composition. Rows with those legacy reserved names stay in the catalog, are hidden from the resume picker, and can no longer be written; a resume re-points the row to the id the restored session carries.

A new-format run resumed by another process refuses with a live-lease error once the first process's lease has been stamped (after its first heartbeat, about 40 seconds). Within that first interval an early resume can be admitted, and the old holder's next fenced write then fails with a stale-revision error instead, so fencing still prevents the silent two-writer corruption the old reserved-name fork allowed.

The clichat and CLI session listings are out of scope and are not filtered; only the TUI resume picker hides the legacy reserved rows.

### Resume Procedure

1. Acquire the fenced single-fire claim for `automation:<automation_id>`.
2. Locate the worktree path recorded on the run row, or use the workspace root.
3. Join the pooled session for `session_name` when one is already live, else spawn a fresh session; either way the saved transcript is restored before the caller proceeds.
4. Set the run state to `running`.
5. Start step execution at `step_index`. No index arithmetic is applied.
6. If all steps complete successfully, mark the run `succeeded`.

If the step index already equals or exceeds the total step count, the runner marks the run `succeeded` immediately without spawning a session.

Each step's checkpoint re-saves the run's full session transcript, on both the successful path and an ordinary step failure, so a resumed run has everything the earlier steps produced, not just its starting prompt. A resumed run's own checkpoints continue re-saving the same way, so a second interruption does not lose the progress the resume itself made.

### Live Session View

A run's session is a normal session in the catalog: `/resume` attaches to it, shows the transcript so far, and - while the run keeps going - streams the run's turns into the view live. The screen subscribes to the conversation's event fan-out; the run's own event stream keeps exactly one consumer, so the run cannot stall on a slow viewer. The status row shows the run's activity the same way a foreground turn's does, driven by the run's own turn framing. If the viewer falls behind, the screen reloads the transcript from history and resubscribes.

Sending is refused while a run executes on the session - from the composer and from remote steering - so a user turn cannot interleave into the run's transcript. Once the run reaches a terminal state, the session accepts sends like any other. The live view covers runs hosted by the same TUI process; a cross-process resume is refused at the session lease (see Resuming Runs).

## CLI Commands

The `mivia automations` subcommand group manages and executes automations.

All subcommands accept two common flags:
- `--workspace <dir>`: Target workspace directory. Defaults to `.`.
- `--config <path>`: Explicit configuration file path.

### `list`

Lists all defined automations across the active project scope.

```bash
mivia automations list [--workspace dir] [--config path]
```

Output includes:
- Automation ID
- Display Name
- Schedule / Trigger details
- Enabled status

### `show`

Displays full configuration details and recent run history for an automation.

```bash
mivia automations show <id> [--workspace dir] [--config path]
```

Output includes:
- Identification: ID, Name, Description, Scope, Enabled
- Worktree settings: Mode, Base Ref
- Approval policy: Unattended mode
- Trigger specification: Interval, At-times, or Cron expression
- Step definitions: Kind, Target reference, Prompt text
- Run history: Last 20 runs with ID, State, Origin, Start time, and Duration

### `run`

Triggers an immediate manual execution of an automation.

```bash
mivia automations run <id> [--wait] [--workspace dir] [--config path]
```

Flags:
- `--wait`: Outputs detailed multiline run progress and results instead of a single-line summary.

Execution runs synchronously. The command returns a non-zero exit code if the run fails.

### `runs`

Displays execution history across automations.

```bash
mivia automations runs [--automation <id>] [--limit <n>] [--workspace dir] [--config path]
```

Flags:
- `--automation <id>`: Filters run history to a specific automation ID.
- `--limit <n>`: Maximum number of runs to display. Defaults to `20`.

When `--automation` is omitted, the command aggregates runs across all automations, sorts them by start time descending, and applies the limit.

### `resume`

Resumes an interrupted or failed run from its last checkpointed step.

```bash
mivia automations resume <run-id> [--workspace dir] [--config path]
```

The command loads the saved session, re-acquires the execution claim, and continues execution.

### `serve`

Runs the background scheduling daemon.

```bash
mivia automations serve [--workspace dir] [--config path]
```

Lifecycle behavior:
1. Runs an initial crash-recovery sweep to mark orphaned running rows as `interrupted`.
2. Arms independent timers for all enabled scheduled automations.
3. Evaluates deadlines on a 30-second loop tick (`serveTickInterval`).
4. Dispatches due automations sequentially.
5. Shuts down cleanly when receiving `SIGINT` or `SIGTERM`.

## Settings UI

The TUI Settings screen includes an **Automations** section.

### Navigation and Display

- **Unbacked Rendering**: If no automation backend is wired, the section renders an "unavailable" notice rather than failing navigation.
- **Master List**: Displays a table of configured automations showing enabled state, name, and trigger format.
- **Detail Area**: Shows selected automation details, full step lists, and the last 5 runs.
- **Live Run View**: Triggering a run subscribes the view to live status events and shows real-time progress without blocking the rest of the interface.

### Interactive Form Editor

Pressing `a` or `e` opens the full-screen form editor:
- **New Automation (`a`)**: Displays input fields for ID, Name, Description, Enabled state, Trigger type, Interval duration, Action type, Prompt/Skill reference, and Unattended policy.
- **Edit Automation (`e`)**: Opens the same editor over an existing automation. The automation ID is immutable during edits.

Form shortcuts:
- `Tab` / `Shift+Tab`: Move between form fields.
- `Enter`: Submit and save changes atomically to `automations.toml`.
- `Esc`: Cancel editing and discard form changes.

Other keys, while browsing the automation list:
- `t`: Trigger a manual run of the selected automation.
- `s`: Cancel the selected automation's live run, while it is pending or running.
- `space`: Toggle the selected automation enabled or disabled.
- `x`: Remove the selected automation.

## Failure and Retry Behavior

### Step Failures

When a step fails:
1. Step execution halts immediately. Subsequent steps do not run.
2. The current step index is saved as `step_index` on the run row.
3. The run state transitions to `failed`.
4. The error reason is classified into `fail_kind` and written to `message`.
5. The fenced claim is released.

### Interrupted Run Sweep

If the daemon or the TUI shuts down while a run is in progress:
- A run stopped by a deliberate cancel is marked `cancelled`.
- A run that was still executing when the process shut down is marked `interrupted` and remains eligible for resume.
- If a process dies outright (crash, power loss) without marking anything, the run row stays `running` until its claim expires.

The next startup of `automations serve`, or the TUI's own startup sweep, scans all `running` rows: if a row's claim is expired or missing, the runner marks it `interrupted` and sets `ended_at`. Because the CLI and the TUI read and write the same store, a run left `running` by either surface is found and marked `interrupted` by whichever one starts next - a CLI `serve` daemon restarted after a TUI session crashed, or a TUI relaunch after a `serve` daemon was killed, both see and resolve the same stuck row.

## Limitations

- **Concurrency Limit**: Only one run may execute per automation at a time. Colliding scheduled fires are skipped.
- **Sequential Daemon Dispatch**: The `serve` daemon dispatches due automations sequentially on each tick to maintain headless session stability.
- **Manual Worktree Cleanup**: Managed worktrees created for runs are not deleted automatically. Operators must clean up old worktree branches manually.
- **No Interactive Approvals**: Background runs cannot prompt for runtime permissions. Unattended tool calls must be auto-approved or denied fast.
- **Shared Subagent Panel**: A background run's dispatched subagent threads appear in the same registry the foreground session's subagent panel reads, so they may be visible there alongside the foreground session's own activity.
