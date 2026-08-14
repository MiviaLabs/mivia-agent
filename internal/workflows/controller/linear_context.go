package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/textutil"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// contextForStep resolves a step's context bindings into the evidence map,
// inputs map, and content-addressed artifact refs handed to the step runtime.
func (c *LinearController) contextForStep(ctx context.Context, step definition.Step, attempts []workflowledger.StepAttempt) (map[string]any, map[string]any, map[string]ArtifactRef, error) {
	inputs := make(map[string]any)
	evidence := make(map[string]any)
	refs := make(map[string]ArtifactRef)
	for _, binding := range step.Context {
		parts := strings.Split(binding.From, ".")
		if len(parts) == 2 && parts[0] == "inputs" {
			value, err := c.resolveInputsBinding(binding, parts[1])
			if err != nil {
				return nil, nil, nil, err
			}
			inputs[binding.As] = value
			continue
		}
		if len(parts) == 2 && parts[0] == "run" && parts[1] == "salvage" {
			// run.salvage binds the verified outputs preserved when a repair
			// loop exhausted (R2 Phase 2). The partial_target step reads the
			// content-addressed refs to recover or deliver the verified work.
			salvaged, err := c.salvageLoopSuccesses(ctx)
			if err != nil {
				return nil, nil, nil, err
			}
			raw, err := json.Marshal(salvaged)
			if err != nil {
				return nil, nil, nil, err
			}
			evidence[binding.As] = string(raw)
			continue
		}
		if len(parts) == 3 && parts[0] == "steps" && parts[2] == "output" {
			// Chunk-mode grace: a binding to a step that produced no output in
			// THIS run (the plan phase ran in the parent run) resolves as
			// absent instead of failing, for mandatory bindings too; the chunk
			// description and replayed plan inputs carry the context. A
			// binding to a step that DID run resolves to the real evidence,
			// exactly as in plan mode. Plan and single runs never take this
			// branch (preImplementStep is chunk-only).
			if _, hasOutput := latestOutputAttempt(attempts, parts[1]); !hasOutput && c.preImplementStep(parts[1]) {
				evidence[binding.As] = ""
				continue
			}
			value, ref, ok, err := c.resolveBindingOutput(ctx, binding, attempts)
			if err != nil {
				return nil, nil, nil, err
			}
			if !ok {
				// Optional-absent binding: resolve to "" with no artifact to
				// reference (the evidence-refs block skips it).
				evidence[binding.As] = ""
				continue
			}
			refs[binding.As] = ref
			evidence[binding.As] = value
			continue
		}
		// delivery.failure is a HOST-injected context source: the controller
		// reads the latest wf-delivery failure hint from the ledger and places
		// it directly into the step's evidence, so the repair agent never
		// fetches it. Empty text resolves to "" like an optional-absent steps
		// binding. The binding cap truncates rune-safely WITHOUT a marker; the
		// full text stays on the wf-delivery attempt for workflow_inspect.
		if len(parts) == 2 && parts[0] == "delivery" && parts[1] == "failure" {
			text, err := delivery.LatestFailureText(ctx, c.Repo, c.RunID)
			if err != nil {
				return nil, nil, nil, err
			}
			threshold := binding.MaxBytes
			if threshold <= 0 {
				threshold = definition.MaxEvidenceBindingBytes
			}
			if len(text) > threshold {
				text = textutil.TruncateRuneSafe(text, threshold)
			}
			evidence[binding.As] = text
			continue
		}
		return nil, nil, nil, fmt.Errorf("unsupported context binding %q", binding.From)
	}
	return inputs, evidence, refs, nil
}

// resolveInputsBinding resolves an inputs.* context binding. An optional
// binding not present in this run's admission payload resolves to "",
// mirroring the optional-absent grace steps.*.output bindings already have.
// Needed for a reserved input only some admission modes carry (for example
// remaining_scope, carried only by a decompose-continuation run) referenced
// by a template that always renders the same placeholder regardless of mode.
func (c *LinearController) resolveInputsBinding(binding definition.ContextBinding, key string) (any, error) {
	value, ok := c.Inputs[key]
	if !ok {
		if binding.Optional {
			return "", nil
		}
		return nil, fmt.Errorf("missing input %q", key)
	}
	return value, nil
}
