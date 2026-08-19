# Skill System Architecture

This document describes the host skill system: how skills are discovered,
validated, exposed to the model, invoked, and granted access to optional local
resources. It is not a catalog of the repository's individual skills.

## Boundaries

A skill is untrusted Markdown task guidance. The host parses its small
frontmatter subset and passes its instructions to an agent; it does not execute
commands, tool declarations, or code embedded in `SKILL.md`.

The system has four distinct boundaries:

1. **Discovery** finds skill directories and reads `SKILL.md` plus optional
   resource metadata.
2. **Selection** exposes sanitized names, descriptions, triggers, and slash
   eligibility to model and UI surfaces.
3. **Activation** creates a per-invocation capability for declared text
   resources when the invocation surface supports tools.
4. **Execution** runs the skill through the normal dispatcher and policy
   boundaries; skill text never overrides system, developer, safety, or tool
   policy.

Keeping discovery separate from activation is the central design rule: loading
a skill does not grant it a filesystem reader, and a resource body is not read
until the active model requests its declared ID.

## Discovery and precedence

The loader scans two optional roots:

| Origin | Root | Precedence |
|---|---|---|
| User | `~/.mivia/skills/<name>/` | Lower |
| Project | `<workspace>/.mivia/skills/<name>/` | Higher |

Each child directory is considered a skill only when it contains a regular
`SKILL.md`. Project definitions replace user definitions with the same skill
name. Invalid, unreadable, duplicate, reserved, or unsluggable definitions are
skipped with bounded warnings on the merged production path; the loader never
executes content while scanning.

`SKILL.md` instructions are read eagerly because they are the skill body.
`resources.toml`, when present beside it, is parsed as a strict declaration of
available references. Resource bodies are not read during discovery.

The supported frontmatter is intentionally small: name, description, triggers,
invocability, argument hint, short description, and optional `tools` (declared
tool requirements for agent skill binding). Unknown keys are rejected.
Model-facing text is sanitized and bounded before it reaches tool schemas,
slash catalogs, or prompts.

### Frontmatter `tools`

```yaml
---
name: bug-audit
description: ...
tools:
  - read_file
  - grep
---
```

- **Omitted** → `Definition.Tools` is nil (no declared requirements).
- **`tools: []`** → non-nil empty (author declared none required).
- **Non-empty list** → those tool names; used for the non-vacuous check
  `agent.EffectiveTools ⊇ skill.Tools` when a named agent invokes the skill.

Skill text still does not grant tools. The host already scopes nested skill
handlers; the `tools` field is metadata for **agent–skill binding**, not a
second tool registration path.

## Agent–skill binding

Named agents (`.mivia/agents/*.toml` / `~/.mivia/agents/*.toml`) may set:

```toml
skills = ["bug-audit", "verify-change"]
```

| Authoring | Runtime meaning |
|---|---|
| Key omitted | All trusted skills (see gate below) |
| `skills = []` | No skill handlers |
| `skills = ["a", "b"]` | Only those skill names |

Enforcement is root fan-out only (v1):

- `dispatch_tasks` when an explicit task `skill` is present
- `spawn_agent` when an explicit task `skill` is present
- Skill handler invoke on resume/retry (rechecks the selected agent snapshot)

Nested `multi_step` agents do not receive privileged orchestration tools, so
they cannot synthesize skill tasks. The selected root agent's immutable
snapshot is built at dispatcher construction (startup, `/agent` switch, model
switch).

### Trust and the workspace gate

| Gate (`load_workspace_config` in `~/.mivia/mivia.toml`) | Skill sources loaded for handlers |
|---|---|
| Off | User skills only (`~/.mivia/skills/`) |
| On (default) | User + project; project may shadow same-named user skills |

Agent allowlist resolution uses a dual-origin catalogue: user skills win over
project for provenance when both exist; project-only names require the gate.
When the gate is off, project skill sources are not loaded at all, so a
workspace skill cannot erase a user skill of the same name.

This gate is deliberately narrower than agent discovery. Workspace
`.mivia/agents/*.toml` files always load and retain workspace provenance; the
gate only controls workspace `[chat]`/`[subagents]` prompt surfaces and project
skill handlers. A workspace agent cannot inherit from a user agent, and a
workspace-only skill cannot enter an agent allowlist while the gate is off.
Direct user-invoked skill handlers and prompt turns are separate from the
task-agent `agent` + optional `skill` binding enforced by the root dispatcher.

## Skill directory contract

```text
.mivia/skills/<skill-name>/
├── SKILL.md                 # required untrusted instructions
├── resources.toml           # optional resource catalogue
└── report-template.md       # local convention for report-producing skills
```

`report-template.md` is a repository convention enforced by the control-surface
checks for report-producing skills. It is not a host-level semantic required of
arbitrary user skills. A skill may declare other text resources in its
`resources.toml` as long as each one is explicitly allowlisted.

The v1 manifest shape is TOML:

```toml
format = 1

[[resources]]
id = "report-template"
path = "report-template.md"
summary = "Required report structure."
```

Only resource IDs and summaries are model-visible at activation time. The
backing path remains host-private.

## Activation lifecycle

When a tool-enabled invocation selects a resource-bearing skill, the host:

1. Rechecks that the source directory is the same directory identity that
   supplied `SKILL.md`.
2. Pins a descriptor-relative resource root and creates a fresh activation.
3. Adds a catalogue of IDs and purposes to the active skill prompt.
4. Adds one ephemeral `read_skill_resource` tool to the invocation's derived
   registry.
5. Allows the model to request only a declared ID.
6. Reads, validates, caches, and budgets that text for the activation.
7. Closes the activation and revokes the reader when the invocation ends.

The root session registry is not mutated by activation. Nested skill handlers
clone their registry; direct TUI slash turns derive a turn-local registry and
dispatcher. Queued TUI skill turns defer activation until they become active, so
waiting work does not retain resource roots.

## Invocation surfaces and fallbacks

Resource loading is capability-dependent, not universal across every legacy
handler:

| Surface | Current behavior |
|---|---|
| Model-directed skill subagent | Multi-step handler; resource-bearing skills use the scoped activation wrapper. |
| TUI `/skill-name` with tools | Direct per-turn activation and ephemeral reader. |
| TUI `/skill-name` without tools | Instruction-only prompt; use the inline fallback in `SKILL.md`; no resource reader exists. |
| Compatibility `runtime.Skill` handler | Retains the typed `Definition.Run` one-shot path; it receives instructions but does not activate sidecar resources. |
| Plain REPL slash catalog | Built-in commands only; skill slash commands are currently a TUI surface. |

Therefore a skill must describe an essential inline fallback when its report or
resource is integral to the task. “Always load the resource” is accurate only
for an invocation where the scoped reader is available.

## Resource safety and limits

The reader accepts an ID, never a path or skill selector. The host resolves the
ID through the manifest and rejects undeclared, absolute, parent-escaping,
symlinked, hard-linked, special, oversized, non-UTF-8, or otherwise invalid
content. The activation is bound to the original skill directory identity, so a
replacement or origin change fails closed.

The current limits are:

| Limit | Value |
|---|---:|
| Manifest size | 32 KiB |
| Declared resources | 32 |
| Summary | 200 bytes, truncated without splitting UTF-8 runes |
| One resource body | 64 KiB |
| One activation aggregate | 128 KiB |
| Resource tool result | 64 KiB plus fixed framing |

Resource text is labeled as untrusted when returned to the model. The resource
tool's raw result is replaced with a bounded marker before event/session
persistence, and activation cache/root state is released on close. This does
not prevent a model from quoting resource text in an assistant response, nor
does it make provider-side logging or crash dumps impossible.

Linux uses descriptor-relative, no-follow file handling and identity/link checks.
Other supported platforms use `os.Root` plus identity/link checks, but retain a
replacement-special-file race residual; callers must not treat the boundary as
an absolute guarantee against every platform filesystem race.

Each resource read is content-addressed: `ResourceContent.Digest` and
`ResourceSnapshot.Digest` record a SHA-256 hash so a snapshot can be persisted
and compared durably without keeping the backing path alive. Reads within one
activation are served from a per-activation cache — a repeat request for the
same declared ID does not re-touch the filesystem or re-count against the
activation's byte budget. `SkillActivation.Read` is fully serialized behind a
single mutex; this trades read concurrency for atomic cache and quota
accounting within one activation, consistent with the bounded, non-concurrent
nature of a single skill invocation.

`validateDeclaredSkillTools` checks an agent's declared `tools` against a
static built-in tool catalogue and fails closed on any name it does not
recognize. This includes `read_skill_resource` itself: a skill or agent cannot
self-declare the ephemeral resource-reader tool as a required tool, since the
host — not skill authoring — grants it during activation.

`checkSkillDefinition` fails closed on an origin mismatch: if the skill
selected at activation time does not match the origin (user vs. project)
recorded when the agent's allowlist was resolved, the request is rejected
rather than silently falling back to a same-named skill from the other
origin. This is a distinct guarantee from the directory-identity recheck
described in step 1 of the activation lifecycle above.

## Verification and maintenance

The implementation is covered by loader, activation, dispatcher, slash, and
ephemeral-result tests. The tests exercise malformed manifests, duplicate and
unsafe declarations, traversal, symlink/hard-link rejection, size and text
limits, origin replacement, scoped registry behavior, queued and direct turns,
multi-step skill invocation, and persistence scrubbing.

`python3 scripts/test_semgrep_rules.py` validates every report-producing skill's
manifest and local template. It also byte-checks the `secure-change` and
`verify-change` copies against `.agents/templates/agent-report-v1.md` to catch
template drift. Local copies remain deliberate: resource access is confined to
the selected skill directory rather than allowing a model to read arbitrary
workspace files.

## See also

- [Architecture overview](overview.md)
- [Agent tools and safety](../product/agent.md)
- [Agent workflow](../development/agent-workflow.md)
- [Concurrency model](concurrency.md)
- `.mivia/skills/*/resources.toml` and `.mivia/skills/*/report-template.md`
