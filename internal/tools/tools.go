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
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Tool is a single agent capability.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args json.RawMessage) (string, error)
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

// CapableTool may expose scheduling metadata in addition to Tool.
type CapableTool interface {
	Tool
	Capability(args json.RawMessage) Capability
}

// Registry holds tools by name.
type Registry struct {
	order []Tool
	by    map[string]Tool
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{by: make(map[string]Tool)}
}

// Register adds a tool.
func (r *Registry) Register(t Tool) {
	if _, ok := r.by[t.Name()]; ok {
		return
	}
	r.by[t.Name()] = t
	r.order = append(r.order, t)
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.by[name]
	return t, ok
}

// List returns tools in registration order.
func (r *Registry) List() []Tool {
	return append([]Tool(nil), r.order...)
}

// OpenAITools returns the tools array for chat completions.
func (r *Registry) OpenAITools() []map[string]any {
	out := make([]map[string]any, 0, len(r.order))
	for _, t := range r.order {
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
	t, ok := r.by[name]
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
	required, _ := schema["required"].([]any)
	if raw, ok := schema["required"].([]string); ok {
		for _, name := range raw {
			required = append(required, name)
		}
	}
	for _, raw := range required {
		name, ok := raw.(string)
		if ok {
			if _, present := object[name]; !present {
				return fmt.Errorf("invalid arguments: missing required field %q", name)
			}
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
				return fmt.Errorf("invalid arguments: unknown field %q", name)
			}
			continue
		}
		definition, _ := property.(map[string]any)
		kind, _ := definition["type"].(string)
		if enum, ok := schemaEnum(definition["enum"]); ok && !enumContains(enum, value) {
			return fmt.Errorf("invalid arguments: field %q must be one of the declared values", name)
		}
		if !schemaTypeMatches(value, kind, definition) {
			return fmt.Errorf("invalid arguments: field %q must be %s", name, kind)
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
	t, ok := r.by[name]
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
	case "search":
		var in struct {
			Scope string `json:"scope"`
		}
		_ = json.Unmarshal(args, &in)
		if in.Scope == "local" {
			class = ExecutionRead
		} else {
			class = ExecutionExternal
		}
	case "write_file", "search_replace":
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

// DefaultSecretPathPatterns is the default list of path patterns that are
// blocked from read/write/grep/glob by file tools. These prevent accidental
// leakage of credentials and private keys into model context.
// Patterns are matched case-insensitively against the relative workspace path.
// Users can extend or replace via ToolsConfig / DefaultOptions.
var DefaultSecretPathPatterns = []string{
	".env",       // dotenv files (exact, catches .env, .env.local, .env.production)
	".pem",       // private key certificates
	".key",       // private keys
	"id_rsa",     // SSH private keys
	"id_ed25519", // SSH ed25519 keys
}

// DefaultSecretPathExceptions are paths matching secret patterns that should
// still be accessible (e.g. .env.example is a template, not a real secret).
var DefaultSecretPathExceptions = []string{
	".env.example",
}

func isSecretPath(rel string, exceptions, patterns []string) bool {
	base := strings.ToLower(filepath.ToSlash(strings.TrimSpace(rel)))
	if base == "" {
		return false
	}
	// Fall back to package-level globals if no per-tool overrides provided.
	if exceptions == nil {
		exceptions = DefaultSecretPathExceptions
	}
	if patterns == nil {
		patterns = DefaultSecretPathPatterns
	}
	// Check exceptions first (allowlist overrides blocklist).
	for _, ex := range exceptions {
		if strings.Contains(base, ex) {
			return false
		}
	}
	// Apply blocklist patterns.
	return isSecretPathMatch(base, patterns)
}

// isSecretPathMatch checks whether path matches any of the given patterns.
// A pattern like ".pem" matches any path ending in ".pem".
// A pattern like ".env" matches any path containing ".env" (catches .env, .env.local, etc.).
func isSecretPathMatch(path string, patterns []string) bool {
	for _, pat := range patterns {
		if strings.Contains(path, pat) {
			return true
		}
	}
	return false
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
