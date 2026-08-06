package cli

import (
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// resolveTaskSchemas picks task > skill > agent > none and admits each schema
// (compile caps, no remote $ref) before spawn cost. Only nil means "absent":
// an empty object {} is a valid schema and must survive the resolution chain.
func resolveTaskSchemas(taskOut, taskIn map[string]any, route taskRoute, skillReg *skills.Registry) (out, in map[string]any, err error) {
	out = taskOut
	in = taskIn
	if out == nil && route.skill != "" && skillReg != nil {
		if def, ok := skillReg.Get(route.skill); ok && def.OutputSchema != nil {
			out = def.OutputSchema
		}
	}
	if out == nil && route.agent.OutputSchema != nil {
		out = route.agent.OutputSchema
	}
	if in == nil && route.skill != "" && skillReg != nil {
		if def, ok := skillReg.Get(route.skill); ok && def.InputSchema != nil {
			in = def.InputSchema
		}
	}
	if in == nil && route.agent.InputSchema != nil {
		in = route.agent.InputSchema
	}
	if out != nil {
		if _, err := jschema.Compile(out); err != nil {
			return nil, nil, fmt.Errorf("output_schema: %w", err)
		}
	}
	if in != nil {
		if _, err := jschema.Compile(in); err != nil {
			return nil, nil, fmt.Errorf("input_schema: %w", err)
		}
	}
	return out, in, nil
}

func validateTaskInput(schema map[string]any, input json.RawMessage) error {
	compiled, err := jschema.Compile(schema)
	if err != nil {
		return err
	}
	// Task.Input is typically a JSON-encoded string (the prompt).
	var prompt string
	if err := json.Unmarshal(input, &prompt); err == nil {
		// Schema authors usually constrain an object; for a prompt string
		// validate the string instance when the schema type is string, else
		// wrap as {"prompt": ...} only if the schema looks object-shaped.
		if typ, _ := schema["type"].(string); typ == "string" {
			return compiled.Validate(prompt)
		}
	}
	var inst any
	if err := json.Unmarshal(input, &inst); err != nil {
		return fmt.Errorf("input is not valid JSON: %w", err)
	}
	return compiled.Validate(inst)
}
