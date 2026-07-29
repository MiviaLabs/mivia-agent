// Package tools implements workspace-bound agent tools.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
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

// DefaultOptions configures built-in tools.
type DefaultOptions struct {
	Workspace         *workspace.Root
	RunAllowlist      []string
	RunTimeoutSec     int
	MaxReadBytes      int
	MaxOutputBytes    int
	MaxWriteKB        int // max KiB for write_file content (0 = 500)
	MaxListDirEntries int // max dir entries to list (0 = 500)
	// RedactToolArgs hides run_command argv in results when true.
	// Default false; also controlled by package SetRedactToolArgs / env.
	RedactToolArgs bool
	// TavilyAPIKey is the API key for Tavily web search. When set, the search
	// tool uses Tavily as the primary search engine with free-engine fallback.
	TavilyAPIKey string
}

// DefaultAllowlist is the default run_command binary allowlist.
// Intentionally multi-language/multi-ecosystem: mivia is a generic coding agent host.
// Prefer bare names only; paths are rejected at execute time.
//
// To extend at runtime, pass RunAllowlist in DefaultOptions or set
// tools.Allowlist in config. The full refactor to make this configurable
// via TOML and CLI flags is tracked in PLAN.md.
var DefaultAllowlist = []string{
	// VCS / build orchestration
	"git", "make", "cmake", "ninja",
	// Shell (for scripted builds and test runners; opt-in via RunAllowlist)
	"sh", "bash",
	// Common language toolchains & package managers (project-agnostic host)
	"go", "gofmt", "govulncheck",
	"python", "python3", "pip", "pip3", "pytest", "uv", "nox",
	"node", "npm", "npx", "yarn", "pnpm", "bun", "deno", "tsx",
	"cargo", "rustc", "rustfmt", "clippy-driver",
	"ruby", "bundle", "gem", "rake", "rspec",
	"php", "composer", "phpunit",
	"java", "javac", "mvn", "gradle", "kotlin", "kotlinc",
	"dotnet", "dotnet-script",
	"swift", "swiftc",
	"zig", "zigcc",
	"elixir", "mix", "erl",
	"ghc", "cabal", "stack", "hlint",
	"perl", "cpan",
	"R", "lua", "luac",
	// Shell scripting support
	"awk", "sed", "grep", "egrep", "fgrep",
	"xargs", "tee", "envsubst",
	// Compression & archive (common in build/test pipelines)
	"tar", "gzip", "gunzip", "bzip2", "bunzip2", "xz", "unxz",
	"unzip", "zip", "zstd",
	// File system operations (create, delete, clean)
	"mkdir", "mkdirp", "rm", "cp", "mv", "touch", "ln", "chmod", "chown",
	"install", "mktemp", "realpath", "readlink", "basename", "dirname",
	// Search / trivial utilities
	"rg", "ag", "fd", "fzf", "bat", "delta",
	"echo", "ls", "cat", "tac", "nl", "od", "xxd", "hexdump",
	"pwd", "true", "false", "yes", "seq", "printf",
	// Standard Unix text processing
	"head", "tail", "sort", "uniq", "wc", "cut", "tr", "fold", "fmt",
	"diff", "patch", "comm", "cmp", "sdiff",
	"join", "paste", "expand", "unexpand",
	"strings", "iconv", "base64", "uuencode", "uudecode",
	// Process control
	"timeout", "nice", "nohup", "stdbuf", "parallel",
	// Network (lightweight, not interactive)
	"curl", "wget", "ssh", "scp",
	// Container / infra (common in CI/CD)
	"docker", "kubectl", "helm", "terraform", "tofu", "vagrant",
	// System info
	"env", "which", "id", "whoami", "date", "hostname", "uname", "arch", "nproc",
	// JSON / YAML processing
	"jq", "yq", "tomlq",
	// Developer tooling
	"strace", "ltrace", "perf", "tracepath", "traceroute",
	"gdb", "lldb", "dlv",
	"ps", "top", "htop", "free", "df", "du", "lsof",
	"dmesg", "sysctl", "uptime",
	// SQL clients (read-only queries)
	"sqlite3", "psql", "mysql", "redis-cli",
	// Editors / pagers (non-interactive usage)
	"nano", "vim", "vi", "less", "more", "most",
	// Image processing (non-interactive)
	"convert", "identify", "magick",
	// Audio / video (non-interactive)
	"ffmpeg", "ffprobe", "sox",
	// GitHub CLI
	"gh",
	// Nix ecosystem
	"nix", "nix-shell", "nix-build", "nix-env",
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
	if opts.MaxListDirEntries <= 0 {
		opts.MaxListDirEntries = 500
	}
	if opts.RunTimeoutSec <= 0 {
		opts.RunTimeoutSec = 300
	}
	if len(opts.RunAllowlist) == 0 {
		opts.RunAllowlist = DefaultAllowlist
	}
	r := NewRegistry()
	ws := opts.Workspace
	r.Register(&readFileTool{ws: ws, maxBytes: opts.MaxReadBytes})
	r.Register(&listDirTool{ws: ws, maxEntries: opts.MaxListDirEntries})
	r.Register(&grepTool{ws: ws, maxMatches: 50})
	r.Register(&globTool{ws: ws, maxMatches: 200})
	r.Register(&writeFileTool{ws: ws, maxWriteKB: opts.MaxWriteKB})
	r.Register(&searchReplaceTool{ws: ws})
	r.Register(&runCommandTool{
		ws:         ws,
		allowlist:  opts.RunAllowlist,
		timeoutSec: opts.RunTimeoutSec,
		maxOut:     opts.MaxOutputBytes,
		redactArgs: opts.RedactToolArgs || RedactToolArgs(),
	})
	// Web search: Tavily API → free engine fallback.
	r.Register(&webSearchTool{
		ws:         ws,
		maxFetchKB: 100,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		tavilyKey:  opts.TavilyAPIKey,
	})
	// URL fetch with SSRF protection.
	r.Register(&fetchURLTool{
		ws:            ws,
		maxLocalBytes: opts.MaxReadBytes,
		maxFetchKB:    100,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		fetchClient:   newSafeFetchHTTPClient(15 * time.Second),
	})
	// Tavily content extraction (requires TAVILY_API_KEY).
	r.Register(&extractTool{
		tavilyKey:  opts.TavilyAPIKey,
		httpClient: &http.Client{Timeout: 15 * time.Second},
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
	base := strings.ToLower(filepath.ToSlash(strings.TrimSpace(rel)))
	if base == "" {
		return false
	}
	// Bare names and nested paths: .env, cfg/.env.local, foo.env.local, …
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

// secretPathInArgv returns the first argv element that looks like a secret path
// (for run_command). Skips flag-like tokens (-x, --long).
func secretPathInArgv(args []string) string {
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		// Check as given and base name (../.env, /abs/path/.env).
		if isSecretPath(a) || isSecretPath(filepath.Base(a)) {
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
