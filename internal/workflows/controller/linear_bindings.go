package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// resolveBindingOutput resolves a steps.X.output context binding to the
// LATEST attempt of X that actually produced an output artifact
// (latestOutputAttempt). An in-flight attempt (no OutputRef yet) or a failed
// attempt without output must not shadow a prior completed output: a review
// step binding its OWN prior output (prior_findings) must resolve to the
// previous COMPLETED review, and attempt NUMBERING keeps using latestAttempt
// (nextAttemptNo).
//
// Every resolved steps.X.output binding records its artifact reference; the
// controller renders them into the prompt's evidence-refs block.
// Optional-absent bindings resolve to "" (ok=false) and are skipped by the
// caller (they have no artifact to address).
//
// Envelope_only bindings ALWAYS resolve to the ledger-backed reference
// envelope when the prior output is non-empty — even when the content is
// small enough to inline under the cap (the operator directive: findings pass
// as ledger refs, never as verbatim inline payloads). The cap applies to the
// MARSHALED envelope (the existing fit invariant in buildEvidenceEnvelope);
// optional-absent content still resolves to ” above, and empty content ("")
// inlines as before — there is no artifact to reference.
//
// The size check runs BEFORE json.Unmarshal: an oversized prior output — JSON
// or not — substitutes a compact ledger-backed reference envelope instead of
// failing the run. The full artifact stays content-addressed in the workflow
// ledger and is read back with workflow_inspect. The builder measures the
// envelope skeleton exactly and budgets/halves the preview so the marshaled
// envelope always fits the binding cap; it errors only for a cap that cannot
// fit even the no-preview envelope (the historical reject, defense-in-depth).
// A SMALL prior output that is not valid JSON (e.g. prose or binary) also
// degrades to the ledger-backed reference envelope instead of rejecting the
// run: the child reads the full artifact with workflow_inspect. Reject only
// when even the no-preview envelope cannot fit the binding cap.
func (c *LinearController) resolveBindingOutput(ctx context.Context, binding definition.ContextBinding, attempts []workflowledger.StepAttempt) (any, ArtifactRef, bool, error) {
	parts := strings.Split(binding.From, ".")
	prior, ok := latestOutputAttempt(attempts, parts[1])
	if !ok {
		if binding.Optional {
			return "", ArtifactRef{}, false, nil
		}
		return nil, ArtifactRef{}, false, fmt.Errorf("missing prior output %q", binding.From)
	}
	raw, err := c.Repo.LoadContent(ctx, prior.OutputRef)
	if err != nil {
		return nil, ArtifactRef{}, false, err
	}
	threshold := binding.MaxBytes
	if threshold <= 0 {
		threshold = definition.MaxEvidenceBindingBytes
	}
	ref := ArtifactRef{
		Step: prior.StepID, Attempt: prior.AttemptNo, Ref: prior.OutputRef,
		Bytes: len(raw), Digest: prior.OutputDigest,
	}
	if binding.EnvelopeOnly && len(raw) > 0 && !bytes.Equal(raw, []byte(`""`)) {
		envelope, err := buildEvidenceEnvelope(c.RunID, prior, raw, threshold)
		if err != nil {
			return nil, ArtifactRef{}, false, fmt.Errorf("context binding %q exceeds %d bytes", binding.From, threshold)
		}
		return envelope, ref, true, nil
	}
	if len(raw) > threshold {
		envelope, err := buildEvidenceEnvelope(c.RunID, prior, raw, threshold)
		if err != nil {
			return nil, ArtifactRef{}, false, fmt.Errorf("context binding %q exceeds %d bytes", binding.From, threshold)
		}
		return envelope, ref, true, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		envelope, err := buildEvidenceEnvelope(c.RunID, prior, raw, threshold)
		if err != nil {
			return nil, ArtifactRef{}, false, fmt.Errorf("context binding %q exceeds %d bytes", binding.From, threshold)
		}
		return envelope, ref, true, nil
	}
	return value, ref, true, nil
}
