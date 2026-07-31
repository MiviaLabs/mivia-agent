# 29 — Config-owned model selection dialog

**Status:** Implemented — validated by repository investigation and hostile
architecture, correctness, and TUI reviews on 2026-07-31; delivered with the
plan-28 amendment recorded below.

**Depends on:** the amended `.mivia/plans/archived/28-model-context-windows.md`.
Plan 28 now owns the strict finite catalog, provider-qualified profile, exact
restore, and idle binding-generation foundations required here.

## Goal

Make bare `/model` in the TUI open a centered, provider-grouped picker. The
picker exposes only models explicitly declared in the user's configuration;
the current provider/model is highlighted; Enter changes the current session's
provider and model, including rebuilding the provider backend when necessary.

## Investigation evidence

The current path is:

```text
tui_message.go:91-107
  → tui_keys.go:148-190
  → tui_slash_handlers.go:53-78
  → chat.Session.SelectModel
```

The classic REPL has a separate text path at
`internal/cli/chat_slash_handlers.go:23-40`.

The current implementation has three constraints this feature must change or
preserve deliberately:

- `config.Resolved.Models` contains only the active provider's models
  (`internal/config/types.go:167-217`).
- `providerregistry.Descriptor` contains one built-in default model, not a
  catalog (`internal/providerregistry/registry.go:9-45`).
- `chat.Session` owns one completer and current turns read the model/completer
  around `internal/chat/session.go:271-379`; provider construction is currently
  startup-only in `internal/provider/provider.go:170-193`.

The modal surface is already centralized in
`internal/cli/dialog_geometry.go`, `dialog_compositor.go`, `overlay.go`,
`tui_view.go`, `tui_message.go`, and `tui_keys.go`. `sessionsDialog` is the
closest selection/Enter/cursor example.

## Locked behavior

### Catalog ownership

1. The selectable catalog is exactly the explicit `models` array under each
   configured `[providers.<name>]` table. Provider-registry defaults are never
   added to the catalog.
2. A provider with omitted or empty `models` has no selectable models. It is not
   treated as unrestricted, and its registry default is not a fallback.
3. The active provider must have at least one declared model and the resolved
   startup model must be one of those entries. A missing config, missing active
   catalog, invalid `default_model`, invalid `--model`, removed model, or
   invalid saved pair is an error or an explicit non-destructive notice; none
   falls back to a registry default, arbitrary model string, or current model.
4. No remote model discovery, provider API catalog call, inferred model, or
   legacy compatibility fallback is in scope. The dialog must say when no
   configured models are available instead of claiming completeness.
5. Models are provider-qualified internally. A model ID may contain `/` (for
   example `openai/gpt-4o-mini`) and must never be parsed by splitting the model
   ID. Duplicate model IDs are allowed across providers only because their
   provider-qualified identities differ; duplicate entries within one provider
   are a config error.

### Provider switching

1. Every explicitly configured provider group is shown in deterministic order:
   active provider first, then remaining provider names in lexical order.
   Model order follows the config array. Map iteration order is never exposed.
2. Rows whose provider has no supported backend or no usable credential are
   visible but disabled with a short safe reason. Reasons never include API
   keys, URLs, raw environment values, or provider error payloads.
3. Selecting a row from another configured provider constructs that provider's
   completer and session dispatcher before committing. The session transition
   atomically publishes provider, model, completer, model profile, and
   dispatcher generation only after every prerequisite succeeds.
4. A provider/model switch is allowed only while the session is idle and has no
   active session-owned orchestration. While work is active, the dialog may be
   opened for inspection but commit rows are disabled and the footer says to
   finish the current work first. This is the lifetime contract that prevents
   an old completer or dispatcher from being closed while still in use.
5. An in-flight turn always completes on the provider/model binding captured at
   its start. A successful idle switch affects the next turn. New nested work
   uses the new binding; old work is never retargeted.
6. Saved sessions persist provider and model together. Loading a saved session
   first validates and builds the exact configured provider/model binding; only
   then does it replace history. If the pair is missing, undeclared, disabled,
   or cannot be built, history and the current binding remain unchanged.

### Command and dialog semantics

1. TUI bare `/model` opens the picker. `/model <model>` remains a direct
   active-provider command, but now accepts only an explicitly declared model.
   `/model <provider> <model>` is the unambiguous provider-qualified direct
   form; the model argument is kept intact, including slash-containing IDs.
2. The classic REPL remains text-based, but uses the same strict catalog and
   provider-aware transition service. It does not gain a TUI window.
3. The dialog has grouped provider headers, a cursor, a selected marker based
   on the full `Session.CurrentSelection()` value, disabled rows, bounded
   scrolling, and an explicit footer. Headers are not selectable.
4. Up/down, `j`/`k`, Home/End, PageUp/PageDown, wheel, and a click on an
   enabled model row move the cursor. Clicking or focusing a disabled row does
   not commit or mutate state. Enter commits; Esc/`q` cancels; a failed commit
   leaves the dialog, cursor, and existing binding intact with a safe error.
5. The dialog reuses `dialogLayout`, `renderDialogFrame`, `overlayAt`, modal
   input ownership, resize clamping, and hit-map invalidation. No generic
   dialog framework or second ANSI compositor is introduced.
6. Modal paste, transcript clicks, composer input, stale hit-map events,
   Ctrl+C, and Ctrl+Q retain the existing global/modal precedence rules.
   Closing the picker invalidates the hit map before any post-modal input.
7. Exact-canvas rendering is maintained at every size. At very small sizes,
   rows are truncated or paged deterministically; no provider/model identity
   is silently substituted with a default. Every enabled row remains reachable
   through navigation.

## API and ownership contract

The exact names may be adjusted during plan-28 amendment, but the following
shapes are required and must not be replaced with raw maps or cross-package
secret exposure.

### Config: secret-free catalog plus private runtime material

`internal/config/types.go` owns the model/profile and provider configuration
types. Plan 28's `ModelSpec` is the single model type.

```go
type ProviderModelGroup struct {
	Provider       string
	Models         []ModelSpec
	Active         bool
	Selectable     bool
	DisabledReason string // safe display text; empty when selectable
}

func (r *Resolved) ModelCatalog() []ProviderModelGroup
```

`ModelCatalog` returns deep copies and contains no key, URL, raw env value, or
provider response. `Resolved` also gets provider-qualified runtime records for
the provider package, but those records are not passed to TUI rendering or
model-facing output. The runtime record contains the provider name, resolved
transport settings, key-env name, key-presence state, and private key needed by
the provider factory.

`config.Load` must resolve and validate every configured provider's explicit
model list from the already loaded env map. It must sort provider names before
building the catalog, reject case-colliding provider keys, and keep the active
provider first. Transport URL/API-key-env defaults may remain provider transport
defaults; they must never manufacture a model entry.

### Provider: build a selected backend without exposing credentials

`internal/provider/provider.go` adds a provider-qualified constructor seam,
reusing the existing private built-in factory registry:

```go
func NewForProvider(res *config.Resolved, providerName string) (Completer, error)
```

It validates the selected provider's private runtime record and reports only a
safe missing/unsupported-provider error. `New` delegates to this seam for the
startup provider. The provider package remains below CLI and does not import
CLI or TUI code.

### Chat: immutable binding and idle atomic transition

`internal/chat/session.go` replaces independent provider/model assumptions with
one mutex-owned binding snapshot:

```go
type ModelBinding struct {
	ProviderName string
	Model        string
	Completer    provider.Completer
	Dispatcher   *runtime.Dispatcher
	Profile      config.ModelSpec
}

type Selection struct {
	ProviderName string
	Model        string
}

func (s *Session) CurrentSelection() Selection
func (s *Session) SwitchBinding(binding ModelBinding) error
```

`SwitchBinding` validates provider-qualified membership against copied config
policy, rejects a non-idle session, and swaps the complete binding under one
lock. The caller constructs the binding and dispatcher outside the lock. The
old binding is closed only after the idle precondition and successful swap are
both established. Production code no longer reads `s.Completer` or
`s.Dispatcher` after unlocking; each turn captures both in one immutable local
binding before releasing the lock.

The session exposes a small binding-factory interface to CLI-owned wiring, not
a `chat → cli` dependency. CLI builds the provider and dispatcher generation;
chat owns atomic publication and turn snapshots.

### CLI: one provider-aware transition service

`internal/cli` owns the factory that combines `provider.NewForProvider`, tool
registry construction, workspace skill loading, and `NewSessionDispatcher`.
It returns a complete `chat.ModelBinding` or an error without mutating the
session. The TUI and classic REPL call the same transition helper, so dialog,
direct command, and text command behavior cannot diverge.

Dispatcher generations must be built against generation-owned tool/skill
registries. Reusing the live `sess.Tools` registry is forbidden because
`NewSessionDispatcher` registers delegation and ledger tools into the supplied
registry and duplicate registration would partially mutate the live session.
The implementation must either add a tested registry clone or introduce an
equivalent complete generation builder. On successful idle commit, the old
dispatcher is closed; on any build/validation error it is untouched.

### Persistence: exact pair and matching metadata

`internal/chat/save_manager.go` and `persistence.go` must capture provider and
model from the same binding generation. Extend save methods to accept a
selection snapshot rather than changing only the model while retaining the
startup provider. Update explicit save, rolling turn save, exit save, and
interrupted-turn save paths.

Load must read the saved provider/model pair, prepare its binding before
assigning messages, then publish both together. A failed provider switch leaves
both the old messages and old binding in place.

## File-level implementation slices

### Wave 0 — completed dependency alignment

- `.mivia/plans/archived/28-model-context-windows.md` was amended to remove unrestricted
  empty-model semantics, registry-model fallbacks, and current-model restore
  fallback; require explicit provider model declarations and provider-qualified
  selection; and replace the startup-frozen dispatcher decision with the
  binding-generation contract above. Context-window validation remains owned
  by plan 28's `ModelSpec`.
- `.mivia/INDEX.md`: register this plan and record the dependency/amendment
  relationship as a control-surface bookkeeping step if it is not already
  covered by the surrounding plan-index change.

Gate: passed — plan 28 and this plan describe one model/profile/binding API,
not two. Index bookkeeping is separate and does not block implementation.

### Wave 1 — RED then GREEN: strict catalog and provider runtime

Tests first in `internal/config/load_test.go`, `policy_test.go`, and new
catalog-focused tests:

- omitted/empty `models` has no selectable entries and cannot resolve startup;
- registry-only defaults are never inserted;
- `default_model`, `--model`, and saved names must be declared entries;
- duplicate names within one provider, invalid names, controls, invalid UTF-8,
  over-limit catalogs, unknown/case-colliding providers, and malformed provider
  blocks fail safely;
- multiple providers resolve in stable order and retain slash-containing IDs;
- all provider keys are resolved without printing key values;
- inactive missing credentials produce disabled catalog groups rather than
  leaking secret material or making an invalid selectable row.

Then modify `internal/config/types.go` and `internal/config/load.go` to add the
secret-free `ProviderModelGroup`, private provider runtime records, strict
finite catalog validation, deep-copy accessors, and stable ordering. Update
`internal/providerregistry` tests only to ensure registry metadata cannot leak
into the model catalog.

### Wave 2 — RED then GREEN: backend factory and session binding

Tests first in `internal/provider/provider_test.go` and
`internal/chat/session_test.go`:

- construct every supported configured provider from its runtime record;
- missing key/unsupported provider fails without a partial binding;
- model and provider are swapped as one selection;
- a factory failure leaves the old binding untouched;
- direct sends capture completer, dispatcher, provider, model, and profile
  together before unlocking;
- concurrent send/switch is race-free and an active turn keeps its original
  backend;
- switch is rejected while the session is busy; a later idle switch succeeds.

Then add `provider.NewForProvider` and the `chat.ModelBinding`, `Selection`,
turn-snapshot, idle guard, and atomic `SwitchBinding` implementation. Replace
post-unlock reads of public completer/dispatcher fields with binding snapshots.

### Wave 3 — RED then GREEN: generation-owned dispatcher and persistence

Tests first in `internal/tools`/`internal/cli` dispatcher tests and
`internal/chat` persistence tests:

- a new generation has independent tool/skill registration and no duplicate
  mutation of the live registry;
- new turns and all nested one-shot/multi-step/skill requests use the new
  provider after an idle switch;
- old in-flight work never uses the new provider;
- old generation close happens only after the idle boundary;
- explicit save, rolling save, exit save, and interrupted save contain matching
  provider/model metadata;
- loading an exact configured cross-provider pair switches binding and history
  together; missing provider/key/model leaves both unchanged.

Then update `internal/tools/tools.go` (or the chosen generation builder),
`internal/cli/dispatcher.go`, `internal/cli/chat_repl.go`,
`internal/subagents/{oneshot,multi_step}.go` as required by the binding seam,
and `internal/chat/{persistence,save_manager,session_store}.go`.

### Wave 4 — RED then GREEN: TUI model dialog

Tests first in new/updated `internal/cli/model_dialog_test.go`,
`model_dialog_input_test.go`, and `dialog_program_test.go`:

- bare `/model` opens over the existing chat base;
- every configured provider group and only explicit model rows are present;
- active provider/model uses the full-name selected marker;
- duplicate model IDs across providers remain distinct rows;
- disabled provider rows are visible, navigable only as specified, and cannot
  commit;
- up/down/Home/End/Page/wheel/click preserve cursor and row identity;
- Enter performs same-provider and cross-provider selection, updates header,
  closes on success, and shows the safe error while remaining open on failure;
- Esc/q cancel with no state mutation;
- modal mouse, paste, stale hit-map, Ctrl+C, Ctrl+Q, and post-close composer
  behavior remain isolated;
- resize, narrow/tiny canvases, wide IDs, ANSI/control input, and exact canvas
  bounds hold at 1x1, 2xN, Nx2, and normal sizes.

Then add `internal/cli/model_dialog.go`, wire `/model` in
`internal/cli/tui_slash_handlers.go`, `tui_view.go`, `tui_message.go`,
`tui_keys.go`, and the shared provider-aware transition helper. Reuse the
existing dialog compositor; do not duplicate it.

### Wave 5 — RED then GREEN: direct command, docs, and invariants

Tests first in `internal/cli/provider_model_test.go` and slash handler tests:

- `/model name` validates only the active provider's declared entries;
- `/model provider model` selects the exact provider-qualified entry;
- slash-containing IDs round-trip without truncation;
- ambiguous/missing/disabled entries fail without mutation;
- classic REPL and TUI use identical transition results;
- no model path accepts a value absent from config.

Then update `internal/cli/chat_slash_handlers.go`, `tui_slash.go`, help text,
`docs/product/config.md`, `docs/architecture/overview.md`, and
`docs/development/terminal-input.md` under their existing ownership entries.
Update `.mivia/mivia.toml.example` and any tracked config fixture atomically.
Do not add a new documentation topic or duplicate an owned page.

Add invariant rows only with the implementation, after exact tests exist. The
minimum expected mapping is:

- a config invariant for finite explicit model ownership and no fallback;
- a session invariant for atomic provider/model/completer/dispatcher binding
  and old-turn isolation;
- a TUI invariant for provider-qualified highlighting, disabled rows, and
  Enter commit.

Use the next free IDs at landing and list every named test in each row. Existing
`INV-TUI-28` (geometry/composition) and `INV-TUI-29` (modal input ownership)
remain the base invariants and must not be duplicated.

## Validation and delivery gates

Run the narrowest gates after each wave, then the full set:

```text
go test ./internal/config/... ./internal/provider/... -count=1
go test ./internal/chat/... ./internal/tools/... ./internal/subagents/... -count=1
go test ./internal/cli/... -count=1
go test -race ./internal/config/... ./internal/provider/... ./internal/chat/... ./internal/tools/... ./internal/subagents/... ./internal/cli/...
go vet ./...
go build ./...
python3 scripts/check_go_structure.py --strict --all
make validate-invariants
make docs-check
make secret-scan
make semgrep
git diff --check
make verify
make pre-commit
```

The baseline must be captured before implementation. Existing unrelated dirty
worktree changes at planning time must remain untouched and must not be folded
into the feature commit.

## Challenge disposition

- **Confirmed:** current `Resolved` has only active-provider models; the plan
  adds a secret-free provider catalog.
- **Confirmed:** provider construction is startup-only; the plan adds a
  provider-qualified factory and complete binding transition.
- **Confirmed:** current `Session` reads completer/dispatcher after unlocking;
  the plan requires an immutable turn snapshot.
- **Confirmed:** current SaveManager stores a fixed provider name; the plan
  passes provider/model snapshots through every save path.
- **Confirmed:** current load assigns messages before model restoration and
  ignores persisted provider; the plan makes exact-pair binding validation
  precede atomic history replacement.
- **Confirmed:** rebuilding a dispatcher against the live tool registry can
  duplicate registration; the plan requires generation-owned registries.
- **Resolved:** plan 28's unrestricted/default/frozen-dispatcher decisions were
  amended in Wave 0 to use the strict provider-qualified binding contract.
- **Rejected:** introducing a generic dialog abstraction is necessary; the
  existing concrete `sessionsDialog` plus shared compositor is sufficient.
- **Rejected:** remote discovery belongs in this slice; the user's explicit
  config-owned requirement makes it a separate future feature.

## Rollback criteria

Return to architecture review before implementation continues if any of these
remain true:

1. A model can enter the catalog or session without an explicit config entry.
2. A cross-provider switch can partially publish provider, model, completer,
   dispatcher, profile, or metadata.
3. An active turn or session-owned orchestration can observe a newly published
   backend, or an old dispatcher can be closed while still reachable.
4. Saved history can be replaced while its exact provider/model binding cannot
   be built.
5. The TUI exposes credentials, raw provider errors, or unbounded/unsafe model
   labels.
