# 06.01 — Skill metadata

**Goal:** Make the skill tool subset check real by parsing and publishing validated metadata.
**Depends on:** plan `25`'s frontmatter contract.

## Work

- Extend the existing skill parser to accept the documented `tools` metadata;
  reject malformed values and unknown frontmatter keys according to the current
  skill contract.
- Preserve the existing untrusted-content wrapper and source provenance.
- Expose metadata through the existing `skills.Definition`/catalogue seam without
  importing agent policy into `internal/skills`.
- Keep the parser independent of agent files: `skills` owns skill metadata;
  `agents` consumes it.

## Verification

- RED/GREEN tests for valid tools, malformed tools, unknown keys, and source
  provenance.
- `TestSkillToolsParsedAndPublished` proves the metadata is non-empty when the
  fixture declares tools.
- `go test ./internal/skills/...` and `go test -race ./internal/skills/...`.

## Guard

The subset check is not considered implemented until a fixture with declared
skill tools fails when the selected agent omits one of them.
