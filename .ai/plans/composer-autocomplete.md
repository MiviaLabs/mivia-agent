# Composer Autocomplete — `/` and `@` suggestion popup

**Status:** Implementation-ready (challenged + revalidated)
**Date:** 2026-07-28
**SoT path:** `.ai/plans/composer-autocomplete.md`
**Product goal:** While typing in the chat composer, show a filterable suggestion box **above** the input when the user types `/` or `@`. Select with ↑↓ / mouse click; accept without sending the message.

---

## 0. Challenge method

| # | Lane | Outcome |
|---|------|---------|
| 1 | Codebase explore (TUI, slash, skills, hitmap) | No popup today; Tab ≠ complete in TUI; `@` absent; dual slash inventories |
| 2 | Internet patterns (Claude Code, OpenCode, Warp, Docker Agent, Bubble Tea) | Layout-stack popup; provider split; Enter/Tab accept; line-start `/` safer |
| 3 | Parent re-verify high-impact claims | Confirmed against `tui_*`, `composer.go`, `chat.go` `handleTab`, `skills/*`, hitmap |
| 4 | Conflict with existing TUI UX plans | Full-experience plan explicitly deferred `@`-mentions; this plan owns them |
| 5 | Security / privacy | File paths only in UI; no secret-bearing content in suggestions; gitignore + caps |

Agents must re-verify symbols before editing; this plan is not a substitute for reading current source.

---

## 1. Ground truth (validated 2026-07-28)

### Stack (locked)

| Keep | Reject |
|------|--------|
| Bubble Tea + bubbles `textarea` + lipgloss | Second TUI framework, nested `tea.Model` rewrite of whole chat |
| Monomorphic `tuiModel` + pure modules | Raising `tui.go` / `chat.go` baselines to silence size gates |
| Layout **stack** (popup row above composer) | Untrusted overlay packages; full-screen modal for every keystroke |

### What exists

| Capability | Where | Gap vs goal |
|------------|--------|-------------|
| Composer card | `internal/cli/composer.go`, `tui_view.go` | No strip above input for suggestions |
| TUI slash on Enter | `tui_keys.go` → `handleSlash` / `tui_slash_handlers.go` | No live list while typing |
| Plain Tab complete | `chat.go` `handleTab` + fixed string list | Prefix only; no popup; TUI does not use it |
| TUI Tab | `tui_keys.go` → `cycleChatFocus` | **Owns Tab** for focus; cannot also mean “complete” unless gated |
| Session list nav | welcome picker (`tui_keys` + `welcome.go`) | Pattern for ↑↓ lists, not wired to composer |
| Mouse hit zones | `tui_hitmap.go` (`hitTranscript`, `hitTools`, `hitComposer`) | No suggest zone |
| Skills registry | `internal/skills/*` loaded into dispatcher | **Not** user slash surface; no `/skill` command |
| `@` mentions | — | **None** |

### Slash inventory drift (must fix as part of catalog SoT)

| Command | TUI `handleSlashImpl` | Plain `handleTab` / handlers | Help MD |
|---------|----------------------|------------------------------|---------|
| `/help` `/clear` `/status` `/model` `/budget` `/steps` | yes | yes | yes |
| `/save` `/load` `/list` `/delete` `/session` `/tools` `/plain` | yes | yes | partial |
| `/search` | rewrite → model | rewrite → model | help dialog |
| `/exit` `/quit` `/q` | **no** (docs claim yes) | yes | docs claim |
| `/provider` `/workspace` | **no** | yes | dialog yes |

**Decision:** introduce a single command catalog used by autocomplete, help, and handlers. TUI quit remains Ctrl+C/D; catalog may still list `/exit` if implemented or hide it until implemented.

### Skills vs slash (do not conflate)

| Concept | Reality | Autocomplete role |
|---------|---------|-------------------|
| UI slash commands | Local session control | Primary `/` source |
| `.ai/skills/*/SKILL.md` | Agent/runtime skills via dispatcher | Phase 2+ optional `/skill` or skill names under `/` — **not** identical to slash today |
| Subagent handlers | `delegate`, `oneshot`, `multi_step`, skill names | Optional `@agent:` or `/delegate` later |

User language “skills” in the prompt maps best to **discoverable slash actions + future skill names**, not only `internal/skills.Registry`.

### Textarea API constraints

Bubbles `textarea.Model` exposes: `Value`, `SetValue`, `InsertString`, `Line`, `LineCount`, `SetCursor` (column on current line), `CursorStart` / `CursorEnd`.
Token replace must compute rune offsets carefully (multiline). Prefer: detect token left of cursor → build new full value → `SetValue` + place cursor after insert (may need small helper that maps absolute rune index → row/col).

---

## 2. Product decisions (locked for v1)

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Primary surface | **TUI composer** (welcome + chat) | Where users type; plain gets catalog parity later |
| `/` trigger | **Line-start only** for v1 (`^\s*/`) | Avoid `src/foo` false opens; matches Claude Code default |
| `@` trigger | Token left of cursor starting with `@` | Mid-message file refs |
| Popup placement | **Above** composer card (layout stack) | Shrinks viewport height when open; no ANSI overlay |
| Accept keys | **Enter** and **Tab** while popup open with items | Enter does **not** send; Tab does **not** focus-cycle |
| Dismiss | Esc, Backspace past trigger, cursor leaving token, empty matches after close-on-zero policy | Standard |
| Mouse | Single click row = accept (when mouse enabled) | Reuse hitmap pattern |
| `/` insert | Replace token with command + trailing space if args expected | e.g. `/load ` then optional stage-2 |
| `@` insert | Replace token with `@rel/path` (display token) | v1: token in message text; model sees path string — **not** silent attachment payload |
| File search | Workspace root, respect ignore patterns if cheap, hard cap results | No full tree dump |
| Max visible rows | 8 (scroll window if more) | Fits short terminals |
| Debounce | Commands sync; files ≥50ms debounce + cancel | Keep paint responsive |

### Explicit non-goals (v1)

- True floating overlay over transcript scanlines
- Mid-line `/` commands
- Symbol / LSP completion
- Silent attachment blocks separate from message text (Docker-style)
- Full command palette (`Ctrl+P`) — backlog
- Nested Bubble Tea models for entire chat
- Auto-executing slash on accept (always leave buffer; user Enter to run, except we accept-into-buffer only)
- Raising go-structure baselines

### Open product calls (defaults if no answer)

1. **`@` multi-kind in v1?** Default: **files only**. Agents/skills under `@` wait for phase 2.
2. **Stage-2 `/load` session names?** Default: **yes** when query is `/load ` + suffix — provider switch to session list.
3. **Plain REPL popup?** Default: **no popup**; wire same catalog into Tab + print match list if multi (improve `handleTab` only).

---

## 3. Architecture

```text
┌──────────────── tuiModel ─────────────────┐
│  textarea.Model                           │
│  suggestState  ── open, trigger, query,   │
│                   items[], selected, y0/y1│
│         │                                 │
│         ▼ after each relevant key         │
│  detectTrigger(value, cursorAbs)          │
│         │                                 │
│         ▼                                 │
│  SuggestProviders                         │
│    ├─ SlashCommandProvider (catalog SoT)  │
│    ├─ SlashArgProvider (/load sessions)   │
│    └─ FileProvider (@ path fuzzy)         │
│         │                                 │
│         ▼                                 │
│  renderSuggestPopup (above composer)      │
│  hitSuggest zone + key preemption         │
└───────────────────────────────────────────┘
```

### New files (keep ≤500 LOC each)

| File | Responsibility |
|------|----------------|
| `internal/cli/suggest.go` | Types: `SuggestItem`, `SuggestKind`, `suggestState`, trigger detect, filter, accept/replace |
| `internal/cli/suggest_providers.go` | Command catalog + session arg + file provider interfaces |
| `internal/cli/suggest_render.go` | Lipgloss box View; window around selection |
| `internal/cli/suggest_test.go` | Trigger, filter, accept, key priority pure tests |
| `internal/cli/suggest_providers_test.go` | Catalog match, `/load` args, file filter caps |
| `internal/cli/slash_catalog.go` | **Single SoT** of slash commands (name, aliases, desc, argHint, local/rewrite) |

Optional later: `suggest_files.go` if file provider grows.

**Do not** grow `tui.go` further — wire thin hooks only.

### Key types (sketch)

```go
type SuggestKind uint8 // Command, Session, File, Agent (reserved)

type SuggestItem struct {
	Kind        SuggestKind
	Label       string // "/load", "internal/cli/tui.go"
	Description string // short help / path meta
	Insert      string // exact replacement for the token
	Score       int
}

type suggestState struct {
	Open     bool
	Trigger  rune // '/' or '@' or 0
	Query    string
	TokenFrom, TokenTo int // rune offsets in full buffer
	Items    []SuggestItem
	Selected int
	Scroll   int
	Y0, Y1   int // last paint absolute rows for mouse
}
```

### Trigger detection

```text
token = runes from last whitespace (or start) to cursor
if buffer has only whitespace before first non-ws and token matches ^/.*  → slash
else if token matches ^@.*                                    → at
else closed
```

v1 slash: require token starts at first non-whitespace of entire buffer (line-start rule for multiline: only when cursor is on first non-empty segment / first line). **Safer default:** only open `/` when entire `Value()` (trim left) starts with `/` and cursor is still within that first token.

### Key preemption (order matters)

In `handleChatKey` / welcome key path, **before** `routeFocusKey` and textarea:

```text
if suggest.Open:
  up/down/ctrl+p/ctrl+n → move selection, skipTextarea
  enter/tab             → accept selected, skipTextarea, do not send
  esc                   → close popup, skipTextarea
  else                  → fall through to type (recompute after textarea.Update)
```

After textarea updates (or on any value change path), call `m.syncSuggest()`.

**Conflicts resolved:**

| Key | Popup open | Popup closed (unchanged) |
|-----|------------|---------------------------|
| ↑↓ | Select item | Welcome session nav / history / scrollback as today |
| Tab | Accept | Focus cycle |
| Enter | Accept | Send / slash execute / queue |
| Esc | Close popup | Clear selection → composer |

### View / layout

In `chatViewLayout` / `renderChatView` (and welcome view symmetrically):

```text
header
viewport (height reduced by popupH when open)
[ suggest popup box ]   ← new, only if open && len(items)>0
composer card
hint line (show "↑↓ select · enter/tab accept · esc dismiss" when open)
```

Recompute `viewportHeight` like composer height shrink loop. Register hit zone `hitSuggest` with per-row Y map (mirror `toolPanelState.rowY`).

### Accept algorithm

```text
buf = Value as runes
new = buf[:TokenFrom] + Insert + buf[TokenTo:]
SetValue(string(new)); place cursor after Insert
close popup (or reopen if stage-2 arg provider wants — only for trailing space + known command)
```

Do **not** call `handleSlash` / `startAI` on accept.

### Mouse

`handleMouseMsg`: if `hitSuggest` and click Y maps to row → set Selected + accept. Click outside while open → dismiss (optional v1: dismiss).

---

## 4. Providers

### 4.1 SlashCommandProvider

Source: `slash_catalog.go` slice of:

```go
type SlashCommand struct {
	Name        string   // "/load"
	Aliases     []string // "/h"
	Description string
	Args        string   // "<name>" or ""
	// Local: true → isLocalSlash; false → rewrite/model (e.g. /search)
}
```

Match: prefix on `Name` and aliases (case-insensitive). Rank: exact prefix length, then alpha. Empty query after `/` → full catalog (capped ~25).

**Unify:** `isLocalSlash`, help MD generation, plain `handleTab` list all derive from this catalog (or generated helpers). Stop hand-maintaining three lists.

### 4.2 SlashArgProvider (v1: `/load`, `/delete`, `/save` optional)

When first field is a command that takes a session name and there is a trailing space or second token:

- Query session names via `session.ListSessions()`
- Filter by prefix/substring
- Insert: `/load <name>` (full replacement of slash token range)

### 4.3 FileProvider (`@`)

- Root: workspace path from config / process cwd (same as tools)
- Query: basename + path fuzzy (simple subsequence or `strings.Contains` + basename boost first; upgrade to `sahilm/fuzzy` only if already a dep — **do not add deps lightly**)
- Ignore: `.git/`, common junk; prefer `.gitignore` if implementable without new heavy deps (optional phase 1.5)
- Cap: 25 results; empty query → recent files if available, else top-level entries only (not recursive flood)
- Insert: `@path/with/slashes` workspace-relative, forward slashes

**Security:** never read file contents into suggestion rows; labels are paths only. No secrets in descriptions.

### 4.4 Phase 2 providers (not v1)

| Provider | Trigger | Notes |
|----------|---------|-------|
| Skills | `/` or `@skill:` | Names from `skills.Registry.List()` / markdown frontmatter |
| Agents | `@agent:` | Subagent handler names from dispatcher |
| Tools | `/tools` already lists; optional `@tool:` | Low priority |

---

## 5. Phases

| Phase | Name | Outcome | Gate |
|-------|------|---------|------|
| **0** | Catalog SoT | `slash_catalog.go`; TUI + plain + help consume it | unit tests for catalog completeness vs handlers |
| **1** | Suggest core + `/` popup | Open/filter/nav/accept/dismiss; view strip; key preemption | pure + TUI model tests |
| **2** | `@` files | File provider + caps | unit + fake FS tests |
| **3** | Arg stage + mouse | `/load` names; hitSuggest click | mouse test pattern from `tui_mouse_test.go` |
| **4** | Plain REPL parity | `handleTab` uses catalog; optional multi-match print | existing input tests |
| **5** | Hardening | Hint text, help MD Tab wording when popup, structure LOC, journey tests | `make test` scoped packages |

Implement 0→1 first shippable. 2–3 same PR if small. Do not block 1 on fuzzy perfection.

### Phase 0 detail

1. Extract catalog from `handleTab` list + `isLocalSlash` + help.
2. Generate `isLocalSlash` from catalog flags.
3. Align TUI handlers: either implement missing `/provider` `/workspace` or mark catalog `TUI: false`.
4. Fix help MD: document Tab = complete when `/` or `@` active; else focus cycle. Document `/exit` only if implemented.

### Phase 1 detail — edit surfaces

| Step | Files |
|------|--------|
| State on model | `tui.go` field `suggest suggestState` only |
| Keys | `tui_keys.go` preemption + post-type `syncSuggest` |
| Message loop | Ensure welcome path also syncs (same composer) |
| View | `tui_view.go` insert popup; height math |
| Tests | `suggest_test.go`, `tui_suggest_test.go` (model Update with KeyMsg) |

### Phase 2–3 detail

- File provider uses `os.ReadDir` / walk with depth limit or `filepath.WalkDir` with early stop.
- Prefer reuse of workspace root from session/config already used by tools.
- Hitmap: add `hitSuggest`; rebuild in `renderChatView` with popup Y range.

---

## 6. Tests (acceptance)

| ID | Assert |
|----|--------|
| T1 | Typing `/` alone opens popup with ≥1 catalog items |
| T2 | Typing `/lo` filters to `/load` (and aliases if any) |
| T3 | ↓ then Enter inserts `/load ` and does not call startAI / does not clear waiting |
| T4 | Tab with popup open accepts; Tab with empty composer still focus-cycles |
| T5 | Esc closes; buffer keeps typed prefix |
| T6 | `hello /lo` does **not** open slash popup (line-start rule) |
| T7 | `@cli` returns paths under workspace fixture |
| T8 | Accept `@` replaces token only (preserves leading prose) |
| T9 | Mouse click on suggest row accepts when mouse path enabled |
| T10 | Catalog names ⊇ all handled slash cases in TUI switch |

Use pure unit tests for detect/filter/accept. Prefer model-level Update tests over PTY for core logic (match `tui_keys_test.go` / `tui_mouse_test.go` style).

---

## 7. Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Enter double-meaning (send vs accept) | Hard gate: if `suggest.Open && len(Items)>0` → accept only |
| Tab focus regression | Gate accept only when popup open with items; tests T4 |
| Viewport height thrash | Cap popup height; recompute layout like composer shrink |
| File walk hangs on huge trees | Depth/count caps, debounce, cancel, empty-query non-recursive |
| Cursor after SetValue wrong on multiline | Dedicated rune→row/col helper + tests |
| Slash inventory drift returns | Single catalog; test T10 |
| Privacy: paths in UI | Paths OK; never dump file bodies into popup |
| LOC baseline | New files; thin hooks in tui_* only |
| User expects skills in `/` immediately | Phase 2; document v1 = commands + files |

---

## 8. Implementation order (agent checklist)

1. Re-read `tui_keys.go`, `tui_view.go`, `tui_message.go`, `composer.go`, `chat.go` handleTab, `tui_hitmap.go`.
2. Land Phase 0 catalog + tests (no UI yet).
3. Land Phase 1 suggest state + render + keys + T1–T6.
4. Land Phase 2 file provider + T7–T8.
5. Land Phase 3 mouse + session args + T9.
6. Phase 4 plain Tab.
7. Run: `go test ./internal/cli/ -count=1` (and package-focused first).
8. Update `slashHelpMD` / placeholder hint only as owned by this feature.
9. Completion report: files, verification commands, residual risk.

---

## 9. Out of scope / backlog

- Command palette (`Ctrl+K` / `Ctrl+P`)
- Mid-line slash commands
- Attachment payload model separate from `@path` text
- Symbol completion
- Skills/agents as first-class `@` sections
- Classic REPL floating UI
- Theme token `suggestion` color system (use existing lipgloss styles)

---

## 10. Challenged findings (summary)

| Claim | Verdict |
|-------|---------|
| “We already have autocomplete” | **Partial false** — plain Tab prefix only; TUI has zero |
| “Skills are slash commands” | **False** — skills are runtime/dispatcher; UI slash is separate |
| “Need z-order overlay” | **False for v1** — layout stack is correct and safer |
| “Tab must complete” | **Conditional** — only while popup open; else focus cycle stays |
| “`@` for agents now” | **Premature** — files first; agents need roster UX |
| “`/load` is the only command” | **False** — full catalog; `/load` is best arg-completion example |
| Existing UX plan forbids mentions | **True as non-goal of that plan** — this plan supersedes for autocomplete only |

---

## 11. Success definition

A user on TUI:

1. Types `/` → sees commands with descriptions above the composer.
2. Filters with more characters; ↑↓ highlights; Enter/Tab inserts; Esc dismisses.
3. Types `@` → sees workspace files; selects one; message contains `@path`.
4. After accept, can still edit and press Enter to send/run.
5. Mouse click on a row works when mouse is enabled.
6. No send-on-accept; no focus-cycle-on-Tab while popup open; no mid-line slash false positives.
