# CLI Split Handoff — clichat (slice 5 of 6)

Repo: `/home/mac/projects/mivialabs/mivia-agent`, branch `dev`.
Start only after `internal/cliorchestrate` and `internal/cliworkflow` are
committed on `dev`. Confirm with `git log --oneline dev | head -6`.

---

## Background

`internal/cli` is being split into focused domain packages. Slices 1–4
are already committed:

- `internal/cliworktree` — worktree lifecycle
- `internal/cliagents` — AgentSessionState, agent binding, tool tiers
- `internal/cliorchestrate` — dispatcher, coordinator wiring, event routing
- `internal/cliworkflow` — workflow commands and tool engine

**Slice 5** extracts `internal/clichat` — the interactive REPL session loop
and all UI rendering support that lives in cli.

After this slice `internal/cli` should contain only:
`root.go`, `errors.go`, `tui_launcher.go`, `*_wiring.go` files,
`*_aliases.go` files, and the small top-level subcommands that delegate
into domain packages (login, logout, verify, completion, config_cmd,
hooks_command, memory_command, sessions_command, stack_command, register).

---

## Architecture invariants (never violate)

- One-way dependency: `internal/clichat` must never import `internal/cli`.
  Verify: `go list -deps ./internal/clichat/... | grep '/internal/cli$'` → 0.
- Import policy: every new edge declared in `.mivia/policy/import-layers.json`.
  Edge count ≤ 381. Gate: `python3 scripts/check_import_layers.py`.
- `internal/cli/characterization_test.go` must not change.
  `git diff HEAD -- internal/cli/characterization_test.go` → empty.
  All 5 characterization tests must pass.
- Exported symbols: every exported symbol needs a doc comment starting with
  the symbol name. Gate: `scripts/check_docs.py`.
- Structure: file ≤ 500 lines (hard), function ≤ 80 lines (hard).
  Gate: `python3 scripts/check_go_structure.py --strict --all` → 0 hard failures.
- No TODO/FIXME/HACK/XXX in comments. Gate: semgrep pre-commit hook.
- gofmt clean on all touched files before committing.
- No third-party imports unless already in `.mivia/policy/thirdparty.json`.
- Func-var wiring pattern for cycles: declare `var X func(...)` in clichat,
  wire from `internal/cli/clichat_wiring.go` init(). TestMain in clichat
  must also wire these vars.
- Never use `--no-verify` to bypass the pre-commit hook.

---

## Step 1 — Audit remaining internal/cli files

```bash
ls /home/mac/projects/mivialabs/mivia-agent/internal/cli/*.go | grep -v _test | xargs -I{} basename {}
```

Files that belong in `internal/clichat` are those whose dominant concern is
the interactive REPL session loop and its UI rendering support. Read each
candidate and classify:

**Core REPL loop**
chat.go, chat_command.go, chat_flags.go, chat_hub.go,
chat_invocation_sink.go, chat_json_slash.go, chat_json_writer.go,
chat_repl.go, chat_repl_linemode.go, chat_repl_loop.go,
chat_repository_binding.go, chat_slash.go, chat_slash_handlers.go,
context_setup_session.go, context_summary_setup.go, ask_flow.go,
action.go, parked_wait.go, errors.go (if session-scoped).

**Session stack**
stack_admit.go, stack_admit_inflight.go, stack_admit_integration.go,
stack_decompose_continue.go, stack_drive.go, stack_followup.go,
stack_grant_pause.go, stack_merge.go, stack_merge_cancel.go,
stack_merge_checker.go, stack_merge_overlap.go, stack_publish_gate.go,
stack_reconcile.go, stack_sibling_files.go, stack_state.go.

**UI rendering and terminal**
bubble_leftrail.go, bubble_rail_roles.go, chatblock.go,
chatblock_render.go, chatblock_status.go, chatblock_workgroup.go,
classic_agent_ui.go, dialog.go, dialog_compositor.go, dialog_geometry.go,
diff_render.go, diff_style.go, glyphs.go, highlight.go,
highlight_blocks.go, highlight_regions.go, input.go, keymap.go,
markdown.go, markdown_table_helpers.go, markdown_table_render.go,
markdown_tables.go, messagebubble.go, msgcard.go, prompt.go,
read_output.go, read_output_paging.go, renderer.go, terminal.go,
thinking.go, tool_render.go, tool_verbs.go, tool_wave_status.go,
tui_autosave.go, tui_focus.go, tui_help_content.go, tui_helpers.go,
tui_history.go, tui_queue.go, tui_shared.go, tui_slash.go,
tui_stream.go, tui_style.go, tui_subagent_tracker.go,
tui_tools_apply.go, tui_view_status.go, subagent_progress.go.

**Session support**
session_catalog.go, session_delivery_repair.go, sessions_command.go,
session_tool_catalog.go, limits_summary.go.

**Tool support**
load_tools_tool.go, ledger_read_paging.go, ledger_tools.go,
messaging_tools.go, send_to_task_tool.go.

**Slash/skill support**
slash_catalog.go, slash_shared.go, effort_slash.go,
skill_activation_handler.go, skill_resource_tool.go.

**Agent task handler**
agent_task_handler.go.

**Compact**
compact_command.go.

For each file, verify: does it use unexported functions staying in cli?
Does it use unexported fields of types defined here? Build the actual list
from reading, not from assumptions.

**Files that stay in cli (do NOT move)**
root.go, tui_launcher.go, cliagents_aliases.go, cliagents_wiring.go,
cliorchestrate_wiring.go, cliworkflow_wiring.go, cliworktree_wiring.go,
orchestration_wrappers.go, resume_wrappers.go,
login.go, logout.go, register.go, verify.go, completion.go,
config_cmd.go, hooks_command.go, hooks_runner.go,
memory_command.go, memory_store.go, stack_command.go, auth_shared.go,
delegate.go, setup.go.

---

## Step 2 — Determine imports for internal/clichat

Read import blocks of every file being moved. Build the exact set.
Expected to include some of: internal/cliagents, internal/cliworktree,
internal/cliorchestrate, internal/cliworkflow, internal/chat,
internal/composition, internal/config, internal/contextmgr,
internal/contextstate, internal/events, internal/hooks, internal/hub,
internal/jschema, internal/ledger, internal/memory, internal/provider,
internal/providerregistry, internal/reasoning, internal/redact,
internal/remainder, internal/runtime, internal/skills, internal/storage,
internal/subagents, internal/textutil, internal/tools, internal/vcs,
internal/workspace.

---

## Step 3 — Execute the move

1. `git mv internal/cli/<file>.go internal/clichat/<file>.go` for each file.
2. Change `package cli` → `package clichat` at the top.
3. Export every symbol called from internal/cli, internal/legacytui,
   internal/cliagents, internal/cliorchestrate, or internal/cliworkflow
   (rename lower→upper).
4. Add a doc comment on every newly exported symbol (first word = symbol name).
5. Update all callers in internal/cli, internal/legacytui, internal/cliagents,
   internal/cliorchestrate, internal/cliworkflow.
6. Update `.mivia/policy/import-layers.json`:
   - Add `internal/clichat` row with its actual imports.
   - Add `internal/clichat` to `internal/cli`'s allowed list.
   - Add `internal/clichat` to any other package's allowed list that now
     imports it (check legacytui, cliagents, cliorchestrate, cliworkflow).
   - Edge count must stay ≤ 381.
7. If clichat needs something from internal/cli (cycle), declare a
   package-level `var X func(...)` in clichat and create
   `internal/cli/clichat_wiring.go` with an `init()`.
   TestMain in clichat must wire the same vars.

---

## Step 4 — Test files

- Test files testing moved functions → move to internal/clichat/.
- Test files testing functions staying in cli → keep in internal/cli/.
- Test files accessing unexported fields of types defined in clichat → move.
- Shared helpers used in both packages → duplicate (no cross-package _test.go).
- `internal/cli/characterization_test.go` must not change at all.

---

## Step 5 — Full gate run

Run every check; fix all failures before committing.

```bash
cd /home/mac/projects/mivialabs/mivia-agent
gofmt -l ./internal/clichat/ ./internal/cli/ ./internal/legacytui/ \
      ./internal/cliagents/ ./internal/cliorchestrate/ ./internal/cliworkflow/
go build ./...
go vet ./...
git diff HEAD -- internal/cli/characterization_test.go   # must be empty
go test ./internal/cli/ -run TestCharacterization -count=1 -v
go test ./internal/clichat/... -count=1
go test ./internal/cli/... -count=1
go test ./internal/legacytui/... -count=1
go test ./internal/cliagents/... -count=1
go test ./internal/cliorchestrate/... -count=1
go test ./internal/cliworkflow/... -count=1
python3 scripts/check_go_structure.py --strict --all
python3 scripts/check_import_layers.py
go list -deps ./internal/clichat/... | grep '/internal/cli$' | wc -l   # must be 0
go list -deps ./internal/cli/... | grep '/internal/clichat$' | wc -l   # must be ≥ 1
```

---

## Step 6 — Report, then commit

State:
- Exact list of production files moved (one-line reason for each).
- Files that were NOT moved and why.
- Symbols exported (renamed lower→upper), one per line.
- Whether func-var wiring was needed and what cycle it breaks.
- Edge count after import-layers.json update.
- Every gate result.

Commit message:

```
refactor(cli): extract internal/clichat domain package
```

Include in body: file count, import count, topology verification result.
Pre-commit hook runs verify-fast. Never use `--no-verify`.

---

## What comes after (slice 6)

After this commit, `internal/cli` should contain only the thin router:
root.go, errors.go, tui_launcher.go, *_wiring.go, *_aliases.go, and
the small delegating subcommands. Slice 6 is a cleanup commit that:
- Updates the import-layers.json edgeCap from 381 to the real measured count.
- Regenerates the allow map from the actual final tree.
- Removes any remaining alias shim files that are no longer needed.
- Runs the full `make verify` (not just verify-fast) to confirm.
