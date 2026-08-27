package controller

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// renderStepPrompt produces the fully bounded step prompt in the controller
// (plan v3 P1+P3): the template is expanded over the step's inputs and
// evidence, the evidence-refs block is appended, and the prompt is persisted
// content-addressed under the attempt so a resume JOIN reuses the
// byte-identical prompt (fingerprint-stable) instead of re-rendering. When
// the attempt already carries a PromptRef the stored prompt is loaded and
// reused verbatim. Prompt persistence is FAIL-SOFT: a store failure never
// fails the step — the prompt is still dispatched and the attempt simply
// carries no PromptRef (a later resume then re-renders fresh).
func (c *LinearController) renderStepPrompt(ctx context.Context, attempt workflowledger.StepAttempt, runtime StepRuntime, step definition.Step, stepInputs, evidence map[string]any, refs map[string]ArtifactRef) (string, error) {
	if attempt.PromptRef != "" {
		stored, err := c.Repo.LoadContent(ctx, attempt.PromptRef)
		if err == nil {
			return string(stored), nil
		}
		log.Printf("workflow: run %s step %s attempt %d prompt %s is not loadable (%v); rendering fresh", c.RunID, step.ID, attempt.AttemptNo, attempt.PromptRef, err)
	}
	// INV-68-6: the evidence-refs block renders FIRST so the fixed
	// "Evidence refs:" header (the run ID is constant per run) is a
	// byte-identical prefix of every step prompt in a run, which is what a
	// provider-implicit prompt cache can reuse across steps. delivery.Render
	// still bounds the body at maxStepContextBytes and the defense-in-depth
	// check below keeps the FINAL prompt at the cap, exactly as before the
	// reorder (single-file revert per site).
	block := evidenceRefsBlock(c.RunID, refs)
	prompt, err := delivery.Render(runtime.Template, stepInputs, evidence, maxBinding(step), maxStepContextBytes)
	if err != nil {
		return "", err
	}
	prompt = block + prompt
	// Defense-in-depth: delivery.Render already bounded the rendered body at
	// maxStepContextBytes; the prepended evidence-refs block is the only thing
	// that can push the final prompt over the cap.
	if len(prompt) > maxStepContextBytes {
		return "", fmt.Errorf("rendered step prompt exceeds %d bytes", maxStepContextBytes)
	}
	promptRef := "sha256:" + workflowledger.DigestHex([]byte(prompt))
	if err := c.Repo.StoreContent(ctx, promptRef, []byte(prompt)); err != nil {
		log.Printf("workflow: run %s step %s attempt %d prompt storage failed (%v); continuing without a stored prompt", c.RunID, step.ID, attempt.AttemptNo, err)
		return prompt, nil
	}
	if err := c.Repo.SetStepAttemptPrompt(ctx, c.RunID, attempt.AttemptID, promptRef); err != nil {
		log.Printf("workflow: run %s step %s attempt %d prompt ref %s not recorded (%v); continuing without a stored prompt", c.RunID, step.ID, attempt.AttemptNo, promptRef, err)
	}
	return prompt, nil
}

// evidenceRefsBlock renders the evidence-reference section appended to a step
// prompt. The header names the workflow_inspect paging contract (the run ID is
// interpolated; step/attempt/offset/limit stay literal placeholders like the
// envelope note); each resolved binding lists its ledger artifact address,
// sorted by binding name so the rendered prompt is fingerprint-stable.
func evidenceRefsBlock(runID string, refs map[string]ArtifactRef) string {
	var b strings.Builder
	b.WriteString("Evidence refs: every prior-step output is stored in the workflow ledger; read a full artifact with workflow_inspect(run_id=")
	b.WriteString(runID)
	b.WriteString(", step=<step>, attempt=<attempt>, offset=<n>, limit=<n>)\n")
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := refs[name]
		fmt.Fprintf(&b, "- %s: step=%s attempt=%d ref=%s bytes=%d digest=%s\n", name, ref.Step, ref.Attempt, ref.Ref, ref.Bytes, ref.Digest)
	}
	return b.String()
}
