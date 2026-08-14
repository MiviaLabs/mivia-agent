package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
			if err := c.resolveStepsOutputBinding(ctx, binding, parts[1], attempts, evidence, refs); err != nil {
				return nil, nil, nil, err
			}
			continue
		}
		// implement.touched_files is a HOST-injected context source: the
		// actual worktree diff's file list (git diff --name-only vs the
		// admitted base), NOT the implementing agent's own self-reported
		// files_changed. A review panel that only reads the agent's summary
		// cannot catch an undisclosed out-of-scope file touch (confirmed
		// live: a chunk silently rewrote the centralized default agent
		// prompt while claiming to add three unrelated utility packages).
		// Best-effort: no git context wired, no base commit, or a
		// measurement failure all resolve to "" like an optional-absent
		// steps binding, so a workflow without a pinned git context is
		// unaffected.
		if len(parts) == 2 && parts[0] == "implement" && parts[1] == "touched_files" {
			evidence[binding.As] = c.touchedFilesEvidence(ctx)
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

// touchedFilesEvidence returns the JSON-encoded list of files the worktree
// diff actually touches vs the admitted base commit, or "" when no git
// context is wired, no base commit was admitted, or the measurement fails.
// This is host ground truth, unlike the implementing agent's own
// self-reported files_changed: a reviewer with only the self-report cannot
// notice a file the agent touched but never mentioned.
func (c *LinearController) touchedFilesEvidence(ctx context.Context) string {
	if c.gitRunner == nil || c.gitCtx.Dir == "" || c.admission.BaseCommit == "" {
		return ""
	}
	gitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := c.gitRunner.Run(gitCtx, c.gitCtx, "-c", "core.fsmonitor=false", "add", "-A"); err != nil {
		return ""
	}
	out, err := c.gitRunner.Run(gitCtx, c.gitCtx, "-c", "core.quotePath=false", "diff", "--cached", "--name-only", c.admission.BaseCommit)
	if err != nil {
		return ""
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return ""
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return ""
	}
	return string(raw)
}

// resolveStepsOutputBinding resolves one steps.X.output binding into evidence
// (and refs, when the resolved value has a content-addressed artifact).
// Split out of contextForStep to keep that function under the file-size
// gate's function-length cap.
func (c *LinearController) resolveStepsOutputBinding(ctx context.Context, binding definition.ContextBinding, stepID string, attempts []workflowledger.StepAttempt, evidence map[string]any, refs map[string]ArtifactRef) error {
	// Chunk-mode grace: a binding to a step that produced no output in THIS
	// run (the plan phase ran in the parent run) resolves as absent instead
	// of failing, for mandatory bindings too; the chunk description and
	// replayed plan inputs carry the context. A binding to a step that DID
	// run resolves to the real evidence, exactly as in plan mode. Plan and
	// single runs never take this branch (preImplementStep is chunk-only).
	if _, hasOutput := latestOutputAttempt(attempts, stepID); !hasOutput && c.preImplementStep(stepID) {
		evidence[binding.As] = ""
		return nil
	}
	value, ref, ok, err := c.resolveBindingOutput(ctx, binding, attempts)
	if err != nil {
		return err
	}
	if !ok {
		// Optional-absent binding: resolve to "" with no artifact to
		// reference (the evidence-refs block skips it).
		evidence[binding.As] = ""
		return nil
	}
	refs[binding.As] = ref
	evidence[binding.As] = value
	return nil
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
