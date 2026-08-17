package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func maxBinding(step definition.Step) int {
	max := 0
	for _, b := range step.Context {
		if b.MaxBytes > max {
			max = b.MaxBytes
		}
	}
	return max
}

// validateBindingLimits measures every context binding's resolved value against
// its max_bytes. Evidence bindings measure the EVIDENCE VALUE already built by
// contextForStep — the inlined value or the reference envelope — never the
// original artifact bytes: an enveloped artifact passes the binding cap, or the
// envelope substitution in contextForStep would be pointless. Inputs bindings
// keep reading the controller's inputs unchanged.
func validateBindingLimits(step definition.Step, inputs map[string]any, evidence map[string]any) error {
	for _, binding := range step.Context {
		if binding.MaxBytes <= 0 {
			continue
		}
		// A delivery.failure binding is bounded by contextForStep's rune-safe
		// truncation; re-measuring it here would reject the already-truncated
		// text against the same cap (JSON quoting adds two bytes).
		if strings.HasPrefix(binding.From, "delivery.") {
			continue
		}
		// An implement.touched_files binding resolves into evidence, not
		// inputs, despite being a 2-part "prefix.name" path like inputs.X -
		// measure it via the evidence branch below, not the inputs lookup.
		if strings.HasPrefix(binding.From, "implement.") {
			raw, err := json.Marshal(evidence[binding.As])
			if err != nil {
				return fmt.Errorf("marshal context binding %q: %w", binding.From, err)
			}
			if len(raw) > binding.MaxBytes {
				return fmt.Errorf("context binding %q exceeds %d bytes", binding.From, binding.MaxBytes)
			}
			continue
		}
		var value any
		parts := strings.Split(binding.From, ".")
		if len(parts) == 2 && parts[0] == "inputs" {
			value = inputs[parts[1]]
		} else {
			value = evidence[binding.As]
			if binding.Optional {
				// A missing optional prior output resolves to "" (contextForStep);
				// never reject it against a tiny max_bytes on the first attempt.
				if s, ok := value.(string); ok && s == "" {
					continue
				}
			}
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshal context binding %q: %w", binding.From, err)
		}
		if len(raw) > binding.MaxBytes {
			return fmt.Errorf("context binding %q exceeds %d bytes", binding.From, binding.MaxBytes)
		}
	}
	return nil
}

func cloneValues(values map[string]any) map[string]any {
	raw, _ := json.Marshal(values)
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&out)
	return out
}
