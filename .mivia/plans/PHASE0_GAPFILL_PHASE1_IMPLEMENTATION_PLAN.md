# Mivia Agent Workflows — Phase 0 Gap-Fill + Phase 1 Implementation Plan

**Status:** LOCKED after ADLC Step 0 challenge (3 reviewers, 12 findings dispositioned)
**Date:** 2025-01-29
**Scope:** Phase 0 gap-fill + Phase 1 (discovery, strict parsing, compilation, CLI)

---

## Phase 0 Gap-Fill (Wave 0)

The following gaps were identified by the architecture review and hostile audit.
All must be resolved before Phase 1 Wave 2 begins.

### Gap G1: Missing invalid fixtures

Add 8 invalid fixtures to `internal/workflows/testdata/invalid/`:

| File | Tests | Compiler Wave |
|------|-------|--------------|
| `empty-step-id.toml` | Step ID is empty string | Wave 2 |
| `reserved-step-id.toml` | Step ID is "success" or "failure" | Wave 2 |
| `missing-terminal-path.toml` | No transition leads to success/failure | Wave 2 |
| `missing-agent.toml` | Step references agent name not in known set | Wave 2 |
| `overlapping-transitions.toml` | Two transitions from same step with identical match criteria | Wave 2 |
| `bad-context-source.toml` | Context `from` references nonexistent step | Wave 3 |
| `bad-context-path-traversal.toml` | Context `from` uses path traversal | Wave 3 |
| `bad-transition-output-key.toml` | Transition output key not in step's output_schema | Wave 2 |

### Gap G2: Valid fixture has `base = "master"`

Fix `internal/workflows/testdata/valid-feature-delivery.toml`:
- Change `base = "master"` → `base = "main"`

### Gap G3: `on_failure` missing from architecture doc

Add `on_failure` to `docs/architecture/workflows.md` contract section:
- `on_failure` is an optional per-step field; when omitted, defaults to "failure"
- Must reference an existing step ID (usually "failure")
- Compiler rejects non-existent on_failure targets

### Gap G4: Scope `explain` out of Phase 1

Phase 1 CLI ships `list`, `show`, and `validate` only.
`explain` is deferred to a post-Phase-1 increment.

### Gap G5: No controller/matcher stubs

Omit `controller/` and `matcher/` packages entirely from Phase 1.
If transition overlap types are needed, define them in `compiler/`.

---

## Phase 1 Package Structure

```
internal/workflows/
  definition/
    types.go         # WorkflowFile, Step, Transition, etc.
    decode.go        # ParseWorkflowTOML(data []byte) (WorkflowFile, error)
    discovery.go     # DiscoverWorkflows(workspaceRoot string) ([]DiscoveredWorkflow, error)
    discovery_test.go
    decode_test.go   # Uses testdata fixtures
  compiler/
    compiler.go      # Compile(def definition.WorkflowFile) (CompiledWorkflow, error)
    graph.go         # Graph checks: reachability, terminal paths, loop bounds
    graph_test.go
    transition.go    # Transition overlap check
    transition_test.go
    compiler_test.go # Integration: compile valid fixture, reject all invalid fixtures
  template/
    loader.go        # LoadTemplates(dir string) (map[string]string, error)
    loader_test.go
  presentation/
    catalog.go       # Catalog of discovered workflows
    show.go          # ShowWorkflow compiled workflow as human-readable
    catalog_test.go
    show_test.go
  testdata/
    valid-feature-delivery.toml
    invalid/
      unknown-field.toml
      unbounded-loop.toml
      unreachable-step.toml
      empty-step-id.toml      (NEW)
      reserved-step-id.toml   (NEW)
      missing-terminal-path.toml (NEW)
      missing-agent.toml      (NEW)
      overlapping-transitions.toml (NEW)
      bad-context-source.toml (NEW)
      bad-context-path-traversal.toml (NEW)
      bad-transition-output-key.toml (NEW)
    schemas/
      plan-v1.json, review-v1.json, verification-v1.json, change-summary-v1.json
    templates/
      plan.md, implement.md, review.md
```

---

## Phase 1 Go Types

### definition/types.go

```go
package definition

type WorkflowFile struct {
    Version       int                    `toml:"version"`
    Name          string                 `toml:"name"`
    Description   string                 `toml:"description"`
    InitialStep   string                 `toml:"initial_step"`
    Inputs        map[string]InputDef   `toml:"inputs"`
    Limits        Limits                 `toml:"limits"`
    Steps         []Step                 `toml:"steps"`
    Transitions   []Transition           `toml:"transitions"`
    Delivery      *Delivery              `toml:"delivery"`
}

type InputDef struct {
    Type     string `toml:"type"`
    Required bool   `toml:"required"`
    MaxBytes int    `toml:"max_bytes"`
}

type Limits struct {
    MaxStepAttempts    int `toml:"max_step_attempts"`
    MaxDurationSeconds int `toml:"max_duration_seconds"`
}

type Step struct {
    ID           string           `toml:"id"`
    Kind         string           `toml:"kind"`
    Agent        string           `toml:"agent"`
    Verifier     string           `toml:"verifier"`
    Template     string           `toml:"template"`
    OutputSchema string           `toml:"output_schema"`
    Context      []ContextBinding `toml:"context"`
    OnFailure    string           `toml:"on_failure"`
}

type ContextBinding struct {
    From     string `toml:"from"`
    As       string `toml:"as"`
    MaxBytes int    `toml:"max_bytes"`
}

type Transition struct {
    From          string        `toml:"from"`
    To            string        `toml:"to"`
    Match         MatchCriteria `toml:"match"`
    Loop          string        `toml:"loop"`
    MaxIterations int           `toml:"max_iterations"`
}

type MatchCriteria struct {
    Status string            `toml:"status"`
    Output map[string]string `toml:"output"`
}

type Delivery struct {
    Kind                   string `toml:"kind"`
    Mode                   string `toml:"mode"`
    Provider               string `toml:"provider"`
    Base                   string `toml:"base"`
    TitleTemplate          string `toml:"title_template"`
    CommitMessageTemplate  string `toml:"commit_message_template"`
}
```

### DiscoveredWorkflow (discovery result)

```go
type DiscoveredWorkflow struct {
    Name  string
    Path  string
    Raw   []byte
}
```

### CompiledWorkflow (compiler output)

```go
type CompiledWorkflow struct {
    Name        string
    Description string
    Version     int
    InitialStep string
    Inputs      map[string]InputDef
    Limits      Limits
    Steps       []Step
    Transitions []Transition
    Delivery    *Delivery
    // Derived sets for O(1) lookups
    StepIDs     map[string]bool
    LoopNames   map[string]bool
}
```

---

## Phase 1 Waves (Implementation Order)

### Wave 0: Phase 0 Gap-Fill
- Fix `base = "master"` → `"main"` in valid fixture
- Add 8 invalid fixtures
- Update architecture doc with `on_failure` contract

### Wave 1: Definition Package
**TDD micro-tasks:**
1. Write `types.go` with all structs above
2. Write `decode_test.go`: valid fixture decodes cleanly, unknown-field.toml rejected
3. Write `decode.go`: `ParseWorkflowTOML(data, filename) (WorkflowFile, string, error)` following agents_parse.go pattern
4. Write `discovery_test.go`: test discovery with testdata directory (mocked workspace root)
5. Write `discovery.go`: `DiscoverWorkflows(workspaceRoot string) ([]DiscoveredWorkflow, error)` following agents_io.go pattern (workspace-only, no user-home split, symlink rejection, .toml suffix, size cap)

### Wave 2: Compiler Package (Graph Checks)
**TDD micro-tasks:**
1. Write `compiler.go` with `CompiledWorkflow` struct and `Compile()` function signature
2. Write `graph_test.go`: test reachability from initial_step, test unreachable step rejection, test missing terminal path, test unbounded loop rejection, test loop max_iterations validation
3. Write `graph.go`: `validateGraph()` — single initial step, all steps reachable, at least one path to success/failure, loop bounds
4. Write `transition_test.go`: test overlapping transitions rejection, test bad transition output key (when schema available)
5. Write `transition.go`: `validateTransitions()` — from/to references, no overlap, match criteria well-formed
6. Write step-level validation: empty IDs, reserved IDs (success/failure), duplicate IDs, on_failure targets
7. Wire all checks in `Compile()` — valid fixture compiles, all invalid fixtures produce structured errors

### Wave 3: Template Package
**TDD micro-tasks:**
1. Write `loader_test.go`: load valid templates, reject missing directory, reject path traversal
2. Write `loader.go`: `LoadTemplates(baseDir string) (map[string]string, error)` — bounded directory, no symlink escape, .md suffix, size cap

### Wave 4: Presentation Package
**TDD micro-tasks:**
1. Write `catalog_test.go`: format list of discovered workflows
2. Write `catalog.go`: `FormatWorkflowList([]DiscoveredWorkflow) string`
3. Write `show_test.go`: format compiled workflow details
4. Write `show.go`: `FormatWorkflowShow(CompiledWorkflow) string`

### Wave 5: CLI Integration
**TDD micro-tasks:**
1. Add `case "workflows": return runWorkflows(args[1:])` to root.go
2. Write `workflows_command.go`: `list`, `show <name>`, `validate [name]` subcommands
3. Write `workflows_command_test.go`
4. Update usage text

---

## ADLC Step 0 Challenge Dispositions

### Architecture Review Findings:
- AR-1 (Delivery kind/mode naming): No gap — struct already has both fields
- AR-2 (Missing context-binding fixtures): **Fixed** — G1 adds bad-context-source.toml, bad-context-path-traversal.toml
- AR-3 (on_failure in architecture doc): **Fixed** — G3
- AR-4 (No bad transition output key fixture): **Fixed** — G1
- AR-5 (Missing compiler rejection fixtures): **Fixed** — G1 adds 6 fixtures
- AR-6 (explain semantics undefined): **Fixed** — G4 defers explain
- AR-7 (Omit controller/matcher stubs): **Fixed** — G5
- AR-8 (jschema indirect dep): No action needed
- AR-9 (Wave ordering correct): Confirmed

### Correctness Audit Findings:
- F1 (status collision): No actual collision — MatchCriteria separates attempt status from output fields
- F2 (planner agent nonexistent): Design decision — compiler validates structure only, not runtime agent availability
- F3 (6/9 fixture categories missing): **Fixed** — G1
- F4 (map[string]string no enum enforcement): Deferred — compiler validates match output keys against schema
- F5 (Context precedence in loops): Design decision — compiler validates structural reference validity only
- F6 (Discovery user/workspace split): **Fixed** — workspace-only discovery
- F7 (Closed-field enums): Compiler validates enum values after decode
- F8 (base = "master"): **Fixed** — G2

### TOML Decode Audit:
- All 7 proposed Go types decode correctly with go-toml/v2 under DisallowUnknownFields
- No decode bugs found

---

## Design Decisions

1. **Agent resolution is deferred**: The compiler validates workflow structure, not runtime agent availability. Agent names in steps are stored as-is. Runtime resolution (does this agent exist in the workspace catalog?) happens in Phase 2+.
2. **Workspace-only discovery**: Workflows exist only in `<workspace>/.mivia/workflows/`. No user-home merge.
3. **Compiler is pure function**: `Compile(WorkflowFile) → (CompiledWorkflow, error)` takes decoded TOML, returns compiled result or structured errors. No side effects.
4. **Match output key validation is deferred**: Wave 2 checks output key existence only when the step has an output_schema that can be loaded. Schema loading requires the workflow directory context, which is not available to the pure compiler. Validation happens at the CLI layer (Wave 5) when both the compiled workflow and its directory are available.
5. **Template rendering is deferred**: Wave 3 loads and validates templates exist. Actual rendering (variable substitution) is deferred to Phase 4 coordinator integration.
6. **Limits as value type**: `Limits` uses zero-value semantics. Omitted limits mean "no limit" (zero). This differs from agents' pointer-based optional fields because workflows don't inherit.
