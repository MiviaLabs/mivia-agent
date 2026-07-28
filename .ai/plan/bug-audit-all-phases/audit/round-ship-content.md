# Bug Audit — Shipped-Content Host-Specific References

Auditor: hostile sub-agent  
Scope: `ship/`, `.ai/rules/05-adlc-*`, `.ai/rules/80-commit-message.md`, `.ai/rules/40-docs-ownership.md`, `.ai/skills/*/SKILL.md`, `.ai/policy/*.json`, `.ai/templates/agent-report-v1.md`  
Date: audit pass  

---

## Finding 1. HIGH: `ship/INDEX.md` references host-only ADLC rule that does not exist in shipped context

**File**: `ship/INDEX.md:14`  
**Evidence**: Line reads: *"`.ai/rules/05-adlc-agentic-development-lifecycle.md` — MANDATORY process. Read before any work."*  
**Problem**: The shipped INDEX tells users to read `.ai/rules/05-adlc-agentic-development-lifecycle.md`. That file is host-specific and lives only in the mivia source repo. When shipped content is written to a user project, **that file does not exist** — creating a broken reference.  
**Severity**: HIGH — shipped instructions mandate reading a non-existent file, which causes agent confusion.  
**Fix**: Either (a) create a `ship/rules/05-adlc-shipped.md` with a generic ADLC that has no Go/MiviaLabs references, or (b) remove the mandatory-ref line from `ship/INDEX.md` and add a note that ADLC applies to host development only.

---

## Finding 2. MEDIUM: `.ai/rules/05-adlc-agentic-development-lifecycle.md` — host-specific, cannot ship as-is

**File**: `.ai/rules/05-adlc-agentic-development-lifecycle.md`  
**Status**: This file lives in `.ai/rules/` (host space). It is NOT in `ship/`. That is correct — it should stay host-only **unless** a generic version is needed for users.

**Host-specific content found**:

| What | Example | Fix-for-ship |
|------|---------|-------------|
| Go-specific commands | `go build`, `go test -race`, `go vet` | Replace with generic `build`, `test` |
| Go-specific packages | `internal/cli/`, `internal/tools/`, `internal/agent/`, `internal/chat/`, `internal/config/`, `internal/ledger/`, `internal/coordinator/`, `internal/events/`, `internal/storage/` | Replace with generic `internal/*` or remove |
| Go module path | `github.com/MiviaLabs/mivia-agent` | Remove |
| Mivia-specific tools | `dispatch_tasks`, `spawn_agent`, `delegate`, `cancel_run`, `inspect_agents`, `join_run` | Keep — these are the tool names mivia exposes. They are the API surface. |
| Mivia report template refs | `mivia-report/v1` | Keep or genericise to `agent-report/v1` |

**Verdict**: The `internal/` package names are **not** generic — they are the specific Go package layout of the mivia product. A project using Python or Node.js would not have `internal/cli/`. **A shipped ADLC must be language-agnostic.**

**Recommendation**: If shipping the ADLC to users, create `ship/rules/05-adlc-shipped.md` stripping all Go-specific references and MiviaLabs branding. Keep only: the 7-step cycle, templates (with generic verification commands like `build`/`test`), and tool references.

---

## Finding 3. MEDIUM: `.ai/rules/80-commit-message.md` — references host-specific `.ai/policy/commit-message.json`

**File**: `.ai/rules/80-commit-message.md`  
**Evidence**: Line 4: *"Always validate before committing. Do not guess the format — use the dry-run check."* and lines 7-8: *"Rules (from `.ai/policy/commit-message.json`)"*  

**Problem**: The commit message rule references `.ai/policy/commit-message.json`, which contains:

```json
{
  "brand": "MiviaLabs",
  "binaryName": "mivia",
  "scopes": ["cli", "agent", "mcp", "hooks", "ai", "docs", ...],
  "scopeGuide": {
    "cli": "cmd/mivia, flags, TUI, user-facing CLI behavior",
    ...
  }
}
```

This is entirely host-specific (mivia product scopes, MiviaLabs brand). A shipped version must be generic.

**Also**: The rule also references a host-specific validation script:
```bash
python3 scripts/git-hooks/check-commit-subject "feat(cli): your subject under 72 chars"
```
This script does not exist in a generic user project.

**Fix**: Create `ship/rules/80-commit-message.md` with:
- Conventional commit format (standard, no host-specific scopes)
- A generic validation command (e.g., `echo "<subject>" | grep ...`)
- No reference to `.ai/policy/commit-message.json`  
- No reference to `scripts/git-hooks/`

---

## Finding 4. MEDIUM: `.ai/rules/40-docs-ownership.md` — references host-specific `docs/OWNERS.yaml`

**File**: `.ai/rules/40-docs-ownership.md`  
**Evidence**: Multiple references:
- Line 6: *"Which path owns a topic — `docs/OWNERS.yaml`"*
- Line 7: *"Machine enforcement rules — `.ai/policy/docs-ownership.json`"*
- Line 59: *"Owner field must be a real team/role or CODEOWNERS-compatible identity used by MiviaLabs for this product."*

**Problem**: `docs/OWNERS.yaml` is a host-specific registry that does not exist in generic projects. The policy `.ai/policy/docs-ownership.json` contains MiviaLabs brand references. The docs tree layout (`docs/architecture/`, `docs/development/`, etc.) is host-specific.

**Fix**: Create `ship/rules/40-docs-ownership.md` with:
- Generic doc-ownership principles (one topic → one canonical doc)
- No reference to specific `docs/OWNERS.yaml` or `.ai/policy/docs-ownership.json`
- No MiviaLabs reference
- Generic docs-tree guidance (e.g., "use the project's existing docs structure")

---

## Finding 5. LOW: `ship/` directory itself is clean of brand/host references

**Files audited**:
- `ship/AGENTS.md` — ✅ Clean. Only describes "mivia binary" as context.
- `ship/INDEX.md` — ⚠️ See Finding 1 (broken ref to host ADLC). Otherwise clean.
- `ship/rules/00-operating-doctrine.md` — ✅ Clean. Generic principles.
- `ship/rules/01-output-budget.md` — ✅ Clean. Generic output formatting.
- `ship/rules/10-security-privacy.md` — ✅ Clean. Generic security rules.

**No host-specific references found** in `ship/`:
- No `cmd/mivia/` references → ✅
- No `internal/` references → ✅  
- No `MiviaLabs` brand → ✅
- No `github.com/MiviaLabs/` → ✅
- No `go test` / `go build` commands → ✅
- No "this repo" / "working on yourself" language → ✅

**Verdict**: The `ship/` directory content is already generic except for the INDEX pointing to a host-only file. This is a good foundation. Only Finding 1 needs fixing in `ship/`.

---

## Finding 6. INFO: MiviaLabs brand references in `.ai/` (host space) are correct — but do not ship them

Brand reference summary in `.ai/` (host space — NOT shipped):

| File | Contains MiviaLabs/mivia? | Shipped? |
|------|--------------------------|----------|
| `.ai/rules/00-operating-doctrine.md` | Yes (brand, binary, module path) | No (host only) |
| `.ai/rules/10-security-privacy.md` | Yes (brand) | No (host only) |
| `.ai/rules/30-go-standards.md` | Yes (brand, cmd/mivia) | No (host only) |
| `.ai/rules/40-docs-ownership.md` | Yes (brand ref) | No (host only) |
| `.ai/rules/60-tools-project-language-generic.md` | Yes (cmd/mivia, internal) | No (host only) |
| `.ai/policy/commit-message.json` | Yes (brand, binaryName) | No (host only) |
| `.ai/policy/docs-ownership.json` | Yes (brand, product) | No (host only) |
| `.ai/policy/agent-hook-bypass.json` | Yes (brand, product) | No (host only) |
| `.ai/templates/agent-report-v1.md` | Yes (MiviaLabs, mivia) | No (host only) |
| `.ai/quality/contracts/project-runtime.yaml` | Yes (brand) | No (host only) |
| `.ai/agent-prompt.md` | Yes (brand, binary) | No (host only) |

**Verdict**: These are all in `.ai/` (host space). They are correctly host-specific. **Do not ship any of these files as-is.** If generic versions are needed, strip all MiviaLabs/mivia references.

---

## Finding 7. MEDIUM: `.ai/skills/` — 6 of 8 skill files contain host-specific references

| Skill | Host-specific content | Severity |
|-------|----------------------|----------|
| `bug-audit/SKILL.md` | ✅ Clean (generic bug audit) | None |
| `concurrency-review/SKILL.md` | ⚠️ References "mivia" binary, `mivia-report/v1` | MEDIUM |
| `docs-update/SKILL.md` | ⚠️ References "MiviaLabs brand", "binary name mivia", `mivia-report/v1` | MEDIUM |
| `engineering-working-contract/SKILL.md` | ⚠️ Has "mivia host vs tool surface" section with Go references | MEDIUM |
| `feature-delivery/SKILL.md` | ⚠️ References "mivia feature", "Brand MiviaLabs; binary mivia", `mivia-report/v1` | MEDIUM |
| `secure-change/SKILL.md` | ⚠️ References "mivia", "Brand is MiviaLabs. CLI is `mivia`.", `mivia-report/v1` | MEDIUM |
| `verify-change/SKILL.md` | ⚠️ References "mivia", "Binary under test is `mivia`", `mivia-report/v1` | MEDIUM |
| `verify-code-change/SKILL.md` | ✅ Clean (generic verification) | None |

**Verdict**: These are in `.ai/skills/` (host space), so branding is expected. **However, if any skill is intended to be shipped to users**, it must be sanitized. The `mivia-report/v1` template reference in 6 skills would break in a user project that doesn't have that template.

**Fix**: Either (a) keep skills as host-only (they reference `mivia-report/v1` which is host-only), or (b) create `ship/skills/` with generic versions that use a standard report format.

---

## Finding 8. INFO: Template `agent-report-v1.md` is entirely host-specific

**File**: `.ai/templates/agent-report-v1.md`  
**Content**: Contains `MiviaLabs`, `mivia`, `mivia-report/v1` throughout. The report format is specifically tied to the mivia product.  
**Verdict**: This template cannot be shipped without being genericised. A shipped version would need to remove brand references and rename the report format to something generic (e.g., `agent-report/v1`).

---

## Summary Table

| # | Finding | Severity | Location | Status |
|---|---------|----------|----------|--------|
| 1 | `ship/INDEX.md` references host-only ADLC rule that doesn't exist in shipped projects | **HIGH** | `ship/INDEX.md:14` | Open |
| 2 | `.ai/rules/05-adlc` is host-specific (Go, internal packages, MiviaLabs) — cannot ship | MEDIUM | `.ai/rules/05-adlc-*` | Open |
| 3 | `.ai/rules/80-commit-message.md` references host-specific policy + validation script | MEDIUM | `.ai/rules/80-commit-message.md` | Open |
| 4 | `.ai/rules/40-docs-ownership.md` references host-specific OWNERS.yaml + MiviaLabs | MEDIUM | `.ai/rules/40-docs-ownership.md` | Open |
| 5 | `ship/` dir content is clean (no brand/host refs) except Finding 1 | LOW | `ship/*` | ✅ Clean |
| 6 | `.ai/` host files correctly contain MiviaLabs brand — do not ship | INFO | `.ai/rules/*`, `.ai/policy/*` | ✅ Correct |
| 7 | 6/8 skill files have host-specific refs (mivia, MiviaLabs, mivia-report/v1) | MEDIUM | `.ai/skills/*/SKILL.md` | Open |
| 8 | Template `agent-report-v1.md` is entirely host-specific | INFO | `.ai/templates/agent-report-v1.md` | Open |

---

## Recommendations (priority order)

1. **Fix ship/INDEX.md** (HIGH) — Replace the broken `.ai/rules/05-adlc-*` reference with a note that ADLC applies to the project's own development lifecycle if the project defines one, or remove the mandatory-read requirement for shipped context.

2. **Audit all .ai/ files before any shipping pipeline** — The `.ai/` directory is host-specific. If a mechanism copies `.ai/` content to user projects, every file must be sanitized. The current `ship/` approach (manual curated copies) is correct. Do NOT add `.ai/rules/*` to the ship set without genericising them.

3. **Create ship/ versions for universally useful rules** — If ADLC, commit-message format, or doc-ownership are useful to end users, create dedicated `ship/rules/` versions that strip:
   - Go-specific commands (`go test` → `test`)
   - MiviaLabs brand/binary references
   - Host-specific package paths (`internal/*`, `cmd/mivia`)
   - Host-specific policy file references (`.ai/policy/*.json`)
   - Host-specific script paths (`scripts/git-hooks/`)

4. **Sanitize skill files for shipping** — 6/8 skill files contain host-specific references. Either keep skills as host-only (they depend on `mivia-report/v1` template), or create generic `ship/skills/` versions.

5. **Create a generic report template** — `agent-report/v1` (without the MiviaLabs brand) if shipping skills to users.
