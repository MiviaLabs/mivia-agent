# Composer Autocomplete Design — `/` and `@` suggestion popup

**Status:** Implementation-ready for Phase 0 + Phase 1 after gates below (v3).
**Date:** 2026-07-28
**SoT:** `.mivia/plans/composer-autocomplete.md`
**Revisions:** v1 product draft → v2 industry challenge → **v3 implementation challenge** (3 adversarial subagents + parent re-verify).

**Product goal:** In TUI chat composer, `/` and `@` open a filterable suggestion box above the input. ↑↓ navigate; Tab/Enter accept without accidental send/queue; mouse later.

---

## Document map

| § | Content |
|---|--------|
| 0 | v3 challenge verdict (what blocks coding) |
| 1 | Ground truth (LOC + wiring facts) |
| 2 | Product decisions (locked) |
| 3 | Architecture + file split |
| 4 | Catalog arity (Enter policy) |
| 5 | Trigger + token replace (bubbles API) |
| 6 | Key preemption (must early-return) |
| 7 | View / layout |
| 8 | Providers (slash, args, files) |
| 9 | Security |
| 10 | Phases, gates, tests |
| 11 | Symbols + LOC budget |
| 12 | Risks / non-goals / backlog |
| 13 | Checklist |

---

## 0. v3 challenge verdict

### What is green

| Phase | Ready? | Notes |
|-------|--------|-------|
| **0 Catalog** | **Yes** | Pure code + tests; no Update-loop risk |
| **1 Chat `/` popup** | **Yes after gates** | No `wsRoot` required; keyboard only |
| **2 `@` files** | After Phase 1 | Needs `wsRoot` plumb + WalkDir index |
| **3 Mouse / `/load` args** | Later | Hitmap signature churn |
| **Welcome popup** | Out of v1 | ↑↓ always session-nav; mouse early-return |

### P0 gates before UI wiring (must be green)

1. Catalog + A0 tests.
2. `applyTokenReplace` unit tests on real `textarea.Model` (A15).
3. Preemption design: **full early-return from `updateMessageImpl`**, not only `skipTextarea` (viewport also consumes ↑↓).
4. Enter arity flags (`EmptyExecutes` / `ArgsRequired`) locked to handlers — not guessed from “has Args string”.
5. `handleSuggestKey` extracted so `handleChatKey` stays ≤ soft 80 LOC.

### Critical corrections vs v2

| v2 claim | Reality (evidence) | v3 rule |
|----------|-------------------|---------|
| “skipTextarea enough for ↑↓” | `handleChatKey` always `skipViewport=false` (`tui_keys.go`); viewport still `Update`s keys (`tui_message.go:108-110`) | Suggest keys **return from Update** before textarea **and** viewport |
| “ALWAYS syncSuggest after textarea” | Early return when `len(cmds)>0` skips foot path (`tui_message.go:76-78`) | Call sites: fall-through after textarea; **closeSuggest on every early return that mutates/sends** |
| A5 `/model` insert only | Bare `/model` **executes** and shows model (`tui_slash_handlers.go:41-48`); same class `/budget` `/steps` | Catalog `EmptyExecutes` + optional `PreferInsert` product flag |
| TokenTo = cursor | Mid-token `/loa|d` + replace to cursor corrupts | Replace **full token** `[TokenStart, TokenEnd)` |
| Absolute cursor “small helper” | No public col getter; `row` unexported; `SetCursor` = current row only | `Line()` + `LineInfo` reconstruct **or** hot-path end-of-buffer; place cursor via `SetValue` + `KeyLeft`×N |
| End-of-token-only product limit | Sufficient for most `/`, wrong for `@` trailing text | Implement full `applyTokenReplace`; empty-`after` is fast path only |
| git ls-files first | cli has zero `os/exec`; tests need determinism | **WalkDir first**; git optional later |
| Phase 1 needs `runTUI`/`wsRoot` | Signature churn hits many tests | **Phase 1: no wsRoot**. Phase 2 plumbs it |
| Slash Enter clears buffer | **False today** — local slash does not `Reset` (`handleChatEnter`) | Smart execute **must** `Reset` + `closeSuggest` or popup reopens |
| Mirror tools hitmap | Tools zone dead (`rebuild(...,1,0,...)`) | Don’t copy; Phase 3 adds `hitSuggest` carefully |
| §14 success includes `@` | Overclaims Phase 1 | Split Phase 1 vs full success |
| tui.go headroom from baseline 1682 | Real file ~454; structure_test **≤600** | Fields only in `tui.go` |

### Naive bugs (will ship if ignored)

1. ↑↓ scrolls transcript while moving selection.
2. Tab inserts **and** focus-cycles.
3. Enter inserts then falls through → queue/send partial token.
4. Execute leaves `/help` in buffer → popup reopens.
5. Mid-token replace uses cursor as end → garbage.
6. Popup height not in layout → bottom chrome clipped (`tui_view.go` termH clamp).
7. Soft-wrap absolute cursor wrong → false open/close.
8. Stuffing preemption into `handleChatKey` → LOC soft/hard fail.
9. `@` lists `.env` if secret policy not shared.
10. Hitmap `rebuild` arity change without updating tests → compile break.

---

## 1. Ground truth (revalidated)

### Stack

| Piece | Version / fact |
|-------|----------------|
| bubbletea | v1.3.10 |
| bubbles textarea | v1.0.0 — `SetValue`=`Reset`+`InsertString`; `Update` no-ops if unfocused |
| lipgloss | v1.1.0 — no z-compositor |
| New deps | **None** |

### Current file sizes (≈ lines)

| File | LOC | Gate |
|------|-----|------|
| `tui.go` | ~454 | structure ≤600 |
| `tui_keys.go` | ~315 | soft 500 / hard 800 |
| `tui_message.go` | ~250 | soft 500 |
| `tui_view.go` | ~282 | soft 500 |
| `chat.go` | ~214 | soft 500 |
| `tui_hitmap.go` | ~67 | OK |

`handleChatKey` ~77 lines (already at soft 80). **Extract suggest keys; do not grow it.**

### Wiring anchors

| Concern | Location |
|---------|----------|
| Chat keys | `tui_keys.go` — Tab first (`cycleChatFocus`), then `routeFocusKey`, then switch |
| Enter send/slash/queue | `handleChatEnter` — slash before queue before `startAI` |
| Update early return | `tui_message.go:74-78` when `len(c)>0` |
| Textarea + viewport after keys | `tui_message.go:100-114` |
| Layout | `tui_view.go` — `header, body, paddedInput, hint`; no popup |
| Hitmap | `tui_hitmap.go` — transcript/tools/composer; tools dead in chat |
| Workspace | `chat_repl.go` `wsRoot` → `configureChatWorkspace` (tools only if `useTools`); `SessionDir=wsRoot/.mivia/sessions`; **not** passed to TUI |
| `--no-tools` | No `sess.Tools`; SessionDir still under wsRoot |
| Secret paths | `tools.isSecretPath` unexported (`tools.go:392`) |
| Walk skips | `.git`, `node_modules`, `vendor` in `tools/search.go` |
| Welcome ↑↓ | Always session nav (`handleWelcomeKey`) |
| Welcome mouse | Always `return true` — never hitmap |

### Industry (kept from v2)

- Never Tab→execute for arg commands (Claude/Docker footgun).
- Layout stack on Bubble Tea v1.
- `@` path insert first; body attach backlog.
- Isolate file index from tool registry (OpenCode MCP stall).

---

## 2. Product decisions (locked)

| Decision | Choice |
|----------|--------|
| Phase 1 surface | **TUI chat only** |
| Welcome suggest | **Out** |
| Phase 1 mouse | **Out** (keyboard ships) |
| Layout | Stack above composer, max **8** rows |
| `/` trigger | Left-trimmed buffer starts with `/`; cursor still in first token |
| `@` trigger | Token left of cursor starts with `@` |
| Tab (open+items) | **Insert only** (never execute, never focus-cycle) |
| Enter (open+items) | **Smart** per catalog flags (§4) |
| Esc (open) | Close popup; keep buffer; **do not** clear block selection |
| `@` payload | Insert `@rel/path` text only |
| File index | WalkDir + shared secret/skip policy; no tool Execute |
| Dependencies | None new |
| Plain REPL | Catalog + Tab list only (no popup) |

### Defaults

- Waiting + local slash execute: **allowed** (pre-existing). Insert accept **must never queue**.
- Empty query `/`: full catalog (capped).
- Zero matches: **close** suggest (so Enter keeps send semantics).
- Single match while typing: do not auto-insert.

---

## 3. Architecture

```text
tuiModel
  textarea.Model
  suggest suggestState          // Phase 1
  wsRoot string                 // Phase 2 only

updateMessageImpl KeyMsg (modeChat):
  if handleSuggestKey(...) handled → return  // before handleChatKey
  handleChatKey ...
  if early return with cmds → closeSuggest first
  textarea.Update if !skip
  syncSuggest()
  viewport / drain ...
```

### New files (≤400 LOC preferred, hard 800)

| File | Role |
|------|------|
| `slash_catalog.go` | Command SoT |
| `slash_catalog_test.go` | A0 + plain Tab parity |
| `suggest.go` | state, detect, rank, `applyTokenReplace`, `absoluteRuneCursor`, `syncSuggest`, `closeSuggest`, `handleSuggestKey` |
| `suggest_providers.go` | catalog query; Phase 2 file index; Phase 3 sessions |
| `suggest_render.go` | lipgloss box |
| `suggest_test.go` | pure + A15 |
| `tui_suggest_test.go` | Phase 1 Update tests |

### Types

```go
type SuggestKind uint8 // Command, Session, File

type SuggestItem struct {
	Kind        SuggestKind
	Label       string
	Description string
	Insert      string
	Score       int
}

type suggestState struct {
	Open      bool
	Trigger   rune // '/' | '@' | 0
	Query     string
	TokenFrom int // full token start (rune)
	TokenTo   int // full token end (rune), NOT mid-cursor
	Items     []SuggestItem
	Selected  int
	Scroll    int
	Y0, Y1    int // paint (Phase 3 mouse)
	Gen       uint64
}

type SlashCommand struct {
	Name          string
	Aliases       []string
	Description   string
	ArgsHint      string // "", "<name>", "[n]"
	Local         bool   // isLocalSlash
	Rewrite       bool   // /search
	Surface       string // "tui" | "plain" | "both"
	Implemented   bool   // false → help must not claim for that surface
	ArgsRequired  bool   // /load /save /delete true; /model false
	EmptyExecutes bool   // bare command runs handler (show status / usage)
	PreferInsert  bool   // Enter on selection inserts Name+" " instead of execute
}
```

---

## 4. Catalog arity (Enter policy)

### Smart Enter when popup open + items > 0

```text
item = Items[Selected]
cmd  = catalog match for item

if item.Kind != Command:
  applyTokenReplace(item.Insert); closeSuggest; return

// PreferInsert wins (arg-taking UX)
if cmd.PreferInsert || cmd.ArgsRequired:
  applyTokenReplace(cmd insert form with trailing space if needed)
  closeSuggest
  return  // never handleSlash, never startAI, never queue

// EmptyExecutes: exact complete command only
if cmd.EmptyExecutes && selectionIsExactCommand(item):
  applyTokenReplace if buffer not exact (optional)
  handleSlash(fullCmd)
  textarea.Reset()
  closeSuggest
  renderVP
  return

// Default: insert
applyTokenReplace(item.Insert); closeSuggest
```

`selectionIsExactCommand`: selected Label is a full command name/alias (not “user typed partial and multiple remain” — if multiple items, still only execute if PreferInsert false and EmptyExecutes and selected is complete command name). Safer rule: **execute only if `EmptyExecutes && !PreferInsert && !ArgsRequired`**.

### Recommended flags (match today’s handlers)

| Command | ArgsRequired | EmptyExecutes | PreferInsert |
|---------|--------------|---------------|--------------|
| `/help` `/h` `/?` | false | true | false |
| `/clear` `/status` `/list` `/session` `/tools` `/plain` | false | true | false |
| `/model` `/budget` `/steps` | false | true | **true** (A5: insert `/model `) |
| `/load` `/save` `/delete` | true | true (shows usage) | **true** |
| `/search` | true | false (rewrite path) | **true** |
| plain-only `/exit` `/quit` `/q` `/provider` `/workspace` | per surface | — | Surface=plain; TUI Implemented=false |

**Product note:** PreferInsert on `/model` **changes** bare-Enter-from-popup vs typing `/model`+Enter without popup (still shows model). That is intentional for arg UX. Document in help later.

### Catalog also drives

- `isLocalSlash`
- plain `handleTab` list
- optional help sync tests (Phase 0 can test-only assert; rewrite help MD in same PR or follow-up)

---

## 5. Trigger + token replace

### 5.1 Trigger (Phase 1 slash)

```text
value, absCursor := textarea state
tokenStart, tokenEnd, token := tokenAtCursor(...)  // whitespace-bounded

slash if:
  left-trim(value) starts with '/'
  and token is that first slash-token
  and absCursor within [tokenStart, tokenEnd]

@ if (Phase 2):
  token starts with '@'
```

Replace span is always **`[tokenStart, tokenEnd)`**, even if cursor is mid-token.

### 5.2 Absolute cursor (public API)

```go
// Line() = hard row; LineInfo().StartColumn+ColumnOffset ≈ hard col.
// Soft-wrap edge cases MUST be unit-tested; if flaky, slash Phase 1 may
// fall back to: if abs reconstruct fails, treat cursor as len(Value()) when
// user is typing at end (common path).
func absoluteRuneCursor(ta textarea.Model) int
```

### 5.3 `applyTokenReplace` (proven recipe)

```go
// Focus required: bubbles Update no-ops when blurred.
func applyTokenReplace(ta *textarea.Model, from, to int, insert string) {
	full := []rune(ta.Value())
	// clamp from,to
	before := string(full[:from])
	after := string(full[to:])
	newVal := before + insert + after
	_ = ta.Focus()
	ta.SetValue(newVal) // cursor at end
	for i := 0; i < len([]rune(after)); i++ {
		*ta, _ = ta.Update(tea.KeyMsg{Type: tea.KeyLeft})
	}
}
```

- Hot path `after==""`: `SetValue` only.
- Do **not** ship “refuse if trailing text” as product limit.
- A15 must cover: empty after, trailing after, mid-token, multi-line, Focus required.

---

## 6. Key preemption (implementation-critical)

### Preempt condition

```text
modeChat && focusComposer && suggest.Open && len(Items) > 0
```

### Matrix

| Key | Action | Must return before viewport? |
|-----|--------|------------------------------|
| up / ctrl+p | Selected-- | **Yes** full return |
| down / ctrl+n | Selected++ | **Yes** |
| tab | Insert accept | **Yes** |
| enter | Smart accept (§4) | **Yes** |
| esc | closeSuggest only | **Yes** |
| shift+tab | **closeSuggest** (v1); no reverse cycle | **Yes** |
| alt+enter | newline (textarea); then syncSuggest | No steal — fall through after close optional |
| left/right / printable / BS | fall through → textarea → syncSuggest | No |
| pgup/pgdn/home/end | closeSuggest then existing route | Close first |
| ctrl+c / ctrl+d | existing cancel/quit (close as side effect) | Cancel first OK |

### Placement (exact)

```go
// tui_message.go modeChat KeyMsg:
if handled, cmds := m.handleSuggestKey(key, msg.Alt); handled {
	return m, tea.Batch(cmds...)
}
skipTextarea, skipViewport, c := m.handleChatKey(...)
if len(c) > 0 {
	m.closeSuggest()
	return m, tea.Batch(append(cmds, c...)...)
}
// textarea.Update ...
m.syncSuggest()
```

**Do not** implement preemption only inside `handleChatKey` with `skipTextarea=true` — viewport will still scroll.

### `syncSuggest`

```text
if mode != modeChat || focus != focusComposer:
  close; return
detect trigger; fill Items; clamp Selected
if trigger gone or zero items: close (v1 zero-item policy)
```

Call after textarea mutates. After insert accept: either close or resync (usually close then idle).

---

## 7. View / layout

```text
parts = header | body | [suggest box] | paddedInput | hint
popupH = 0 or min(8, n) + border overhead (measure lipgloss height, don’t guess)
viewportHeight = termH - header - input - hint - pad - popupH  (≥ minVp)
composerY0/Y1 must include suggest band if mouse later
```

Clamp policy if over termH: shrink viewport → reduce popup rows → shrink composer.

Hint when open: `↑↓ select · tab insert · enter accept · esc dismiss`.

Phase 1: **no hitmap change**. Phase 3: extend rebuild without breaking call sites (optional arg or `registerSuggestZone`).

---

## 8. Providers

### 8.1 Slash (Phase 1)

- `AllSlashCommands()` filtered `Surface` includes tui + `Implemented`.
- Prefix match name+aliases; rank shared prefix length.
- Cap 25.

### 8.2 Session args (Phase 3)

- After `/load ` / `/delete ` second token; `session.ListSessions()`.

### 8.3 Files (Phase 2)

```text
wsRoot on tuiModel from runChat → runTUI → newTUIModel
// NOT SessionDir parent hack; NOT sess.Tools.Execute

WalkDir(wsRoot):
  skip .git, node_modules, vendor
  skip tools.IsSecretPath(rel)  // export in Phase 2
  maxScanFiles=5000, maxDepth=12
  return max 25 filtered

Empty @: depth≤1 only
Non-empty: basename prefix > path contains (no fuzzy dep)
Build once on first @; Gen fence if async later
```

git ls-files: **optional later** accelerator, never shell, timeout ≤200ms, fallback walk.

`--no-tools`: still plumb wsRoot; `@` path hints work.

---

## 9. Security

| Rule | Action |
|------|--------|
| Secret paths | Export `tools.IsSecretPath` (or move to `workspace`); reuse; no copy-drift |
| No file bodies in popup | Paths/meta only |
| Workspace confine | `workspace.Root` Resolve/Rel |
| Caps | scan + depth + return limits |
| No new deps | stdlib walk |
| No secrets in tests/fixtures dumps | use temp dirs |

---

## 10. Phases, gates, tests

### Phase table

| Phase | Deliverable | Ship alone |
|-------|-------------|------------|
| **0** | Catalog + isLocalSlash + handleTab rewire | Yes |
| **1** | Chat `/` popup keys+render+insert/smart Enter | Yes |
| **2** | wsRoot + `@` WalkDir | After 1 |
| **3** | Session args + mouse hitSuggest | Optional |
| **4** | Help MD/dialog sync polish | Optional |
| **5** | Hardening | With merge |

### Order of operations (do not reorder)

1. Catalog + pure tests (A0).
2. Rewire `isLocalSlash` + `handleTab`.
3. A15 `applyTokenReplace` / cursor (no TUI).
4. `suggest` field + `syncSuggest` closed by default.
5. Detect+render only (optional).
6. **`handleSuggestKey` full early-return** preemption.
7. Smart Enter + Reset on execute.
8. Phase 1 acceptance suite.
9. Phase 2 wsRoot + files.
10. Phase 3 mouse last.

### Phase 0 tests (P0)

| ID | Assert |
|----|--------|
| A0a | Every TUI `handleSlashImpl` case + `/search` meta ∈ catalog |
| A0b | Every catalog `surface=tui && Implemented` has handler |
| A0c | `isLocalSlash` ≡ catalog Local∩Implemented∩tui |
| A0d | Plain `handleTab` list ≡ catalog plain\|both names |
| A0e | Flags table: ArgsRequired / EmptyExecutes / PreferInsert per cmd |
| A0f | Aliases `/h` `/?` → `/help` |

### Phase 1 tests (P0)

| ID | Assert |
|----|--------|
| A15 | applyTokenReplace goldens (after empty/trailing/mid/focus) |
| A1 | `/` opens items≥1 |
| A2 | `/lo` shows `/load` |
| A3 | Tab → `/load `; `!waiting`; queue empty; no user block |
| A4 | Enter on `/help` → help blocks; Reset; `!waiting`; no queue |
| A5 | Enter on `/model` (PreferInsert) → `/model ` only; no status if PreferInsert |
| A5b | Incomplete `/he` multi-match Enter → insert only, never execute |
| A6 | Esc closes; buffer kept |
| A7 | Tab closed → still focus cycle (`TestHandleChatKey_TabCyclesFocus` green) |
| A8 | `hello /lo` no popup |
| A11 | waiting + insert accept → queue unchanged |
| A11b | zero items: closed; Enter does not send partial as slash garbage |
| A12 | height=10 open popup: no panic; View lines ≤ height |
| A14 | tui.go ≤600; new files ≤500 preferred |
| A16 | ↑ changes Selected; focus stays composer; no scrollback steal |
| A17 | shift+tab closes (v1 policy) |
| A18 | early return send path closes suggest |

### Phase 2+

| ID | Assert |
|----|--------|
| A9 | `@` lists fixture files; excludes `.env` |
| A10 | `review @path` token replace preserves prose |
| A13 | mouse click row accepts (Phase 3) |

### “No startAI” recipe

There is **no** startAI spy. Assert:

```text
!m.waiting && len(m.pendingQueue)==0 && no new ChatBlockUser for draft
```

Optional counting Completer stub. Completer nil is normal (journey tests avoid real SendUser).

### Fixture

```go
// chatComposerModel: modeChat, focusComposer, ready, 80x40,
// Completer nil, empty queue/blocks, textarea Focused CharLimit 0
// helpers: typeRunes, assertNoTurn, assertSuggest
```

Reuse `keyRunes` pattern from `tui_welcome_input_test.go`. No PTY for Phase 0–1.

---

## 11. Symbols + LOC budget

### Must touch

| File | Change |
|------|--------|
| `slash_catalog.go` | **new** |
| `suggest.go` | **new** — state + keys + replace + sync |
| `suggest_providers.go` | **new** |
| `suggest_render.go` | **new** |
| `tui.go` | fields only (`suggest`; Phase 2 `wsRoot`) |
| `tui_message.go` | call `handleSuggestKey`; closeSuggest on early return; syncSuggest after textarea |
| `tui_view.go` | popup stack + popupH in layout |
| `tui_slash.go` | `isLocalSlash` from catalog |
| `chat.go` | `handleTab` from catalog |
| Phase 2: `tui_run.go`, `chat_repl.go`, `newTUIModel` | pass wsRoot |
| Phase 2: `tools.go` | export `IsSecretPath` |
| Phase 3: `tui_hitmap.go`, mouse | hitSuggest |

### Must not

- Grow `handleChatKey` / `handleChatEnter` past soft 80 without extract.
- Put suggest logic in `tui.go`.
- Change `routeFocusKey` for suggest.
- Call `sess.Tools.Execute` for file list.
- Add fuzzy/gitignore deps.

---

## 12. Risks / non-goals / backlog

### Risks → mitigation

| Risk | Mitigation |
|------|------------|
| Viewport dual-update | Full early-return A16 |
| Enter double path | Preempt Enter entirely when open+items |
| Slash non-reset | Reset on execute A4 |
| Soft-wrap cursor | A15; fallback end-of-buffer for slash |
| Layout clip | popupH in remain math A12 |
| Secret path drift | Export IsSecretPath |
| Test compile break hitmap | Phase 3 only |
| PreferInsert changes bare `/model` from popup | Document; keep bare typed Enter without popup as today |

### Non-goals (v1)

Welcome popup, mouse, mid-line `/`, body inject, palette, fuzzy dep, Charm v2 overlay, nested tea.Model, raising structure baselines.

### Backlog

Welcome parity; `@` attach; skills in `/`; progressive path Tab; git accelerator; implement TUI `/exit` or stop advertising; help MD generation from catalog.

---

## 13. Implementation checklist

1. Re-read this plan + `tui_message.go`, `tui_keys.go`, `tui_view.go`, bubbles textarea `SetValue`/`Update`.
2. Phase 0 catalog + A0*.
3. A15 replace helper green.
4. Phase 1 field + render + `handleSuggestKey` + A1–A8, A11*, A12, A14, A16–A18.
5. Phase 2 wsRoot + files + A9–A10.
6. Phase 3 mouse/args optional.
7. Verify: `go test ./internal/cli/ -count=1` (narrow first).
8. Completion report: files, commands, residual risk.

---

## Success definitions

### Phase 1 (MVP ship)

User in **TUI chat**:

1. Types `/` → command list above composer.
2. Filters; ↑↓; Tab inserts; Esc dismisses.
3. Enter on PreferInsert/`ArgsRequired` inserts with trailing space; on EmptyExecutes non-PreferInsert runs local command and **clears** buffer.
4. Tab without popup still focus-cycles.
5. No mid-line slash popup; no queue on insert accept while waiting; no transcript scroll on ↑↓.

### Full plan (Phase 2+)

6. `@` file paths (no secrets); token replace preserves prose.
7. Optional mouse row click.

---

## Appendix A — Related plans

- `tui-chat-ux-full-experience.md` — deferred `@`; this plan owns composer suggest
- `tui-clean-chat-ux.md` — monomorphic model / no nested tea.Model

## Appendix B — Research index

| Topic | URL |
|-------|-----|
| Claude Code interactive | https://code.claude.com/docs/en/interactive-mode |
| OpenCode TUI | https://opencode.ai/docs/tui/ |
| Warp @ context | https://docs.warp.dev/agent-platform/local-agents/agent-context/using-to-add-context/ |
| Overlay pitfalls | https://lmika.org/2022/09/24/overlay-composition-using.html |

## Appendix C — Challenge scorecard (v3)

| Area | Score | Residual |
|------|-------|----------|
| Catalog SoT | Strong | Help MD rewrite can lag tests |
| Key preemption | Fixed in plan | Must code early-return |
| Viewport dual-update | Fixed in plan | A16 required |
| Token replace API | Fixed recipe | Soft-wrap A15 risk |
| Enter arity | Fixed flags | PreferInsert product choice |
| Workspace | Phase-split | Don’t hack SessionDir |
| File index | WalkDir first | Export secret path |
| LOC | Explicit extracts | handleChatKey at soft 80 already |
| Tests | A0–A18 | No startAI spy — side effects only |
| Welcome/mouse | Correctly deferred | — |

**Verdict:** Implement Phase 0 now. Phase 1 after A15 + preemption extract. Do not open `@` or hitmap until Phase 1 green.
