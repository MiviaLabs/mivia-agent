package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/matcher"
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
// workflow: it validates the reserved inputs, synthesizes the run graph from
// the compiler when the workflow is stacking, injects runtimes for the
// synthesized steps, and binds the reserved chunk-mode inputs as context
// inputs on downstream steps. Non-stacking workflows are returned unchanged.
func admitStackingRun(wf *compiler.CompiledWorkflow, steps map[string]StepRuntime, inputs map[string]any) (*compiler.CompiledWorkflow, map[string]StepRuntime, error) {
	if wf == nil || wf.Stacking == nil {
		return wf, steps, nil
	}
	mode, err := validateStackingReservedInputs(inputs, wf.Stacking)
	if err != nil {
		return nil, nil, err
	}
	synth, err := compiler.SynthesizeStacking(wf)
	if err != nil {
		return nil, nil, fmt.Errorf("synthesize stacking run graph: %w", err)
	}
	steps = ensureSynthesizedStepRuntimes(synth, wf, steps)
	if mode != "chunk" {
		return synth, steps, nil
	}
	if err := validateChunkModeBindings(synth); err != nil {
		return nil, nil, err
	}
	return injectChunkModeInputBindings(synth, inputs), steps, nil
}

// ensureSynthesizedStepRuntimes adds a runtime for every engine-synthesized
// step (decompose, chunk_plan_validate) so an admitted stacking run can execute
// it even though the author never declared one. The synthesized steps carry no
// output_schema in the compiled definition, so the runtime schema stays nil.
func ensureSynthesizedStepRuntimes(synth *compiler.CompiledWorkflow, original *compiler.CompiledWorkflow, steps map[string]StepRuntime) map[string]StepRuntime {
	needed := false
	for _, step := range synth.Steps {
		if step.ID == synthesizedDecomposeStepID || step.ID == synthesizedChunkPlanValidateStepID {
			if _, ok := steps[step.ID]; !ok {
				needed = true
				break
			}
		}
	}
	if !needed {
		return steps
	}
	extended := make(map[string]StepRuntime, len(steps)+2)
	for id, rt := range steps {
		extended[id] = rt
	}
	agent := agentsFromWorkflow(original, synth.Stacking)
	if _, ok := extended[synthesizedDecomposeStepID]; !ok {
		extended[synthesizedDecomposeStepID] = StepRuntime{Agent: agent}
	}
	if _, ok := extended[synthesizedChunkPlanValidateStepID]; !ok {
		extended[synthesizedChunkPlanValidateStepID] = StepRuntime{Agent: agent}
	}
	return extended
}

// agentsFromWorkflow names the agent that owns the engine-synthesized steps:
// the stacking config's agent when set, otherwise the plan step's agent.
func agentsFromWorkflow(wf *compiler.CompiledWorkflow, cfg *definition.StackingConfig) agents.ResolvedAgent {
	if wf == nil || cfg == nil {
		return agents.ResolvedAgent{}
	}
	if strings.TrimSpace(cfg.Agent) != "" {
		return agents.ResolvedAgent{Name: cfg.Agent}
	}
	for _, step := range wf.Steps {
		if step.ID == cfg.PlanStep {
			return agents.ResolvedAgent{Name: step.Agent}
		}
	}
	return agents.ResolvedAgent{}
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

// validateChunkModeBindings enforces that no mandatory binding to a
// pre-implement step survives into chunk mode: their outputs would not exist
// when the run starts at the implement step. Optional and envelope_only
// bindings pass; they resolve empty in contextForStep.
func validateChunkModeBindings(synth *compiler.CompiledWorkflow) error {
	preImplement := preImplementSteps(synth)
	for _, step := range synth.Steps {
		for _, binding := range step.Context {
			fromStep, ok := bindingStepFrom(binding.From)
			if !ok || !preImplement[fromStep] {
				continue
			}
			if binding.Optional || binding.EnvelopeOnly {
				continue
			}
			return fmt.Errorf("chunk mode binding %q on step %q references pre-implement step %q; it must be optional or envelope_only (or sourced from inputs.chunk_plan)", binding.From, step.ID, fromStep)
		}
	}
	return nil
}

// bindingStepFrom extracts the referenced step ID from a steps.<id>.<field>
// binding; ok is false for non-step bindings such as inputs.* and delivery.*.
func bindingStepFrom(from string) (string, bool) {
	parts := strings.Split(from, ".")
	if len(parts) < 3 || parts[0] != "steps" {
		return "", false
	}
	return parts[1], true
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

// stackingRepairLoopMax reads the repair loop's max_iterations from the
// synthesized graph so the controller stays in sync with the compiler.
func stackingRepairLoopMax(wf *compiler.CompiledWorkflow) int {
	if wf == nil {
		return 0
	}
	for _, tr := range wf.Transitions {
		if tr.From == synthesizedChunkPlanValidateStepID && tr.Loop == stackingRepairLoopName {
			return tr.MaxIterations
		}
	}
	return 0
}

// chunkPlanRepairRoute is the deterministic decompose gate. When a stacked run
// routes a succeeded decompose step toward the engine-synthesized
// chunk_plan_validate gate, the controller validates the decompose output
// against the stacking rules first. An invalid plan is routed back to
// decompose through the engine's repair loop (the synthesized graph already
// carries the edge); the route is only refused when the loop is exhausted.
// Valid plans and single-mode/no_bug routes pass through untouched.
func (c *LinearController) chunkPlanRepairRoute(ctx context.Context, step definition.Step, route RouteDecision, outMap map[string]any) (RouteDecision, bool, error) {
	if c.Workflow == nil || c.Workflow.Stacking == nil {
		return route, false, nil
	}
	if step.ID != synthesizedDecomposeStepID || route.ToStepID != synthesizedChunkPlanValidateStepID {
		return route, false, nil
	}
	raw, err := json.Marshal(outMap)
	if err != nil {
		return route, false, fmt.Errorf("chunk plan validation could not marshal decompose output: %w", err)
	}
	outcome, err := ValidateChunkPlan(raw, c.Workflow.Stacking)
	if err != nil {
		return route, false, fmt.Errorf("chunk plan validation failed: %w", err)
	}
	if outcome.Valid {
		return route, false, nil
	}
	maxRepairs := stackingRepairLoopMax(c.Workflow)
	if err := c.checkLoopCap(ctx, stackingRepairLoopName, maxRepairs); err != nil {
		decision := matcher.Decision{
			TransitionIndex: route.TransitionIndex,
			ToStepID:        route.ToStepID,
			MatchDigest:     route.MatchDigest,
			DecisionJSON:    append([]byte(nil), route.DecisionJSON...),
		}
		rr, rerr := c.loopExhaustionRoute(ctx, step, decision, c.loopExhaustedRouteError(ctx, err, step.ID))
		return rr, true, rerr
	}
	repair := RouteDecision{
		ToStepID:        synthesizedDecomposeStepID,
		TransitionIndex: route.TransitionIndex,
		MatchDigest:     route.MatchDigest,
		DecisionJSON:    append([]byte(nil), route.DecisionJSON...),
		Loop:            stackingRepairLoopName,
		MaxIterations:   maxRepairs,
	}
	return repair, true, nil
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
