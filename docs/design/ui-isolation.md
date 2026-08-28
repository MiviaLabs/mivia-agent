# mivia-ui isolation - mocks only, integration later

Status: binding policy for the new terminal UI. Decided by the product
owner on 2026-08-19.

## The rule

The new terminal UI is SELF-CONTAINED:

- `cmd/mivia-ui`, `internal/ui/**`, and
  `internal/uikit/**` build and run against mock data or ports interfaces.
- `internal/ui/**` and `internal/uikit/**` must not import `internal/cli*`,
  `internal/chat`, `internal/agent`, `internal/coordinator`, `internal/hub`,
  or `internal/uiadapter`. Semgrep enforces this with rule
  `mivia.go.ui-no-harness-imports`, which runs in `make semgrep` and in
  `make verify`.
- The ONLY integration seam for UI components is the ports surface in
  `internal/uikit/ports` (`Conversation`, `TurnHandle`, `Approver`,
  `CommandRunner`, `Settings`) plus the event vocabulary in
  `internal/uikit/uievent`.
- `internal/uiadapter` connects real sessions (`internal/chat`, `internal/agent`)
  to `internal/uikit/ports` for `cmd/mivia-ui` live mode (`--demo=false`).
  Per invariant INV-TUI-29, `internal/uiadapter` must never import `internal/cli*`.

## Why

The CLI and the harness code under `internal/cli` will be refactored
BEFORE the UI connects to them. Building UI code against that moving
surface now would couple both sides to shapes that are about to change.
The UI therefore develops against fakes that implement the same ports,
and integration becomes one adapter written AFTER the refactor settles,
not a thousand call sites.

## What this means for feature work

- Build the WHOLE UI - every screen, component, key, mouse action, and
  command - with full logic and state, driven by the fake
  implementations in `internal/uikit/replay` (and whatever richer fakes
  replace them).
- When a feature needs something the ports do not carry yet, EXTEND the
  ports or the uievent vocabulary. Do not reach around them.
- A fake that grows a second behavior (different replies per turn,
  streaming with pacing, mid-turn tool calls and approvals) is feature
  work on the UI, not integration, and belongs under `internal/uikit`
  with tests of its own.
- The day real wiring starts, this file changes first: the semgrep rule
  narrows or retires, and one adapter under the UI (not a refactor of
  it) connects the ports to the real harness.

## The demo harness (internal/uikit/demoharness)

`internal/uikit/demoharness` is the fake `cmd/mivia-ui --demo` runs
against today. It replaced `internal/uikit/replay` as the demo driver.
One `Harness` type implements the whole ports surface -
`ports.Conversation`, `ports.Approver`, and `ports.CommandRunner` - over
shared state, so a `/model` pick or an approval decision changes what
later turns and later commands see. `internal/uikit/replay` still
exists: some existing tests build a `conversation.Screen` against it
directly, and it stays a valid minimal fake for that use. It is no
longer what the demo binary runs.

The scripted conversation is DATA, not code: `internal/uikit/demoharness/testdata`
holds one JSON turn-script file per turn shape (small talk, a tool
call, a diff, a failing tool, a plan, reasoning, a usage summary, and a
mid-turn approval), in the wire shape `uievent.LoadFixture` already
reads. A `--scenario` flag on `cmd/mivia-ui` picks which named,
ordered list of those files to play; `New` errors on an unknown name.

## The command-dispatch seam (ports.CommandRunner)

Slash commands are the other integration knob this phase adds.
`internal/uikit/ports.CommandRunner` (`Run`, `SelectModel`) is what a
`conversation.Screen` calls when Enter submits a `/command` line; it
never inspects harness state directly, only this interface
(`internal/ui/screen/conversation/commands.go`). `demoharness.Harness`
implements it today. A future real-harness adapter implements the same
interface; the screen does not change.

## Source

Product owner decision, 2026-08-19: at this point there is no
integration with the CLI, because that code will be refactored first.
The goal is a fully functional, fully interactive UI with the whole
feature set, on fake data, with clean knobs (the ports) for integration
later.
