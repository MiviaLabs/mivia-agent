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
// LATEST attempt of X that produced an output artifact (latestOutputAttempt).
// An in-flight or failed-without-output attempt must not shadow a prior
// completed output — e.g. a review step binding prior_findings must resolve
// to the previous COMPLETED review — while attempt numbering keeps using
// latestAttempt (nextAttemptNo).
//
// envelope_only bindings ALWAYS resolve to the ledger-backed reference
// envelope when the prior output is non-empty, even if it would fit inline
// under the cap: findings pass as ledger refs, never verbatim inline
// payloads (operator directive).
//
// The size check runs BEFORE json.Unmarshal: an oversized prior output (JSON
// or not) substitutes a compact ledger-backed reference envelope rather than
// failing the run — the full artifact stays content-addressed and is read
// back with workflow_inspect. A run only fails when even the no-preview
// envelope cannot fit the binding cap.
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
