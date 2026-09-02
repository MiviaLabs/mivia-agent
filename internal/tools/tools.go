// Package tools implements workspace-bound agent tools.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Tool is a single agent capability.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// EphemeralResultTool marks output that must be retained only while an active
// agent loop needs it. Its marker is safe for events and persisted history.
type EphemeralResultTool interface {
	Tool
	EphemeralResultMarker(args json.RawMessage) string
}

// PrivilegedTool marks a session-control tool that must never be exposed to a
// nested agent. The marker travels with the tool so future control tools do
// not depend solely on a name denylist.
type PrivilegedTool interface {
	Tool
	Privileged()
}

// ExecutionClass describes the side effects and safe scheduling behavior of a tool.
type ExecutionClass uint8

const (
	ExecutionRead ExecutionClass = iota
	ExecutionWrite
	ExecutionExternal
)

// Capability describes scheduling and safety metadata for one tool invocation.
type Capability struct {
	Class          ExecutionClass
	ResourceKey    string
	Timeout        time.Duration
	MaxResultBytes int
}

// Dedups reports whether this capability participates in the per-turn tool
// dedup. ExecutionRead calls always execute fresh; Write/External tools dedup.
func (c Capability) Dedups() bool { return c.Class != ExecutionRead }

// CapableTool may expose scheduling metadata in addition to Tool.
type CapableTool interface {
	Tool
	Capability(args json.RawMessage) Capability
}

// ResultBudgetTool is implemented by tools whose result size is bounded by a
// configured content budget (read_file's max_read_bytes, run_command's
// max_output_bytes, find_references' JSON budget). The declared budget feeds
// the runtime dispatcher's runaway-output backstop derivation, which must sit
// strictly above every honest result. Unlike Capability.MaxResultBytes this
// is NOT a truncation bound: framing the tool emits on top of the content -
// window headers, truncation notices, argv echo - is covered by the
// dispatcher's input-allowance and slack terms, never cut.
type ResultBudgetTool interface {
	ResultBudgetBytes() int
}

// Registry holds tools by name.
type Registry struct {
	mu    sync.RWMutex
	order []Tool
	by    map[string]Tool
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{by: make(map[string]Tool)}
}

// Clone returns an independent registry with the same tool instances and
// registration order. Dispatcher generations may register session-owned tools
// on the clone without mutating the live session registry.
func (r *Registry) Clone() *Registry {
	if r == nil {
		return nil
	}
	out := NewRegistry()
	for _, tool := range r.List() {
		out.Register(tool)
	}
	return out
}

// CloneForGeneration copies the base workspace tools while omitting
// privileged session-control tools that a fresh dispatcher must register once
// for its own generation.
func (r *Registry) CloneForGeneration() *Registry {
	return r.CloneForGenerationExcluding()
}

// CloneForGenerationExcluding copies non-privileged tools while omitting
// generation-owned tools that the new dispatcher must construct afresh.
func (r *Registry) CloneForGenerationExcluding(excludedNames ...string) *Registry {
	if r == nil {
		return nil
	}
	excluded := make(map[string]struct{}, len(excludedNames))
	for _, name := range excludedNames {
		excluded[name] = struct{}{}
	}
	out := NewRegistry()
	for _, tool := range r.List() {
		if _, privileged := tool.(PrivilegedTool); privileged {
			continue
		}
		if _, omit := excluded[tool.Name()]; omit {
			continue
		}
		out.Register(tool)
	}
	return out
}

// Register adds a tool.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.by[t.Name()]; ok {
		return
	}
	r.by[t.Name()] = t
	r.order = append(r.order, t)
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.by[name]
	return t, ok
}

// List returns tools in registration order.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Tool(nil), r.order...)
}

// OpenAITools returns the tools array for chat completions.
func (r *Registry) OpenAITools() []map[string]any {
	registered := r.List()
	out := make([]map[string]any, 0, len(registered))
	for _, t := range registered {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name(),
				"description": t.Description(),
				"parameters":  t.Parameters(),
			},
		})
	}
	return out
}

// Execute runs a tool by name with JSON args.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return "", fmt.Errorf("invalid arguments: malformed JSON")
	}
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) > 0 && trimmed[0] != '{' {
		return "", fmt.Errorf("invalid arguments: expected JSON object")
	}
	var object map[string]any
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return "", fmt.Errorf("invalid arguments: expected JSON object")
	}
	if err := validateSchema(object, t.Parameters()); err != nil {
		return "", err
	}
	return t.Execute(ctx, args)
}

func validateSchema(object map[string]any, schema map[string]any) error {
	properties, _ := schema["properties"].(map[string]any)
	for _, name := range requiredFields(schema) {
		if _, present := object[name]; !present {
			return fmt.Errorf("invalid arguments: missing required field %q", name)
		}
	}
	additional := true
	if raw, present := schema["additionalProperties"]; present {
		additional, _ = raw.(bool)
	}
	for name, value := range object {
		property, known := properties[name]
		if !known {
			if !additional {
				return fmt.Errorf("invalid arguments: unknown field")
			}
			continue
		}
		definition, _ := property.(map[string]any)
		if err := validateProperty(name, value, definition); err != nil {
			return err
		}
	}
	return nil
}

// requiredFields returns the schema's required field names, accepting both the
// JSON-decoded []any form and a literal []string form.
func requiredFields(schema map[string]any) []string {
	if raw, ok := schema["required"].([]string); ok {
		return raw
	}
	var required []string
	if raw, ok := schema["required"].([]any); ok {
		for _, name := range raw {
			if s, ok := name.(string); ok {
				required = append(required, s)
			}
		}
	}
	return required
}

// validateProperty validates a single known property against its definition:
// enum membership, type match, numeric bounds and array constraints.
func validateProperty(name string, value any, definition map[string]any) error {
	kind, _ := definition["type"].(string)
	if enum, ok := schemaEnum(definition["enum"]); ok && !enumContains(enum, value) {
		return fmt.Errorf("invalid arguments: field %q must be one of the declared values", name)
	}
	if !schemaTypeMatches(value, kind, definition) {
		return fmt.Errorf("invalid arguments: field %q must be %s", name, kind)
	}
	// minimum/maximum for integer/number fields.
	if kind == "integer" || kind == "number" {
		if err := validateNumberBounds(name, value, definition); err != nil {
			return err
		}
	}
	// minItems/maxItems and per-item enums for array fields.
	if kind == "array" {
		if err := validateArrayConstraints(name, value, definition); err != nil {
			return err
		}
	}
	return nil
}

// validateNumberBounds enforces minimum/maximum on integer and number fields.
func validateNumberBounds(name string, value any, definition map[string]any) error {
	numVal, ok := value.(float64)
	if !ok {
		return nil
	}
	if min, ok := definition["minimum"].(float64); ok && numVal < min {
		return fmt.Errorf("invalid arguments: field %q must be >= %v", name, min)
	}
	if max, ok := definition["maximum"].(float64); ok && numVal > max {
		return fmt.Errorf("invalid arguments: field %q must be <= %v", name, max)
	}
	return nil
}

// validateArrayConstraints enforces minItems/maxItems and enum-on-items for
// array fields.
func validateArrayConstraints(name string, value any, definition map[string]any) error {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	if minItems, ok := definition["minItems"].(float64); ok && int(minItems) > len(values) {
		return fmt.Errorf("invalid arguments: field %q must have >= %d items", name, int(minItems))
	}
	if maxItems, ok := definition["maxItems"].(float64); ok && int(maxItems) < len(values) {
		return fmt.Errorf("invalid arguments: field %q must have <= %d items", name, int(maxItems))
	}
	if items, ok := definition["items"].(map[string]any); ok {
		if itemEnum, ok := schemaEnum(items["enum"]); ok {
			for _, item := range values {
				if !enumContains(itemEnum, item) {
					return fmt.Errorf("invalid arguments: array items for %q must be one of the declared values", name)
				}
			}
		}
	}
	return nil
}

func schemaTypeMatches(value any, kind string, definition map[string]any) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number", "integer":
		number, ok := value.(float64)
		return ok && (kind != "integer" || math.Trunc(number) == number)
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		values, ok := value.([]any)
		if !ok {
			return false
		}
		items, _ := definition["items"].(map[string]any)
		itemType, _ := items["type"].(string)
		for _, item := range values {
			if !schemaTypeMatches(item, itemType, items) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func enumContains(values []any, value any) bool {
	for _, candidate := range values {
		if reflect.DeepEqual(candidate, value) {
			return true
		}
	}
	return false
}

func schemaEnum(raw any) ([]any, bool) {
	switch values := raw.(type) {
	case []any:
		return values, true
	case []string:
		out := make([]any, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out, true
	default:
		return nil, false
	}
}

// Capability returns scheduling metadata, using a conservative external
// classification for tools that do not implement CapableTool.
func (r *Registry) Capability(name string, args json.RawMessage) Capability {
	t, ok := r.Get(name)
	if !ok {
		return Capability{Class: ExecutionExternal}
	}
	if capable, ok := t.(CapableTool); ok {
		capability := capable.Capability(args)
		if capability.Class == ExecutionWrite && capability.ResourceKey == "" {
			capability.ResourceKey = "workspace:mutation"
		}
		if capability.Class > ExecutionExternal {
			capability = Capability{Class: ExecutionExternal}
		}
		return capability
	}
	var class ExecutionClass
	switch name {
	case "read_file", "list_dir", "grep", "glob":
		class = ExecutionRead
	case "write_file", "search_replace", MultiEditToolName:
		class = ExecutionWrite
	default:
		class = ExecutionExternal
	}
	var in struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(args, &in)
	key := ""
	if (class == ExecutionRead || class == ExecutionWrite) && in.Path != "" {
		key = "path:" + filepath.ToSlash(filepath.Clean(in.Path))
	}
	if class == ExecutionRead && key == "" {
		key = "workspace:read"
	}
	if class == ExecutionWrite && key == "" {
		key = "workspace:mutation"
	}
	if class == ExecutionExternal {
		// Independent external operations may run concurrently. A tool that
		// shares an external resource must declare that key via CapableTool.
		key = ""
	}
	return Capability{Class: class, ResourceKey: key}
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

func decodeArgs[T any](raw json.RawMessage, dst *T) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// Secret path filtering is entirely configuration-driven. There is no
// compiled-in pattern list: what counts as a secret is a property of a
// workspace, not of this binary, and a hardcoded list is both wrong for some
// repos and a false sense of coverage for the rest.
//
// Recommended starting values ship in .mivia/mivia.toml.example under
// [tools].secret_path_patterns / .secret_path_exceptions. With neither set,
// no path is filtered.
//
// This is not a privilege boundary. The file tools consult it, and run_command
// screens argv via secretPathInArgv, but a shell invocation that builds the
// path at runtime ("sh -c" with concatenation, a glob, a copy) reaches the file
// regardless. Treat it as an accident guard that keeps credentials out of model
// context, not as enforcement.

func isSecretPath(rel string, exceptions, patterns []string) bool {
	policy, err := secretpath.New(patterns, exceptions)
	if err != nil {
		return true
	}
	return policy.Match(rel)
}

// isSecretPathMatch checks whether path matches any of the given patterns.
// A pattern like ".pem" matches any path ending in ".pem".
// A pattern like ".env" matches any path containing ".env" (catches .env, .env.local, etc.).
func isSecretPathMatch(path string, patterns []string) bool {
	policy, err := secretpath.New(patterns, nil)
	return err == nil && policy.Match(path)
}

// secretPathInArgv returns the first argv element that looks like a secret path
// (for run_command). Skips flag-like tokens (-x, --long).
func secretPathInArgv(args []string, exceptions, patterns []string) string {
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		// Check as given and base name (../.env, /abs/path/.env).
		if isSecretPath(a, exceptions, patterns) || isSecretPath(filepath.Base(a), exceptions, patterns) {
			return a
		}
	}
	return ""
}

func pathCapabilityKey(args json.RawMessage, ws *workspace.Root) string {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &input); err != nil || strings.TrimSpace(input.Path) == "" {
		return "workspace:read"
	}
	if ws != nil {
		if absolute, err := ws.Resolve(input.Path); err == nil {
			return "path:" + filepath.ToSlash(absolute)
		}
	}
	return "path:" + filepath.ToSlash(filepath.Clean(input.Path))
}

// CapabilityOf reports the execution class of one tool VALUE.
//
// A tool that declares no capability is ExecutionExternal - the most
// restrictive class - so an unclassified tool is gated rather than waved
// through. That default is load-bearing on the approval paths: several real
// tools declare no capability (post_message and run_messages in
// internal/clichat, every workflow_* tool in internal/workflows/ledger), and
// classifying one of those below Write would run it unprompted under a
// write-only policy.
//
// This is deliberately the TOOL-value form. CapabilityFor above answers the
// same question from a registry and a NAME, and carries a name-based fallback
// for the compiled-in tools; the two are not interchangeable, and the approval
// paths hold a tool rather than a name.
func CapabilityOf(t Tool, args json.RawMessage) Capability {
	if capable, ok := t.(CapableTool); ok {
		return capable.Capability(args)
	}
	return Capability{Class: ExecutionExternal}
}
