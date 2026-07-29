package tools

import (
	"net/http"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// DefaultOptions configures built-in tools.
type DefaultOptions struct {
	Workspace                                                                  *workspace.Root
	RunAllowlist, RunAllowlistOnly, RunBlocklist, DisableTools                 []string
	RunTimeoutSec, MaxReadBytes, MaxOutputBytes, MaxWriteKB, MaxListDirEntries int
	TavilyAPIKey                                                               string
	EnvAllowlist, EnvAllowlistOnly, EnvBlocklist                               []string
	SecretPathPatterns, SecretPathExceptions                                   []string
}

// The run_command program allowlist is configuration-only. No binary list is
// compiled in: which programs a workspace may run is that workspace's policy,
// and a list baked into the agent is both too permissive for locked-down repos
// and too narrow for unusual toolchains.
//
// A recommended multi-ecosystem list ships in .mivia/mivia.toml.example under
// [tools].run_allowlist. With it unset, run_command executes nothing.

// NewDefaultRegistry registers all v1 tools.
func NewDefaultRegistry(opts DefaultOptions) *Registry {
	normalizeDefaultOptions(&opts)
	secretPatterns, secretExceptions := configuredSecretPaths(opts)
	allowlist := configuredRunAllowlist(opts)
	envExact, envPrefix := resolveEnvAllowlist(opts.EnvAllowlist, opts.EnvAllowlistOnly, opts.EnvBlocklist)
	disabled := disabledToolNames(opts.DisableTools)
	r := NewRegistry()
	registerDefaultTools(r, opts, allowlist, envExact, envPrefix, secretPatterns, secretExceptions, disabled)
	return r
}

func normalizeDefaultOptions(opts *DefaultOptions) {
	if opts.MaxReadBytes <= 0 {
		opts.MaxReadBytes = 256 * 1024
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = 200_000
	}
	if opts.MaxWriteKB <= 0 {
		opts.MaxWriteKB = 500
	}
	if opts.MaxListDirEntries <= 0 {
		opts.MaxListDirEntries = 500
	}
	if opts.RunTimeoutSec <= 0 {
		opts.RunTimeoutSec = 300
	}
}

func configuredSecretPaths(opts DefaultOptions) ([]string, []string) {
	return opts.SecretPathPatterns, opts.SecretPathExceptions
}

func configuredRunAllowlist(opts DefaultOptions) []string {
	// With no compiled-in list there is nothing to extend or replace, so
	// run_allowlist_only and run_allowlist differ only in name; both are
	// honoured so existing configs keep working.
	allowlist := opts.RunAllowlistOnly
	normalized := make([]string, 0, len(allowlist)+len(opts.RunAllowlist))
	for _, program := range allowlist {
		normalized = append(normalized, strings.ToLower(program))
	}
	for _, program := range opts.RunAllowlist {
		normalized = append(normalized, strings.ToLower(program))
	}
	blocked := disabledToolNames(opts.RunBlocklist)
	filtered := normalized[:0]
	for _, program := range normalized {
		if !blocked[program] {
			filtered = append(filtered, program)
		}
	}
	return filtered
}

func disabledToolNames(names []string) map[string]bool {
	disabled := make(map[string]bool, len(names))
	for _, name := range names {
		disabled[strings.ToLower(name)] = true
	}
	return disabled
}

func registerDefaultTools(r *Registry, opts DefaultOptions, allowlist []string, envExact map[string]bool, envPrefix []string, patterns, exceptions []string, disabled map[string]bool) {
	register := func(tool Tool) {
		if !disabled[strings.ToLower(tool.Name())] {
			r.Register(tool)
		}
	}
	ws := opts.Workspace
	register(&readFileTool{ws: ws, maxBytes: opts.MaxReadBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&listDirTool{ws: ws, maxEntries: opts.MaxListDirEntries, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&grepTool{ws: ws, maxMatches: 50, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&globTool{ws: ws, maxMatches: 200, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&writeFileTool{ws: ws, maxWriteKB: opts.MaxWriteKB, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&searchReplaceTool{ws: ws, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&runCommandTool{ws: ws, allowlist: allowlist, timeoutSec: opts.RunTimeoutSec, maxOut: opts.MaxOutputBytes, redactArgs: RedactToolArgs(), envExact: envExact, envPrefix: envPrefix, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&webSearchTool{ws: ws, maxFetchKB: 100, httpClient: &http.Client{Timeout: 15 * time.Second}, tavilyKey: opts.TavilyAPIKey})
	register(&fetchURLTool{ws: ws, maxLocalBytes: opts.MaxReadBytes, maxFetchKB: 100, httpClient: &http.Client{Timeout: 15 * time.Second}, fetchClient: newSafeFetchHTTPClient(15 * time.Second)})
	register(&extractTool{tavilyKey: opts.TavilyAPIKey, httpClient: &http.Client{Timeout: 15 * time.Second}})
}
