# P2.4 — Split `handleSlashImpl` into per-command-group functions

**Status:** Implemented (2026-07-31) — `handleSlashImpl` is a thin router; command bodies live in `handleTuiInfoSlash`, `handleTuiModelSlash`, `handleTuiLimitsSlash`, `handleTuiSessionLifecycleSlash`, `handleTuiSessionStoreSlash`, `handleTuiMiscSlash`, `handleTuiResumeSlash` (all ≤80 LOC soft). Live switch SoT kept (`/sessions`/`/select`/`/plain`, default `false`). `var handleSlashImpl` test seam retained. Verified: `go test ./internal/cli`, `go test -race ./internal/cli`, structure check, `go vet`.
**Date:** 2026-07-31
**Depends on:** relationship to **P1.2** (see §1 — this plan may be **moot** if P1.2 lands first).
**Blocks:** nothing.
**Blast radius:** LOW — single file (`tui_slash_handlers.go`), pure decomposition, no behavior change.

---

## 1. Relationship to P1.2 (read first)

This plan and **P1.2 (unify slash dispatch)** address overlapping debt. Which one you do
depends on the P1.2 decision:

| P1.2 decision | This plan (P2.4) |
|---|---|
| **P1.2 Option B** chosen (catalog becomes the dispatch table; `SlashCommand` carries a per-surface handler) | **MOOT — do not execute.** The flat switch is replaced by a registry lookup; there is nothing left to split. |
| **P1.2 Option A** chosen (extract shared pure logic into `slash_shared.go`; keep per-surface switches) | **Still useful** — the TUI switch remains ~205 LOC and should still be decomposed into sub-functions (this plan). |
| **P1.2 deferred / not done** | **Execute this plan** as a standalone size reduction. |

If P1.2 is on the roadmap, **decide it first**. This plan exists to capture the size debt
independently in case P1.2 slips.

## 2. Problem

`tui_slash_handlers.go:11-216` defines `handleSlashImpl` as a single package-level
`var ... = func(m *tuiModel, cmd string) bool` containing a flat
`switch strings.ToLower(fields[0])` over ~16 commands (~205 LOC).

The classic REPL already decomposed its equivalent `handleSlash`
(`chat_slash.go:11`) into focused sub-handlers:

| Classic REPL sub-handler | File:line | Commands |
|---|---|---|
| `handleSlashInfo` | `chat_slash.go` | `/help`, `/status`, `/tools` |
| `handleSlashLimits` (`handleBudget`) | `chat_slash_handlers.go` | `/budget`, `/steps` |
| `handleSlashSessions` | `chat_slash_handlers.go` | `/save`, `/load`, `/delete`, `/list`, `/session` |

The TUI never got that treatment — every command body is inline in the switch. This makes
the function hard to read and hard to unit-test in isolation.

## 3. Goals and non-goals

### Goals
- Decompose `handleSlashImpl` into sub-functions grouped by concern, mirroring the classic
  REPL's split, without changing any behavior.
- Bring each sub-function under the 80-LOC soft limit.
- Keep the `var handleSlashImpl = func(...)` test seam intact (tests override it; see
  `budget_integration_test.go`, `chat_slash_handlers.go` usage).

### Non-goals
- Do not unify the two surfaces (that is P1.2).
- Do not change command behavior, output text, or ordering.
- Do not remove the `var ... = func` indirection — it is a deliberate test seam.

## 4. Approach

Keep `handleSlashImpl` as the dispatcher (the `switch`), but move each command-group body
into a dedicated method. Proposed split (names mirror the classic REPL where the concern
matches):

| New method | Commands moved in | Mirrors |
|---|---|---|
| `(m *tuiModel) handleTuiInfoSlash(cmd string, fields []string) bool` | `/help`, `/status`, `/tools`, `/exit` | `handleSlashInfo` |
| `(m *tuiModel) handleTuiModelSlash(cmd string, fields []string) bool` | `/model` | (TUI-specific dialog path) |
| `(m *tuiModel) handleTuiLimitsSlash(cmd string, fields []string) bool` | `/budget`, `/steps` | `handleSlashLimits` |
| `(m *tuiModel) handleTuiSessionSlash(cmd string, fields []string) bool` | `/save`, `/load`, `/delete`, `/list`, `/session`, `/new`, `/clear` | `handleSlashSessions` |
| `(m *tuiModel) handleTuiResumeSlash(cmd string, fields []string) bool` | `/resume` | `handleResumeSlash` (already exists in `resume.go`) |

`handleSlashImpl` becomes a thin router:

```go
var handleSlashImpl = func(m *tuiModel, cmd string) bool {
    fields := strings.Fields(cmd)
    if len(fields) == 0 { return false }
    if isLocalSlash(fields[0]) { m.appendBlock(/* ... unchanged ... */) }
    switch strings.ToLower(fields[0]) {
    case "/help", "/h", "/?", "/status", "/tools", "/exit", "/quit", "/q":
        return m.handleTuiInfoSlash(cmd, fields)
    case "/model":
        return m.handleTuiModelSlash(cmd, fields)
    case "/budget", "/steps":
        return m.handleTuiLimitsSlash(cmd, fields)
    case "/save", "/load", "/delete", "/list", "/session", "/new", "/clear":
        return m.handleTuiSessionSlash(cmd, fields)
    case "/resume":
        return m.handleTuiResumeSlash(cmd, fields)
    default:
        m.appendInfo("unknown command: " + fields[0])
        return true
    }
}
```

**Verify the actual command set and default branch at implementation time** — the switch at
`tui_slash_handlers.go:18` is the source of truth; the table above is derived from the report
and must be confirmed against HEAD.

## 5. Implementation waves

REFACTOR — TDD-preserving. The existing TUI slash tests (`chat_slash_handlers_test.go`-equivalent
TUI tests, `chat_repl_skills_test.go`, `budget_integration_test.go`, `new_session_slash_test.go`,
`chat_slash_resume_test.go`) are the behavior-preservation gate.

| Wave | Scope | Required proof |
|---|---|---|
| 0 | Confirm the command→method mapping against the live switch; confirm no command body has side effects that span groups. | Step-0 disposition. |
| 1 | Extract `handleTuiInfoSlash` (simplest group). | All existing TUI slash tests green. |
| 2 | Extract `handleTuiLimitsSlash`. | `budget_integration_test.go` green. |
| 3 | Extract `handleTuiSessionSlash`. | `new_session_slash_test.go` + session tests green. |
| 4 | Extract `handleTuiModelSlash` (dialog side effects — most care needed). | Model dialog tests green. |
| 5 | Wire `/resume` to the existing `handleTuiResumeSlash`. | `chat_slash_resume_test.go` green. |
| 6 | `handleSlashImpl` is now a thin router; run full slash + smoke suite. | `go test ./internal/cli -count=1`; `make structure-check` (confirm file under limits). |

## 6. Verification

```text
go test ./internal/cli -count=1
go test -race ./internal/cli -count=1
python3 scripts/check_go_structure.py --strict --all internal/cli
go vet ./...
make verify
```

- `handleSlashImpl` body is a pure dispatch switch (no command logic inline).
- No sub-function exceeds 80 LOC.

## 7. Rollback

Pure revert — the sub-functions fold back into the switch. No behavior change to preserve.

## 8. Out of scope

- Sharing the sub-handlers with the classic REPL → P1.2.
- Generating help from the catalog → P2.7.
