# .ai Control Surface

Product: **mivia** (MiviaLabs)
Binary: `mivia` (`cmd/mivia/`)
`.mivia/` is the canonical project-level control surface for agentic development in this repo. Root `AGENTS.md` is the canonical instruction file; `.mivia/` holds durable rules, skills, policy, and quality contracts that tool adapters reference.

## Read Order

1. `AGENTS.md`
2. `.mivia/INDEX.md` (this file)
3. **`.mivia/rules/05-adlc-agentic-development-lifecycle.md` — MANDATORY process. Read this before any work.**
4. Relevant other `.mivia/rules/*.md` in numeric order when multiple apply
5. Relevant `.mivia/skills/*/SKILL.md`
6. Relevant `.mivia/policy/*.json` when hooks, commits, or docs ownership are in play
7. Tool adapter files only when running that tool: `CLAUDE.md`, `.agents/`, `.claude/`, `.codex/`, `.github/copilot-instructions.md`

If an adapter conflicts with `AGENTS.md` or `.mivia/`, follow `AGENTS.md` / `.mivia/` and fix the adapter.

## Rules

### ⚠️ MANDATORY — read and follow before any work

`.mivia/rules/05-adlc-agentic-development-lifecycle.md` — **ADLC protocol: 7-step engineering cycle for all work. Do not skip.**
See also "Mandatory process" in `AGENTS.md`.

### Reference rules (read when relevant)

| File | Purpose |
|------|---------|
| `.mivia/rules/00-operating-doctrine.md` | Scope control, docs-first work, idempotency, verification contracts |
| `.mivia/rules/01-output-budget.md` | Terse status, final-answer shape, task slicing |
| `.mivia/rules/10-security-privacy.md` | Secrets, network, hooks, PII, fail-closed protected actions |
| `.mivia/rules/20-agent-quality.md` | Tests, mutation proofs, review gates, contract coverage |
| `.mivia/rules/30-go-standards.md` | Go layout for `cmd/mivia` + `internal/`, errors, naming, embed |
| `.mivia/rules/40-docs-ownership.md` | Single source of truth per topic; no parallel docs; `docs/OWNERS.yaml` |
| `.mivia/rules/50-concurrency-subagents.md` | Subagents as tasks/goroutines; shared MCP; caps; no process farm |
| `.mivia/rules/60-tools-project-language-generic.md` | Generic model-facing tools, default prompts, and portable review skill |
| `.mivia/rules/70-long-running-heartbeat.md` | Heartbeat protocol for long-running tasks |
| `.mivia/rules/80-commit-message.md` | Conventional commit format |

## Plans

Active plans follow ADLC protocol (zero `.md` files). Completed plans are archived under `.mivia/plans/archived/`.
Pending (not yet implemented) plans may reside in `.mivia/plans/` temporarily until the ADLC step zero challenge completes.

| File | Status |
|------|--------|
| `.mivia/plans/00-agent-roles-program-overview.md` | 🔄 Program index — see 01-09 |
| `.mivia/plans/archived/01-dispatch-boundary-tool-authorization.md` | ✅ Completed (2026-07-29) — index was stale; the plan header already said so |
| `.mivia/plans/archived/02-run-handle-ownership.md` | ✅ Completed (`402ca3f`) — two test gaps documented in the header |
| `.mivia/plans/03-agentkit-embedded-serving.md` | ❌ CLOSED — `internal/agentkit` + `agentkitdata` deleted; nothing blocked, 04/06 no longer depend on it |
| `.mivia/plans/archived/04-workspace-namespace-mivia.md` | ✅ Implemented — §5 gate decided against; see header |
| `.mivia/plans/05-role-model-core.md` | 🔄 Design-ready — **rewritten TOML-only 2026-07-31** (`c329a5f`); `.mivia/agents/*.md` is dropped, so P2 and the frontmatter work no longer apply and `internal/skills/frontmatter.go` is untouched. §12 records what the pivot removed. **Blocked on `27`** — it moves user config and hands `05` the home-equals-workspace guard (§5). `--agent` moved here from `08` §2 |
| `.mivia/plans/06-role-skill-binding.md` | 🔄 Design-ready — blocked on 05 |
| `.mivia/plans/07-role-routing.md` | 🔄 Design-ready — blocked on 05 |
| `.mivia/plans/08-role-cli-and-observability.md` | 🔄 Design-ready — blocked on 07 |
| `.mivia/plans/09-role-docs-and-examples.md` | 🔄 Design-ready — blocked on 08 |
| `.mivia/plans/archived/10-configurable-redaction.md` | ✅ Implemented — **redaction is off by default; read §5** |
| `.mivia/plans/archived/11-audit-metadata-honesty.md` | ✅ Implemented — §3 decided **C**: renamed to `InputPreview`/`OutputPreview`, computed only when a sink is attached |
| `.mivia/plans/archived/12-resume-restores-task-config.md` | ✅ Implemented — resume restores work, never authority |
| `.mivia/plans/archived/13-run-execution-fencing.md` | ✅ Implemented — **§5 AND §6 both shipped** (index previously said §6 was not started; re-verified at HEAD 2026-07-30). Registered retroactively as INV-AG-13; it had shipped with no manifest row. Unblocks `15` |
| `.mivia/plans/14-retire-the-legacy-namespace.md` | 🔄 Design-ready — **one open decision (§4)**; removes the last `.ai` references |
| `.mivia/plans/15-resume-user-surface.md` | ✅ Implemented — `/resume` and a dashboard key behind one shared path; the resumed handle is owned by the chat session principal so inspect/join/cancel work, and resume fails closed without an identity. Pinned by INV-AG-19. `12` + `13` are now reachable |
| `.mivia/plans/archived/16-discoverable-skills.md` | ✅ Implemented — `b17988f`; skills are now discoverable with name + description in tool surface, sanitized for schema safety |
| `.mivia/plans/18-agent-codebase-intelligence-tools.md` | 🔄 Implementation-ready — not started; §5 accepts `golang.org/x/tools`, one tool in phase one |
| `.mivia/plans/archived/19-ledger-query-tools-for-agents.md` | ✅ Implemented — execution references are resolvable; see header for implementation corrections |
| `.mivia/plans/20-scope-content-reads-to-their-principal.md` | ❌ VALIDATED → **DO NOT BUILD**; §3 decided **D** (accept and document). §1's defect is real; the proposed gate defends against no principal that exists today and costs a measured availability regression on the SQLite backend. Registered as INV-AG-12; INV-AG-9's scope corrected |
| `.mivia/plans/archived/21-durable-event-ordering-and-timestamps.md` | ✅ Implemented — durable event timestamps and derived ordering are recorded by INV-AG-11 |
| `.mivia/plans/archived/22-idempotent-spawn-fingerprints-the-work.md` | ✅ Implemented (`3aa2438`, `d1d470e`) — explicit work fingerprints and caller-scoped idempotency keys fix cross-turn retries without exposing foreign runs; pinned by INV-AG-16 |
| `.mivia/plans/archived/23-content-retention-and-durable-deletion.md` | ✅ Implemented (`99609fc`) — decision E: recorded content is deliberately unbounded and pinned by INV-AG-15; retention is not a privacy control because the same bytes remain in session transcripts |
| `.mivia/plans/archived/24-durable-run-deletion.md` | ✅ Implemented — durable tombstone-pinned hard deletion prevents resurrection and preserves the incremental cursor; content remains untouched |
| `.mivia/plans/25-skill-triggers.md` | ✅ Implemented — `triggers:` now parse and reach the model-facing surface; unknown frontmatter keys are rejected at load. Pinned by INV-AG-17. **`05` §6 was amended** — the subset parser lives in `internal/skills/frontmatter.go`; do not build a second one in `internal/roles` |
| `.mivia/plans/30-streaming-ledger-read-paging.md` | 🔄 Design-ready — explores bounded source work for `ledger_read`; implementation is blocked on a redaction strategy that remains safe across stream boundaries |
| `.mivia/plans/31-kimi-provider-integration.md` | 🔄 Design-ready — direct Kimi Open Platform integration; provider-specific request shaping and preserved reasoning state are required before the Kimi TOML catalog can ship |
| `.mivia/plans/archived/32-skill-resources.md` | ✅ Implemented (2026-08-01) — manifest-gated, lazy TOML text resources with invocation-scoped reader capabilities and ephemeral output retention |
| `.mivia/plans/archived/28-model-context-windows.md` | ✅ Implemented — explicit model context capacities, effective prompt budgets, nested-request enforcement, and exact restore |
| `.mivia/plans/archived/29-model-selection-dialog.md` | ✅ Implemented — explicit provider-qualified model catalog, atomic model bindings, persistence pairing, and TUI picker |
| `.mivia/plans/archived/27-user-config-path-alignment.md` | ✅ Implemented — user config and env discovery use `~/.mivia/`; hard cutover only, with no legacy fallback, probe, notice, or auto-migrate. Ships before `05`, which owns the home-equals-workspace guard |
| `.mivia/plans/cli-mvp-standalone.md` | 🔄 BLOCK — not implementation-ready |
| `.mivia/plans/composer-autocomplete.md` | 🔄 v5 — not started. **Phase 0 is a gate:** reuse `overlayAt`/`sliceANSI`/`sgrBefore` from `tui-centered-dialogs`; do not write a second ANSI compositor. v5 also owns skills-as-slash-commands (`/bug` → `/bug-audit`), sized for hundreds of skills across a project and a `~/.mivia/skills` user scope |
| `.mivia/plans/archived/events-eventbus-refactor-plan.md` | ✅ Implemented (Phases 1–3) — `events.Bus`, agent-loop publishing, and the poll-chain fix all shipped; pinned by INV-TUI-1/2. **Phase 4 (OTEL) was always optional and is not built.** Do not implement from the document — 1713 stale lines; write a short new plan for the OTEL adapter instead |
| `.mivia/plans/tui-chat-ux-full-experience.md` | ⚠️ Needs re-audit — substantially overtaken by shipped TUI work (INV-TUI-1…22, progress transparency). The story blocks it specifies already exist. Re-derive against HEAD before treating anything as outstanding |
| `.mivia/plans/archived/progress-transparency-plan.md` | ✅ Implemented — model heartbeat and thinking-phase progress are visible in TUI chrome |

### Implementation order (triaged 2026-07-30)

Re-verify each status against HEAD before starting — four rows in the table above were
stale when this triage ran, and one of them (`13` §6) changed the ordering.

**Tier 0 — correct the record.** Done 2026-07-30: `13` registered as INV-AG-13, `13` and
the eventbus RFC archived, stale rows fixed. Remaining: none.

1. ~~**`23`** — implemented 2026-07-30 (`99609fc`): decision **E** landed (accept, pin,
   document), pinned by INV-AG-15. No production behaviour change.~~
2. ~~**`24`** — implemented 2026-07-30: tombstone-pinned hard deletion, with Wave 0
   (`internal/storage/store.go` split) landed separately.~~
3. **`14`** — LOW; test/doc surface only, one open decision (its own recommendation is B).
4. **`18`** — implementation-ready, all decisions closed, no dependencies.
5. **`05` → `06`/`07` → `08` → `09`** — the roles program, as one coherent investment.
   `05` is unblocked and HIGH blast radius (privilege surface); `07` has two unconfirmed
   decisions. Do not interleave with 1–4; `00` §3's program invariants assume the set lands
   together.
6. **`composer-autocomplete`** — genuinely not started (no implementation in `internal/cli`).
   Sequence after `tui-centered-dialogs`: its `dialog_compositor.go` supplies the ANSI splice
   primitive this plan's popup needs, and `sgrBefore` closes the one high-severity risk
   (an unterminated SGR run crossing the cut column). Two plans, one compositor.

**Do not build:** `25` option D (parse triggers with no consumer — see its §3) · `20` (validated DO-NOT-BUILD, decision D) · `03` (closed, packages deleted)
· `cli-mvp-standalone` (independent challenge returned BLOCK; owner approval required)
· eventbus Phase 4 (write a fresh short plan if OTEL is wanted) · `tui-chat-ux` (re-audit first).

**Sequencing hazards.** Plans that touch `.mivia/invariants.md` concurrently must merge, not
overwrite. Invariant ids are allocated **at landing time**, lowest free per prefix.
`INV-AG-1` through `INV-AG-27` are all taken (re-verified at HEAD 2026-07-31); lowest free is
`INV-AG-28`. This line previously read "`INV-AG-8` is a permanent gap; 12 through 17 are taken" —
both halves were false, and `05` had allocated `INV-AG-8` on the strength of it. `25` (recorded
result-size decisions), `26` (bounded web tool results) and `27` (per-tool output ceilings) landed
on 2026-07-31 after that recount.

`scripts/validate_invariants.py` now **rejects duplicate ids** and runs inside `make verify`,
so a duplicate no longer passes silently. It counts only the id column of a definition row:
ids cited inline in a description, and the cross-reference tables under "Liveness Gap Notes",
are not definitions. A naive grep over the file counts both and reports duplicates that do
not exist — that mistake was made once already.

## Doctrines

- `.mivia/doctrines/evidence-before-claims.md` — from mivia-agent-skills
- `.mivia/doctrines/verification-is-part-of-delivery.md` — from mivia-agent-skills

## Skills

Canonical project skills (under `.mivia/skills/` only; do not fork into tool adapters):

Ported from **mivia-agent-skills** (higher reliability than agentkit MVP copies):

- `engineering-working-contract` — standing communication, evidence, engineering, verification
- `verify-code-change` — blast-radius verification ladder; PASS/PARTIAL/FAIL
- `bug-audit` — confirmed reachable bugs only; hard anti-false-positive rules

Repo-native:

- `verify-change` — mechanical package/gates report via `mivia-report/v1`
- `docs-update` — OWNERS-safe documentation edits; no duplicates
- `secure-change` — secrets, authz, network, tool isolation
- `concurrency-review` — subagent caps, pools, cancel, race
- `architecture-review` — portable structural review of boundaries, dependencies, abstraction cost, and evolution risk; runs at ADLC Step 0
- `feature-delivery` — bounded feature slice with verification

`bug-audit` and `architecture-review` remain report-only. They do not commit or push.

## Policy

Machine-readable hook and agent policy:

| File | Purpose |
|------|---------|
| `.mivia/policy/commit-message.json` | Conventional commits: types, scopes, subject length |
| `.mivia/policy/agent-hook-bypass.json` | Blocked verification-bypass flags/env vars + corrective message |
| `.mivia/policy/docs-ownership.json` | Required `docs/OWNERS.yaml`, forbidden duplicate titles, canonical path rules |

## Quality

- `.mivia/quality/contracts/` — project contract matrices for doctor/audit/runtime gates (populate as product surfaces land).

## Runtime Artifacts

- `.mivia/runs/` is for workflow traces and summaries and must be gitignored.
- Never persist raw prompts, raw model outputs, provider payloads, credentials, or plausible secrets under `.mivia/runs/` or elsewhere in the tree.

## Product Commands (once Go lands)

```bash
go test ./...
go vet ./...
go build -o bin/mivia ./cmd/mivia
./bin/mivia --help
```

Use binary name **`mivia`** only. Do not invent or document a `mivia-agent` binary for this product.

## Documentation Ownership

- Topic ownership and canonical paths are declared in `docs/OWNERS.yaml`.
- Agents update the existing canonical document for a topic; they do not create parallel or duplicate docs (see `.mivia/rules/40-docs-ownership.md` and `.mivia/policy/docs-ownership.json`).

## Hooks

- Install: `make install-hooks` (sets `core.hooksPath=.githooks`)
- Implementations: `scripts/git-hooks/*`
- Wrappers: `.githooks/*`
- Agent bypass guard: `scripts/run_agent_hook_guard.sh` + `.mivia/policy/agent-hook-bypass.json`
- Docs: `docs/development/hooks.md`

## Semgrep

- Rules: `semgrep/agent-standards.yml`
- Run: `make semgrep` / pre-commit staged / pre-push full
- Contract: `python3 scripts/test_semgrep_rules.py`

## Verification After Control-Surface Edits

After changing `AGENTS.md`, `.mivia/`, adapter configs, hooks, or Semgrep agent standards:

1. Re-read this INDEX and the touched rule/policy.
2. Run `make verify` (or the narrowest contract test for the change).
3. Report what was verified and what remains unverified.
