# 32 — Explicit, lazy skill resources

**Status:** IMPLEMENTED — 2026-08-01. The implementation followed a fresh
ADLC Step 0 challenge against then-current `HEAD`; see §12.
**Date:** 2026-08-01
**Depends on:** the existing skill discovery, project-over-user precedence, and
workspace file-tool safety boundaries.
**Blocks:** moving required architecture-review fallback material out of
`SKILL.md`.
**Blast radius:** HIGH — model context, user- and project-scope filesystem
access, prompt-injection exposure, direct slash execution, and subagent tools.

---

## 1. Goal

Support explicitly declared, bounded, on-demand **text references** in both
**project** and **user** skill scopes. A skill can point to a named sibling file
without eagerly sending every file in its directory to a model.

The resulting contract must preserve progressive disclosure:

1. discovery exposes only skill metadata;
2. activation exposes `SKILL.md` plus a small resource catalogue; and
3. the model reads a declared text reference only when the task needs it.

This matches the Agent Skills model of an activated `SKILL.md` with supporting
files loaded as needed, rather than directory-wide prompt injection. Sources:
<https://agentskills.io/specification> and
<https://agentskills.io/client-implementation/adding-skills-support>.

## 2. Current gap

`internal/skills/loadSkillDirAt` reads only `SKILL.md`, renders it into
`Definition.Instructions`, and closes the skill directory. Direct slash turns
and multi-step skill handlers receive that instruction string only. They do not
receive a skill-root capability, resource catalogue, or reliable base path for
relative references.

The ordinary `read_file` tool is workspace-relative. It cannot reliably resolve
a user-scope skill resource, and using it with a guessed path would not constrain
access to the resources deliberately declared by a skill.

Therefore a Markdown link alone is insufficient. Moving a required report
template today would make the current skill unreliable, particularly with
`--no-tools` or user-scoped skills.

There are two further lifecycle gaps. A direct TUI slash turn injects the
selected instructions into the root chat loop rather than invoking the skill's
multi-step handler, so a global resource tool would not know the selected
skill/origin. In addition, ordinary tool results enter the agent transcript,
session persistence, and tool-event previews; resource text would leak there
unless the runtime handles it as a distinct ephemeral result class.

## 3. Locked decisions

| Concern | Decision |
|---|---|
| Scope | Support project and user skill resources from the first release. |
| Discovery | Do not read resource contents at skill discovery. |
| Declaration | Use one custom `resources.toml` manifest per skill directory. |
| V1 surface | Support manifest-gated, UTF-8 text references only. Defer scripts, assets, remote resources, globs, recursive discovery, and resource chains. |
| Visibility | For a tool-enabled invocation, expose only a bounded catalogue of declared IDs and summaries. Do not expose directory listings, absolute paths, or resource contents. |
| Access | The host creates one opaque `SkillActivation` per invocation. Its scoped `read_skill_resource` tool accepts only `id`; it has no model-supplied skill, origin, activation, or path selector. |
| Resolution | Bind a resource to the selected skill definition and origin. A project skill overriding a user skill also overrides its resource catalogue. |
| Capability-aware prompts | Tool-enabled activations receive the catalogue and scoped tool. No-tools and one-shot activations omit the catalogue and retain the self-sufficient base workflow. |
| Retention | Resource text is model-visible only for the active turn. Persisted history, UI/event previews, logs, ledger data, and safe errors use ID-only markers and metadata. |
| Compatibility | The manifest is a Mivia-specific access-control addendum. Standard clients may follow `SKILL.md` relative links, but Mivia grants resource access only to manifest entries. |
| Report templates today | Each of the eight report-producing project skills declares a local `report-template` resource. Tool-enabled invocations can load it; each skill retains an inline fallback for paths without a scoped reader. |

## 4. External-harness validation

The Agent Skills specification supports supporting files and progressive
disclosure; its client guidance calls for a base directory, deterministic
project-over-user precedence, and listing resources without eagerly reading
them. Sources: <https://agentskills.io/specification> and
<https://agentskills.io/client-implementation/adding-skills-support>.

Grok Build's public skills guide likewise supports `scripts/` and `references/`
alongside `SKILL.md`, with local/repository/user precedence. Its current source
rewrites safe relative links and offers a whole-directory `SKILL_DIR` variable.
The lean entry point and origin-wide override are useful precedents; exposing the
entire directory is rejected because this plan authorizes some files, not all.
Sources: <https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/08-skills.md>
and <https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-tools/src/implementations/skills/skill.rs>.

Claude Code offers an analogous skill-directory variable, while GitHub Copilot
can make all files in an invoked skill directory available. Both improve
ergonomics, but neither is the desired authorization boundary for Mivia's
user- and project-scope resource model. Sources:
<https://code.claude.com/docs/en/slash-commands> and
<https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-skills>.

## 5. Proposed skill structure

Small, singular resources may live alongside `SKILL.md`; the manifest is the
allowlist. A skill with several readable resources should group them under
`references/` for clarity. Both layouts use the same relative-path rules.

```text
architecture-review/
├── SKILL.md
├── resources.toml
└── report-template.md
```

```toml
# resources.toml
format = 1

[[resources]]
id = "report-template"
path = "report-template.md"
summary = "Required report template for every architecture review."
```

`SKILL.md` names the resource and the condition for using it, for example:

```text
Load the `report-template` resource before every report when a scoped reader is available.
```

The manifest owns the path and metadata. `SKILL.md` owns workflow intent. Do
not duplicate a resource's complete contents into `SKILL.md` once it is truly
optional and the runtime support exists.

## 6. Resource contract

### 5.1 Manifest

`resources.toml` is a closed allowlist. V1 has a required `format = 1` and each
entry has:

- `id`: stable lower-case identifier used by the model-facing resource tool;
- `path`: relative regular-file path under the selected skill directory;
- `summary`: short, sanitized reason for use.

Reject duplicate IDs and paths, unknown keys, empty fields, absolute paths,
`.`/`..` components, and a manifest that exceeds a small count or byte budget.
Reject remote URLs, globs, recursive discovery, manifest/resource chains,
non-text data, and all undeclared files. A missing manifest means that the skill
has zero resources. A malformed manifest leaves its selected `SKILL.md` usable
with zero resources and emits a bounded warning; it never falls back to a
same-named lower-priority skill or its resources.

### 5.2 Activation prompt

For a tool-enabled `SkillActivation`, keep the existing untrusted-skill framing
and add a bounded, structured catalogue after the skill instructions, without
absolute host paths or resource bodies:

```text
<skill-resources>
- id: report-template
  purpose: Generic fallback report structure.
</skill-resources>
```

The catalogue is descriptive, not authority. Resource bodies remain untrusted
workspace content and cannot override system, developer, safety, security, or
tool policy.

### 5.3 Dedicated read capability

The host creates a `SkillActivation` containing one selected definition, its
origin, parsed descriptors, an open root-confined skill-directory handle, a
per-turn quota, and a per-turn cache. That object is private to the invocation.

Introduce an activation-scoped `read_skill_resource` capability rather than
exposing an arbitrary path parameter. The tool accepts only `{"id":"..."}`;
the closure, not model input, supplies the selected definition, origin, root,
and manifest descriptor.

The capability must:

- accept only a resource ID from the selected activation's manifest;
- use a root-confined file open, never string path concatenation;
- return a labelled untrusted-resource text block;
- enforce per-resource and per-turn byte/result budgets; and
- be absent from ordinary root turns, other skills, and unrelated subagents.

Do not expose a whole-directory variable such as `SKILL_DIR`. Future scripts and
assets require separate declaration, execution/copy semantics, permissions, and
security review; neither is automatically read or executed by this feature.

### 5.4 No-tools and direct-slash behavior

Render skill instructions by invocation capability, not as one permanently
expanded string. Tool-enabled direct slash and multi-step invocations receive
the catalogue and the scoped tool. No-tools and one-shot invocations receive
only the base instructions. Do not create a `required` load mode in v1.
Material mandatory for every execution stays inline; material that is large,
conditional, reusable, or variant-specific is eligible for `on-demand`
resources.

For direct slash turns, construct a turn-local cloned registry and matching
dispatcher containing the activation-scoped tool; never mutate the shared
session registry. For multi-step skills, add a per-invocation extra-tool factory
before its restricted registry is constructed. This avoids a hidden dependency
where a skill advertises an unavailable resource tool or one invocation can read
another invocation's resources.

### 6.5 Retention, preview, and cache semantics

Resource text is available to the model only during its active turn. Before
provider history, saved sessions, ledger records, event/audit sinks, tool-end UI
previews, cancellations, and safe errors are persisted or rendered, replace its
body with an ID-only marker such as `skill resource loaded:
report-template`. Preserve whatever paired tool-call structure the
provider transcript requires; never leave an invalid assistant/tool sequence.

The first successful read copies bounded bytes into the activation-local cache,
records a safe digest/size, and returns that exact value for repeated reads. The
cache and aggregate quota are concurrency-safe: duplicate reads coalesce and
distinct reads atomically reserve budget. Close the root and discard cache,
capability, and quota on turn completion, cancellation, dispatcher close, or
model switch. An expired activation fails closed.

## 7. Security and privacy requirements

Workspace and user-scope skills are untrusted task guidance. Resource support
must not turn that guidance into arbitrary file disclosure.

- Use the manifest as a known-good allowlist; never accept raw names or paths
  from the model or user.
- Refuse symbolic links (including path components), directories, devices,
  FIFOs, sockets, binaries, unreadable files, and oversized files. Preserve the
  existing regular-file and opened-file identity checks used for `SKILL.md`.
- Keep the skill-root handle alive for the activation and open references through
  it. Reject an identity change or vanished file rather than following a replaced
  path. Repeated reads use the activation cache, never re-open the resource.
- Where the platform can establish hard-link count, enforce the same no-shared-
  link policy as `SKILL.md`. Do not claim universal hard-link rejection on
  platforms where that fact cannot be established; document and test the
  platform-specific guarantee.
- Do not reveal absolute user-home or host paths in model-facing catalogues,
  logs, errors, fixtures, or UI summaries.
- Bound manifest size, entry count, per-resource bytes, aggregate activation
  catalogue bytes, and per-turn resource-result bytes.
- Preserve origin binding: a project override must never reuse a same-named
  user skill's resource root or manifest.
- Persist and render only stable IDs, digest/size, origin class, and safe outcome
  metadata, never resource content.

Known-good IDs and confined file opens are deliberate protections against path
traversal and file-include errors. See
<https://owasp.org/www-community/attacks/Path_Traversal>.

## 8. Rejected alternatives

| Alternative | Rejection reason |
|---|---|
| Eagerly read all files below a skill directory | Defeats progressive disclosure, creates unbounded context, and expands prompt-injection exposure. |
| Let the model call `read_skill_resource(path)` | A raw path is an unsafe, difficult-to-audit filesystem capability. |
| Reuse ordinary `read_file` with an inferred relative path | Fails for user-scope skills and does not bind access to the selected skill or manifest. |
| Parse arbitrary Markdown links as the allowlist | Ambiguous, hard to validate, and creates a hidden policy language. |
| Expose a `SKILL_DIR`/base-path variable | Allows normal file tools to read undeclared siblings, which violates the explicit-resource boundary. |
| Add `required` resources in v1 | Breaks no-tools/direct slash usability or forces eager injection, negating the token benefit. |
| Move the current short fallback template immediately | It is mandatory fallback behavior; extraction now provides no reliable runtime benefit. |

## 9. Delivery design

### Wave 0 — Rechallenge against current code

Read all skill discovery, definition, registry, slash, subagent, filesystem,
and tool-registration paths. Confirm no existing capability can safely carry a
skill-root-bound resource handle. Re-run architecture, security, and correctness
challenge reviews before implementation.

### Wave 1 — Manifest parsing and definition model

Add a narrow immutable descriptor and parsed manifest to the loaded skill
definition. Keep its root opaque to model-facing serialization; acquire the
root handle only when building an activation. A missing or malformed manifest
cannot change selection precedence or cross-bind a lower-priority skill.

RED/GREEN tests cover valid manifests, unknown fields, duplicate IDs/paths,
invalid identifier/path/summary/format, missing and malformed manifests,
undeclared files, and project-over-user override binding.

### Wave 2 — Activation wiring, capability-aware rendering, and retention

Build a private `SkillActivation` for each tool-enabled invocation. Wire a
turn-local scoped tool into direct slash execution without changing the shared
session registry, and add a per-invocation extra-tool factory before multi-step
restricted-registry construction. Render the resource catalogue only where that
scoped tool exists; no-tools and one-shot paths receive base instructions only.

Add the resource-output retention transform across provider history, session
persistence, ledger/event sinks, errors, and UI previews.

RED/GREEN tests prove catalogue presence without bodies or absolute paths,
bounded output, unavailable-resource absence in no-tools/one-shot paths,
valid paired transcript structure after retention, metadata-only previews, and
unchanged activation for skills without manifests.

### Wave 3 — Root-bound resource read capability

Implement the capability with a selected-definition/resource-ID contract. Reuse
or factor the existing safe regular-file open behavior rather than duplicating
security-sensitive file handling.

RED/GREEN tests cover declared reads, unknown ID rejection, path traversal,
absolute path, symlink/directory/FIFO/binary/oversize rejection, supported-
platform hard-link behaviour, identity replacement, per-resource/per-turn caps,
cancellation, activation expiry, concurrent duplicate reads, concurrent
aggregate-budget reservation, and no content in safe error messages.

### Wave 4 — Integration and portable-skill adoption

Prove direct slash, multi-step subagent, one-shot skill execution, project scope,
user scope, simultaneous same-named activations, and project override execution.
Confirm `--no-tools` remains useful without resource reads.
Then ensure every report-producing skill has a declared local report template.
Tool-enabled invocations can load it before reporting; the essential inline
fallback remains for no-tools and other paths without a scoped reader.

Expand portability and configuration checks from only
`architecture-review/SKILL.md` to every declared readable resource beneath that
skill. Require every manifest path to exist and every readable resource to pass
the same generic-surface review corpus.

### Wave 5 — Audit and release

Run hostile security, portability, and lifecycle audits until no confirmed bugs
remain. Execute focused race tests for affected packages, full verification,
and the committed-path hook checks.

## 10. Acceptance criteria

- A project or user skill can declare and on-demand read exactly one safe,
  named text resource.
- No undeclared sibling file is visible in the activation prompt or readable
  through the resource capability.
- A project override binds both its instructions and its own resources; no
  cross-origin resource read is possible.
- Direct slash and multi-step execution receive the same bounded catalogue.
- The resource reader accepts only a manifest ID within a host-held activation;
  it has no skill/origin/path selector and is unavailable outside that activation.
- A no-tools session can still use every skill's essential workflow.
- One-shot skill execution does not advertise a resource it cannot read.
- Resource contents are never loaded at discovery and never automatically sent
  merely because they exist in a skill directory.
- Repeated reads return the same activation-cached bytes; concurrent reads cannot
  exceed the aggregate budget or cross-bind same-named user/project skills.
- Resource text is absent from saved history, ledger/event output, tool previews,
  logs, safe errors, and fixtures; only ID-safe metadata remains.
- Resource files receive the same portability/config validation appropriate to
  their model-facing role.
- Security tests prove traversal, supported-platform link behaviour, special-file,
  oversized-file, replacement-race, expired-activation, and origin-binding
  resistance.

## 11. Rollback criterion

Reject or roll back the feature if it requires arbitrary model-controlled file
paths, leaks user-scope paths or undeclared content, weakens existing skill-file
safety checks, makes core skills unusable with `--no-tools`, or cannot preserve
project-over-user origin binding. Also reject it if resource text can persist
outside the active turn or a scoped resource capability becomes reachable from
an unrelated root turn, skill, or subagent.

## 12. Implementation record

- The manifest is `resources.toml`, decoded as a closed `format = 1` allowlist.
  Discovery retains descriptors only; it never reads resource bodies.
- Each tool-enabled invocation opens an identity-bound skill root and receives
  a fresh `read_skill_resource` capability accepting only declared IDs. Linux
  uses descriptor-relative `openat` with component link checks and nonblocking
  final opens; other platforms retain the documented best-effort confinement.
- Direct slash turns create their activation only on dequeue, then use a
  turn-local cloned registry and matching dispatcher. Resource-bearing
  multi-step skills use an invocation wrapper that creates the same isolated
  capability before the nested restricted registry is made.
- Resource output remains available only to the active model loop. Runtime
  previews, UI events, persisted session history, and later turns receive the
  ID-only marker. This cannot redact a model's own final prose if it quotes a
  resource.
- All eight report-producing skills now declare a local `report-template`
  resource. Sidecar templates contain the full report structures; each
  `SKILL.md` keeps a concise fallback for paths without a scoped reader.

Verification completed: focused package tests; full `make verify`; and race
tests for skills, tools, runtime, agent, chat, and CLI.
