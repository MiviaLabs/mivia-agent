# 09.02 — Parser-backed examples

**Goal:** Provide separate examples for global settings and individual agent files, all validated by the real loader.
**Depends on:** [01](01-product-and-security-docs.md) and plan `05`.

## Examples

```text
mivia.toml.example                 # global settings and agent gate only
agents/researcher.toml             # one named agent definition
agents/engineer.toml               # one named agent definition
agents/reviewer.toml               # one named agent definition
```

Do not put definitions in `mivia.toml`, do not ship `[[agents.roles]]`, and do
not show a `test-runner` with only `run_command`. Every example must pass the
real TOML parser, filename/name identity checks, trust checks, secret scan, and
tool catalogue validation.

## Verification

Add parser tests for valid examples, unknown fields, filename mismatch,
malformed tools/skills, explicit empty lists, and gated workspace examples.
