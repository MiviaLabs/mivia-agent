# Plan: embed-agent-instructions (v3 — locked)
Template-Version: v1

## Goal
Ship generic agent instructions embedded in the binary via `ship/` directory. Host-specific `.ai/` stays on disk for developing mivia itself.

## The Critical Insight
`.ai/agent-prompt.md` says "You are mivia — working on **yourself**". If we embed `.ai/` as-is, a user copying the binary to another project gets instructions saying "edit cmd/mivia/ and internal/". This is WRONG.

**Solution**: Two parallel instruction sets:

| Set | Location | Purpose | Embedded? |
|-----|----------|---------|-----------|
| **Host** (this repo) | `.ai/` on disk | For developing mivia itself | ❌ NO |
| **Shipped** (any project) | `ship//` embedded in binary | For end users running mivia on their projects | ✅ YES |

## Scope
- **In scope**: New `ship/` directory with GENERIC versions of: AGENTS.md, INDEX.md, rules/, doctrines/, skills/. Generator reads from `ship/` not `.ai/`. Auto-write on startup creates `.ai/` from shipped content.
- **Out of scope**: Changing `.ai/` (stays host-specific). Modifying agent loop tools.
- **Boundary**: `ship/`, `agentkitdata/`, `internal/agentkit/`, `cmd/mivia/main.go`.

## File Audit — What Goes in ship/

| Source .ai/ File | Host-Specific? | ship/ Version Needed? |
|---|---|---|
| INDEX.md | PARTIAL (references AGENTS.md) | ✅ Rewrite — generic, no host refs |
| agent-prompt.md | YES (cmd/mivia/, internal/, "working on yourself") | ❌ Do NOT embed |
| invariants.md | YES (mivia-specific tests) | ❌ Do NOT embed |
| rules/00-operating-doctrine.md | PARTIAL (MiviaLabs, cmd/mivia/) | ✅ Rewrite — generic scope control |
| rules/01-output-budget.md | GENERIC | ✅ Copy as-is |
| rules/05-adlc-agentic-development-lifecycle.md | GENERIC (tool refs are shipped) | ✅ Copy as-is |
| rules/10-security-privacy.md | PARTIAL | ✅ Copy (rules are universal) |
| rules/20-agent-quality.md | PARTIAL (Go testing refs) | ✅ Rewrite — language-agnostic |
| rules/30-go-standards.md | YES (Go/interna/cmd specific) | ❌ Do NOT embed |
| rules/40-docs-ownership.md | PARTIAL | ✅ Rewrite — generic SSOT |
| rules/50-concurrency-subagents.md | PARTIAL | ✅ Copy (concept is universal) |
| rules/60-tools-project-language-generic.md | PARTIAL | ✅ Copy (core rule is critical) |
| rules/70-long-running-heartbeat.md | PARTIAL | ✅ Copy (heartbeat concept universal) |
| rules/80-commit-message.md | PARTIAL | ✅ Copy (commit conventions are generic) |
| doctrines/evidence-before-claims.md | GENERIC | ✅ Copy as-is |
| doctrines/verification-is-part-of-delivery.md | GENERIC | ✅ Copy as-is |
| skills/*/SKILL.md | MIXED | ✅ Copy generic ones, skip host-specific |
| plan/ | ALL HOST-SPECIFIC | ❌ Do NOT embed |
| plans/ | ALL HOST-SPECIFIC | ❌ Do NOT embed |
| policy/ | ALL HOST-SPECIFIC | ❌ Do NOT embed |
| quality/ | ALL HOST-SPECIFIC | ❌ Do NOT embed |
| templates/ | PARTIAL | ✅ Copy if generic enough |

## Files to Create
- `ship/AGENTS.md` — Generic agent instructions
- `ship/INDEX.md` — Generic control surface index
- `ship/rules/*.md` — Rewritten generic versions of rules
- `ship/doctrines/*.md` — Copies of generic doctrines
- `ship/skills/*/SKILL.md` — Copies of generic skills

## Files to Modify
- `agentkitdata/gen_embed.go` — Read from `ship/` instead of `.ai/` and `AGENTS.md`
- `internal/agentkit/agentkit.go` — Verify paths use `.ai/` on disk (auto-written from ship)
- `cmd/mivia/main.go` — Wire EnsureInstructions on startup

## Dependency Graph
```
Wave 1: [t1] — Audit: create ship/ directory with generic file copies
Wave 2: [t2] — Rewrite gen_embed.go to read from ship/
Wave 3: [t3] — Regenerate agentkitdata/data.go from ship/
Wave 4: [t4] — Wire EnsureInstructions into main.go
Wave 5: [t5] — Run all tests, fix failures
Wave 6: [t6] — Final verify (go build, go vet, go test -race)
```

## Rollback Criterion
If any embedded file still contains host-specific references (cmd/mivia, internal, "you are working on yourself"), the plan fails until fixed. A test must verify this.
