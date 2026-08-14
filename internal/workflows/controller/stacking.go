package controller

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// Engine-synthesized step IDs and loop names (compiler s2 contract).
const (
	synthesizedDecomposeStepID         = "decompose"
	synthesizedChunkPlanValidateStepID = "chunk_plan_validate"
	stackingRepairLoopName             = "decompose_repair"
	stackingRepairLoopMaxIterations    = 3
	// maxChunkPlanOutputBytes bounds the decompose output the deterministic
	// validator will parse; oversized output is rejected as unparseable.
	maxChunkPlanOutputBytes = 64 << 10
)

// reservedStackingInputs names the inputs the controller owns for stacking
// runs; they are never forwarded to workflow steps.
func reservedStackingInputs() []string {
	return []string{"stack_mode", "chunk", "pr_base", "stack_part", "chunk_plan"}
}

// validateStackingReservedInputs admits the reserved stacking inputs against
// the workflow's stacking config. stack_mode=chunk requires chunk, pr_base and
// stack_part; stack_mode=single and plan forbid chunk_plan; unknown values are
// admission errors. Runs without stack_mode (plan mode) accept the reserved
// inputs untouched.
func validateStackingReservedInputs(inputs map[string]any, cfg *definition.StackingConfig) (string, error) {
	if cfg == nil {
		return "", nil
	}
	rawMode, hasMode := inputs["stack_mode"]
	if !hasMode || rawMode == nil || rawMode == "" {
		return "plan", nil
	}
	mode, ok := rawMode.(string)
	if !ok {
		return "", fmt.Errorf("reserved input stack_mode must be a string")
	}
	switch mode {
	case "plan", "single":
		if _, present := inputs["chunk_plan"]; present {
			return "", fmt.Errorf("reserved input chunk_plan is forbidden in stack_mode=%s", mode)
		}
	case "chunk":
		for _, key := range []string{"chunk", "pr_base", "stack_part"} {
			if _, present := inputs[key]; !present {
				return "", fmt.Errorf("stack_mode=chunk requires reserved input %q", key)
			}
		}
	default:
		return "", fmt.Errorf("reserved input stack_mode=%q is invalid; want plan, chunk or single", mode)
	}
	return mode, nil
}

// admitStackingRun applies the stacking admission rules to a run's compiled
// workflow: it validates the reserved inputs, ensures the run graph carries the
// engine-synthesized steps (decompose, chunk_plan_validate), verifies every
// synthesized step has a complete agent runtime, and binds the reserved
// chunk-mode inputs as context inputs on downstream steps. Chunk-mode runs
// need no further binding checks: bindings to pre-implement step outputs are
// legal and resolve absent at runtime (contextForStep's chunk-mode grace),
// because the plan phase ran in the parent run. Non-stacking workflows are
// returned unchanged.
func admitStackingRun(wf *compiler.CompiledWorkflow, steps map[string]StepRuntime, inputs map[string]any) (*compiler.CompiledWorkflow, map[string]StepRuntime, error) {
	if wf == nil || wf.Stacking == nil {
		return wf, steps, nil
	}
	mode, err := validateStackingReservedInputs(inputs, wf.Stacking)
	if err != nil {
		return nil, nil, err
	}
	// Synthesis is idempotent (compiler.SynthesizeStacking returns an already
	// synthesized graph unchanged), so this is safe whether the runtime build
	// synthesized the graph before building step runtimes or the controller is
	// constructed directly over the compiled workflow.
	synth, err := compiler.SynthesizeStacking(wf)
	if err != nil {
		return nil, nil, fmt.Errorf("synthesize stacking run graph: %w", err)
	}
	if err := requireSynthesizedStepRuntimes(synth, steps); err != nil {
		return nil, nil, err
	}
	if mode != "chunk" {
		return synth, steps, nil
	}
	return injectChunkModeInputBindings(synth, inputs), steps, nil
}

// requireSynthesizedStepRuntimes refuses to admit a stacking run whose
// engine-synthesized steps (decompose, chunk_plan_validate) have no complete
// agent runtime. The controller has no agent registry, so it can never resolve
// one: the workflow runtime build - which has the registry - must have
// synthesized the run graph first and resolved the synthesized steps like any
// declared step, routing digest included. Admitting a synthesized step without
// a digest would hand the runner a null routing snapshot, and the agent handler
// would refuse every dispatch attempt ("agent task routing snapshot mismatch"),
// so the run is refused at admission instead.
func requireSynthesizedStepRuntimes(synth *compiler.CompiledWorkflow, steps map[string]StepRuntime) error {
	for _, step := range synth.Steps {
		if step.ID != synthesizedDecomposeStepID && step.ID != synthesizedChunkPlanValidateStepID {
			continue
		}
		rt, ok := steps[step.ID]
		if !ok {
			return fmt.Errorf("synthesized step %q has no agent runtime; the workflow runtime build must resolve it before admission", step.ID)
		}
		if strings.TrimSpace(rt.Agent.Name) == "" {
			return fmt.Errorf("synthesized step %q has an empty agent runtime", step.ID)
		}
		if strings.TrimSpace(rt.Digest) == "" {
			return fmt.Errorf("synthesized step %q has no agent routing digest; refusing to admit a step that could never dispatch", step.ID)
		}
	}
	return nil
}

// preImplementSteps returns the set of steps whose outputs cannot exist when a
// chunk-mode run starts at the implement step (all declared steps except the
// implement step itself, which the engine keeps for chunk runs).
func preImplementSteps(synth *compiler.CompiledWorkflow) map[string]bool {
	implementID := ""
	if synth.Stacking != nil {
		implementID = synth.Stacking.ImplementStep
	}
	set := make(map[string]bool, len(synth.Steps))
	for _, step := range synth.Steps {
		if step.ID != implementID {
			set[step.ID] = true
		}
	}
	return set
}

// injectChunkModeInputBindings binds the reserved chunk-mode inputs as context
// inputs on every downstream step, so author steps read inputs.chunk,
// inputs.pr_base, inputs.stack_part and (when present) inputs.chunk_plan
// without declaring their own bindings. Only inputs present in the admission
// payload are bound; an absent chunk_plan is never required. Existing author
// bindings with the same target are left untouched. Returns a shallow copy of
// the compiled workflow with the extended step contexts.
func injectChunkModeInputBindings(synth *compiler.CompiledWorkflow, inputs map[string]any) *compiler.CompiledWorkflow {
	preImplement := preImplementSteps(synth)
	var injected []string
	for _, key := range reservedStackingInputs() {
		if key == "stack_mode" {
			continue
		}
		if _, present := inputs[key]; present {
			injected = append(injected, key)
		}
	}
	if len(injected) == 0 {
		return synth
	}
	steps := make([]definition.Step, len(synth.Steps))
	copy(steps, synth.Steps)
	for i := range steps {
		if preImplement[steps[i].ID] {
			continue
		}
		has := make(map[string]bool, len(steps[i].Context))
		for _, b := range steps[i].Context {
			has[b.As] = true
		}
		for _, key := range injected {
			if has[key] {
				continue
			}
			steps[i].Context = append(steps[i].Context, definition.ContextBinding{From: "inputs." + key, As: key})
		}
	}
	out := *synth
	out.Steps = steps
	return &out
}

// runStartStepID is the workflow's initial step, or the stacking implement step
// for chunk-mode runs. The controller uses it for the persisted run's
// ActiveStepID and for resume, so a chunk run always starts at implement.
func (c *LinearController) runStartStepID() string {
	if c.Workflow != nil && c.Workflow.Stacking != nil {
		if mode, err := validateStackingReservedInputs(c.Inputs, c.Workflow.Stacking); err == nil && mode == "chunk" {
			if implement := c.Workflow.Stacking.ImplementStep; implement != "" {
				return implement
			}
		}
	}
	if c.Workflow == nil || c.Workflow.InitialStep == "" {
		return ""
	}
	return c.Workflow.InitialStep
}

// preImplementStep reports whether the run starts at the implement step
// (chunk mode) and stepID belongs to a step whose output cannot exist before
// the implement step, so bindings to it must resolve optional-absent.
func (c *LinearController) preImplementStep(stepID string) bool {
	if c.Workflow == nil || c.Workflow.Stacking == nil {
		return false
	}
	mode, err := validateStackingReservedInputs(c.Inputs, c.Workflow.Stacking)
	if err != nil || mode != "chunk" {
		return false
	}
	for _, step := range c.Workflow.Steps {
		if step.ID == stepID {
			return step.ID != c.Workflow.Stacking.ImplementStep
		}
	}
	return false
}

// ChunkPlanValidation is the deterministic result of validating a decompose
// step's chunk plan. A rejected plan carries human-readable reasons.
type ChunkPlanValidation struct {
	Valid   bool
	Reasons []string
}

// chunkPlanDocument is the decomposed stack_mode=multi payload.
type chunkPlanDocument struct {
	Chunks []chunkPlanEntry `json:"chunks"`
}

type chunkPlanEntry struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Files        []string `json:"files"`
	EstDiffLines int      `json:"est_diff_lines"`
	Tests        bool     `json:"tests"`
	DependsOn    []string `json:"depends_on"`
}

// ValidateChunkPlan deterministically validates a decompose step output
// against the stacking rules: stack_mode enum; chunks <= max_chunks;
// est_diff_lines <= hard_lines; files per chunk <= max_files; file sets
// disjoint; every chunk has tests (const true); depends_on is a DAG. The
// stack_mode=single and no_bug payloads are valid by construction. Malformed
// or oversized output is an error, not an invalid plan.
func ValidateChunkPlan(raw json.RawMessage, cfg *definition.StackingConfig) (ChunkPlanValidation, error) {
	out := ChunkPlanValidation{Valid: true}
	if cfg == nil {
		return out, nil
	}
	if len(raw) > maxChunkPlanOutputBytes {
		return out, fmt.Errorf("chunk plan output exceeds the %d byte validation cap", maxChunkPlanOutputBytes)
	}
	var envelope struct {
		StackMode string            `json:"stack_mode"`
		ChunkPlan chunkPlanDocument `json:"chunk_plan"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return out, fmt.Errorf("chunk plan is not valid JSON: %w", err)
	}
	switch envelope.StackMode {
	case "single", "no_bug":
		return out, nil
	case "multi":
	default:
		out.Valid = false
		out.Reasons = append(out.Reasons, fmt.Sprintf("stack_mode %q is invalid; want single, multi or no_bug", envelope.StackMode))
		return out, nil
	}
	validateChunkPlanMulti(&out, envelope.ChunkPlan.Chunks, cfg)
	return out, nil
}

// validateChunkPlanMulti applies the per-plan and cross-chunk rules for a
// stack_mode=multi payload, appending reasons to out.
func validateChunkPlanMulti(out *ChunkPlanValidation, chunks []chunkPlanEntry, cfg *definition.StackingConfig) {
	if len(chunks) == 0 {
		out.Valid = false
		out.Reasons = append(out.Reasons, "chunk_plan has no chunks")
		return
	}
	if len(chunks) > cfg.MaxChunks {
		out.Valid = false
		out.Reasons = append(out.Reasons, fmt.Sprintf("chunk_plan has %d chunks, exceeding max_chunks=%d", len(chunks), cfg.MaxChunks))
	}
	ids := map[string]bool{}
	seenFiles := map[string]bool{}
	byID := map[string]*chunkPlanEntry{}
	for i := range chunks {
		validateChunkPlanEntry(out, &chunks[i], i, ids, seenFiles, byID, cfg)
	}
	for i := range chunks {
		chunk := &chunks[i]
		for _, dep := range chunk.DependsOn {
			if !ids[dep] {
				out.Valid = false
				out.Reasons = append(out.Reasons, fmt.Sprintf("chunk %q depends_on unknown chunk %q", chunk.ID, dep))
			}
			if dep == chunk.ID {
				out.Valid = false
				out.Reasons = append(out.Reasons, fmt.Sprintf("chunk %q depends on itself", chunk.ID))
			}
		}
	}
	if hasChunkPlanCycle(chunks, byID) {
		out.Valid = false
		out.Reasons = append(out.Reasons, "chunk depends_on graph contains a cycle")
	}
}

// validateChunkPlanEntry applies one chunk's structural rules: unique id,
// est_diff_lines <= hard_lines, 0 < files <= max_files, tests required, and
// file sets disjoint across chunks.
func validateChunkPlanEntry(out *ChunkPlanValidation, chunk *chunkPlanEntry, index int, ids, seenFiles map[string]bool, byID map[string]*chunkPlanEntry, cfg *definition.StackingConfig) {
	if chunk.ID == "" {
		out.Valid = false
		out.Reasons = append(out.Reasons, fmt.Sprintf("chunk %d has no id", index))
		return
	}
	if ids[chunk.ID] {
		out.Valid = false
		out.Reasons = append(out.Reasons, fmt.Sprintf("chunk id %q appears more than once", chunk.ID))
	}
	ids[chunk.ID] = true
	byID[chunk.ID] = chunk
	if chunk.EstDiffLines > cfg.HardLines {
		out.Valid = false
		out.Reasons = append(out.Reasons, fmt.Sprintf("chunk %q est_diff_lines=%d exceeds hard_lines=%d", chunk.ID, chunk.EstDiffLines, cfg.HardLines))
	}
	if len(chunk.Files) == 0 {
		out.Valid = false
		out.Reasons = append(out.Reasons, fmt.Sprintf("chunk %q lists no files", chunk.ID))
	}
	if len(chunk.Files) > cfg.MaxFiles {
		out.Valid = false
		out.Reasons = append(out.Reasons, fmt.Sprintf("chunk %q has %d files, exceeding max_files=%d", chunk.ID, len(chunk.Files), cfg.MaxFiles))
	}
	if !chunk.Tests {
		out.Valid = false
		out.Reasons = append(out.Reasons, fmt.Sprintf("chunk %q must have tests", chunk.ID))
	}
	for _, file := range chunk.Files {
		if seenFiles[file] {
			out.Valid = false
			out.Reasons = append(out.Reasons, fmt.Sprintf("file %q overlaps between chunks", file))
		}
		seenFiles[file] = true
	}
}

// hasChunkPlanCycle reports whether the chunk depends_on graph has a cycle,
// using the classic three-color topological walk.
func hasChunkPlanCycle(chunks []chunkPlanEntry, byID map[string]*chunkPlanEntry) bool {
	state := map[string]uint8{}
	var visit func(id string) bool
	visit = func(id string) bool {
		switch state[id] {
		case 1:
			return true // gray: back edge
		case 2:
			return false
		}
		state[id] = 1
		if chunk, ok := byID[id]; ok {
			for _, dep := range chunk.DependsOn {
				if visit(dep) {
					return true
				}
			}
		}
		state[id] = 2
		return false
	}
	for _, chunk := range chunks {
		if visit(chunk.ID) {
			return true
		}
	}
	return false
}
