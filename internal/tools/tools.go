// Package tools implements workspace-bound agent tools.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	return t.Execute(ctx, args)
}

// DefaultOptions configures built-in tools.
type DefaultOptions struct {
	Workspace      *workspace.Root
	RunAllowlist   []string
	RunTimeoutSec  int
	MaxReadBytes   int
	MaxOutputBytes int
	MaxWriteKB     int // max KiB for write_file content (0 = 500)
}

// DefaultAllowlist is the default run_command binary allowlist.
// Intentionally multi-language/multi-ecosystem: mivia is a generic coding agent host.
// Prefer bare names only; paths are rejected at execute time.
var DefaultAllowlist = []string{
	// VCS / build orchestration
	"git", "make", "cmake", "ninja",
	// Common language toolchains & package managers (project-agnostic host)
	"go", "gofmt",
	"python", "python3", "pip", "pip3", "pytest", "uv",
	"node", "npm", "npx", "yarn", "pnpm", "bun", "deno",
	"cargo", "rustc",
	"ruby", "bundle", "gem",
	"php", "composer",
	"java", "javac", "mvn", "gradle",
	"dotnet",
	// Search / trivial utilities
	"rg", "echo", "ls", "cat", "pwd", "true", "false",
}

// NewDefaultRegistry registers all v1 tools.
func NewDefaultRegistry(opts DefaultOptions) *Registry {
	if opts.MaxReadBytes <= 0 {
		opts.MaxReadBytes = 256 * 1024
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = 200_000
	}
	if opts.MaxWriteKB <= 0 {
		opts.MaxWriteKB = 500 // 500 KiB — matches pre-commit file-size-check
	}
	if opts.RunTimeoutSec <= 0 {
		opts.RunTimeoutSec = 120
	}
	if len(opts.RunAllowlist) == 0 {
		opts.RunAllowlist = DefaultAllowlist
	}
	r := NewRegistry()
	ws := opts.Workspace
	r.Register(&readFileTool{ws: ws, maxBytes: opts.MaxReadBytes})
	r.Register(&listDirTool{ws: ws})
	r.Register(&grepTool{ws: ws, maxMatches: 50})
	r.Register(&globTool{ws: ws, maxMatches: 200})
	r.Register(&writeFileTool{ws: ws, maxWriteKB: opts.MaxWriteKB})
	r.Register(&searchReplaceTool{ws: ws})
	r.Register(&runCommandTool{
		ws:         ws,
		allowlist:  opts.RunAllowlist,
		timeoutSec: opts.RunTimeoutSec,
		maxOut:     opts.MaxOutputBytes,
	})
	// Web search uses a plain client (public engines; tests inject httptest).
	// URL fetch uses an SSRF-hardened client (private IPs / redirect chains blocked).
	r.Register(&searchTool{
		ws:            ws,
		maxLocalBytes: opts.MaxReadBytes,
		maxFetchKB:    100,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		fetchClient:   newSafeFetchHTTPClient(15 * time.Second),
	})
	return r
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

func isSecretPath(rel string) bool {
	base := strings.ToLower(rel)
	if strings.Contains(base, ".env") {
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return true
	}
	if strings.Contains(base, "id_rsa") || strings.Contains(base, "id_ed25519") {
		return true
	}
	return false
}
