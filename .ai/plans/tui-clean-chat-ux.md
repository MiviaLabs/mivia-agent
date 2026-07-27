# Clean Chat CLI UX — Implementation Plan (challenged + revalidated)

**Status:** Implementation-ready (post challenge)
**Date:** 2026-07-27
**SoT path:** `.ai/plans/tui-clean-chat-ux.md`
**Goal framing:** Transcript-first **correctness + quiet chrome** (not “exceptional peer parity” after theme polish alone)

---

## 0. Challenge method (this revision)

Five adversarial read-only lanes (2026-07-27) against **current** `internal/cli/` + plan text:

| # | Lane | Outcome |
|---|------|---------|
| 1 | Plan vs code facts | Confirmed residuals; fixed overstated dual-fire, layout nuance, focusTools, help drift, unwired fence |
| 2 | Phase order / scope | R0 data integrity first; mouse earlier; theme off critical path; reframe success bar |
| 3 | Charm / architecture | Throttle paint-side; no nested tea.Model rewrite; dirty-tail backlog; dual height SoT |
| 4 | Security / privacy | History tool expand uncapped; thinking wording half-true; session file risk |
| 5 | Test / acceptance | Measurable asserts; journey never runs `startAI`; TTY smoke must be scripted |

Parent verified high-impact claims against source before applying edits below.

---

## 1. Stack (locked)

| Keep | Version | Reject |
|------|---------|--------|
| Bubble Tea | v1.3.10 | Ink / Ratatui / tview |
| Bubbles | v1.0.0 | Second event loop |
| Lip Gloss | v1.1.0 | Unbounded ANSI without theme slots (later) |
| Dual surface | TUI + `--plain` + `-p` | Card rewrite of plain |
| Structure | soft 500 / hard 800 | Raising baselines; **also lower `tui.go` grandfather (1682) once stable** |

Charm v2 / teatest goldens: **post-R5 backlog**, not phase gates.

**Architecture rule:** monomorphic `tuiModel` root + pure modules/files. **Do not** introduce nested `tea.Model` trees for chat chrome (LOC + finishStream cost).

---

## 2. Current code reality (revalidated)

### Shipped (keep)

- Welcome + session picker + auto labels + `ctrl+o` continue-last (`tui_keys.go`)
- `ChatBlock` SoT + `RenderChatBlocks` + hit ranges
- Focus enum; Tab cycles **composer ↔ scrollback only** (`nextTUIFocus` never returns `focusTools`)
- Stream bridge + caps; `pollCmd` ~80 ms or notify
- User cards + composer; assistant header + **footer** (`formatModelFooter`)
- Live strip **not** in View → status line `◐ running · done · total`
- Finish: one collapsed `ChatBlockTool` group + `ChatBlockDivider` duration
- Thinking UI + `adjustThinkingScroll` + `ctrl+t` (tests exist)
- Mouse always-on (`WithMouseCellMotion`)
- At-bottom auto-scroll when `AtBottom`; `↓ more below` when not (not unread-stream chip)

### Confirmed residuals (ordered by severity)

| Sev | Residual | Evidence / correction |
|-----|----------|------------------------|
| **P0** | **Truncate-by-line corrupts blocks** | `appendBlock` drops `messages` by line count then slices `blocks` by same count (`chatblock.go`) |
| **P0** | **Dual height systems** | `layout()` still uses `calcToolPanelLines`; **View** uses `chatViewLayout` and overwrites `viewport.Height` (no strip budget; may not reserve 1-line tool status → clip) |
| **P0** | **Live user ≠ history** | `startAI` `appendMsg` → `ChatBlockSystem{Rendered}` card lines; hydrate → `ChatBlockUser` |
| **P0** | **History tool expand uncapped / unredacted** | `renderToolBlock` expanded dumps full text; hydrate stores raw args/results; live path uses `redactPreview` |
| **P1** | **Dead `OnAgentEvent` in `runTUI`** | Not dual-fire: `SendUserWithEvent` **overrides** callback. Weak handler is dead landmine if future path omits override |
| **P1** | **`FenceTurn` unwired** | Bridge API exists; `startAI` never sets turn ID / fence; safety ≈ Close+new bridge |
| **P1** | **`focusTools` fully orphaned** | Tab never selects it; tool nav keys require it; strip unpainted |
| **P1** | **Help MD stale** | Claims Tab/tool strip/Space/e/E/G; omits Ctrl+T; no `G` handler |
| **P2** | Dense status + stepDetail double-paint (status + composer) | `brand.go`, drain clears step on empty tick |
| **P2** | No unread-stream chip while scrolled up | Only `↓ more below` / AtBottom |
| **P2** | Full `renderStreamVP` rebuild per drain | Bridge coalesces; paint not throttled |
| **P2** | Mouse always-on → copy friction | Binary on/off via Enable/Disable cmds |
| **P2** | Thinking gate: push when `activeTools==0` may drop early reasoning | `tui_stream.go` |

### Product privacy truth (must not overclaim)

| Claim | Truth |
|-------|--------|
| Thinking not in JSONL as thinking blocks | **True** (only `provider.Message` lines) |
| Thinking “ephemeral” | **Partial:** UI keeps full `ChatBlockThinking.Text` in RAM until process exit; assistant `Content` **is** persisted |
| Tool expand same as live redaction | **False today** — must implement |
| Session files fail-closed perms | **Not guaranteed** (umask / Create) |

---

## 3. External research (unchanged intent, tightened application)

**Charm:** pure Update; I/O via Cmd; measured layout; throttle paint; structs as SoT; mouse opt-in; teatest later.
Refs: bubbletea, bubbles, lipgloss, chat example, Crush separation of domain/UI.

**Peers:** transcript-first; progressive disclosure; ambient status; stream prose / batch tools; composer sacred; density > decoration; minimal monochrome path.

**Product rule:** If the user cannot scan the last answer and active tool status in under a second, fold or delete chrome.

---

## 4. Product defaults

| Decision | Default | Notes |
|----------|---------|-------|
| UI thinking → JSONL as thinking | **No** | RAM-only thinking blocks |
| Assistant/tool messages on disk | **Yes** (existing) | Document sensitivity |
| Focus after send | **Stay composer** | |
| Continue-last | **`ctrl+o` welcome** | |
| Tool expand paint | **Shared redact + byte/line caps** | Required, not aspirational |
| Mouse default | **On until R1 toggle ships**; target default **off or user-config** for copy-friendly | Runtime Disable/Enable, not restart |
| Vim mode | Out of scope | |

---

## 5. Non-goals (this plan)

- Nested Bubble Tea sub-models rewrite
- Dirty-tail incremental history paint (backlog)
- Side-by-side diff, plan mode UI, multipane IDE
- Charm v2, teatest goldens (backlog)
- Plain REPL card rewrite
- Theme multi-pack / OS auto theme as sign-off gate
- Skill blocks without real events
- Raising structure baselines

**Reframe:** full “exceptional agent product” needs trust UX (permissions) later; this plan delivers **correct, quiet, scannable chat**.

---

## 6. Cross-cutting contracts

1. `blocks` = transcript SoT; `messages` = render cache only.
2. Only Bubble Tea `Update` mutates `tuiModel`.
3. **Either** wire `SetTurnID`+`FenceTurn` in `startAI` **or** document bridge-swap-only and test cancel+late events.
4. **Every** tool paint path (finish group, hydrate, expand) uses one shared cap+redact helper.
5. No secrets in fixtures; expanded tool dumps cannot bypass redaction.
6. Prefer new files; shrink dead strip code before adding chrome.
7. Structure check must pass; consider lowering `tui.go` maxLines after R0.

---

## 7. Phases (resequenced)

### R0 — Data integrity + single layout + single event path (must ship alone)

**Order inside R0:**

1. **Block-based truncate** (fix SoT corruption)
2. **Live user → `ChatBlockUser{Text}` only** (delete pre-rendered system card lines)
3. **Delete dead `runTUI` `OnAgentEvent`** (override-only bridge)
4. **Wire turn fence or explicit Close-only contract + tests**
5. **Single height owner** shared by Update + View (`measureChatChrome` / `chatViewLayout`): reserve 0–1 tool status; remove `calcToolPanelLines` from active path
6. **Retire `focusTools` + strip-only key surface** (or restore strip — choose delete unless product re-adds strip)
7. **Help MD honesty** for keys that already exist (do not wait for R3)

**Files:** `chatblock.go`, `tui.go`, `tui_layout.go`, `tui_view.go`, `tui_focus.go`, `tui_keys.go`, `tui_events.go`, help const, tests.

**Measurable acceptance**

- [ ] `TestLayout_ViewportHeightStableWithTools`: H=24 W=80; idle vs waiting+tools; `|vpH_tools - vpH_idle| ≤ 1`
- [ ] View line count ≤ height with tools running (no composer clip)
- [ ] Live send path → last user block `Kind==ChatBlockUser`; reflow W∈{40,80,120} matches hydrate (ANSI-stripped)
- [ ] Truncate keeps N newest **blocks**; kinds/order stable; hit ranges valid
- [ ] After Close/fence: drain applies 0 stream/tools/thinking
- [ ] TUI init: no weak dual `OnAgentEvent` landmine (`nil` or bridge-only)
- [ ] Tab/S-Tab never lands on `focusTools`
- [ ] Help text matches handlers (table-driven)
- [ ] `go test ./internal/cli/ -count=1`
- [ ] `python3 scripts/check_go_structure.py --all`

**Stop:** persistence schema change; truncate still by line index into blocks.

---

### R1 — Quiet chrome + follow semantics + mouse toggle

| Work | Detail |
|------|--------|
| Slim status | brand + model + phase (`idle`/`working`/`queue N`) + elapsed **only while working**; no tool names/args |
| stepDetail | update only when non-empty; TTL; stop clearing every empty drain; at most one surface (composer **or** status) |
| Follow | Prefer extend `AtBottom` semantics: when stream advances while not following, show **`↓ new`**; End/jump re-enables follow. Add `followTail` only if AtBottom proven insufficient (loadMore/resize footguns) |
| Scrollback focus | non-color-only selection marker (prefix/rail) |
| Mouse toggle | runtime `DisableMouse` / `EnableMouseCellMotion`; default document; keyboard path complete |
| Queue display | prefer count chip `▣N`; **no** full prompt text in system blocks if avoidable |

**Measurable acceptance**

- [ ] Status allowlist test: no tool name/args; elapsed only if waiting
- [ ] Scrolled-up + stream drains → YOffset unchanged; View has `↓ new` (or documented equivalent); jump clears
- [ ] Keyboard-selected block has stable substring marker in View
- [ ] Mouse-off: MouseMsg does not change focus
- [ ] Keys/journey tests green for 2-pane matrix

---

### R2 — Stream paint throttle + tool history privacy

| Work | Detail |
|------|--------|
| Throttle | **Paint-side only:** min 33–50 ms between `renderStreamVP`; **flush immediately** on done/finish/cancel/resize. Do not starve `Drain` or replace poll with “throttle.” |
| Dirty-tail | **Out of scope** — acceptance is throttled full rebuild |
| Tool expand | Shared `formatToolExpand` = redactPreview + byte + line caps; hydrate tools **collapsed by default** |
| Thinking | collapsed after turn; expand cap unchanged; document RAM retention |
| Optional | queue chips count-only |

**Measurable acceptance**

- [ ] Burst stream ticks → paint count ≤ ceil(T/minInterval)+ε
- [ ] Expand path fixtures: fake secrets must not appear full
- [ ] `TestRenderChatBlocksToolExpanded` asserts **cap/redact**, not full dump
- [ ] Post-finish: thinking collapsed; one tool group
- [ ] `go test -race ./internal/cli ./internal/chat`
- [ ] Race scenarios: concurrent bridge+tick; cancel + late Finish

**Stop:** any expand path paints raw uncapped tool I/O.

---

### R3 — Dead code purge + doctor (optional thin)

- Delete unused strip render call graph if R0 chose delete
- Quarantine/update toolpanel tests that only exercise dead View
- `mivia doctor` cheap TUI notes if free
- Structure: lower `tui.go` baseline if still inflated

**Sign-off for “clean chat correctness” is allowed after R0–R2 + headless gates.** R3 is cleanup.

---

### R4 — Theme v0 / welcome polish (optional, separate PR)

Not required for clean-chat complete. Dark theme struct extraction only if timeboxed.

---

### R5 — Interactive certification (scripted)

Headless green ≠ interactive done.

| Step | Env | Expect |
|------|-----|--------|
| Welcome → new chat | 80×24 | picker works |
| Send | same | user card + stream; focus composer |
| Queue while waiting | same | queue N; no lost draft |
| Cancel | same | waiting false; no further append |
| Scroll up mid-stream | same | no auto-jump; `↓ new` |
| Block + tool expand | keyboard | redacted expand |
| Resize | 80×24→120×40→60×20 | no panic; lines ≤ height |
| Mouse off | human | native select works |
| `--plain` / `TERM=dumb` | separate | usable non-Bubble path |

Gates:

```text
go test ./internal/cli/ ./internal/chat/ -count=1
go test -race ./internal/cli ./internal/chat
python3 scripts/check_go_structure.py --all
go test ./... -count=1
go vet ./...
make verify
make build
make secret-scan
```

Record PASS/FAIL/NOT_RUN. Sign interactive only with TTY table.

---

## 8. PR slicing

| PR | Content | Merge rule |
|----|---------|------------|
| PR1 | R0 only | Must ship alone; no chrome |
| PR2 | R1 quiet + follow + mouse | Headless + structure |
| PR3 | R2 throttle + tool privacy | Headless + race + redaction fixtures |
| PR4 | R3 purge | Optional |
| PR5 | R4 theme | Optional, never gates “clean chat done” |
| Cert | R5 TTY | Required for interactive claim |

---

## 9. Later backlog

1. Permission modal (trust surface)
2. Command palette / fuzzy slash
3. Plan mode + write gate
4. Multi-theme + minimal mode
5. Tasks pane for subagents
6. `@` files + `$EDITOR`
7. teatest fixed-size goldens
8. Dirty-tail block line cache
9. Charm v2
10. Session files `0600` / dir `0700`
11. Product decision: `redact_tool_args` default-on for shared environments

---

## 10. File ownership

| Path | Role |
|------|------|
| `chatblock.go` / `chatblock_render.go` | SoT, truncate-by-block, tool expand paint |
| `tui_view.go` + shared measure | Height SoT + View |
| `tui_layout.go` | finishStream, stream paint (no dual strip budget) |
| `tui.go` | startAI, runTUI, mouse opts, fence wire |
| `tui_keys.go` / `tui_focus.go` | 2-pane only after R0 |
| `tui_stream.go` / `tui_events.go` | bridge + single callback |
| `toolui.go` | shared tool line + expand helper |
| `toolpanel.go` | delete or restore deliberately |
| `brand.go` | slim status |
| `composer.go` / `msgcard.go` / `welcome.go` | chrome |

---

## 11. Global stop conditions

- Truncate desyncs blocks/messages
- Race, leak, post-cancel paint
- Uncapped tool expand or secrets in fixtures
- Structure only fixable by raising baseline
- Dual height systems left in place after R0

---

## 12. Relation to other artifacts

| Artifact | Role |
|----------|------|
| **This file** | Sole TUI clean-chat SoT |
| `docs/product/agent.md` | Update after user-visible ship |
| Deleted: HANDOFF, tui-ux-revamp, tui-revamp-implementation-plan | Do not resurrect |

---

## 13. Challenge changelog (vs prior plan revision)

- Dual event: **dead landmine**, not dual-fire
- Layout: **dual systems**; View may clip without reserving tool status
- focusTools: **fully** orphaned
- turn fence: **aspirational** until wired
- Truncate: **P0 corruption**, not polish
- Help: **current bug**, not deferred polish
- Nested models: **rejected**
- Dirty-tail: **backlog**
- Throttle: **paint-side only**
- Tool expand privacy: **must implement**, not claim current
- Thinking ephemeral: **wording fixed**
- Theme: **optional**; sign-off after R0–R2
- Mouse toggle: **R1**, not R3
- Acceptance: **measurable tests + scripted TTY**
- Goal: **correct quiet chat**, not theme-defined exceptional

---

## 14. Completion report (each PR)

- Outcome
- Changed files
- Verification (commands + results)
- Residual risk
- Checkbox updates here
