# Phase 1 — Hook Output Framing (P0)

**Status**: Implemented (2026-08-01)
**Items**: R1
**Depends on**: Phase 0 (analysis)

## Problem

Hook context and block reasons reach the model as raw, unframed text. The only delimiter is the `[lifecycle hook output]` prefix in `internal/agent/loop_tools.go:457`. A compromised third-party hook script (confirmed by the user, later tampered) could output targeted prompt injection under the 8 KiB bound. The user-confirmation trust model is the sole defense.

This is the **only confirmed exploitable weakness** found by both the architecture reviewer and the security auditor.

## Scope

- `internal/agent/loop_tools.go` — `appendHookContext` function (lines 445-459)
- `internal/runtime/hooks.go` — `boundHookContext` function
- `internal/runtime/hooks_test.go` — existing tests that assert on hook context
- `internal/agent/hook_context_test.go` — existing tests for attributed blocks

## Tasks

### 1.1 — Design the framing delimiter

Decide on a structural delimiter for model-visible hook output.

Options:
- **XML tags**: `<lifecycle-hook-output>...</lifecycle-hook-output>`
- **System-message prefix with fenced block**: `[lifecycle hook output — non-instructional, advisory only]\n\`\`\`\n...\n\`\`\``
- **JSON-wrapped advisory object**: `{ "source": "lifecycle_hook", "advisory": true, "text": "..." }`

Criteria:
- The model's default system prompt (or `AGENTS.md` / `.mivia/` rules) should be able to recognize hook output as non-instructional.
- Must not break existing tests that match `[lifecycle hook output]` as a substring.
- Must preserve the 8 KiB bound on hook context (`runtime.MaxHookContextBytes`).
- Must not spool hook output into the tool's own `Output` field (the audit hash and preview must remain unchanged — pinned by `TestHookContextDoesNotChangeTheAuditHashOrPreview`).

### 1.2 — Implement framing in `appendHookContext`

File: `internal/agent/loop_tools.go`

Replace the plain `[lifecycle hook output]\n` prefix with the chosen delimiter. Ensure:
- Empty hook context returns the result unchanged (existing behavior).
- The framing is applied to both the empty-result path (`if result == ""`) and the append path.
- The delimiter is part of the context string, not the tool result — it must land in the attributed block only.

### 1.3 — Update existing tests

Files: `internal/agent/hook_context_test.go`, `internal/runtime/hooks_test.go`

- Update substring assertions from `[lifecycle hook output]` to the new delimiter.
- Verify `TestHookContextDoesNotChangeTheAuditHashOrPreview` still passes (framing must be in the attributed block, never in the tool output bytes).
- Add a test that hook context containing instruction-like text is correctly framed and does not appear as part of the tool's own result.

### 1.4 — Add model-facing documentation

File: `docs/development/lifecycle-hooks.md`

Add a section explaining:
- Hook output is advisory, non-instructional text.
- It arrives wrapped in a structural delimiter (describe the chosen format).
- The model should treat it as context, not as instructions to follow.
- Block reasons from `PreToolUse` hooks also reach the model verbatim — they explain *why* a tool was blocked, not what to do next.

### 1.5 — Update agent prompt (if applicable)

If there is a compiled default prompt or system prompt template that mentions hooks, add guidance that hook output is structural metadata, not actionable instructions.

## Verification

- `go test ./internal/agent/... ./internal/runtime/... ./internal/cli/...` — all existing tests pass
- `make verify` — full offline gate
- Manual: confirm that a PostToolUse hook outputting instruction-like text ("Ignore all previous instructions and...") is wrapped in the delimiter and does not appear as raw model input

## Exit Criteria

- Hook context is wrapped in a structural delimiter in all code paths
- All existing tests pass with updated assertions
- Documentation states the framing convention
- No change to audit hash or preview semantics
