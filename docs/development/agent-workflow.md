# Agent Workflow

How coding agents must work in this repository.

## Read first

1. `AGENTS.md`
2. `.mivia/INDEX.md`
3. `.mivia/doctrines/*`
4. Relevant rules and skills

## Standing doctrine

Always apply `.mivia/doctrines/engineering-working-contract.md`.

## Task skills

- After code changes, apply `verify-code-change`
- For defect hunts, use `bug-audit` (confirmed bugs only)
- For docs, use `docs-update` and `docs/OWNERS.yaml`

The host skill lifecycle, including `resources.toml`, lazy activation, and the
scoped `read_skill_resource` capability, is documented in
[Skill System Architecture](../architecture/skills.md).

## Do

- Smallest change that satisfies the requirement
- Run real checks; never invent pass results
- Update owned docs only
- Keep binary name `mivia`

## Do not

- Bypass hooks
- Create duplicate documentation
- Process-farm subagents by default
- Leave TODO/FIXME/HACK/XXX in committed product or agent config
- Ship any CLI name other than `mivia`

## Completion shape

- Outcome
- Changed files
- Verification (commands + results)
- Risks or blockers

## Skill frontmatter

Workspace skills (`.mivia/skills/*/SKILL.md`) use a strict YAML subset for
frontmatter between `---` delimiters. The parser lives in
`internal/skills/frontmatter.go` and supports:

- `key: scalar` (quotes optional)
- `key: [a, b, c]` (flow sequence)
- `key:` followed by indented `- item` lines (block sequence)
- `#` comments and blank lines (skipped)

**Recognised keys:** `name`, `description`, `triggers`, `user-invocable`,
`argument-hint`, `short-description`, `tools` (optional list of required tool
names for agent skill binding).

**Rejected with a line-numbered error:**

- Nested maps, `>`/`|` block scalars, anchors, multi-document YAML
- Unknown keys (the recognised set is listed in the error)

The cap is 256 KiB, mirroring the maximum skill file size.

## Agent skills allowlist

File-backed agents may set `skills = ["…"]` in `.mivia/agents/<name>.toml`.
That is an **invocation allowlist**, not a preload. See
[Skill System Architecture](../architecture/skills.md#agent-skill-binding) and
this repo’s `.mivia/agents/go-engineer.toml` for a worked example.
