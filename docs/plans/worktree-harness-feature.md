# Plan: Isolated Worktree as a Harness Feature

> Locked ADLC Step 0 plan — amended after hostile review. No deviation without returning to Step 0.

## Goal

Implement `internal/vcs/` — a thin, testable git worktree package — and expose it through three user surfaces: a new `worktree` CLI subcommand (standalone use), a `/worktree` slash command (in-session use), and a reusable Go API that the existing workflow subsystem can call programmatically.

## Context: What Exists Today

| Area | Status |
|------|--------|
| `internal/vcs/` | Does not exist |
| Git integration in production code | **Zero** — no `exec.Command("git", …)` anywhere |
| Hook stubs | `hooks/config.go` has `WorktreeCreate`/`WorktreeRemove` as `"not implemented in v1"` |
| `workspace.Root` | Immutable per process, set once at startup, never switched |
| Slash commands | 23 builtins, dispatch via `chat_slash.go` (plain) / `tui_slash_handlers.go` (TUI), catalog in `slash_catalog.go` |
| TUI dialog system | 5 existing dialogs (agent, model, session, effort, help) sharing `renderDialogFrame` |
| CLI subcommands | `chat`, `version`, `workflows`, `config`, `doctor`, `agents` |
| Config pipeline | TOML → `config.Resolved` → `workspace.Root` |
| Namespace | `internal/workspace/namespace.go` owns all `.mivia/` path construction via `NamespacePath()`. **Invariant: nothing outside this file may name a namespace directory.** |

- **Location**: Worktrees are created under the mivia namespace worktrees directory inside the repository root. The path is constructed exclusively via `workspace.WorktreesDir(repoRoot)` (added to `namespace.go`). This keeps the directory name in one place, preserving the namespace invariant.

## Key Design Decisions

1. **Process vs Directory**: Creating an isolated worktree means launching a **new `mivia` process** in the worktree directory. Worktrees share git content but get **separate** `context.db`, session stores, and orchestration stores — which is the correct isolation model. The current process does **not** hot-swap its `workspace.Root`.

2. **Location**: All worktrees live in the directory returned by `workspace.WorktreesDir(repoRoot)` — currently `workspace.NamespacePath(repoRoot, "worktrees")`, i.e. `<repo-root>/.mivia/worktrees/<name>`. The path is never hardcoded in `vcs/`; it always goes through the namespace package. The `.gitignore` entry for `.mivia/worktrees/` will be **removed** — git worktrees handle status exclusion natively (the worktree's `.git` file points to the main repo's gitdir). Removing it ensures `grep`/`glob`/`list_dir` can see worktree contents from any process rooted there.

3. **Shell-out, not embed**: `mivia worktree create …` shells out to `git worktree add`, validates the result, then optionally launches a new `mivia` process. This keeps the VCS package thin and the tool surface honest. **All git invocations use `exec.CommandContext(ctx, "git", args...)` with no shell** — args are passed as a list, so shell metacharacters in branch names are inert. A mechanical guard test in `vcs/` scans its own source for shell-pattern violations.

4. **Reuse path for workflows**: Workflows will import `internal/vcs/` directly and call the same `Create`/`Remove`/`List` functions. The workflow compiler already validates step types — a new step kind `worktree_create` would be added there.

5. **Hook integration**: `WorktreeCreate`/`WorktreeRemove` lifecycle events move from stub to implementation. They fire as observation-only post-hocs (not blocking). Event payload shape and gateability deferred to Wave 6.

6. **`IsMain` dropped**: `List()` returns only mivia-managed worktrees (under the worktrees directory). The main worktree is filtered out internally. This avoids exposing a field nothing reads.

## Files to Create

| File | Purpose |
|------|---------|
| `internal/vcs/worktree.go` | Core `Create`, `Remove`, `List`, `Resolve` functions |
| `internal/vcs/worktree_test.go` | Tests with real git repos (temp directories) + shell-pattern guard |
| `internal/vcs/naming.go` | Deterministic worktree name sanitisation |
| `internal/vcs/naming_test.go` | Naming edge cases |
| `internal/vcs/errors.go` | Typed errors (NotGitRepo, NameConflict, etc.) |
| `internal/cli/worktree_command.go` | CLI subcommand: `mivia worktree create/remove/list` |
| `internal/cli/worktree_command_test.go` | CLI wiring tests |
| `internal/cli/chat_slash_worktree.go` | `/worktree` slash handler (plain REPL) |
| `internal/cli/tui_slash_worktree.go` | `/worktree` TUI handler (dialog + direct) |
| `internal/cli/worktree_dialog.go` | TUI worktree picker dialog |

## Files to Modify

| File | Change |
|------|--------|
| `internal/workspace/namespace.go` | Add `WorktreesDir(root string) string` following `SessionsDir`/`SkillsDir` pattern |
| `internal/workspace/namespace_test.go` | Add `WorktreesDir` coverage |
| `.gitignore` | **Remove** `.mivia/worktrees/` line (git worktree handles exclusion natively) |
| `internal/cli/root.go` | Add `worktree` to `Execute()` switch |
| `internal/cli/chat_slash.go` | Add `/worktree` case to plain REPL dispatch |
| `internal/cli/tui_slash_handlers.go` | Add `/worktree` to TUI dispatch |
| `internal/cli/slash_catalog.go` | Add `/worktree` to `builtInSlashCommands()` |
| `internal/hooks/config.go` | Remove `WorktreeCreate`/`WorktreeRemove` from `deferredEvents`, wire as observation-only post-hocs |
| `internal/cli/tui.go` | Add `worktreeDlg *worktreeDialog` field |
| `internal/cli/tui_view.go` | Add worktree dialog rendering (after agentDlg, before sessionsDlg) |
| `internal/cli/tui_keys.go` | Route worktree dialog keys |
| `internal/cli/tui_message.go` | Add `worktreeDlg` to modal/scroll/focus checks |
| `internal/cli/tui_cancel.go` | Add `worktreeDlg` to modal-open check **and fix pre-existing bug**: add missing `agentDlg` guard |
| `internal/cli/overlay.go` | Add `worktreeDlg` to `closeModal()` nil-safety cascade |
| `internal/cli/clipboard_read.go` | Add `worktreeDlg` to paste guard **and fix pre-existing bugs**: add missing `modelDlg`, `agentDlg`, `effortDlg` guards |
| `internal/cli/keymap.go` | Register worktree dialog key bindings (new scope or extend `scopeOverlay`) |

## API Surface (Go signatures)

```go
// internal/workspace/namespace.go (ADDITION)
// WorktreesDir returns the absolute path of the worktrees directory
// inside the mivia namespace for the given repo root.
func WorktreesDir(repoRoot string) string

// internal/vcs/worktree.go
package vcs

import "mivia/internal/workspace"

// WorktreeInfo describes a single mivia-managed worktree.
type WorktreeInfo struct {
    Name   string // human name (sanitised)
    Path   string // absolute path on disk (under workspace.WorktreesDir())
    Branch string // checked-out branch/commit
}

// Create adds a new worktree under workspace.WorktreesDir(repoRoot).
// The branch is created from baseRef (branch, tag, or SHA).
// Returns the WorktreeInfo for the new worktree.
func Create(ctx context.Context, repoRoot string, name string, baseRef string) (*WorktreeInfo, error)

// Remove deletes a worktree by name. Prunes stale worktree references.
func Remove(ctx context.Context, repoRoot string, name string) error

// List returns all mivia-managed worktrees for the repo at repoRoot.
// The main worktree is filtered out.
func List(ctx context.Context, repoRoot string) ([]WorktreeInfo, error)

// Resolve finds a worktree by name. Returns nil, nil if not found.
func Resolve(ctx context.Context, repoRoot string, name string) (*WorktreeInfo, error)

// RepoRoot finds the git repository root from any directory inside it.
func RepoRoot(dir string) (string, error)

// internal/vcs/naming.go
// SanitizeName converts a user-provided worktree name to a safe directory name.
func SanitizeName(input string) string

// internal/vcs/errors.go
type NotGitRepoError struct{ Dir string }
type WorktreeExistsError struct{ Name string }
type WorktreeNotFoundError struct{ Name string }
```

## CLI Subcommand

```text
mivia worktree create [name] [--branch base-ref] [--create-branch]
mivia worktree remove <name>
mivia worktree list
```

With no name, `create` generates one from the current branch name. `--create-branch` creates a new branch from the current HEAD (default: detach or checkout existing). If successful, prints the **absolute** worktree path:

```text
mivia worktree create feature-auth
  Created worktree "feature-auth" at /home/user/project/.mivia/worktrees/feature-auth
  Run 'mivia --workspace /home/user/project/.mivia/worktrees/feature-auth chat' to start a session
```

## Slash Command

```
/worktree                     open TUI picker dialog (TUI) or list (plain)
/worktree create [name]       create and print path
/worktree remove <name>       remove a worktree
/worktree list                list all worktrees
```

## TUI Dialog

Follows the exact same pattern as `agent_dialog.go` / `model_dialog.go`:
- Struct `worktreeDialog` with `rows []WorktreeInfo`, `cursor int`, `notice string`, `confirm int`
- Methods: `newWorktreeDialog()`, `ViewAt(w,h)`, `layout(w,h)`, `footer()`, `selected()`
- Rendered via `renderDialogFrame("◇ worktrees", rows, d.footer(), l)`
- **Enter**: prints absolute worktree path to session as a message (same UX pattern as sessions dialog printing session info)
- `d`: delete with single-level confirmation (`confirm` field cycles through 0→1→dismiss)
- `n`: create new (prompt for name inline)
- Rendering order in overlay chain: `modelDlg > agentDlg > effortDlg > worktreeDlg > sessionsDlg > overlay`
- Key bindings: `scopeOverlay` keys (j/k/up/down/enter/esc/q/pgup/pgdown/home/end) + `d` (delete) + `n` (new). The `d` and `n` keys are registered in `keymap.go` under a new `scopeWorktree` scope, or added to `scopeOverlay` if they don't conflict.

## Pre-existing Bugs Fixed Alongside

These bugs exist today and would be silently copied by adding a 6th dialog. Fixed in the same wave:

| Bug | File | Fix |
|-----|------|-----|
| `tui_cancel.go:74` missing `agentDlg` in modal-open guard | `tui_cancel.go` | Add `m.agentDlg != nil` to the check |
| `clipboard_read.go:92` missing `modelDlg`, `agentDlg`, `effortDlg` in paste guard | `clipboard_read.go` | Add all three to the paste-blocking check |

## Workflow Integration (Future Path)

The same `vcs.Create()` / `vcs.Remove()` calls would be available to workflow step handlers:
- A workflow step of kind `worktree_create` would call `vcs.Create()`, then pass the worktree path to subsequent steps
- The `WorktreeCreate` / `WorktreeRemove` hook events would fire as observation-only post-hocs

This is explicitly **out of scope** for the initial delivery but the API is designed to be called both from CLI/slash and from workflow step handlers without refactoring.

## Dependency Graph (Waves)

```
Wave 0 (prerequisite fixes + namespace):
  - internal/workspace/namespace.go (modify — add WorktreesDir)
  - internal/workspace/namespace_test.go (modify)
  - .gitignore (modify — remove .mivia/worktrees/)

Wave 1 (foundation):
  - internal/vcs/errors.go
  - internal/vcs/naming.go + naming_test.go

Wave 2 (core):
  - internal/vcs/worktree.go + worktree_test.go (includes shell-pattern guard)

Wave 3 (CLI):
  - internal/cli/worktree_command.go + worktree_command_test.go
  - internal/cli/root.go (modify)

Wave 4 (slash + dialog):
  - internal/cli/chat_slash_worktree.go
  - internal/cli/tui_slash_worktree.go
  - internal/cli/worktree_dialog.go
  - internal/cli/slash_catalog.go (modify)
  - internal/cli/chat_slash.go (modify)
  - internal/cli/tui_slash_handlers.go (modify)

Wave 5 (TUI plumbing + pre-existing bug fixes):
  - internal/cli/tui.go (modify)
  - internal/cli/tui_view.go (modify)
  - internal/cli/tui_keys.go (modify)
  - internal/cli/tui_message.go (modify)
  - internal/cli/tui_cancel.go (modify — add worktreeDlg + fix missing agentDlg)
  - internal/cli/overlay.go (modify — add worktreeDlg to closeModal)
  - internal/cli/clipboard_read.go (modify — add worktreeDlg + fix missing modelDlg/agentDlg/effortDlg)
  - internal/cli/keymap.go (modify — register worktree key bindings)

Wave 6 (hooks):
  - internal/hooks/config.go (modify)
```

## Test Strategy

| Scenario | Test |
|----------|------|
| `WorktreesDir` returns correct namespace path | `TestWorktreesDir` |
| `SanitizeName` sanitises special chars, truncates, lowercases | `TestSanitizeName` |
| `Create` creates a worktree, verifies path and branch | `TestCreate` |
| `Create` rejects non-git directory | `TestCreate_NotGitRepo` |
| `Create` rejects duplicate name | `TestCreate_DuplicateName` |
| `Remove` deletes a worktree and prunes | `TestRemove` |
| `List` returns only mivia-managed worktrees (excludes main) | `TestList` |
| `RepoRoot` finds root from subdirectory | `TestRepoRoot` |
| Shell-pattern guard: no `exec.Command` with shell in vcs/ | `TestNoShellPatterns` |
| CLI `worktree create` parses flags correctly | `TestParseWorktreeFlags` |
| CLI output shows absolute path | `TestWorktreeOutputAbsolute` |
| Slash `/worktree list` prints worktrees | `TestSlashWorktreeList` |
| TUI dialog renders and navigates | `TestWorktreeDialogView` |
| Modal guard includes worktree dialog | `TestWorktreeDialogIsModal` |
| `closeModal` nils worktreeDlg | `TestCloseModalNilWorktree` |
| Paste guard blocks when worktreeDlg open | `TestPasteGuardWorktree` |

## Plan Scorecard

| Criterion | Status |
|-----------|--------|
| Compiles | PASS — only stdlib + internal deps |
| No cycles | PASS — `vcs` is a leaf package, `cli` depends on it one-way |
| No breaking API | PASS — purely additive |
| Namespace invariant | PASS — `WorktreesDir()` in namespace.go, `vcs/` calls it |
| Testable in isolation | PASS — `vcs/` tests use temp git repos |
| Backward-compatible config | PASS — no config changes needed |
| Every function has a test | PASS — test column above |
| Follows file size rules | PASS — `vcs/worktree.go` estimated ~120 LOC, dialog ~150 LOC |
| Model-facing tools generic | PASS — no new tools exposed to model; slash/CLI only |
| No spaghetti | PASS — each file has one clear purpose |
| TUI modal completeness | PASS — all 8 guard/check locations updated |
| Pre-existing bugs addressed | PASS — 2 bugs fixed alongside |

## Rollback Criterion

- If git is not available at runtime (no git binary in PATH), all operations fail with `NotGitRepoError` or a clear "git not found" error. The feature degrades to a no-op with a helpful message.
- If tests reveal `exec.Command("git", …)` conflicts with any existing hook/security guard, the entire feature is blocked.

## Residual Risk

1. **First git exec in production** — this is the first use of `exec.Command("git", …)` in production code. Hooks are tool-dispatch-level and cannot intercept direct `exec.Command`. The shell-pattern guard test provides belt-and-suspenders protection.
2. **Worktree locking** — git worktrees have file-level locking (`<path>/.git`). Creating/removing while another process holds locks needs care. The VCS package should check for locked files before remove.
3. **Naming collisions** — the sanitisation must prevent path traversal and filesystem collisions. Edge cases with unicode, very long names, and reserved names (`..`, `.git`) need test coverage.
4. **Hook event payload** — `WorktreeCreate`/`WorktreeRemove` payload shape, timeout defaults, and gateability are deferred to Wave 6. The stub removal from `deferredEvents` is paired with actual firing to avoid an observability gap.

## Review Amendments Log

| ID | Source | Severity | Amendment |
|----|--------|----------|-----------|
| AR-1 | reviewer | BLOCK | Added `WorktreesDir()` to namespace.go; `vcs/` imports `workspace` and calls it. No hardcoded `.mivia` in vcs. |
| C3-F4 | auditor | BLOCK | Added `overlay.go`, `clipboard_read.go`, `keymap.go` to files-to-modify. 8 TUI plumbing files total. |
| C2-F1 | auditor | BLOCK | Removing `.mivia/worktrees/` from `.gitignore`. Git worktree handles status exclusion via `.git` file pointer. |
| AR-2 | reviewer | MEDIUM | Dropped `IsMain` from `WorktreeInfo`. `List()` filters main tree internally. |
| C3-F1/F2 | auditor | MEDIUM | Fix pre-existing bugs: `tui_cancel.go` missing `agentDlg`, `clipboard_read.go` missing `modelDlg`/`agentDlg`/`effortDlg`. |
| AR-5 | reviewer | MEDIUM | Added shell-pattern guard test in `worktree_test.go`. |
| C2-F5 | auditor | LOW | CLI output now shows absolute path. |
| AR-6 | reviewer | LOW | Hook event payload deferred to Wave 6. |
| C3-F7 | auditor | LOW | Enter action: prints absolute path to session (same pattern as sessions dialog). |
| C3-F8 | auditor | LOW | Delete confirmation: single-level (confirm field 0→1→dismiss). |
