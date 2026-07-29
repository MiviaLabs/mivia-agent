# 08 — Role CLI surface and observability

**Status:** Design-ready.
**Date:** 2026-07-29
**Commits:** `feat(cli): add --agent and role inspection`, `feat(cli): surface the active role in events and TUI`
**Depends on:** `07`. **Blocks:** `09`.
**Blast radius:** MODERATE.

---

## 1. Why this is its own plan

The predecessor plan had no human-facing surface at all: no way to list roles, no way to see a role's *effective* tools, no role in events or TUI, and an unhelpful error for a bad name. For a **privilege** feature, "which role ran and what could it do" must be answerable. Shipping `05`–`07` without this yields a security surface that cannot be audited by the person who configured it.

## 2. `--agent <name>` on the root session

Parse via `flagValue` (defined `internal/cli/root.go:69`, used for `--provider` at `internal/cli/chat_command.go:18`) — **not** the `chatFlags` switch (`chat_repl.go:20-34`), which handles only boolean flags.

**Wiring sequence.** `runChat` is `internal/cli/chat_command.go:16-94`. Relevant call sites: `configureChatWorkspace` at `:63`, `attachSessionDispatcher` at `:68`, prompt resolution at `:46-52`.

> **Correction to the predecessor plan, which would have produced a silently-unscoped root role.** It prescribed applying `ScopedRegistry` *between* those two calls. But `attachSessionDispatcher` → `NewSessionDispatcher` → `registerDelegationTools`/`registerOrchestrationTools` → `registerSessionTool` **registers tools into that same registry** (`cli/dispatcher.go:169-178`, `orchestrate.go:380-393`). Anything scoped before that call is re-populated after it — the prescribed insertion point is the one place where scoping is guaranteed to be undone. `mivia chat --agent researcher` would have ended up holding all six delegation tools.
>
> Compounding it: `MultiStepHandler.FullRegistry` is the same pointer as `sess.Tools` (`cli/dispatcher.go:133`), so the "spawner's effective pool" would mutate after resolution.

**Correct approach:** pass the role into `NewSessionDispatcher` so delegation/orchestration tools are registered **conditionally**, or scope *after* `attachSessionDispatcher` returns. Prefer conditional registration — it keeps one registry and one truth.

Test `TestRootSession_AgentFlag` must assert the **final** registry contents after dispatcher attach, and must assert **absence of all six mandatory-denylist tools**. As the predecessor specified it, the test would have passed vacuously.

`--agent` cannot be validated at flag-parse time (roles resolve at Layer B, `05` §7). Validate there; the error lists available role names.

## 3. Inspection

| Surface | Behavior |
|---|---|
| `mivia agents list` | name, title, description, source path, effective tool count |
| `mivia agents list --explain <name>` | per-field winning source (H3 in `05` §9) and the fully resolved effective tool set |
| `mivia doctor` | role table with effective tools; if roles were not loaded (non-chat entry point, H9), print **"workspace roles not loaded"** rather than an empty table |
| `/agents` slash command | same as `list`, inside the REPL (`handleSlash` is the natural home) |
| `mivia config` | show `[agents]` resolution |

`mivia doctor` printing each role's *effective* tool set is the single highest-value item here — it is the only way a user can verify what they configured actually resolved that way.

## 4. Errors and events

**Unknown role.** With per-role handlers, a bad name currently reaches `Dispatcher.Invoke` and returns `unknown subagent "foo"` (`runtime/dispatcher.go:207`) with no list of valid names. Both `--agent typo` and a model-emitted bad `role` must produce an error naming available roles.

**Tool-not-available.** A read-only role's model *will* call `write_file`. Today it would receive `unknown tool "write_file"` (`internal/tools/tools.go:100`) — indistinguishable from a hallucinated name, so the model retries. Plan `01` §3d already introduces the right message: `tool "write_file" is not available to this agent`. Extend it to name the role: `... not available to role "researcher"`. Cheap, and it materially changes model behavior.

**Events.** `runtime.Metadata` (`dispatcher.go:44-47`) carries `Name` only, and `OnEventForMultiStep` (`cli/dispatcher.go:183-209`) forwards `e.Name` = the *tool* name, so the role never reaches parent chrome. Add the role to `EventSubagentStart`/`End` and emit the **resolved effective tool set once per spawn** — for a security feature, the privilege decision should be observable, not inferred.

**TUI.** `printReplBanner` shows provider/model only; `--agent researcher` currently gives zero visual confirmation it took effect. Show the active role.

## 5. Deliberately out of scope

- **Cost/token attribution per role.** Budget is keyed on `TurnID`/`ParentID` (`dispatcher.go:241-250`), never on role, and the keying is already incoherent (`01` §2). Fixing attribution means fixing budget keying first — a separate change.
- **Role-scoped memory/context.** Stated as out of scope rather than left absent.
- **Reload.** Roles resolve once at startup while the REPL lives for hours, consistent with skills today. If roles are markdown files, `/reload` becomes cheap — worth doing, but after `09`.

## 6. Verification

```bash
go test ./internal/cli/... ./internal/chat/... ./internal/runtime/... -race
make verify && make invariants
```

**Tests:**

- `TestRootSession_AgentFlag` — asserts final registry after dispatcher attach; **all six delegation tools absent**
- `TestRootSession_AgentFlagUnknownName` — error lists available roles
- `TestAgentsListExplain` — per-field origin
- `TestDoctorShowsEffectiveTools`
- `TestToolNotAvailableErrorNamesRole`
- `TestEventCarriesRole`
- **Built-binary integration test for `mivia chat --agent <role>`** — rule 20 forbids fake-only closure for shipped commands; the predecessor covered this flag with a unit test plus a manual smoke, which does not satisfy the rule.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Apply `ScopedRegistry` before `attachSessionDispatcher` | `TestRootSession_AgentFlag` |
| M2 | Restore `unknown tool` generic message | `TestToolNotAvailableErrorNamesRole` |
| M3 | Drop role from event metadata | `TestEventCarriesRole` |

**Rollback criterion:** if conditional registration in `NewSessionDispatcher` proves invasive, fall back to scoping after `attachSessionDispatcher` — but never to scoping before it.
