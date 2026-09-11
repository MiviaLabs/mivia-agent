# Terminal UI isolation

The terminal UI packages are self-contained. They build against ports interfaces and
fakes, and one adapter connects them to real sessions.

## The rule

`internal/ui/**` and `internal/uikit/**` are self-contained view packages:

- They build against the ports interfaces or against fakes, never against the
  harness.
- They must not import the CLI family (`internal/cli*`), and they must not import
  `internal/uiadapter`. `internal/uiadapter` points the other way: it adapts real
  sessions to the ports the UI already speaks.
- The only integration seam for UI components is the ports surface in
  `internal/uikit/ports` (`Conversation`, `TurnHandle`, `Approver`,
  `CommandRunner`, `Settings`) plus the event vocabulary in
  `internal/uikit/uievent`.
- `internal/uikit/**` must not import bubbletea or lipgloss
  (`internal/uikit/ports/ports.go`), so the toolkit stays testable without a
  terminal.

## The adapter and the composition root

`internal/uiadapter` connects real sessions (`internal/chat`, `internal/agent`) to
`internal/uikit/ports`. It is the one package that touches both sides. Per invariant
INV-TUI-29 (`.mivia/invariants.md`), `internal/uiadapter` must never import
`internal/cli*`.

`internal/newtui` is the composition root: `cmd/mivia` builds the UI there and wires
the `uiadapter` implementations into the screens.

## Why

The harness code under `internal/cli` is a moving surface. UI code built against it
couples both sides to shapes that are about to change. The UI therefore develops
against fakes that implement the same ports, and integration stays one adapter, not
a thousand call sites.

## What this means for feature work

- Build the UI - every screen, component, key, mouse action, and command - with
  full logic and state, driven by the fakes in `internal/uikit/replay` and the
  recorded JSON fixtures that `uievent.LoadFixture` reads.
- When a feature needs something the ports do not carry yet, extend the ports or
  the uievent vocabulary. Do not reach around them.
- A fake that grows a second behaviour (different replies per turn, streaming with
  pacing, mid-turn tool calls and approvals) is feature work on the UI, not
  integration, and belongs under `internal/uikit` with tests of its own.

## The command-dispatch seam (ports.CommandRunner)

Slash commands go through `ports.CommandRunner` (`Run`, `SelectModel`, in
`internal/uikit/ports/commandrunner.go`). A `conversation.Screen` calls the
interface when Enter submits a `/command` line; it never inspects harness state
directly (`internal/ui/screen/conversation/commands.go`). The test fakes implement
it today. The real adapter implements the same interface, and the screen does not
change.

## Enforcement

- `scripts/check_import_layers.py` checks the tree against
  `.mivia/policy/import-layers.json`: every import edge between module packages
  must be declared in the policy's allow map, deny rules override the allow map,
  and the total edge count stays under the policy cap. `make verify` runs it as
  `import-layers-check`.
- `TestConversation_DoesNotImportCLIFamily` and
  `TestUIPackages_DoNotImportUIAdapter` (`internal/uiadapter/conversation_test.go`)
  pin the two halves of the boundary: `internal/uiadapter` never imports the CLI
  family, and `internal/ui/...` plus `internal/uikit/...` never import
  `internal/uiadapter`. Both tests implement invariant INV-TUI-29
  (`.mivia/invariants.md`).
