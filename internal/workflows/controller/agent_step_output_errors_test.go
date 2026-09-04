package controller

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
)

// TestSchemaValidationErrorMessageWithoutCause pins the degenerate-receiver
// wording of SchemaValidationError.Error: a nil receiver or a nil cause has no
// step identity to name, so the message must be the generic sentence rather
// than a %!q(MISSING)-style formatting of an absent step.
func TestSchemaValidationErrorMessageWithoutCause(t *testing.T) {
	const generic = "workflow step output schema validation failed"

	var nilErr *SchemaValidationError
	if got := nilErr.Error(); got != generic {
		t.Fatalf("nil receiver Error() = %q, want %q", got, generic)
	}
	if got := (&SchemaValidationError{StepID: "plan"}).Error(); got != generic {
		t.Fatalf("nil-cause Error() = %q, want %q", got, generic)
	}
	// The populated form still names the step, so the branch above is a real
	// alternative and not the only reachable wording.
	withCause := &SchemaValidationError{StepID: "plan", Err: errors.New("boom")}
	if got := withCause.Error(); got != `workflow step "plan" output schema validation failed` {
		t.Fatalf("populated Error() = %q", got)
	}
	if !errors.Is(withCause.Unwrap(), withCause.Err) {
		t.Fatalf("Unwrap() = %v, want the wrapped cause", withCause.Unwrap())
	}
}

// TestExtractTaskOutputNonObjectPayloadPassesThrough pins that a payload which
// is not a JSON object (or is not JSON at all) is returned byte-for-byte
// instead of being swallowed: plain verifier/gate output must survive the
// envelope unwrap untouched.
func TestExtractTaskOutputNonObjectPayloadPassesThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"json string", `"just a report"`},
		{"json array", `[{"output":"x","status":"completed"}]`},
		{"not json at all", `not json {`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := extractTaskOutput(json.RawMessage(tc.raw))
			if string(got) != tc.raw {
				t.Fatalf("extractTaskOutput(%s) = %s, want the input unchanged", tc.raw, got)
			}
		})
	}
	// A real envelope IS unwrapped, so the pass-through above is a genuine
	// branch rather than the function's only behaviour.
	env := json.RawMessage(`{"output":{"verdict":"pass"},"status":"completed"}`)
	if got := string(extractTaskOutput(env)); got != `{"verdict":"pass"}` {
		t.Fatalf("envelope unwrap = %s, want the inner output", got)
	}
}

// TestValidateOutputEmptyPayloadIsSchemaValidationError pins the empty-output
// gate: a step that produced nothing must fail closed as a
// SchemaValidationError wrapping jschema.ErrValidation, not pass as a nil
// value.
func TestValidateOutputEmptyPayloadIsSchemaValidationError(t *testing.T) {
	value, err := validateOutput("plan", nil, nil)
	if value != nil {
		t.Fatalf("value = %v, want nil for empty output", value)
	}
	var schemaErr *SchemaValidationError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("err = %v (%T), want *SchemaValidationError", err, err)
	}
	if schemaErr.StepID != "plan" {
		t.Fatalf("StepID = %q, want %q", schemaErr.StepID, "plan")
	}
	if !errors.Is(err, jschema.ErrValidation) {
		t.Fatalf("err = %v, want it to wrap jschema.ErrValidation", err)
	}
}

// TestValidateOutputSchemalessInvalidJSON pins the no-schema path's own JSON
// gate: with no declared schema the payload is still parsed, and malformed
// bytes fail closed as a SchemaValidationError naming invalid JSON.
func TestValidateOutputSchemalessInvalidJSON(t *testing.T) {
	value, err := validateOutput("audit", json.RawMessage(`{"unterminated":`), nil)
	if value != nil {
		t.Fatalf("value = %v, want nil", value)
	}
	var schemaErr *SchemaValidationError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("err = %v (%T), want *SchemaValidationError", err, err)
	}
	if schemaErr.StepID != "audit" {
		t.Fatalf("StepID = %q, want %q", schemaErr.StepID, "audit")
	}
	if !errors.Is(err, jschema.ErrValidation) {
		t.Fatalf("err = %v, want it to wrap jschema.ErrValidation", err)
	}
	if !strings.Contains(err.Error(), "invalid JSON") && !strings.Contains(schemaErr.Err.Error(), "invalid JSON") {
		t.Fatalf("cause = %v, want it to name invalid JSON", schemaErr.Err)
	}
}

// TestValidateOutputUncompilableSchemaIsNotSchemaValidationError pins the
// distinction the classifier depends on: a schema the host cannot compile is
// an operator/authoring fault, so it must surface as a plain wrapped
// admission error and must NOT be reported as the step's schema violation.
func TestValidateOutputUncompilableSchemaIsNotSchemaValidationError(t *testing.T) {
	bad := map[string]any{"type": "not-a-json-schema-type"}
	value, err := validateOutput("gate", json.RawMessage(`{"ok":true}`), bad)
	if value != nil {
		t.Fatalf("value = %v, want nil", value)
	}
	if err == nil {
		t.Fatal("validateOutput accepted an uncompilable schema; want a compile error")
	}
	var schemaErr *SchemaValidationError
	if errors.As(err, &schemaErr) {
		t.Fatalf("err = %v, want a compile error, not *SchemaValidationError", err)
	}
	if !strings.HasPrefix(err.Error(), "compile step output schema: ") {
		t.Fatalf("err = %q, want the compile-step-output-schema prefix", err.Error())
	}
	if !errors.Is(err, jschema.ErrAdmission) {
		t.Fatalf("err = %v, want it to wrap jschema.ErrAdmission", err)
	}
}

// TestValidateReportEvidenceNoClaimsPasses pins that a report with no evidence
// claims is accepted with an empty tool history: the cross-check may only
// reject claims that were actually made.
func TestValidateReportEvidenceNoClaimsPasses(t *testing.T) {
	report := "# Agent Report\n\nFormat: mivia-report/v1\n\n## Summary\n\nNothing was run.\n"
	if err := ValidateReportEvidence(report, nil); err != nil {
		t.Fatalf("ValidateReportEvidence with no claims = %v, want nil", err)
	}
	// A claim against an empty history DOES fail, so the pass above comes from
	// the no-claims short circuit and not from a vacuous validator.
	claimed := "# Agent Report\n\nFormat: mivia-report/v1\n\n## Evidence\n\n- make verify: PASS\n"
	if err := ValidateReportEvidence(claimed, nil); err == nil {
		t.Fatal("ValidateReportEvidence accepted an unexecuted claim against an empty history")
	}
}

// TestValidateEvidenceClaimsSectionEndsAtNextHeading pins the section scope of
// the malformed-evidence check: a "- : PASS" line BELOW the evidence section's
// closing heading is prose in another section, not a malformed evidence claim,
// so it must not fail the step. The same line INSIDE the section must fail.
func TestValidateEvidenceClaimsSectionEndsAtNextHeading(t *testing.T) {
	outside := "Format: mivia-report/v1\n" +
		"## Evidence\n" +
		"- make verify: PASS\n" +
		"## Residual risk\n" +
		"- : PASS\n"
	if err := validateEvidenceClaims("audit", mustJSONString(t, outside)); err != nil {
		t.Fatalf("a malformed-looking line after the evidence heading must not fail; got %v", err)
	}

	inside := "Format: mivia-report/v1\n" +
		"## Evidence\n" +
		"- : PASS\n"
	err := validateEvidenceClaims("audit", mustJSONString(t, inside))
	if err == nil {
		t.Fatal("a malformed evidence line inside the section must fail")
	}
	if !strings.Contains(err.Error(), `step "audit" report contains malformed evidence line`) {
		t.Fatalf("err = %q, want it to name the step and the malformed line", err.Error())
	}
}

// TestExtractReportTextFromOutputKey pins that a report delivered under the
// "output" key (not "report") is still read by the evidence check, so a
// malformed evidence line there is not silently skipped.
func TestExtractReportTextFromOutputKey(t *testing.T) {
	body := "Format: mivia-report/v1\n## Evidence\n- : PASS\n"
	payload, err := json.Marshal(map[string]any{"output": body})
	if err != nil {
		t.Fatal(err)
	}
	if got := extractReportText(payload); got != body {
		t.Fatalf("extractReportText = %q, want the output string %q", got, body)
	}
	verr := validateEvidenceClaims("audit", payload)
	if verr == nil {
		t.Fatal("a malformed evidence line under the output key must fail")
	}
	if !strings.Contains(verr.Error(), "malformed evidence line") {
		t.Fatalf("err = %q, want the malformed-evidence wording", verr.Error())
	}
	// A payload with neither key yields no text, so the branch above is real.
	if got := extractReportText(json.RawMessage(`{"other":"x"}`)); got != "" {
		t.Fatalf("extractReportText with no report/output key = %q, want empty", got)
	}
}

func mustJSONString(t *testing.T, s string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
