package presentation

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// FormatWorkflowShow formats a compiled workflow for detailed CLI display.
func FormatWorkflowShow(c *compiler.CompiledWorkflow) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Name:        %s\n", c.Name))
	b.WriteString(fmt.Sprintf("Description: %s\n", c.Description))
	b.WriteString(fmt.Sprintf("Version:     %d\n", c.Version))
	b.WriteString(fmt.Sprintf("Initial:     %s\n", c.InitialStep))
	formatInputs(&b, c)
	formatLimits(&b, c)
	formatSteps(&b, c)
	formatTransitions(&b, c)
	formatDelivery(&b, c)
	return b.String()
}

func formatInputs(b *strings.Builder, c *compiler.CompiledWorkflow) {
	if len(c.Inputs) == 0 {
		return
	}
	b.WriteString("\nInputs:\n")
	for name, inp := range c.Inputs {
		req := "optional"
		if inp.Required {
			req = "required"
		}
		b.WriteString(fmt.Sprintf("  %s (%s, %s", name, inp.Type, req))
		if inp.MaxBytes > 0 {
			b.WriteString(fmt.Sprintf(", max %d bytes", inp.MaxBytes))
		}
		b.WriteString(")\n")
	}
}

func formatLimits(b *strings.Builder, c *compiler.CompiledWorkflow) {
	if c.Limits.MaxStepAttempts == 0 && c.Limits.MaxDurationSeconds == 0 {
		return
	}
	b.WriteString("\nLimits:\n")
	if c.Limits.MaxStepAttempts > 0 {
		b.WriteString(fmt.Sprintf("  max_step_attempts:    %d\n", c.Limits.MaxStepAttempts))
	}
	if c.Limits.MaxDurationSeconds > 0 {
		b.WriteString(fmt.Sprintf("  max_duration_seconds: %d\n", c.Limits.MaxDurationSeconds))
	}
}

func formatSteps(b *strings.Builder, c *compiler.CompiledWorkflow) {
	b.WriteString(fmt.Sprintf("\nSteps (%d):\n", len(c.Steps)))
	for _, s := range c.Steps {
		onFail := ""
		if s.OnFailure != "" {
			onFail = fmt.Sprintf(", on_failure=%s", s.OnFailure)
		}
		b.WriteString(fmt.Sprintf("  %s [%s]%s\n", s.ID, s.Kind, onFail))
		if s.Agent != "" {
			b.WriteString(fmt.Sprintf("    agent: %s\n", s.Agent))
		}
		if s.Verifier != "" {
			b.WriteString(fmt.Sprintf("    verifier: %s\n", s.Verifier))
		}
		if s.Template != "" {
			b.WriteString(fmt.Sprintf("    template: %s\n", s.Template))
		}
		if s.OutputSchema != "" {
			b.WriteString(fmt.Sprintf("    output_schema: %s\n", s.OutputSchema))
		}
		if len(s.Context) > 0 {
			b.WriteString("    context:\n")
			for _, cb := range s.Context {
				maxB := ""
				if cb.MaxBytes > 0 {
					maxB = fmt.Sprintf(", max_bytes=%d", cb.MaxBytes)
				}
				b.WriteString(fmt.Sprintf("      %s → %s%s\n", cb.From, cb.As, maxB))
			}
		}
	}
}

func formatTransitions(b *strings.Builder, c *compiler.CompiledWorkflow) {
	b.WriteString(fmt.Sprintf("\nTransitions (%d):\n", len(c.Transitions)))
	for _, t := range c.Transitions {
		loopInfo := ""
		if t.Loop != "" {
			if t.MaxIterations == definition.UnlimitedIterations {
				loopInfo = fmt.Sprintf(", loop=%s (unlimited)", t.Loop)
			} else {
				loopInfo = fmt.Sprintf(", loop=%s (max %d)", t.Loop, t.MaxIterations)
			}
		}
		matchParts := []string{fmt.Sprintf("status=%s", t.Match.Status)}
		for k, v := range t.Match.Output {
			matchParts = append(matchParts, fmt.Sprintf("%s=%s", k, v))
		}
		b.WriteString(fmt.Sprintf("  %s → %s [%s]%s\n", t.From, t.To, strings.Join(matchParts, ", "), loopInfo))
	}
}

func formatDelivery(b *strings.Builder, c *compiler.CompiledWorkflow) {
	if c.Delivery == nil {
		return
	}
	b.WriteString("\nDelivery:\n")
	b.WriteString(fmt.Sprintf("  kind:   %s\n", c.Delivery.Kind))
	if c.Delivery.Mode != "" {
		b.WriteString(fmt.Sprintf("  mode:   %s\n", c.Delivery.Mode))
	}
	if c.Delivery.Provider != "" {
		b.WriteString(fmt.Sprintf("  provider: %s\n", c.Delivery.Provider))
	}
	if c.Delivery.Base != "" {
		b.WriteString(fmt.Sprintf("  base:   %s\n", c.Delivery.Base))
	}
}

// FormatWorkflowValidate formats a validation result for a single workflow.
func FormatWorkflowValidate(name string, compiled *compiler.CompiledWorkflow, compileErr error) string {
	if compileErr != nil {
		return fmt.Sprintf("✗ %s: invalid\n  %s\n", name, compileErr)
	}
	return fmt.Sprintf("✓ %s: valid\n", name)
}
