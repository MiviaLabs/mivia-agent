package tools

import (
	"net/http"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// DefaultOptions configures built-in tools.
type DefaultOptions struct {
	Workspace                                                                  *workspace.Root
	RunAllowlist, RunAllowlistOnly, RunBlocklist, DisableTools                 []string
	RunTimeoutSec, MaxReadBytes, MaxOutputBytes, MaxWriteKB, MaxListDirEntries int
	// MaxToolResultBytes is the agent-loop tool-result ceiling
	// ([tools] max_tool_result_bytes). 0 = uncapped. When set, tools whose
	// honest output framing depends on not being tail-cut by the loop
	// (read_file's window header, find_references' JSON envelope) pre-clamp
	// their own budgets below it.
	MaxToolResultBytes                           int
	TavilyAPIKey                                 string
	EnvAllowlist, EnvAllowlistOnly, EnvBlocklist []string
	EnvAllowKeywordBlocklist                     []string
	SecretPathPatterns, SecretPathExceptions     []string
}

// readResultReserve is headroom subtracted from a configured result cap when
// pre-clamping read_file's byte budget: it covers the "… lines X–Y" window
// header plus the tool's own truncation notice, so the tool's whole output
// stays under the loop cap and the header stays honest by construction.
const readResultReserve = 128

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
	readMaxBytes := opts.MaxReadBytes
	if opts.MaxToolResultBytes > 0 {
		// Pre-clamp so read_file's whole output (header + content + notice)
		// fits under the loop's result cap; the loop then never tail-cuts
		// below what the "… lines X–Y" header claims. The config floor of
		// 1024 keeps this positive.
		readMaxBytes = min(readMaxBytes, opts.MaxToolResultBytes-readResultReserve)
	}
	// list_dir, grep, glob and write_file cap their results by COUNT (entries,
	// matches) or by input size, neither of which bounds bytes: names reach
	// 255 bytes, workspace-relative paths approach PATH_MAX, and an overwrite
	// diff is sized by the file on disk rather than the request. They take the
	// same read-class budget read_file already declares, so the dispatcher's
	// derived output backstop covers them without being inflated by them.
	readClassMaxBytes := opts.MaxReadBytes
	if opts.MaxToolResultBytes > 0 {
		// These tools account for their own truncation notice inside the
		// budget, so no reserve is needed: clamping to the loop cap means the
		// loop never has to tail-cut them at all.
		readClassMaxBytes = min(readClassMaxBytes, opts.MaxToolResultBytes)
	}
	register(&readFileTool{ws: ws, maxBytes: readMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&listDirTool{ws: ws, maxEntries: opts.MaxListDirEntries, maxBytes: readClassMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&grepTool{ws: ws, maxMatches: 50, maxBytes: readClassMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&globTool{ws: ws, maxMatches: 200, maxBytes: readClassMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&writeFileTool{ws: ws, maxWriteKB: opts.MaxWriteKB, maxBytes: readClassMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&searchReplaceTool{ws: ws, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&runCommandTool{ws: ws, allowlist: allowlist, timeoutSec: opts.RunTimeoutSec, maxOut: opts.MaxOutputBytes, redactArgs: RedactToolArgs(), envExact: envExact, envPrefix: envPrefix, envKeywordBlock: opts.EnvAllowKeywordBlocklist, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&webSearchTool{ws: ws, maxFetchKB: 100, httpClient: &http.Client{Timeout: 15 * time.Second}, tavilyKey: opts.TavilyAPIKey})
	register(&fetchURLTool{ws: ws, maxLocalBytes: opts.MaxReadBytes, maxFetchKB: 100, httpClient: &http.Client{Timeout: 15 * time.Second}, fetchClient: newSafeFetchHTTPClient(15 * time.Second)})
	register(&extractTool{tavilyKey: opts.TavilyAPIKey, httpClient: &http.Client{Timeout: 15 * time.Second}})

	// find_references — code intelligence via type-checking. It self-truncates
	// to maxBytes (valid JSON) and declares the same value as its Capability
	// budget, so clamping to the configured cap keeps the loop from ever
	// cutting its envelope.
	if ws != nil && ws.Abs != "" {
		refMaxBytes := 100_000
		if opts.MaxToolResultBytes > 0 {
			refMaxBytes = min(refMaxBytes, opts.MaxToolResultBytes)
		}
		analyzer := codeintel.NewAnalyzer(ws.Abs)
		register(&findReferencesTool{
			finder:   analyzer,
			maxBytes: refMaxBytes,
			limit:    200,
		})
	}
}
