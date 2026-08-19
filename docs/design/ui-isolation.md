# mivia-ui isolation - mocks only, integration later

Status: binding policy for the new terminal UI. Decided by the product
owner on 2026-08-19.

## The rule

The new terminal UI is SELF-CONTAINED:

- `cmd/mivia-ui`, `cmd/mivia-ui-demo`, `internal/ui/**`, and
  `internal/uikit/**` build and run against MOCK DATA ONLY.
- They must not import `internal/cli`, `internal/chat`, `internal/agent`,
  `internal/coordinator`, or `internal/hub`. Semgrep enforces this with
  rule `mivia.go.ui-no-harness-imports`, which runs in `make semgrep`
  and in `make verify`.
- The ONLY future integration seam is the ports surface in
  `internal/uikit/ports` (`Conversation`, `TurnHandle`, `Approver`) plus
  the event vocabulary in `internal/uikit/uievent`.

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

## Source

Product owner decision, 2026-08-19: at this point there is no
integration with the CLI, because that code will be refactored first.
The goal is a fully functional, fully interactive UI with the whole
feature set, on fake data, with clean knobs (the ports) for integration
later.
