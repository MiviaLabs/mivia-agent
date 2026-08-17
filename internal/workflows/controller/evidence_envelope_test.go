package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
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
	_, evidence, _, err := ctrl.contextForStep(context.Background(), step, attempts)
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

// TestContextForStepEnvelopeOnlyAlwaysEnvelopes pins the Step-5 audit fix: an
// envelope_only steps.<id>.output binding ALWAYS resolves to a ledger-backed
// reference envelope when the prior output is non-empty — even when the content
// is small enough to inline under the binding cap (the operator directive that
// findings always pass as ledger refs). The cap applies to the MARSHALED
// envelope (the existing fit invariant), and the test uses production-sized
// run_id/ref/digest values so the reference envelope skeleton must fit under
// the 4096 cap. The same payload under a plain (non-envelope_only) binding must
// still inline — the flag is opt-in, default false.
func TestContextForStepEnvelopeOnlyAlwaysEnvelopes(t *testing.T) {
	// A SMALL findings payload: comfortably under the 4096 cap, so without the
	// flag it would inline verbatim (the pre-fix contradiction of the operator
	// directive that findings always pass as ledger refs).
	payload := json.RawMessage(`{"verdict":"changes_requested","findings":[{"id":"R0-f1","severity":"high","reason":"x"},{"id":"R0-f2","severity":"medium","reason":"y"}]}`)
	if len(payload) > 4096 {
		t.Fatalf("payload = %d bytes, want a small findings payload under the 4096 cap", len(payload))
	}
	// Production-sized identifiers: ref/digest "sha256:" + 64 hex chars.
	ref := "sha256:" + workflowledger.DigestHex(payload)
	repo := workflowledger.NewMemoryRepository()
	if err := repo.StoreContent(context.Background(), ref, payload); err != nil {
		t.Fatal(err)
	}
	attempts := []workflowledger.StepAttempt{{
		AttemptID: "wfa-plan_review-1", RunID: "wfr-envelope-only", StepID: "plan_review", AttemptNo: 1,
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, OutputDigest: workflowledger.DigestHex(payload),
	}}
	// Production-sized run_id: "wfr-" + 16 base32 chars (see linear_ids.go).
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), nil, nil, newWorkflowRunID(), []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	envStep := definition.Step{ID: "plan", Kind: "agent", Context: []definition.ContextBinding{
		{From: "steps.plan_review.output", As: "review_findings", MaxBytes: 4096, Optional: true, EnvelopeOnly: true},
	}}
	_, evidence, refs, err := ctrl.contextForStep(context.Background(), envStep, attempts)
	if err != nil {
		t.Fatalf("envelope_only binding must not error: %v", err)
	}
	env, ok := evidence["review_findings"].(map[string]any)
	if !ok {
		t.Fatalf("evidence[review_findings] = %#v (%T), want a reference envelope, NOT the inline payload", evidence["review_findings"], evidence["review_findings"])
	}
	artifact, ok := env["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("envelope = %#v, want an artifact key", env)
	}
	if artifact["step"] != "plan_review" || artifact["attempt"] != 1 || artifact["ref"] != ref ||
		artifact["bytes"] != len(payload) || artifact["digest"] != workflowledger.DigestHex(payload) {
		t.Fatalf("artifact = %#v, want step=plan_review attempt=1 ref=%q bytes=%d digest=%q", artifact, ref, len(payload), workflowledger.DigestHex(payload))
	}
	marshaled, err := json.Marshal(env)
	if err != nil || len(marshaled) > 4096 {
		t.Fatalf("marshaled envelope = %d bytes, want <= 4096 (err=%v)", len(marshaled), err)
	}
	if entry, ok := refs["review_findings"]; !ok || entry.Ref != ref || entry.Step != "plan_review" || entry.Attempt != 1 {
		t.Fatalf("refs[review_findings] = %+v (ok=%v), want the plan_review artifact ref", entry, ok)
	}
	// Contrast: the SAME payload under a plain binding (EnvelopeOnly false) must
	// inline — the flag is opt-in and every other binding is unchanged.
	plainStep := definition.Step{ID: "plan", Kind: "agent", Context: []definition.ContextBinding{
		{From: "steps.plan_review.output", As: "review_findings", MaxBytes: 4096, Optional: true},
	}}
	_, plainEvidence, _, err := ctrl.contextForStep(context.Background(), plainStep, attempts)
	if err != nil {
		t.Fatalf("plain binding must not error: %v", err)
	}
	if m, isMap := plainEvidence["review_findings"].(map[string]any); isMap {
		if _, hasArtifact := m["artifact"]; hasArtifact {
			t.Fatalf("plain binding enveloped a payload that fits its cap: %#v", m)
		}
	}
	var parsed any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(plainEvidence["review_findings"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("plain binding evidence = %s, want the inline payload %s", got, want)
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
	_, evidence, _, err := ctrl.contextForStep(context.Background(), evidenceStep, attempts)
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

// TestValidateBindingLimitsMeasuresRunSalvageEvidence pins W-1: a run.salvage
// binding is resolved into the EVIDENCE map by contextForStep (linear_context.go),
// but the generic 2-part branch of validateBindingLimits read inputs["salvage"]
// (nil -> json "null", 4 bytes), so a declared max_bytes on a partial_target
// step was silently bypassed and the step context could inflate past its cap.
// The binding must be measured against evidence[As] like every non-inputs
// source.
func TestValidateBindingLimitsMeasuresRunSalvageEvidence(t *testing.T) {
	salvaged := []SalvagedAttempt{
		{StepID: "plan", AttemptNo: 1, OutputRef: "sha256:abc", OutputDigest: "sha256:def"},
		{StepID: "plan_tests", AttemptNo: 2, OutputRef: "sha256:ghi", OutputDigest: "sha256:jkl"},
		{StepID: "review", AttemptNo: 1, OutputRef: "sha256:mno", OutputDigest: "sha256:pqr"},
	}
	raw, err := json.Marshal(salvaged)
	if err != nil {
		t.Fatal(err)
	}
	// Negative path: an oversized salvage payload under a declared cap must be
	// rejected. The inputs map is empty — run.salvage is not an input, and the
	// pre-fix 2-part branch measured inputs["salvage"] (nil, json "null" = 4
	// bytes) instead of the resolved evidence value.
	step := definition.Step{ID: "partial_target", Context: []definition.ContextBinding{
		{From: "run.salvage", As: "salvage", MaxBytes: 64},
	}}
	err = validateBindingLimits(step, map[string]any{}, map[string]any{"salvage": string(raw)})
	if err == nil || !strings.Contains(err.Error(), "exceeds 64 bytes") {
		t.Fatalf("oversized run.salvage evidence = %v, want rejection containing %q", err, "exceeds 64 bytes")
	}
	// Positive path: a within-cap value passes, and max_bytes<=0 skips
	// measurement entirely.
	within := definition.Step{ID: "partial_target", Context: []definition.ContextBinding{
		{From: "run.salvage", As: "salvage", MaxBytes: 4096},
	}}
	if err := validateBindingLimits(within, map[string]any{}, map[string]any{"salvage": `[]`}); err != nil {
		t.Fatalf("within-cap run.salvage evidence must pass: %v", err)
	}
	unbounded := definition.Step{ID: "partial_target", Context: []definition.ContextBinding{
		{From: "run.salvage", As: "salvage", MaxBytes: 0},
	}}
	if err := validateBindingLimits(unbounded, map[string]any{}, map[string]any{"salvage": string(raw)}); err != nil {
		t.Fatalf("max_bytes<=0 must skip measurement: %v", err)
	}
}

// TestBuildEvidenceEnvelopeFitsUnderHTMLEscaping pins the plan v3 P1 fix:
// json.Marshal HTML-escapes & < > into \u0026 \u003c \u003e (6 bytes each), so
// a 4KiB preview dense with URL/HTML characters can inflate the MARSHALED
// envelope past the binding cap. buildEvidenceEnvelope must measure the
// skeleton exactly, budget the preview, and halve it until the marshaled
// envelope fits the 16,000-byte cap.
func TestBuildEvidenceEnvelopeFitsUnderHTMLEscaping(t *testing.T) {
	const cap = 16000
	raw := []byte("<a href=\"https://ex.test/?a=1&b=2&c=3\">" + strings.Repeat("&", 4<<10) + "</a>")
	prior := workflowledger.StepAttempt{
		StepID: "first", AttemptNo: 1, OutputRef: "sha256:abc", OutputDigest: "sha256:def",
	}
	env, err := buildEvidenceEnvelope("wfr-html", prior, raw, cap)
	if err != nil {
		t.Fatalf("buildEvidenceEnvelope: %v", err)
	}
	marshaled, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(marshaled) > cap {
		t.Fatalf("marshaled envelope = %d bytes, want <= %d", len(marshaled), cap)
	}
	artifact, ok := env["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("envelope = %#v, want an artifact key", env)
	}
	preview, _ := artifact["preview"].(string)
	if len(preview) == 0 || len(preview) >= evidencePreviewBytes {
		t.Fatalf("preview = %d bytes, want a halved preview strictly < %d (HTML escaping must have forced a shrink)",
			len(preview), evidencePreviewBytes)
	}
}

// TestBuildEvidenceEnvelopeFitsRedactionInflation pins the redact-then-
// truncate ordering: a text full of redaction-pattern matches grows when each
// match becomes "[redacted]", and that growth must stay inside the preview
// budget instead of pushing the marshaled envelope over the cap. Truncating
// first would inflate a 4KiB raw preview to ~40KiB of placeholders; redacting
// first caps the preview at its budget.
func TestBuildEvidenceEnvelopeFitsRedactionInflation(t *testing.T) {
	prev := redact.Current()
	policy, err := redact.Compile([]string{"a"}, nil, redact.DefaultPlaceholder)
	if err != nil {
		t.Fatal(err)
	}
	redact.SetPolicy(policy)
	defer redact.SetPolicy(prev)

	const cap = 16000
	raw := []byte(strings.Repeat("a", 4<<10))
	prior := workflowledger.StepAttempt{
		StepID: "first", AttemptNo: 1, OutputRef: "sha256:abc", OutputDigest: "sha256:def",
	}
	env, err := buildEvidenceEnvelope("wfr-redact", prior, raw, cap)
	if err != nil {
		t.Fatalf("buildEvidenceEnvelope: %v", err)
	}
	marshaled, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(marshaled) > cap {
		t.Fatalf("marshaled envelope = %d bytes, want <= %d", len(marshaled), cap)
	}
	artifact, ok := env["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("envelope = %#v, want an artifact key", env)
	}
	preview, _ := artifact["preview"].(string)
	if !strings.Contains(preview, "[redacted]") {
		t.Fatalf("preview = %q, want redacted content", preview)
	}
	if len(preview) > evidencePreviewBytes {
		t.Fatalf("preview = %d bytes, want <= %d even after placeholder growth", len(preview), evidencePreviewBytes)
	}
}

// TestEvidenceEnvelopePreviewRuneSafeAndBounded pins evidencePreview's
// guarantees: the result is at most max bytes, is valid UTF-8 even when the
// raw bytes end mid-rune or are invalid, and cuts only on a rune boundary.
func TestEvidenceEnvelopePreviewRuneSafeAndBounded(t *testing.T) {
	// Two-byte runes: 4096 is an even byte count, so a full-budget preview is
	// exactly 2048 runes and stays on a rune boundary.
	preview := evidencePreview([]byte(strings.Repeat("é", 5000)), evidencePreviewBytes)
	if len(preview) > evidencePreviewBytes {
		t.Fatalf("preview = %d bytes, want <= %d", len(preview), evidencePreviewBytes)
	}
	if !utf8.ValidString(preview) {
		t.Fatalf("preview is not valid UTF-8: %q", preview)
	}
	if len(preview) != evidencePreviewBytes {
		t.Fatalf("preview = %d bytes, want exactly %d (a rune boundary)", len(preview), evidencePreviewBytes)
	}

	// A 3-byte rune spanning the truncation point must not be cut in half.
	midRune := []byte(strings.Repeat("é", 2047) + "€") // 3-byte rune straddles byte 4096
	preview = evidencePreview(midRune, evidencePreviewBytes)
	if !utf8.ValidString(preview) || len(preview) > evidencePreviewBytes {
		t.Fatalf("mid-rune preview = %q (%d bytes), want valid UTF-8 within %d bytes", preview, len(preview), evidencePreviewBytes)
	}

	// Invalid bytes are sanitized to U+FFFD before truncation, so the preview
	// is always valid UTF-8 within the budget.
	dirty := append([]byte("x"), 0xFF, 0xFE, 0x80)
	preview = evidencePreview(dirty, 4)
	if !utf8.ValidString(preview) || len(preview) > 4 {
		t.Fatalf("dirty preview = %q (%d bytes), want <= 4 valid UTF-8 bytes", preview, len(preview))
	}
}

// TestBuildEvidenceEnvelopeNoPreviewFallback pins the budget floor: a cap just
// above the exact no-preview skeleton leaves no preview budget, so the preview
// field is omitted entirely and the envelope still fits; a cap below the
// skeleton cannot fit even the no-preview envelope and returns an error.
func TestBuildEvidenceEnvelopeNoPreviewFallback(t *testing.T) {
	raw := []byte(strings.Repeat("x", 4096))
	prior := workflowledger.StepAttempt{
		StepID: "first", AttemptNo: 1, OutputRef: "sha256:abc", OutputDigest: "sha256:def",
	}
	const runID = "wfr-nopreview"
	// The skeleton is the TRUE no-preview size: the preview key is absent (audit
	// fix: measuring it with "preview":"" overstated the size by 12 bytes and
	// rejected caps that fit the ref-only envelope).
	skeleton, err := json.Marshal(map[string]any{
		"artifact": map[string]any{
			"step": "first", "attempt": 1, "ref": "sha256:abc",
			"bytes": len(raw), "digest": "sha256:def",
		},
		"note": "full artifact is in the workflow ledger; read it with workflow_inspect(run_id=" + runID + ", step=<step>, attempt=<attempt>)",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, cap := range []int{len(skeleton) + 1, len(skeleton)} {
		env, err := buildEvidenceEnvelope(runID, prior, raw, cap)
		if err != nil {
			t.Fatalf("cap %d (>= true no-preview size %d) must not error: %v", cap, len(skeleton), err)
		}
		artifact, ok := env["artifact"].(map[string]any)
		if !ok {
			t.Fatalf("envelope = %#v, want an artifact key", env)
		}
		if _, hasPreview := artifact["preview"]; hasPreview {
			t.Fatalf("artifact = %#v, want the preview omitted when the budget is exhausted", artifact)
		}
		marshaled, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if len(marshaled) > cap {
			t.Fatalf("no-preview envelope = %d bytes, want <= cap %d", len(marshaled), cap)
		}
	}

	if _, err := buildEvidenceEnvelope(runID, prior, raw, len(skeleton)-1); err == nil {
		t.Fatalf("cap below the true no-preview size must error, got nil")
	}
}
