# Skill System Architecture

Status: Implemented v1

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
invocability, argument hint, and short description. Unknown keys are rejected.
Model-facing text is sanitized and bounded before it reaches tool schemas,
slash catalogs, or prompts.

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

## Verification and maintenance

The implementation is covered by loader, activation, dispatcher, slash, and
ephemeral-result tests. The tests exercise malformed manifests, duplicate and
unsafe declarations, traversal, symlink/hard-link rejection, size and text
limits, origin replacement, scoped registry behavior, queued and direct turns,
multi-step skill invocation, and persistence scrubbing.

`python3 scripts/test_semgrep_rules.py` validates every report-producing skill's
manifest and local template. It also byte-checks the `secure-change` and
`verify-change` copies against `.mivia/templates/agent-report-v1.md` to catch
template drift. Local copies remain deliberate: resource access is confined to
the selected skill directory rather than allowing a model to read arbitrary
workspace files.

## See also

- [Architecture overview](overview.md)
- [Agent tools and safety](../product/agent.md)
- [Agent workflow](../development/agent-workflow.md)
- [Concurrency model](concurrency.md)
- `.mivia/skills/*/resources.toml` and `.mivia/skills/*/report-template.md`
