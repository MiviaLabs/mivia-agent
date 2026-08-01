# 08 - Agent CLI surface and observability

**Status:** IMPLEMENTED (2026-08-01) - catalog, doctor, identity, and verification phases shipped.
**Goal:** Make named-agent definitions and runtime instances inspectable without conflating definition, invocation, or model-binding identity.
**Depends on:** shipped `05`–`07`.
**Blast radius:** MODERATE - diagnostics and auditability of a privilege surface.

This plan deliberately does not change agent-file trust, task-resume authority,
or provider selection policy. Those are respectively pinned by `INV-AG-29`,
`INV-AG-31`, and `INV-AG-28`.

## Decisions locked before implementation

1. Workspace `<workspace>/.mivia/agents/*.toml` files always load. They are
   reported as `workspace` provenance, never as gate-disabled. The trusted
   user `load_workspace_config` gate separately controls workspace
   `mivia.toml` prompts and project skill handlers.
2. `mivia agents list` and `mivia agents explain <name>` are new read-only
   commands. Their grammar is `mivia agents list [--workspace DIR]` and
   `mivia agents explain <name> [--workspace DIR]`; they never accept a
   provider/config override or construct a provider, workspace tool registry,
   or dispatcher. `mivia doctor [--config PATH] [--workspace DIR]` keeps its
   current provider-config input and uses the fixed trusted user config for
   agent gates, exactly as agent loading does.
3. `/agent [name]` remains the root-agent selector on both chat surfaces.
   `/agents` is a new read-only alias that lists the current root agent and
   selectable definitions on both surfaces. The slash catalogue remains the
   single source of truth for help, completion, and dispatch.
4. An agent-file `model` remains a spawned-task default, validated immediately
   before the selected routed task executes against the active provider catalog.
   It is not a root-session model policy and does not constrain `/model`.
   Root startup and `/agent` do not silently switch models. `/model` changes
   only the idle root session binding; it never mutates the selected definition,
   prompt, tool scope, or task-agent defaults.
5. Public observability uses typed, allowlisted identity only:
   definition name + source enum, opaque instance ID, and monotonically
   increasing session-local model-generation ID. It never emits or persists
   definition digests, source paths, prompts, effective-tool payloads,
   user/model content, errors, or free-form metadata. The digest remains an
   internal authorization check for routed-task resume (`07`). Root saved chat
   sessions are not task resumes and do not gain agent-selection persistence in
   this plan.
6. A root-agent switch is transactional: build and validate the candidate
   scope/binding first, then atomically publish selected identity, prompt, and
   turn limit. Failure leaves the old root-agent state intact. Selecting an
   agent without a prompt or `max_turns` restores the configured session
   defaults rather than retaining values from the previous agent.

## Identity contract

- `agent_definition` is the logical, immutable resolved snapshot identified to
  operators by canonical name and source (`user` or `workspace`). Its internal
  digest is authorization-only and is never an observability identifier.
- `agent_instance` is one disposable execution. A root instance uses the
  session's opaque ID; every routed invocation generates a fresh opaque ID
  (using the cryptographically generated runtime-ID primitive) before event
  publication. It never exposes a caller/model-controlled run or task ID.
  Many instances may share one definition concurrently.
- `model_generation` is an opaque session-local monotonic ordinal. Initial
  binding is generation 1; every successfully published idle `/model` binding
  increments it, including a switch back to the same model. It is captured
  with the turn binding, never inferred from a model string.
- The private compiled root fallback is displayed separately as `root
  fallback`; it is neither a selectable definition nor an `--agent` enum.
- Definition-effective tools are the resolved allowlist before live registry
  availability. Runtime-effective tools are deliberately out of catalog and
  doctor output because computing them would construct a workspace registry.
  Tool names may appear once only in the CLI definition boundary; events carry
  no tool set.

## Phase map

| Phase | Goal | Depends on |
|---|---|---|
| [01 - catalog CLI and diagnostics seam](01-agent-catalog-cli.md) | Build a provider-independent, traceable agent inspection projection | `05`, `06` |
| [02 - doctor and configuration diagnostics](02-doctor-and-config-diagnostics.md) | Report collection state without hiding it behind provider readiness | `01` |
| [03 - identity and observability](03-identity-and-observability.md) | Preserve typed identity across REPL, TUI, events, and model switches | `01`, `02`, `07` |
| [04 - verification and closeout](04-verification-and-closeout.md) | Audit all surfaces and run repository gates | `03` |

## Required behavior

- List/explain and doctor load only config/skill/agent discovery paths and work
  without provider credentials.
- A diagnostic distinguishes loaded user/workspace definitions, user-wins
  shadows, malformed/unreadable files, no definitions, and the independent
  workspace prompt/project-skill gate. One bad file must not erase unrelated
  valid diagnostic rows.
- Unknown names list sorted selectable names and source provenance. Human
  output has a locked field order; all collections are sorted by canonical
  name/path.
- List, explain, doctor, reports, and identity events never print raw system
  prompts, secret values, definition digests, or unbounded parser/provider
  errors. CLI explain may display the explicitly selected definition's bounded
  local path; event/report payloads never contain paths.
- Routed-task resume remains fail-closed on changed definitions under `07`.
  Root saved-chat load is outside this plan and must not be described as a
  definition resume.

## Delivery graph and rollback criterion

`diagnostic discovery + resolution trace` → `agents CLI` → `doctor` →
`identity/session binding + event propagation` → `REPL/TUI rendering` →
`audit`. Do not start UI/event work until the diagnostic projection and its
privacy contract pass. Return to Step 0 if diagnostic loading requires a
dispatcher/provider, if a public event needs a digest/path/prompt/payload to
be useful, or if an atomic root-agent switch cannot preserve the prior state.

## Verification contract

Focused RED/GREEN tests cover config discovery, resolution trace, CLI routing
and output, doctor exit behavior, chat binding generations, event identity,
root-agent transactionality, routed task behavior, REPL, and TUI parity. The
closeout runs the focused race packages plus `make verify`, `make test`, `make
race`, `make invariants`, `make secret-scan`, and `make docs-check`, followed
by a hostile bug-audit round and a `mivia-report/v1` record. `make verify`
already includes structure, invariant-reference, secret, docs, Semgrep, Go,
and hook gates; do not claim its constituent checks separately unless run.
