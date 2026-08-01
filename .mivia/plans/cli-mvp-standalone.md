# Standalone `mivia` CLI MVP

**Status:** Source-verified plan; independent challenge returned BLOCK / validation NOT implementation-ready; amendments applied; owner approval required before code
**Date:** 2026-07-28
**SoT:** `.mivia/plans/cli-mvp-standalone.md`
**Target:** A reliable standalone CLI that works for humans and automation without requiring the full governed `go-mivia` platform.

## 0. Independent challenge record

- Adversarial challenge `019fa716-ffe5-7f73-bcc2-6ccff805cb00`: `BLOCK` before amendment.
- Implementation validation `019fa716-fec5-7311-9ecd-c477ecbccd68`: `NOT implementation-ready` before amendment.
- Confirmed amendments applied below: exit-code ownership, provider/process test seam, no-persistence exec path, protocol-error semantics, policy precedence, timeout plumbing, and concrete output bounds.
- Parent verification: current `cmd/mivia/main.go`, `internal/cli/root.go`, `internal/cli/chat_repl.go`, `internal/cli/chat.go`, `internal/chat/session.go`, `internal/provider/provider.go`, `Makefile`, and `docs/OWNERS.yaml` re-read. Tests/builds were not run.

This is not a readiness certificate. The plan becomes implementation-ready only after owner approval of the protocol and after Slice 1 proves the real subprocess seam.

## 1. Objective

Make the `mivia` binary MVP-ready as a standalone coding agent with:

- reliable interactive `chat` behavior;
- a headless machine mode for stdin/stdout automation and future runner adapters;
- deterministic exit status and cancellation behavior;
- stdout purity for machine mode;
- bounded/redacted diagnostics;
- minimal documentation and smoke coverage.

The MVP is deliberately small. It should be usable in days of focused work, not become a second orchestration platform.

## 2. Current ground truth

- Binary entrypoint: `cmd/mivia/main.go`; it delegates to `internal/cli.Execute` and exits `1` for any returned error.
- Commands currently are `chat`, `config show`, `doctor`, `version`, and `help`: `internal/cli/root.go`.
- `mivia chat -p` runs `oneShot` in `internal/cli/chat.go`; it renders markdown to stdout and writes human progress/status to stderr.
- `runChat` in `internal/cli/chat_repl.go` owns config loading, provider construction, workspace tools, skills, dispatcher, session persistence, and TUI/REPL selection.
- Interactive chat can seed `.mivia/agent-prompt.md`, create `.mivia/sessions`, and auto-save. Those side effects are inappropriate defaults for automation.
- Agent execution already has context cancellation, tool budgets, bounded tool results, redacted event previews, and typed tool events: `internal/agent/loop.go`, `internal/agent/loop_tools.go`, `internal/tools/**`.
- `mivia` has no current versioned machine protocol. Non-TTY input falls back to line REPL behavior, not a stable request/response contract.
- `doctor` and `config show` print human-readable output only. `doctor` returns an error when the provider key is missing, but cancellation in `oneShot` is currently treated as success.
- Existing tests are shallow at the CLI command boundary: `cmd/mivia/main_test.go` covers version/help/unknown command, not process stdin/stdout/stderr/exit behavior.
- Repository constraints include no raw secrets/prompts/model dumps in artifacts, binary name `mivia`, generic model-facing tool text, file/function size limits, and `make verify`/`make test`/`make build` gates.

## 3. MVP product decisions

| Area | MVP decision |
| --- | --- |
| Human mode | Keep `mivia chat`; preserve TUI, plain REPL, slash commands, tools, and sessions |
| Automation mode | Add `mivia exec --json` as a separate non-interactive command |
| Request input | One JSON object from stdin; no prompt or source dump in argv |
| Response output | JSONL on stdout only: bounded lifecycle events, then exactly one terminal result |
| Diagnostics | Human/debug text only on stderr; bounded and redacted |
| Persistence | Disabled by default for `exec`; no session autosave or prompt-file seeding |
| Workspace | Explicit `--workspace`; tools remain confined by the existing workspace guard |
| Provider/model | Resolve through existing config, with explicit `--provider`/`--model` overrides |
| Tools | Enabled by default only when requested by the input; `--no-tools` remains available |
| Cancellation | SIGINT/context cancellation produces a non-zero cancellation result and exit status |
| Protocol | Start at version `1`; reject unsupported versions and malformed requests |
| Rollback | Keep `chat` unchanged; remove/disable only the new `exec` command if needed |

## 4. Machine protocol

### 4.1 Invocation

```text
mivia exec --json [--config PATH] [--provider NAME] [--model NAME] [--workspace PATH] [--no-tools]
```

- The command must reject TTY-only assumptions and must not start Bubble Tea, the interactive renderer, autosave, or session UI.
- The request is read once from stdin as a bounded JSON object. No streaming request input is required for MVP.
- `--workspace` is the process/tool root. If omitted, use the current directory, matching existing chat behavior.
- Provider credentials remain in the existing env/config path; never accept credentials in JSON input or flags.

### 4.2 Request shape

```json
{
  "protocol_version": 1,
  "prompt": "inspect the repository and report the failing test",
  "max_steps": 8,
  "timeout_ms": 120000,
  "metadata": {"task_id": "safe-opaque-id"}
}
```

Rules:

- `protocol_version` and non-empty `prompt` are required.
- Reject unknown high-risk fields rather than silently accepting policy overrides.
- `metadata` is optional, bounded, and opaque; it must not be echoed unless explicitly allowlisted.
- Input size, prompt size, max steps, and timeout are capped by CLI constants/config. Zero means the documented default, never unlimited for machine mode.
- Workspace, provider, model, and credential settings are controlled by flags/config and validated before execution.
- Provider, model, workspace, and tool enablement are not request fields. Flags/config own those policies; `--no-tools` cannot be overridden by stdin.
- `max_steps` and `timeout_ms` are optional execution controls. Omitted or zero means the finite machine-mode default; negative values and values above the hard cap are rejected.

### 4.3 JSONL stdout shape

Every line is one JSON object with `protocol_version` and `kind`:

```json
{"protocol_version":1,"kind":"step","step":1}
{"protocol_version":1,"kind":"tool_start","tool_call_id":"opaque","name":"read_file"}
{"protocol_version":1,"kind":"tool_end","tool_call_id":"opaque","name":"read_file","status":"completed"}
{"protocol_version":1,"kind":"result","status":"completed","content":"...","usage":{"prompt_tokens":0,"completion_tokens":0}}
```

MVP rules:

- stdout contains JSONL only; no ANSI, markdown wrappers, banners, progress text, stack traces, or blank human lines.
- Event fields are bounded and redacted. Do not emit raw tool arguments, source contents, provider payloads, API keys, or full prompts.
- `result` is the only terminal record and must appear exactly once for every accepted request, including failure/cancellation.
- Terminal statuses are `completed`, `failed`, `cancelled`, `timed_out`, and `protocol_error`.
- Safe failure categories are stable strings; raw provider/process errors go to bounded stderr only and are not governance input.
- If the process cannot emit a valid terminal record, it exits non-zero and the caller must classify the run as protocol failure.

Pre-execution errors are deterministic: malformed JSON, unsupported protocol version, oversized input, and invalid request fields emit exactly one bounded `result` record with `status=protocol_error`, `failure_category=protocol_error`, and no user/provider detail when stdout is available, then exit `2`. If stdout cannot be written, exit `7` with only bounded stderr diagnostics.

Recommended initial bounds (constants, tested and documented): 64 KiB total stdin, 32 KiB prompt, 16 KiB maximum JSONL line, 256 events, 64 KiB terminal content, 16 KiB stderr capture, and 1 KiB per event preview. Truncation must be UTF-8-safe and explicit; terminal content overflow is a protocol/output failure, not silent truncation.

### 4.4 Exit status

Define and test stable process statuses. `cmd/mivia/main.go` and `internal/cli/root.go` must own typed exit errors or an equivalent classification; the current implementation maps every error to `1` and cannot satisfy this contract without change:

- `0`: completed result.
- `2`: invalid CLI arguments or malformed/unsupported request.
- `3`: configuration/provider unavailable before execution.
- `4`: cancelled by user/context.
- `5`: timeout/no-progress termination.
- `6`: agent/tool/provider execution failure.
- `7`: protocol/output failure.

Do not change the existing human `chat` exit behavior in this MVP except to stop reporting cancellation as success if that can be done without breaking interactive UX. Machine mode is the authoritative contract.

## 5. Implementation slices

### Slice 1 - Headless execution and protocol

Files to read first:

- `internal/cli/root.go`
- `cmd/mivia/main.go`
- `internal/cli/chat_repl.go`
- `internal/cli/chat.go`
- `internal/chat/session.go`
- `internal/agent/loop.go`
- `internal/agent/emit.go`
- `internal/tools/tools.go`
- `cmd/mivia/main.go`

Expected changes:

- Add `internal/cli/exec.go` with request decoding, bounded JSONL emission, signal handling, and exit classification.
- Add a small shared constructor, preferably `internal/cli/session_setup.go`, for config/provider/workspace/session setup, used by `chat` and `exec` without moving TUI logic into the shared path.
- Keep the exec constructor separate from interactive persistence: it must not call `SessionDir`, `chat.NewFileSessionStore`, `chat.NewSaveManager`, `SaveLast`, or `ensureAgentPromptFile`.
- Add explicit request-timeout plumbing through `internal/chat/session.go` and `internal/agent/loop.go` (or a narrowly equivalent context/options seam) so `timeout_ms` is not merely parsed and ignored. Distinguish deadline from user cancellation in the terminal result.
- Add typed exit errors/classification in `internal/cli/root.go` and map them in `cmd/mivia/main.go` to the documented statuses.
- Add a test-only provider seam using a local OpenAI-compatible HTTP server configured through `BaseURL` with `MIVIA_ALLOW_INSECURE_HTTP=1`; do not invent an undefined fake provider interface.
- Add protocol types/helpers in a file under `internal/cli` or `internal/agent` that stays below the repository size limits.
- Keep `exec` from creating `.mivia/sessions`, seeding `.mivia/agent-prompt.md`, or writing human UI output.
- Route agent events to the JSONL emitter with `ToolCallID` correlation and safe previews.
- Ensure cancellation and timeout always produce a terminal JSON result and non-zero exit status.

Acceptance:

- `mivia exec --json` can run against a local OpenAI-compatible HTTP test server and real registered read-only tools in a temporary workspace.
- A caller can parse stdout line-by-line without handling human text.
- Invalid JSON, missing prompt, unsupported version, missing API key, tool failure, provider failure, SIGINT, timeout, and output overflow are deterministic.
- Human `mivia chat` and TUI tests remain green.

### Slice 2 - Standalone release hardening

Expected changes:

- Add CLI process-level tests that capture stdin/stdout/stderr and exit status. Prefer a built test binary or `go run` subprocess; do not only call `cli.Execute` in-process for stream/exit claims.
- Add `mivia exec --help` documentation and examples to the owned `docs/product/agent.md`; document flags/config only in owned `docs/product/config.md`; update `docs/product/overview.md` only for the product command summary. Do not create parallel docs.
- Add a small offline smoke script only if it exercises the real built binary with the local OpenAI-compatible HTTP test server; no live provider or internet is required in unit/CI tests.
- Add `doctor --json` only if the first slice needs a machine-readable readiness probe; otherwise defer it to post-MVP. It is not required for the first runner protocol.
- Verify binary build, clean stdout contract, secret scan, generic tool surface, structure limits, and cancellation behavior.

Acceptance:

- A standalone operator can configure a provider, run `mivia doctor`, run interactive `mivia chat`, and run automation through `mivia exec --json`.
- The machine protocol is documented with one copy-paste example and one failure example.
- No secrets, raw prompts, raw model output, or PII are added to tests/docs.

## 6. Tests and verification

Write tests before implementation for the new contract:

1. Request decoder: valid request, empty prompt, malformed JSON, oversized input, unsupported version, unknown policy fields, and exactly-one `protocol_error` output.
2. JSONL emitter: stdout-only JSON, bounded fields, redaction, exactly-one terminal result, duplicate-terminal prevention.
3. Exit mapping: completed, invalid input, config failure, cancellation, timeout, agent/tool/provider failure, protocol failure.
4. Event bridge: step/tool start/tool end/assistant/result ordering and `ToolCallID` correlation.
5. Workspace/config: workspace confinement, secret-file denial, provider override, missing credentials, no implicit session/prompt-file writes.
6. Real process seam: built `mivia` subprocess with a local OpenAI-compatible HTTP server and real registered tools in a temporary workspace; capture stdout, stderr, signal, exit status, terminal result, and filesystem writes.
7. Regression: existing `internal/agent`, `internal/tools`, `internal/chat`, `internal/cli`, and generic-surface tests.

Cancellation/timeout proof must cover provider HTTP cancellation, agent tool-context cancellation, dispatcher/subagent shutdown, and any child command termination. Add bounded shutdown assertions and a race/leak-oriented test where practical; do not claim timeout support from `context.WithTimeout` alone.

Verification order:

```text
go test ./internal/cli ./internal/agent ./internal/tools ./internal/chat -count=1
go test ./cmd/mivia -count=1
make build
./mivia --help
go test ./... -count=1
make structure-check
make docs-check
make verify
```

If native WSL execution is unavailable, report every skipped command as `NOT_RUN`; do not infer standalone readiness from source inspection.

## 7. Security and reliability constraints

- Never place API keys, prompts, raw source, provider payloads, or unrestricted tool output in stdout diagnostics, logs, fixtures, or committed docs.
- Do not accept credentials, arbitrary environment overrides, shell strings, or workspace escapes through the JSON request.
- Preserve `workspace` path guards and secret-file denial. `run_command` remains argv-based and allowlisted; no free shell is added.
- Bound stdin bytes, stdout/stderr bytes, events, tool calls, max steps, request duration, and provider retries.
- Ensure signal cancellation reaches provider HTTP requests, tool contexts, subagent pools, and child processes without leaking goroutines.
- Keep machine mode stateless by default. Persistence/resume is explicitly post-MVP.
- Do not add a new dependency unless the existing standard library and current packages cannot satisfy the protocol.

## 8. Non-goals / deferred work

- No public Go embedding API in this plan.
- No dashboard/Temporal/Postgres integration.
- No replacing Codex/Crush adapters.
- No multi-turn machine session protocol, resume cursor, durable event store, or remote daemon.
- No provider abstraction rewrite or new provider.
- No TUI redesign.
- No automatic Git/PR actions.
- No months-long packaging matrix; support the current local build targets first.

## 9. Stop conditions and owner gates

Stop before Slice 1 if the protocol owner has not approved the request/result schema and exit-code mapping.

Stop before Slice 2 if the real subprocess seam cannot prove stdout purity, terminal-result delivery, cancellation, and workspace/credential safety.

Human review required from:

- CLI owner: command names, flags, output schema, exit statuses.
- Security owner: workspace, environment, secret-file, and subprocess behavior.
- Release owner: supported OS/build scope and documentation ownership.

The MVP is complete only when both slices meet their acceptance criteria and the built binary passes the real process-seam smoke. Package tests alone are insufficient.
