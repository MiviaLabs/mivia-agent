package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// evidenceSelection is one input/evidence value summarized for the ledger: its
// name, source ("input" or "evidence"), byte length, and content digest.
type evidenceSelection struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Bytes  int    `json:"bytes"`
	Digest string `json:"digest"`
	// ArtifactDigest names the ledger content digest embedded in a reference
	// envelope evidence value, pointing the selection metadata at the
	// underlying artifact without re-reading it. Empty for inline values;
	// omitempty keeps inline-only selections byte-identical to the
	// pre-envelope shape.
	ArtifactDigest string `json:"artifact_digest,omitempty"`
}

// marshalEvidenceSelection renders the bounded evidence-selection metadata for
// one workflow step: every input/evidence binding is capped at the per-binding
// budget and the aggregate JSON is capped at the rendered-context budget and
// the ledger persistence cap. It runs before the child is dispatched so an
// oversized selection fails fast instead of after the child ran to completion.
func marshalEvidenceSelection(spec AgentStepRequest) ([]byte, error) {
	items := make([]evidenceSelection, 0, len(spec.Inputs)+len(spec.Evidence))
	bindingCap := spec.MaxBindingBytes
	if bindingCap <= 0 {
		// No explicit per-binding max_bytes: fall back to the shared evidence
		// binding cap. template.Render applies the same 32KiB default
		// (MaxTemplateBytes) to rendered bindings, so the two paths agree.
		bindingCap = definition.MaxEvidenceBindingBytes
	}
	appendItems := func(source string, values map[string]any) error {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			raw, err := json.Marshal(values[key])
			if err != nil {
				return fmt.Errorf("marshal %s binding %q: %w", source, key, err)
			}
			if len(raw) > bindingCap {
				return fmt.Errorf("%s binding %q exceeds %d bytes", source, key, bindingCap)
			}
			sum := sha256.Sum256(raw)
			item := evidenceSelection{Name: key, Source: source, Bytes: len(raw), Digest: "sha256:" + hex.EncodeToString(sum[:])}
			if source == "evidence" {
				item.ArtifactDigest = envelopeArtifactDigest(values[key])
			}
			items = append(items, item)
		}
		return nil
	}
	if err := appendItems("input", spec.Inputs); err != nil {
		return nil, err
	}
	if err := appendItems("evidence", spec.Evidence); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("marshal evidence selection: %w", err)
	}
	if spec.MaxContextBytes > 0 && len(raw) > spec.MaxContextBytes {
		return nil, fmt.Errorf("evidence selection exceeds %d bytes", spec.MaxContextBytes)
	}
	// The ledger persists evidence-selection metadata under
	// workflowledger.MaxEvidenceBytes (16KiB), which is tighter than the
	// rendered-context budget (maxStepContextBytes, 256KiB). Enforce the
	// persistence cap here so an oversized selection fails before the agent is
	// dispatched instead of after the child ran to completion (the previous
	// behavior, which failed only at RecordStepResult persistence time).
	if len(raw) > workflowledger.MaxEvidenceBytes {
		return nil, fmt.Errorf("evidence selection exceeds %d bytes", workflowledger.MaxEvidenceBytes)
	}
	return raw, nil
}

// envelopeArtifactDigest extracts the artifact digest embedded in a reference
// envelope evidence value, or "" when the value is not an envelope. It is
// cheap: the envelope is already in memory, and only envelope values carry the
// artifact key.
func envelopeArtifactDigest(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	artifact, ok := m["artifact"].(map[string]any)
	if !ok {
		return ""
	}
	digest, _ := artifact["digest"].(string)
	return digest
}
