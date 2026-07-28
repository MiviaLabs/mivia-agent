# Round: ADLC — Tool Reference and Discoverability

## Finding 1 — dispatch_tasks `handler` parameter placement inconsistency

- **Severity**: MEDIUM
- **File**: `.ai/rules/05-adlc-agentic-development-lifecycle.md`
- **Status**: confirmed
- **Evidence**:

The actual `dispatch_tasks` tool schema (in `internal/cli/dispatch.go`) defines `handler` as a **per-task** parameter — it lives inside each task object in the `tasks` array:

```go
"properties": map[string]any{
    "id":          ...,
    "prompt":      ...,
    "depends_on":  ...,
    "handler":     ...,   // ← inside the task object
    "timeout_seconds": ...,
},
```

The top-level parameters are only: `tasks`, `timeout_seconds`, `partial_results`. The `Execute()` method unmarshals `handler` from each task item (`pt.Handler`), not from the outer params struct.

The **Tool Reference table** in the ADLC rule correctly shows `handler` per-task:

```
dispatch_tasks({tasks: [{id:"c1", prompt:"...", handler: "multi_step", ...}], partial_results: true})
```

But the **inline actions** in Step 0 and Step 5 place `handler` at the outer call level:

**Step 0 action 5:**
```
- Use `dispatch_tasks({tasks: [...], handler: "multi_step", partial_results: true})`.
```

**Step 5 action 1:**
```
1. Dispatch 3-4 hostile auditors via `dispatch_tasks({handler: "multi_step", partial_results: true})`.
```

- **Impact**: Instructions are syntactically incorrect. An agent copying the inline form verbatim would pass `handler` at the wrong level. The tool silently ignores unknown top-level fields and defaults to `multi_step` (via `buildTasks`), so the call still works — but `handler` at the outer level is not schema-compliant. This is a documentation defect, not a runtime crash.

- **Required fix**: Change the inline actions to match the Tool Reference table: move `handler: "multi_step"` inside each task object.

---

## Finding 2 — Step 0 action 1 still uses `mkdir -p` (not replaced with write_file)

- **Severity**: MEDIUM
- **File**: `.ai/rules/05-adlc-agentic-development-lifecycle.md`, Step 0 action 1
- **Status**: confirmed
- **Evidence**:

Step 0 action 1 currently reads:

```
1. `mkdir -p .ai/plan/<name>/evidence .ai/plan/<name>/audit`
```

This is a raw shell command. It was intended to be replaced with `write_file` (the filesystem tool described in the Tool Reference). The replacement was **not done**.

The ADLC's own Tool Reference header states: *"Use the tool, not manual operations. Do not create files by hand when a tool exists."* Yet the ADLC itself violates this principle in its own Step 0 action 1.

- **Impact**: An agent following ADLC literally runs `mkdir -p` via `run_command` (or shell), which contradicts the tool-first philosophy. `mkdir -p` also silently succeeds if dirs exist, making error handling opaque.

- **Required fix**: Replace with either:
  - `write_file` calls for a `.gitkeep` or `.placeholder` in each directory
  - Or a note that `os.MkdirAll`-equivalent tooling will be used (but `write_file` creates parent dirs automatically in most implementations)

---

## Finding 3 — AGENTS.md placement in fresh-start scenarios

- **Severity**: NONE (not a bug)
- **File**: `agentkitdata/gen_embed.go` + `internal/agentkit/agentkit.go`
- **Status**: rejected — code is correct
- **Evidence**:

The user's concern: "EnsureInstructions writes to dir/.ai/... so AGENTS.md would go to dir/.ai/AGENTS.md, not dir/AGENTS.md."

Tracing the actual code path:

1. **`gen_embed.go`** maps `ship/AGENTS.md` to disk path `"AGENTS.md"` (root), NOT `".ai/AGENTS.md"`:
   ```go
   diskPath = strings.Replace(path, "ship/", ".ai/", 1)
   if diskPath == ".ai/AGENTS.md" {
       diskPath = "AGENTS.md" // AGENTS.md stays at repo root
   }
   ```

2. **`data.go`** (generated) confirms: `RegisterFile("AGENTS.md", ...)` — key is `"AGENTS.md"`, not `".ai/AGENTS.md"`.

3. **`WriteInstructions(dir)`** writes to `filepath.Join(dir, path)` where path is the registered key:
   - `"AGENTS.md"` → `dir/AGENTS.md` ✓ (workspace root)
   - `".ai/INDEX.md"` → `dir/.ai/INDEX.md` ✓
   - `".ai/rules/05-adlc..."` → `dir/.ai/rules/05-adlc..."` ✓

4. **`HasLocalOverride(dir)`** checks `dir/.ai/` — only blocks writing if `.ai/` exists with .md files. This is correct: a project with its own `.ai/` overrides won't have shipped files written over it.

**Conclusion**: The mapping is correct. `AGENTS.md` is written to the workspace root, not to `.ai/`. No bug.

---

## Finding 4 — Read Order chain integrity

- **Severity**: LOW (shipped version has minor implicit-reference gap)
- **Files**: `AGENTS.md`, `.ai/INDEX.md`, `.ai/rules/05-adlc-agentic-development-lifecycle.md`
- **Status**: confirmed (minor gap in shipped chain)

**Host-specific chain (this repo — working on mivia itself):**

| Step | File | References | Verdict |
|------|------|-----------|---------|
| 1 | `AGENTS.md` (root) | `"Read and follow .ai/rules/05-adlc..."` + `".ai/INDEX.md — control-surface index"` | ✓ Explicit |
| 2 | `.ai/INDEX.md` | Read Order: `AGENTS.md → INDEX.md → ADLC rule` | ✓ Explicit |
| 3 | ADLC rule | `"See also AGENTS.md and .ai/INDEX.md"` | ✓ Explicit |

No broken links. ✓

**Shipped chain (fresh-start project — shipped AGENTS.md + shipped INDEX.md):**

| Step | File | References | Verdict |
|------|------|-----------|---------|
| 1 | `AGENTS.md` (shipped, at root) | `"Read .ai/rules/05-adlc..."` + Canonical surfaces: `.ai/INDEX.md` | ⚠️ Implicit — INDEX.md is listed as a "canonical surface" but the AGENTS.md doesn't say "read INDEX.md next" explicitly |
| 2 | `.ai/INDEX.md` (shipped) | Read Order: starts with `.ai/INDEX.md` (itself), then ADLC rule | ⚠️ Doesn't mention AGENTS.md as step 1 — because in shipped context, both are fresh-written simultaneously |
| 3 | ADLC rule | `"See also AGENTS.md"` | ✓ |

**Gap**: In the shipped chain, `AGENTS.md` says "read the ADLC rule" directly but doesn't explicitly say "read `.ai/INDEX.md` first". The INDEX.md is listed only as a "canonical surface" without instruction ordering. This is functional (an agent reading AGENTS.md will discover INDEX.md as a canonical surface) but could be more explicit.

- **Impact**: Low — agents following the chain will still reach the ADLC rule through either path (direct from AGENTS.md, or via INDEX.md). No agent would miss the ADLC rule.

- **Required fix**: Optionally make the shipped AGENTS.md say: *"Read `.ai/INDEX.md` next, then follow its Read Order to the ADLC rule."* Currently it just lists INDEX.md as a canonical surface.

---

## Finding 5 — agent-prompt.md excluded from shipped binary

- **Severity**: NONE (by design, acceptable)
- **File**: `agentkitdata/gen_embed.go` (skip list includes `.ai/agent-prompt`)
- **Status**: rejected — acceptable by design
- **Evidence**:

`gen_embed.go` intentionally excludes `.ai/agent-prompt.md` from embedding:

```go
skipPrefixes := []string{".ai/agent-prompt", ...}
```

The user asks: "in a generic project, the agent doesn't get agent-prompt.md at all. Is that acceptable?"

**Analysis:**

1. `agent-prompt.md` is host-specific: it orients an agent that is *developing mivia itself* (the meta: "you are mivia — working on yourself"). This context is irrelevant to a generic user project.

2. The ADLC mandate is **already shipped** via two other files:
   - **Shipped `AGENTS.md`** has an explicit "ADLC — Mandatory Process" section saying *"Read and follow .ai/rules/05-adlc..."*
   - **Shipped `.ai/INDEX.md`** lists the ADLC rule as the first mandatory item in its read order

3. The shipped `agent-prompt.md`-less experience is:
   - Agent reads `AGENTS.md` → sees ADLC mandate → reads INDEX.md → sees ADLC as mandatory → reads ADLC rule → follows the 7-step protocol

4. `agent-prompt.md` also references long-running orchestration and tool discipline that are product documentation, not ADLC-specific requirements.

**Conclusion**: Acceptable. The ADLC mandate is fully conveyed through shipped AGENTS.md and INDEX.md. agent-prompt.md is correctly excluded as a host-only file.

---

## Disposition Log

| # | Source | Finding | Severity | Verdict | Rationale |
|---|--------|---------|----------|---------|-----------|
| 1 | ADLC Step 0/5 inline actions | `handler` parameter at wrong level (outer vs per-task) | MEDIUM | confirmed | Tool schema places `handler` inside each task object; inline actions place it at outer call level |
| 2 | ADLC Step 0 action 1 | `mkdir -p` still used, not replaced with `write_file` | MEDIUM | confirmed | Was intended for replacement; still uses shell command violating own tool-first principle |
| 3 | AGENTS.md placement | Concern about AGENTS.md going to `.ai/` instead of root | NONE | rejected | `gen_embed.go` correctly maps `ship/AGENTS.md` → `"AGENTS.md"` → written to workspace root |
| 4 | Read Order chain | Shipped chain has implicit (not explicit) INDEX.md reference from AGENTS.md | LOW | confirmed | Functional but could be more explicit in the shipped AGENTS.md |
| 5 | agent-prompt.md excluded | Generic project doesn't get agent-prompt.md | NONE | rejected | By design — agent-prompt.md is host-specific; ADLC mandate shipped via AGENTS.md + INDEX.md |
