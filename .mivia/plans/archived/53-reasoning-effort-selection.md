# 53 - Reasoning effort: declared choices, visible default, runtime selection

**Status:** APPROVED for implementation (ADLC Step 0 run against the tree
2026-08-02).
**Date:** 2026-08-02
**Depends on:** `37` §12 (shipped this session): the per-model reasoning dial,
the wire dialects, and propagation to all five request constructors.
**Blast radius:** MEDIUM - one config key added, one session-scoped override
added, two TUI dialogs touched, one new slash command. No wire-shape change:
plan 37's dialect mapping is reused verbatim.

## 1. Goal

Three things plan 37 left out:

1. A model declares **which** reasoning efforts it offers, not just one value.
2. The `/model` picker **shows** each model's default effort, so the dial is
   visible at selection time. Plan 37 §4a claimed this and never built it.
3. `/effort` lets the user change the effort for the current model at runtime,
   choosing from the declared set, and says so plainly when a model declares
   none.

## 2. Config

```toml
[providers.zai]
models = [
  { name = "glm-5.2", context_window_tokens = 1000000,
    reasoning_efforts = ["low", "medium", "high", "max"],
    reasoning = "high",
    reasoning_dialect = "thinking_effort" },
  { name = "glm-4.6", context_window_tokens = 200000 },
]
```

- `reasoning_efforts` - the ordered set this model offers. Absent or empty
  means the model has no reasoning surface. Order is preserved: it is the
  order the `/effort` dialog lists.
- `reasoning` - the **default**, and it must be a member of
  `reasoning_efforts`. Omitting it while `reasoning_efforts` is non-empty is
  legal and means "the model can reason, but ships with no reasoning field
  sent" - the user opts in through `/effort`.
- `reasoning_dialect` - unchanged from plan 37.

### 2a. Why the default must be a member

The available set is the single source of truth; the default is a pointer into
it. Allowing a default outside the set would create a value `/effort` cannot
return to, and allowing a default with no set at all would give two ways to
spell the same configuration. This tightens what plan 37 shipped hours ago and
has not been released.

There is deliberately no synthetic "off" or "unset" row in the dialog. `off` is
already a real level meaning "disable thinking", so a model that wants a
disable choice lists `"off"` among its efforts. Inventing a second way to say
it would make the dialog disagree with the config.

### 2b. Validation

Extends `checkReasoningIsDeliverable`, which moves from keying on "the default
is active" to keying on "**any** effort could be sent":

- every entry parses as a level; duplicates rejected;
- `reasoning` without `reasoning_efforts` is rejected, naming both keys;
- `reasoning` outside `reasoning_efforts` is rejected, listing the set;
- a non-empty `reasoning_efforts` with no resolvable dialect is rejected -
  same rule as plan 37, now triggered by the capability rather than the
  default, because `/effort` can activate any listed level.

## 3. Session-scoped override

```go
func (s *Session) SetReasoningEffort(level reasoning.Level) error
func (s *Session) ReasoningEffort() reasoning.Level          // effective
func (s *Session) ReasoningChoices() []reasoning.Level       // active model's set
```

`SetReasoningEffort` refuses a level outside the active model's declared set,
and refuses while a turn is active (same rule as `/model`).

**The override folds into `captureBindingLocked`.** That function already
returns a copy, so folding the effective level into the returned
`Profile.Reasoning` means all five request paths from plan 37 keep working
unchanged - they read the binding they already captured, under the same lock,
so the effort cannot change mid-turn. The alternative, threading a sixth value
through every path, would give the override its own chance to drift from the
binding.

The override is **model-scoped**: `SwitchBinding` and `SelectModel` clear it,
exactly like the dial they replace. Choosing an effort for one model must not
follow the user to a model that never offered it.

## 4. UI

### 4a. `/model` picker

`modelDialogRow` gains the model's **default** effort (config, not the live
override - this row describes a model the user has not selected yet):

```
  ● glm-5.2      effort: high
    glm-4.6
```

A model with no declared efforts shows nothing extra rather than "none", so
the annotation is signal rather than noise on catalogs that use no reasoning.

### 4b. `/effort`

A new dialog built on the `agentDialog` shape (flat list, no headers):

- rows are the active model's `reasoning_efforts`, in config order;
- the effective level is marked, and the model default is labelled;
- an empty set renders a single informational line naming the model:
  `no reasoning effort configured for glm-4.6`, and Enter does nothing.

Plain (non-TUI) surface: `/effort` prints the current level and the choices;
`/effort <level>` sets it. Same validation, same error text.

Wiring, in the order the TUI needs it: `tui.go` field, `tui_keys.go` routing,
`tui_view.go` render, `tui_message.go` modal-open predicate and resize clamp,
`overlay.go` close-all, `slash_catalog.go` registration, both slash
dispatchers. Missing the modal-open predicate would breach INV-TUI-29 (a modal
owns every mouse and paste event while open).

## 5. Non-goals

- No global `[chat].effort`. Rejected for the reason plan 37 §4a gives: value
  sets differ per model, so one session-global value is wrong for every model
  it does not match.
- No persistence of the override across sessions. It is a turn-shaping
  preference, not session state; the config default is what persists.
- No change to the wire dialects, the merge order, or sampling.

## 6. Verification

- `go test ./internal/config/... ./internal/chat/... ./internal/cli/...`
- `go test -race ./...`, `go vet ./...`, `go build ./...`
- `make verify` (includes diff-coverage and invariant validation)

Integration tests, not unit tests, for the behaviour that matters:

1. A configured model's default effort reaches the provider request.
2. `/effort <level>` changes the level the **next** request carries, on both
   the plain and agent paths.
3. An effort outside the declared set is refused and the request is unchanged.
4. Switching models clears the override; the new model's default applies.
5. The `/model` dialog renders the default effort for a configured model and
   omits it for an unconfigured one.
6. The `/effort` dialog lists the declared set in config order, marks the
   effective level, and renders the empty-state message naming the model.
7. Selecting a row in the `/effort` dialog changes what the next request sends.

## 7. Invariant

Extends **INV-AG-36** rather than allocating a new ID: the existing invariant
already says the dial travels with the model binding, and the override is part
of that dial. The added clause is that a runtime override is confined to the
model's declared set and dies with the binding.
