package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestValidateBindingLimitsSkipsMissingOptionalWithTinyCap pins the audit-F2
// regression: a missing OPTIONAL prior output resolves to "" in the evidence
// map, and a legal tiny max_bytes (1) must not reject it on the very first
// attempt (json.Marshal("") is 2 bytes). HEAD skipped missing optional
// bindings; the envelope-aware measurement must preserve that.
func TestValidateBindingLimitsSkipsMissingOptionalWithTinyCap(t *testing.T) {
	step := definition.Step{ID: "implement", Context: []definition.ContextBinding{
		{From: "steps.review.output", As: "review_findings", MaxBytes: 1, Optional: true},
	}}
	if err := validateBindingLimits(step, map[string]any{}, map[string]any{"review_findings": ""}); err != nil {
		t.Fatalf("missing optional binding with max_bytes=1 must be skipped, got: %v", err)
	}
}

// TestMarshalEvidenceSelectionRecordsEnvelope pins the envelope-aware
// selection metadata (plan 59 named test): an envelope evidence value is
// recorded with its own bytes and digest, artifact_digest is extracted from
// the envelope's embedded artifact digest, and inline values carry no
// artifact_digest (omitempty keeps inline metadata byte-identical).
func TestMarshalEvidenceSelectionRecordsEnvelope(t *testing.T) {
	envelope := map[string]any{
		"artifact": map[string]any{
			"step": "plan_tests", "attempt": 2, "ref": "sha256:abc",
			"bytes": 35056, "digest": "sha256:def", "preview": "preview",
		},
		"note": "read the full artifact with workflow_inspect",
	}
	spec := validStepRequest()
	spec.MaxBindingBytes = definition.MaxEvidenceBindingBytes
	spec.Inputs = map[string]any{"task": "build"}
	spec.Evidence = map[string]any{"test_plan": envelope, "plan": map[string]any{"ok": true}}
	raw, err := marshalEvidenceSelection(spec)
	if err != nil {
		t.Fatalf("marshal evidence selection: %v", err)
	}
	var items []evidenceSelection
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("unmarshal selection metadata: %v", err)
	}
	var envItem, inlineItem *evidenceSelection
	for i := range items {
		switch items[i].Name {
		case "test_plan":
			envItem = &items[i]
		case "plan":
			inlineItem = &items[i]
		}
	}
	if envItem == nil || inlineItem == nil {
		t.Fatalf("selection items missing envelope or inline entry: %+v", items)
	}
	if envItem.ArtifactDigest != "sha256:def" {
		t.Fatalf("envelope artifact_digest = %q, want sha256:def", envItem.ArtifactDigest)
	}
	if inlineItem.ArtifactDigest != "" {
		t.Fatalf("inline item must not carry artifact_digest, got %q", inlineItem.ArtifactDigest)
	}
	envBytes, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if envItem.Bytes != len(envBytes) {
		t.Fatalf("envelope bytes = %d, want %d", envItem.Bytes, len(envBytes))
	}
	sum := sha256.Sum256(envBytes)
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])
	if envItem.Digest != wantDigest {
		t.Fatalf("envelope digest = %q, want %q", envItem.Digest, wantDigest)
	}
}

// TestContextForStepSubstitutesEnvelopeForOversizedBinding pins Defect 1
// (size-reject): a steps.<id>.output binding whose marshaled prior output
// exceeds the binding's max_bytes must degrade to a ledger-backed reference
// envelope instead of failing the run. The evidence value decodes to
// {artifact:{step,attempt,ref,bytes,digest,preview}, note}, and the run
// proceeds to completion.
func TestContextForStepSubstitutesEnvelopeForOversizedBinding(t *testing.T) {
	// The incident artifact: a 35,056-byte output bound under a 16,000-byte cap.
	firstOutput := json.RawMessage(`{"content":"` + strings.Repeat("a", 35042) + `"}`)
	if len(firstOutput) != 35056 {
		t.Fatalf("first output = %d bytes, want 35056", len(firstOutput))
	}
	wf := linearWorkflow(t)
	wf.Steps[1].Context = []definition.ContextBinding{{From: "steps.first.output", As: "first", MaxBytes: 16000}}
	runner := &linearRunner{outputs: map[string]json.RawMessage{
		"first":  firstOutput,
		"second": json.RawMessage(`{"done":true}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-envelope-run", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v, error = %v, want the run to proceed to success", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.calls))
	}
	env, ok := runner.calls[1].Evidence["first"].(map[string]any)
	if !ok {
		t.Fatalf("evidence[first] = %#v (%T), want a reference envelope", runner.calls[1].Evidence["first"], runner.calls[1].Evidence["first"])
	}
	artifact, ok := env["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("envelope = %#v, want an artifact key", env)
	}
	note, ok := env["note"].(string)
	if !ok {
		t.Fatalf("envelope = %#v, want a note key", env)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var first workflowledger.StepAttempt
	for _, attempt := range attempts {
		if attempt.StepID == "first" {
			first = attempt
		}
	}
	if first.OutputRef == "" {
		t.Fatalf("first attempt = %+v, want stored output", first)
	}
	if artifact["step"] != "first" || artifact["attempt"] != 1 || artifact["ref"] != first.OutputRef ||
		artifact["bytes"] != len(firstOutput) || artifact["digest"] != first.OutputDigest {
		t.Fatalf("artifact = %#v, want step=first attempt=1 ref=%q bytes=%d digest=%q", artifact, first.OutputRef, len(firstOutput), first.OutputDigest)
	}
	preview, ok := artifact["preview"].(string)
	if !ok || len(preview) == 0 || len(preview) > 4<<10 {
		t.Fatalf("preview = %#v, want a bounded non-empty string", artifact["preview"])
	}
	if !strings.HasPrefix(preview, `{"content":"aaa`) {
		t.Fatalf("preview = %q, want the artifact prefix", preview)
	}
	if !strings.Contains(note, "workflow_inspect") {
		t.Fatalf("note = %q, want a workflow_inspect pointer", note)
	}
	// The envelope itself must fit the binding cap.
	envRaw, err := json.Marshal(env)
	if err != nil || len(envRaw) > 16000 {
		t.Fatalf("envelope = %d bytes, want <= 16000 (err=%v)", len(envRaw), err)
	}
}

// TestContextForStepKeepsInlineValuesByteIdentical pins backward
// compatibility: a prior-step output at or under the binding cap stays INLINE,
// byte-identical to the parsed value (compared via json.Marshal of the parsed
// value, not the raw bytes, because map key order is not stable), and carries
// no artifact/note envelope keys.
func TestContextForStepKeepsInlineValuesByteIdentical(t *testing.T) {
	raw := []byte(`{"ok":true,"nested":{"a":1,"b":2},"list":[1,2,3]}`)
	ref := "sha256:" + workflowledger.DigestHex(raw)
	repo := workflowledger.NewMemoryRepository()
	if err := repo.StoreContent(context.Background(), ref, raw); err != nil {
		t.Fatal(err)
	}
	attempts := []workflowledger.StepAttempt{{
		AttemptID: "wfa-plan-1", RunID: "wfr-inline", StepID: "plan", AttemptNo: 1,
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, OutputDigest: workflowledger.DigestHex(raw),
	}}
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), nil, nil, "wfr-inline", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	step := definition.Step{ID: "review", Kind: "agent", Context: []definition.ContextBinding{{From: "steps.plan.output", As: "plan", MaxBytes: 16000}}}
	_, evidence, err := ctrl.contextForStep(context.Background(), step, attempts)
	if err != nil {
		t.Fatalf("inline evidence must not error: %v", err)
	}
	value, ok := evidence["plan"]
	if !ok {
		t.Fatalf("evidence = %#v, want plan binding", evidence)
	}
	if m, isMap := value.(map[string]any); isMap {
		if _, hasArtifact := m["artifact"]; hasArtifact {
			t.Fatalf("inline value carried an artifact key: %#v", m)
		}
		if _, hasNote := m["note"]; hasNote {
			t.Fatalf("inline value carried a note key: %#v", m)
		}
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("inline evidence = %s, want %s", got, want)
	}
}

// TestValidateBindingLimitsMeasuresEnvelope pins the challenge-adopted
// threshold fix: validateBindingLimits measures the EVIDENCE VALUE (the
// reference envelope substituted by contextForStep) instead of re-loading the
// original artifact bytes, so an enveloped oversized artifact passes the
// 16,000-byte cap; the inputs path keeps measuring the input value and still
// rejects an oversized input ("exceeds 1 bytes", pinned at
// linear_changed_paths_test.go:231).
func TestValidateBindingLimitsMeasuresEnvelope(t *testing.T) {
	raw := json.RawMessage(`{"content":"` + strings.Repeat("a", 35042) + `"}`)
	if len(raw) != 35056 {
		t.Fatalf("artifact = %d bytes, want 35056", len(raw))
	}
	ref := "sha256:" + workflowledger.DigestHex(raw)
	repo := workflowledger.NewMemoryRepository()
	if err := repo.StoreContent(context.Background(), ref, raw); err != nil {
		t.Fatal(err)
	}
	attempts := []workflowledger.StepAttempt{{
		AttemptID: "wfa-plan_tests-2", RunID: "wfr-validate", StepID: "plan_tests", AttemptNo: 2,
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, OutputDigest: workflowledger.DigestHex(raw),
	}}
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), nil, nil, "wfr-validate", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	evidenceStep := definition.Step{ID: "review", Kind: "agent", Context: []definition.ContextBinding{{From: "steps.plan_tests.output", As: "test_plan", MaxBytes: 16000}}}
	_, evidence, err := ctrl.contextForStep(context.Background(), evidenceStep, attempts)
	if err != nil {
		t.Fatalf("contextForStep must envelope the oversized artifact, not error: %v", err)
	}
	if err := validateBindingLimits(evidenceStep, map[string]any{}, evidence); err != nil {
		t.Fatalf("enveloped evidence must pass the 16K binding cap: %v", err)
	}
	inputStep := definition.Step{ID: "first", Kind: "agent", Context: []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 1}}}
	err = validateBindingLimits(inputStep, map[string]any{"task": "build"}, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "exceeds 1 bytes") {
		t.Fatalf("oversized input = %v, want rejection containing %q", err, "exceeds 1 bytes")
	}
}
