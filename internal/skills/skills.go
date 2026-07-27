// Package skills defines independently typed, policy-bearing skills.
package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"sort"
	"time"
)

type Definition struct {
	Name                      string
	Version                   string
	Scope                     string
	Permission                string
	Timeout                   time.Duration
	Budget                    int
	InputSchema, OutputSchema map[string]any
	Tools                     []string
	Run                       func(context.Context, json.RawMessage) (json.RawMessage, error)
}
type Registry struct{ items map[string]Definition }

func NewRegistry() *Registry { return &Registry{items: map[string]Definition{}} }
func (r *Registry) Register(d Definition) error {
	if d.Name == "" || d.Run == nil {
		return fmt.Errorf("invalid skill")
	}
	if _, ok := r.items[d.Name]; ok {
		return fmt.Errorf("duplicate skill %q", d.Name)
	}
	r.items[d.Name] = d
	return nil
}
func (r *Registry) Get(name string) (Definition, bool) { d, ok := r.items[name]; return d, ok }
func (r *Registry) Select(name, version string, availableTools map[string]bool) (Definition, error) {
	d, ok := r.Get(name)
	if !ok {
		return Definition{}, fmt.Errorf("unknown skill %q", name)
	}
	if version != "" && d.Version != version {
		return Definition{}, fmt.Errorf("skill version mismatch")
	}
	for _, tool := range d.Tools {
		if !availableTools[tool] {
			return Definition{}, fmt.Errorf("skill %q requires unavailable tool %q", name, tool)
		}
	}
	return d, nil
}
func (r *Registry) Handler(name string) (runtime.Handler, error) {
	d, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown skill %q", name)
	}
	return handler{d}, nil
}
func (r *Registry) Invoke(ctx context.Context, d *runtime.Dispatcher, name, version string, input json.RawMessage, availableTools map[string]bool) runtime.Result {
	skill, err := r.Select(name, version, availableTools)
	if err != nil {
		return runtime.Result{Name: name, Kind: runtime.Skill, Err: err}
	}
	return d.Invoke(ctx, runtime.Request{Kind: runtime.Skill, Name: skill.Name, Scope: skill.Scope, Permission: skill.Permission, Timeout: skill.Timeout, Budget: skill.Budget, Input: input})
}

type handler struct{ d Definition }

func (h handler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if h.d.Permission != "" && req.Permission != h.d.Permission {
		return nil, fmt.Errorf("skill permission denied")
	}
	if h.d.Budget > 0 && req.Budget > h.d.Budget {
		return nil, fmt.Errorf("skill budget exceeded")
	}
	if err := validate(req.Input, h.d.InputSchema); err != nil {
		return nil, err
	}
	callCtx, cancel := ctx, func() {}
	if h.d.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, h.d.Timeout)
	}
	defer cancel()
	out, err := h.d.Run(callCtx, req.Input)
	if err != nil {
		return nil, err
	}
	if err := validate(out, h.d.OutputSchema); err != nil {
		return nil, fmt.Errorf("invalid skill output: %w", err)
	}
	return out, nil
}

func validate(raw json.RawMessage, schema map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("invalid skill JSON: %w", err)
	}
	if err := validateValue(value, schema, "input"); err != nil {
		return err
	}
	return nil
}
func validateValue(value any, schema map[string]any, path string) error {
	typ, _ := schema["type"].(string)
	valid := true
	switch typ {
	case "object":
		_, valid = value.(map[string]any)
	case "array":
		_, valid = value.([]any)
	case "string":
		_, valid = value.(string)
	case "boolean":
		_, valid = value.(bool)
	case "number", "integer":
		_, valid = value.(float64)
	}
	if !valid {
		return fmt.Errorf("%s has type %q", path, typ)
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, candidate := range enum {
			if fmt.Sprint(candidate) == fmt.Sprint(value) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s has invalid enum value", path)
		}
	}
	if typ == "object" {
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s requires object", path)
		}
		if required, ok := schema["required"].([]any); ok {
			for _, item := range required {
				name, _ := item.(string)
				if _, exists := obj[name]; !exists {
					return fmt.Errorf("missing required field %q", name)
				}
			}
		}
		if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
			props, _ := schema["properties"].(map[string]any)
			for name := range obj {
				if _, exists := props[name]; !exists {
					return fmt.Errorf("unknown field %q", name)
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for name, child := range props {
			if got, exists := obj[name]; exists {
				if childSchema, ok := child.(map[string]any); ok {
					if err := validateValue(got, childSchema, path+"."+name); err != nil {
						return err
					}
				}
			}
		}
	}
	if typ == "array" {
		items, _ := schema["items"].(map[string]any)
		for i, item := range value.([]any) {
			if err := validateValue(item, items, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *Registry) RegisterAll(d *runtime.Dispatcher) error {
	names := make([]string, 0, len(r.items))
	for name := range r.items {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := r.items[name]
		h, _ := r.Handler(s.Name)
		if err := d.Register(runtime.Skill, s.Name, h); err != nil {
			return err
		}
		d.Allow(runtime.Skill, s.Name)
	}
	return nil
}

// RegisterAllAsSubagents registers all skills as Subagent kind handlers,
// making them callable by name from the subagents.Pool (via dispatcher
// with Kind: Subagent). This enables the dispatch_tasks tool to invoke
// skills by their registered name.
func (r *Registry) RegisterAllAsSubagents(d *runtime.Dispatcher) error {
	names := make([]string, 0, len(r.items))
	for name := range r.items {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := r.items[name]
		h, _ := r.Handler(s.Name)
		if err := d.Register(runtime.Subagent, s.Name, h); err != nil {
			return err
		}
		d.Allow(runtime.Subagent, s.Name)
	}
	return nil
}
