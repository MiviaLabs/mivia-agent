# Phase 1 Implementation Plan — Discovery, Parsing, Compiler, CLI

**Parent plan:** `.mivia/plans/MIVIA_AGENT_WORKFLOWS_IMPLEMENTATION_PLAN.md`
**Phase:** 1 of 6
**Status:** ~75% complete — CLI `list/show/validate` done, remaining items planned below
**Last updated:** 2025-01

## Phase 0 Gap Fixes (completed this run)

The following Phase 0 security and test gaps were identified and fixed:

1. **Back-edge loop `max_iterations` bypass** — `validateTransitions` now gates on `t.Loop != ""` instead of `t.From == t.To`
2. **`InputDef.MaxBytes` and `ContextBinding.MaxBytes` validation** — rejects negative values and enforces 1 MiB ceiling
3. **6 new tests** — back-edge loop, missing-agent documentation, bad-output-key documentation, path-traversal compiler layer, negative input max_bytes, negative context max_bytes
4. **4 new fixtures** — back-edge-loop-no-max, negative-input-max-bytes, negative-context-max-bytes, path-traversal fixture fix

## Phase 1 Scope (from parent plan)

> Implement safe `.mivia/workflows` discovery modeled on the defensive workspace-file handling in `internal/config/agents.go`: bounded files, regular files only, no path escape/symlink races, normalized names, deterministic ordering.
> Implement strict TOML decode with an explicit closed key set at every level; reject unknown or deprecated fields.
> Resolve and snapshot templates, schemas, agents, verifier profiles and delivery configuration.
> Implement graph checks: single initial state, known references, valid terminals, reachability, no implicit cycles, finite named loops, global bounds, deterministic transition coverage/non-overlap.
> Wire `workflows list/show/validate/explain` into `internal/cli`.
>
> **Exit:** `mivia workflows validate` can reject unsafe/ambiguous workflows and explain a valid compiled one without LLM calls.

## Current State Assessment

### Done ✅

| Item | Location | Tests |
|------|----------|-------|
| Safe `.mivia/workflows` discovery | `definition/discovery.go` | 5 tests (nonexistent, finds, skips non-toml, symlink dir/file rejected) |
| Strict TOML decode with closed keys | `definition/decode.go` | 7 tests (valid, unknown field, empty/reserved IDs, name mismatch, empty name, unsupported version) |
| Graph checks (reachability, terminals, cycles) | `compiler/graph.go` + `compiler/compiler.go` | 6 tests (valid fixture, compiled fields, unreachable, unbounded loop, missing terminal, overlapping) |
| Loop bounds (self-loop + now back-edge) | `compiler/compiler.go` | 1 test (unbounded) + new test (back-edge no max) |
| Context binding validation | `compiler/compiler.go` | 2 tests (bad source, path traversal) |
| CLI `workflows list` | `cli/workflows_command.go` | 2 tests (empty, discovered) |
| CLI `workflows show` | `cli/workflows_command.go` | 2 tests (nonexistent, full pipeline) |
| CLI `workflows validate` | `cli/workflows_command.go` | 4 tests (empty, all valid, by name, invalid reports error) |
| Presentation formatting | `presentation/show.go` + `catalog.go` | 17 tests |
| Template loading + path traversal protection | `template/loader.go` | 7 tests |
| Template reference validation | `template/loader.go` `ValidateReferences` | 4 tests |
| Product doc | `docs/product/workflows.md` | N/A |
| Architecture doc | `docs/architecture/workflows.md` | N/A |
| JSON schemas | `testdata/schemas/*.json` (4 schemas) | N/A |
| Test fixtures | `testdata/valid-feature-delivery.toml` + 15 invalid fixtures | N/A |

### Remaining Work

| # | Item | Priority | Effort | Notes |
|---|------|----------|--------|-------|
| R1 | CLI `workflows explain` subcommand | High | S | New presentation function showing compiled state graph, loop caps, declared authority, delivery policy, resolved references. No secret values. |
| R2 | Delivery config validation in compiler | High | S | Validate `mode` is one of `none/draft/ready`, `provider` is non-empty when mode != `none`, `base` is non-empty when mode != `none`. Reject unknown delivery kinds. |
| R3 | Limits validation in compiler | High | S | Validate `max_step_attempts > 0`, `max_duration_seconds > 0`. Enforce global ceilings (e.g. max_step_attempts ≤ 100, max_duration_seconds ≤ 86400). |
| R4 | Schema reference validation at compile time | Medium | M | Load and validate schema files referenced by `output_schema` fields. Check they exist relative to the workflow file, are valid JSON Schema, have `additionalProperties: false`. The compiler currently doesn't load schemas — only `template.ValidateReferences` does a similar check for templates. |
| R5 | Verifier profile reference validation | Medium | S | Validate that `verifier` fields on `evidence_gate` steps reference a known verifier name. The verifier registry doesn't exist yet — defer to a configurable allowlist or just validate the name format for Phase 1. |
| R6 | Stable definition digest | Medium | S | Compute and store a content digest of the compiled workflow (SHA-256 over canonical JSON of the definition). Needed for immutable run snapshots in Phase 2. The `CompiledWorkflow` struct doesn't have a `Digest` field yet. |
| R7 | On-failure default convention | Low | S | Document that empty `on_failure` defaults to `"failure"` at runtime. Add a test that a step without explicit `on_failure` compiles successfully (already works, just needs documentation). |
| R8 | Overlap detection: subset check | Low | M | `matchCriteriaEqual` only catches identical match criteria, not subset relationships. E.g. `{ status = "succeeded" }` is a superset of `{ status = "succeeded", output = { verdict = "approved" } }`. Decision: document first-match-wins as intentional for Phase 1, defer subset detection to Phase 4 (transition matcher). |

## Implementation Order

### Wave 1: Compiler Hardening (R2, R3, R6)

These are small, focused changes to `compiler/compiler.go` and `compiler/graph.go` that strengthen the compilation contract without changing CLI interfaces.

**R2 — Delivery config validation**
- Add `validateDelivery(wf *definition.WorkflowFile) error` in `compiler/compiler.go`
- Validate `Delivery.Kind` is `"pull_request"` or empty
- When kind is `"pull_request"`: validate `Mode` in `["none", "draft", "ready"]`, `Provider` non-empty, `Base` non-empty
- When mode is `"none"`: skip provider/base checks (no delivery)
- Call from `Compile()` before building the result

**R3 — Limits validation**
- Add `validateLimits(limits definition.Limits) error` in `compiler/compiler.go`
- `max_step_attempts > 0 && max_step_attempts ≤ 100`
- `max_duration_seconds > 0 && max_duration_seconds ≤ 86400`
- Call from `Compile()` before building the result

**R6 — Stable definition digest**
- Add `Digest string` field to `CompiledWorkflow`
- After all validation passes, marshal the workflow to canonical JSON and compute SHA-256
- Use `encoding/json` with stable key ordering (sorted struct fields are already deterministic)

### Wave 2: Reference Validation — Agent Names, Schemas, Verifier Names (R4, R5, R9)

These require resolving file paths relative to the workflow file location, which means `Compile()` needs to know the workflow file's directory.

**R9 — Agent reference validation** *(from reviewer finding F1)*
- Add `ValidateAgentReferences(wf *definition.WorkflowFile, workspaceRoot string) error`
- Discovers agent files in `<workspaceRoot>/.mivia/agents/`
- For each step with non-empty `Agent`, checks that `<agent>.md` or `<agent>.yaml` exists
- No schema validation of the agent file itself (deferred to Phase 4)

**R4 — Schema reference validation**
- New function: `ValidateSchemaReferences(wf *definition.WorkflowFile, baseDir string) error`
- For each step with non-empty `OutputSchema`, resolve relative to `baseDir`
- Read the file, parse as JSON, check it's a valid JSON object with `type` and `properties` keys
- Check `additionalProperties` is `false`
- Record schema path for Phase 2 snapshotting

**R5 — Verifier profile reference validation**
- For Phase 1, validate verifier name format: lowercase alphanumeric + hyphens, non-empty
- The actual verifier registry is Phase 4 concern
- Add to `validateStep()`: check verifier name format when kind is `evidence_gate`

**Pipeline convention** *(from reviewer finding F3)*
- The CLI `show`/`validate`/`explain` handlers call the pipeline in this documented order:
  1. `definition.DiscoverWorkflows(workspaceRoot)` — safe discovery
  2. `definition.ParseWorkflowTOML(raw, filename)` — strict TOML decode
  3. `compiler.Compile(&wf)` — pure semantic validation (graph, transitions, limits, delivery)
  4. `compiler.ValidateAgentReferences(&wf, workspaceRoot)` — filesystem agent check
  5. `compiler.ValidateSchemaReferences(&wf, baseDir)` — filesystem schema check
  6. Presentation formatting
- Steps 4-5 are filesystem operations; steps 1-3 are pure. All errors are collected and reported together.

**R5B — Verifier name format validation (new, Wave 2)**
- Validate verifier name: non-empty, lowercase alphanumeric + hyphens only
- When step kind is `evidence_gate` and verifier is empty → error (required)

### Wave 3: CLI `workflows explain` (R1)

**Presentation function: `FormatWorkflowExplain`**
- Shows the compiled state graph as a text diagram (steps → transitions → targets)
- Lists all loop names and their max_iterations
- Shows delivery policy (mode, provider, base — no secrets)
- Shows resolved references: templates (name only), schemas (path only, no digest), agents (name only)
- Lists declared authority: which agents are referenced by agent/agent_gate steps
- NOTE: No transition coverage analysis (Phase 4 matcher territory). No schema digests (Phase 2 format not finalized).

**CLI handler: `runWorkflowsExplain`**
- New subcommand: `mivia workflows explain <name> [--workspace dir]`
- Discovers → parses → compiles → resolves references → formats
- Must update the error message and usage line in `root.go`

**Tests:**
- Test for valid workflow explain output
- Test for nonexistent workflow name
- Test for invalid workflow explain (shows compile error)
- Test output contains expected sections (graph, loops, delivery, references)

## Design Decisions

### D1: `Compile()` stays pure (no filesystem)
`Compile()` takes only `*definition.WorkflowFile` and performs semantic validation. Filesystem operations (template loading, schema loading) are separate functions called before or after compile. This keeps unit tests fast and filesystem-independent.

### D2: First-match-wins for overlapping transitions
The compiler detects **identical** match criteria as an error. Subset relationships (e.g. a bare status match that subsumes a more specific status+output match) are **not** detected in Phase 1. This is documented as intentional: transitions are evaluated in declaration order, first match wins. Subset detection is deferred to Phase 4 (matcher implementation).

### D3: Verifier names validated by format only
Phase 1 validates verifier names are non-empty and match the naming convention. The actual verifier registry (checking that `"go-default"` exists and is runnable) is a Phase 4 concern. This matches the agent name handling pattern (agent names are not validated against a known set in Phase 1 either).

### D4: Schema validation is structural, not full JSON Schema
Phase 1 checks that schema files exist, are valid JSON, have basic JSON Schema properties (`$schema`, `type`), and use `additionalProperties: false`. Full JSON Schema compilation (using `github.com/santhosh-tekuri/jsonschema/v6` already in `go.mod`) is deferred to Phase 2 when schemas need to validate actual step outputs.

### D5: Digest uses canonical WorkflowFile JSON + SHA-256
The definition digest is computed from a deterministic JSON serialization of the validated `WorkflowFile` struct (the source definition, NOT the derived `CompiledWorkflow`). Go's `encoding/json` marshals struct fields in declaration order, which is deterministic. The digest is hex-encoded SHA-256. This digest will be used by Phase 2 to detect TOML mutation between compile time and snapshot creation.

## Exit Criteria

Phase 1 is complete when:

- [x] `mivia workflows list` shows discovered workflows
- [x] `mivia workflows show <name>` shows compiled workflow details
- [x] `mivia workflows validate [name]` rejects unsafe/ambiguous workflows
- [ ] `mivia workflows explain <name>` shows state graph, loop caps, delivery policy, resolved references
- [x] Discovery rejects symlinks, oversized files, path traversals
- [x] Parser rejects unknown fields, invalid names, bad structure
- [ ] Compiler validates delivery config, limits, and schema/verifier references
- [ ] Compiler computes a stable definition digest
- [x] All graph checks: reachability, terminals, cycles, loop bounds, transition overlap
- [x] Tests cover all invalid fixtures
- [ ] `go test ./internal/workflows/... -race` passes with all new tests
- [ ] `go test ./... -race` passes (no regressions)
- [ ] Minimum 55 tests across `internal/workflows/...` (current ~40 + ~15 new)

## Files to Create/Modify

| File | Action | Wave |
|------|--------|------|
| `internal/workflows/compiler/compiler.go` | Modify: add delivery validation, limits validation, digest computation | 1 |
| `internal/workflows/compiler/compiler_test.go` | Modify: add tests for delivery, limits, digest | 1 |
| `internal/workflows/compiler/references.go` | Create: schema reference validation function | 2 |
| `internal/workflows/compiler/references_test.go` | Create: tests for schema validation | 2 |
| `internal/workflows/compiler/verifier.go` | Create: verifier name format validation | 2 |
| `internal/workflows/testdata/invalid/bad-delivery-mode.toml` | Create: invalid delivery fixture | 1 |
| `internal/workflows/testdata/invalid/bad-delivery-no-base.toml` | Create: invalid delivery fixture | 1 |
| `internal/workflows/testdata/invalid/bad-limits.toml` | Create: invalid limits fixture | 1 |
| `internal/workflows/testdata/invalid/bad-verifier-name.toml` | Create: invalid verifier name fixture | 2 |
| `internal/workflows/testdata/invalid/missing-schema.toml` | Create: references nonexistent schema | 2 |
| `internal/workflows/testdata/invalid/bad-schema-json.toml` | Create: references non-JSON file as schema | 2 |
| `internal/workflows/presentation/explain.go` | Create: `FormatWorkflowExplain` function | 3 |
| `internal/workflows/presentation/explain_test.go` | Create: tests for explain formatting | 3 |
| `internal/cli/workflows_command.go` | Modify: add `explain` subcommand | 3 |
| `internal/cli/workflows_command_test.go` | Modify: add tests for explain | 3 |
| `internal/cli/root.go` | Modify: update usage line to include `explain` | 3 |
