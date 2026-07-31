# 08 — Role CLI surface and observability

**Status:** Design-ready. §2 handed to `05` on 2026-07-31.
**Date:** 2026-07-29 · revised 2026-07-31
**Commits:** `feat(cli): add role inspection`, `feat(cli): surface the active role in events and TUI`
**Depends on:** `07`. **Blocks:** `09`.
**Blast radius:** MODERATE.

---

## 1. Why this is its own plan

The predecessor plan had no human-facing surface at all: no way to list roles, no way to see a role's *effective* tools, no role in events or TUI, and an unhelpful error for a bad name. For a **privilege** feature, "which role ran and what could it do" must be answerable. Shipping `05`–`07` without this yields a security surface that cannot be audited by the person who configured it.

## 2. `--agent <name>` — moved to `05`

**Decided 2026-07-31: `05` owns the flag.** Parsing, Layer-B validation, root-session scoping, `TestRootSession_AgentFlag` and the built-binary integration test all ship with the role model — see `05` §7 ("The registry is not the same object at B and C"). The scoping guarantee and its only caller must not land in different cycles.

This section's analysis survives there in substance and is not repeated: scoping *before* `attachSessionDispatcher` is the one insertion point where scoping is guaranteed to be undone, because `registerSessionTool` registers back into the same registry; `MultiStepHandler.FullRegistry` is the same pointer as `sess.Tools`, so the spawner's pool would keep mutating after resolution; and conditional registration inside `NewSessionDispatcher` is preferred over post-hoc filtering because it keeps one registry and one truth.

**`08` still owns** everything a person needs to *see* the result: §3's inspection surfaces, the unknown-role error text where it is user-facing, and the TUI's active-role indicator (§5) — `--agent researcher` giving zero visual confirmation it took effect is an `08` defect, not an `05` one.

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

- `TestRootSession_AgentFlagUnknownName` — error lists available roles
- `TestAgentsListExplain` — per-field origin
- `TestDoctorShowsEffectiveTools`
- `TestToolNotAvailableErrorNamesRole`
- `TestEventCarriesRole`

`TestRootSession_AgentFlag` and the built-binary `mivia chat --agent <role>` integration test moved to `05` §10 with the flag.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Restore `unknown tool` generic message | `TestToolNotAvailableErrorNamesRole` |
| M2 | Drop role from event metadata | `TestEventCarriesRole` |

**Rollback criterion:** if the inspection surfaces prove too thin to audit a role, the escalation is `mivia agents list --explain` becoming the primary surface and `doctor` deferring to it — not dropping per-field origin, which is `05` H3's only mitigation.
