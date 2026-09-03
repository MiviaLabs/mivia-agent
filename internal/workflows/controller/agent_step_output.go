package controller

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/evidencecheck"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// SchemaValidationError marks output that fails the declared step schema.
type SchemaValidationError struct {
	StepID string
	Err    error
}

func (e *SchemaValidationError) Error() string {
	if e == nil || e.Err == nil {
		return "workflow step output schema validation failed"
	}
	return fmt.Sprintf("workflow step %q output schema validation failed", e.StepID)
}

func (e *SchemaValidationError) Unwrap() error { return e.Err }

// applyChildResult copies the child task's terminal status and output onto a
// step result. Output is extracted from the result envelope like the success
// path does.
func applyChildResult(out *AgentStepResult, res subagents.Result) {
	out.Status = res.Status
	if len(res.Output) > 0 {
		out.Output = extractTaskOutput(res.Output)
	}
}

// extractTaskOutput is the controller-wide mechanism for turning one
// coordinator task result's Output into the payload the workflow step's
// schema governs. Agent handlers (agentTaskHandler/MultiStepHandler) return a
// transport envelope - {"output": <model reply>, "status": "completed",
// "schema": "ok"?, "steps": N, "elapsed": "...", "step_count": N} - and the
// CLI tool surface deliberately exposes that envelope (elapsed/steps/schema).
// Every workflow controller consumer that validates or decodes a task result
// as step-schema JSON MUST unwrap it through this function first; decoding
// the envelope directly silently skips its fields as unknown (e.g. a panel
// member report decoding to verdict ""). Non-envelope payloads pass through
// untouched, so plain verifier/gate JSON is unaffected.
func extractTaskOutput(raw json.RawMessage) json.RawMessage {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	_, hasStatus := envelope["status"]
	_, hasSchema := envelope["schema"]
	if (hasStatus || hasSchema) && envelope["output"] != nil {
		return append(json.RawMessage(nil), envelope["output"]...)
	}
	return append(json.RawMessage(nil), raw...)
}

func cloneSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	raw, _ := json.Marshal(schema)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func validateOutput(stepID string, raw json.RawMessage, schema map[string]any) (any, error) {
	if len(raw) == 0 {
		return nil, &SchemaValidationError{StepID: stepID, Err: jschema.ErrValidation}
	}
	if err := validateEvidenceClaims(stepID, raw); err != nil {
		return nil, &SchemaValidationError{StepID: stepID, Err: err}
	}
	if schema == nil {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, &SchemaValidationError{StepID: stepID, Err: fmt.Errorf("%w: invalid JSON", jschema.ErrValidation)}
		}
		return value, nil
	}
	compiled, err := jschema.Compile(schema)
	if err != nil {
		return nil, fmt.Errorf("compile step output schema: %w", err)
	}
	value, err := compiled.ValidateJSONBytes(raw)
	if err != nil {
		return nil, &SchemaValidationError{StepID: stepID, Err: err}
	}
	return value, nil
}

// ValidateReportEvidence cross-checks report claims against recorded tool executions.
func ValidateReportEvidence(reportText string, history []evidencecheck.ToolExecutionRecord) error {
	claims := evidencecheck.ParseClaims(reportText)
	if len(claims) == 0 {
		return nil
	}
	rep := evidencecheck.Validate(claims, history)
	return rep.Error()
}

func validateEvidenceClaims(stepID string, raw json.RawMessage) error {
	text := extractReportText(raw)
	if text == "" || !strings.Contains(text, "mivia-report/v1") {
		return nil
	}
	lines := strings.Split(text, "\n")
	inEvidence := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "evidence:") || strings.HasPrefix(lower, "## evidence") {
			inEvidence = true
			continue
		}
		if inEvidence && strings.HasPrefix(lower, "#") {
			inEvidence = false
		}
		if inEvidence && (strings.Contains(trimmed, "PASS") || strings.Contains(trimmed, "FAIL")) {
			if strings.HasPrefix(trimmed, "- :") || strings.HasPrefix(trimmed, "* :") || strings.HasPrefix(trimmed, "- PASS") || strings.HasPrefix(trimmed, "* PASS") {
				return fmt.Errorf("step %q report contains malformed evidence line %q", stepID, trimmed)
			}
		}
	}
	return nil
}

func extractReportText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		if report, ok := m["report"].(string); ok {
			return report
		}
		if output, ok := m["output"].(string); ok {
			return output
		}
	}
	return ""
}
