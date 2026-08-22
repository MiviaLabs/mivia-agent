# CLI Split Handoff — cliorchestrate + cliworkflow

Repo: `/home/mac/projects/mivialabs/mivia-agent`, branch `dev`.
Last clean commit: `44a6535c` (extract internal/cliagents).

---

## Working tree state (partial work from a killed agent)

The working tree has uncommitted changes. Do not discard them.

### internal/cliorchestrate — complete, unvalidated

23 files placed in `internal/cliorchestrate/`:
diagnostics.go, dispatch.go, doctor.go, doctor_json.go,
interrupted_runs_report.go, orchestrate.go, orchestrate_lifecycle.go,
orchestrate_salvage.go, orchestrate_spawn_tasks.go,
orchestration_access.go, orchestration_state.go, resume.go,
schema_resolve.go, session_seam.go, synopsis.go, task_routing.go,
test_exports.go, test_helpers_test.go, tool_names.go + test files.

`import-layers.json` has the cliorchestrate row and it is added to
`internal/cli`'s allowed list.

Callers updated in internal/cli: action.go, chat_command.go,
chat_repl.go, chat_slash.go, chat_slash_handlers.go, config_cmd.go,
delegate.go, dispatcher.go, dispatcher_handlers.go, effort_slash.go,
ledger_tools.go, legacytui_test_exports.go, messaging_tools.go,
root.go, send_to_task_tool.go, session_tool_catalog.go, setup.go,
tool_render.go, tool_verbs.go, workflow_run_build.go.

New files in cli: cliorchestrate_wiring.go, orchestration_wrappers.go,
resume_wrappers.go.

internal/legacytui/resume_commands.go updated.

### internal/cliworkflow — partially started, NOT complete

Files already placed in `internal/cliworkflow/` (20 files):
seams.go, workflow_approval.go, workflow_authority.go,
workflow_blocked_admission.go, workflow_cancel_coordinator.go,
workflow_cleanup.go, workflow_command_dispatch.go, workflow_delete.go,
workflow_deliver.go, workflow_delivery_record.go, workflow_events.go,
workflow_gc.go, workflow_inputs.go, workflow_json.go, workflow_mcp.go,
workflow_next_step.go, workflow_process_services.go,
workflow_progress_bus.go, workflow_progress_writer.go, workflow_resume.go.

The originals still exist in `internal/cli/` — not yet removed.
`import-layers.json` has NO cliworkflow row. Gate reports 24 violations
and edge count 382 (cap 381).

---

## Phase A — Finish and commit cliorchestrate

1. Run `go build ./...` — fix any errors.
2. Run full gate suite:

```bash
cd /home/mac/projects/mivialabs/mivia-agent
gofmt -l ./internal/cliorchestrate/ ./internal/cli/ ./internal/legacytui/
go build ./...
go vet ./...
git diff HEAD -- internal/cli/characterization_test.go   # must be empty
go test ./internal/cli/ -run TestCharacterization -count=1 -v
go test ./internal/cliorchestrate/... -count=1
go test ./internal/cli/... -count=1
go test ./internal/legacytui/... -count=1
go test ./internal/cliagents/... -count=1
python3 scripts/check_go_structure.py --strict --all
python3 scripts/check_import_layers.py
go list -deps ./internal/cliorchestrate/... | grep '/internal/cli$' | wc -l  # must be 0
```

3. Fix all failures.
4. Commit with message `refactor(cli): extract internal/cliorchestrate domain package`.
   Pre-commit hook runs verify-fast (gofmt, vet, tests, semgrep, structure gate).
   Never use `--no-verify`.

---

## Phase B — Complete and commit cliworkflow

### B1 — Finish placing files

For each file already in `internal/cliworkflow/`: remove the cli copy
with `git rm internal/cli/<file>.go`.

Remaining workflow files still only in `internal/cli/` that must also
move to cliworkflow:
workflow_resume_handoff.go, workflow_resume_lock.go,
workflow_resume_snapshot.go, workflow_resume_state.go, workflow_run.go,
workflow_run_build.go, workflow_run_helpers.go, workflow_runs.go,
workflow_runtime.go, workflow_session_settle.go, workflow_snapshot.go,
workflow_snapshot_runtime.go, workflow_stack_settle.go,
workflow_status.go, workflow_store_links_other.go,
workflow_store_links_unix.go, workflow_store_links_windows.go,
workflow_tool_engine.go, workflow_tool_engine_guard.go,
workflow_tool_engine_ops.go, workflow_tool_engine_reconcile.go,
workflow_tool_engine_resume.go, workflow_tool_service.go,
workflow_tools_register.go, workflow_verifiers.go,
workflow_workspace.go, workflows_command.go.

Use `git mv` for files not yet in cliworkflow. Confirm `package cliworkflow`
at top of every moved file.

### B2 — Export symbols and update callers

Export every symbol called from internal/cli, internal/legacytui,
internal/cliagents, or internal/cliorchestrate (rename lower→upper).
Add a doc comment on every exported symbol (first word = symbol name).
Update all callers.

### B3 — Func-var wiring if needed

If cliworkflow needs something from internal/cli (cycle), declare
`var X func(...)` in cliworkflow and create
`internal/cli/cliworkflow_wiring.go` with an `init()` that sets it.
TestMain in cliworkflow must wire the same vars.

### B4 — import-layers.json

Add `internal/cliworkflow` row with its actual imports.
Add `internal/cliworkflow` to allowed lists of internal/cli,
internal/legacytui, internal/cliorchestrate (if needed).
Edge count must stay ≤ 381.

### B5 — Test files

Move test files whose subject moved. Keep test files whose subject
stayed. Duplicate shared helpers (Go forbids cross-package _test.go
imports). `internal/cli/characterization_test.go` must not change.

### B6 — Full gate run

```bash
cd /home/mac/projects/mivialabs/mivia-agent
gofmt -l ./internal/cliworkflow/ ./internal/cli/ ./internal/legacytui/ ./internal/cliagents/ ./internal/cliorchestrate/
go build ./...
go vet ./...
git diff HEAD -- internal/cli/characterization_test.go   # must be empty
go test ./internal/cli/ -run TestCharacterization -count=1 -v
go test ./internal/cliworkflow/... -count=1
go test ./internal/cli/... -count=1
go test ./internal/legacytui/... -count=1
go test ./internal/cliagents/... -count=1
go test ./internal/cliorchestrate/... -count=1
python3 scripts/check_go_structure.py --strict --all
python3 scripts/check_import_layers.py
go list -deps ./internal/cliworkflow/... | grep '/internal/cli$' | wc -l   # must be 0
go list -deps ./internal/cli/... | grep '/internal/cliworkflow$' | wc -l   # must be ≥ 1
```

7. Commit with message `refactor(cli): extract internal/cliworkflow domain package`.

---

## Hard invariants

1. `internal/cli/characterization_test.go` must not change.
2. Neither new package may import `internal/cli` (go list -deps → 0).
3. All characterization tests pass.
4. `go build ./...` clean before each commit.
5. import-layers.json edge count ≤ 381.
6. All exported symbols have doc comments starting with the symbol name.
7. `python3 scripts/check_go_structure.py --strict --all` → 0 hard failures.
8. No gofmt issues in touched files.
9. No TODO/FIXME/HACK/XXX in any comment (semgrep blocks commit).
10. Pre-commit hook must pass. Never use `--no-verify`.
