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
	MaxToolResultBytes int
	// MaxTavilyResponseBytes is the byte bound the Tavily-backed tools
	// (`search`'s provider path and `extract`) enforce on the response body
	// AND on their composed result, and declare as their result budget
	// ([tools] max_tavily_response_bytes). It is not a truncation cap: nothing
	// is ever cut, an over-bound response is refused with an explicit error.
	// 0 uses the built-in default. See web_response_budget.go.
	MaxTavilyResponseBytes                       int
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

// webFetchKB bounds the HTML body read by fetch_url and by the free web-search
// engine chain. Unlike the provider-API bound it is compiled in: both paths
// already truncate rather than refuse, so there is nothing for an operator to
// raise.
const webFetchKB = 100

// freeEngineResultBudget bounds `search` when no provider key is configured,
// where the free-engine chain is the only reachable path.
//
// It is NOT the body read (webFetchKB*1024 = 102400): the composition can
// outgrow the body it came from. Each emitted result costs at most 11 bytes of
// framing ("\n" + "• " + two "\n  " separators) ON TOP of body-derived text,
// and the cheapest parser still needs ~13 body bytes per result, so the worst
// case is bounded by 102400 + 11*(102400/13) = 189047 bytes. 256 KiB clears
// that with margin and is exactly the dispatcher's historical ceiling floor,
// so declaring it leaves this tool's own output ceiling — and the global one a
// keyless install derives — unchanged. The guard on the composed result makes
// the declaration true regardless of the estimate above.
const freeEngineResultBudget = 256 << 10

// tavilyToolBudgets returns the result budget each Tavily-backed tool declares
// and enforces. Both are decided here, with the registry's other budget
// decisions, rather than inside the tools: the value turns on whether a
// provider key is configured, which is a composition fact, not a size fact.
//
// Without a key neither tool can reach the provider — `search` only tries
// Tavily under `if t.tavilyKey != ""` and `extract` refuses before issuing any
// request — so neither may inflate the SINGLE global dispatcher ceiling that
// every other tool shares. `search` still declares the free-engine fetch
// bound, the only output it can produce; `extract` declares a nominal positive
// value that bounds nothing but its refusal path, because the registry-wide
// gate reads a non-positive budget as "no decision recorded".
func tavilyToolBudgets(opts DefaultOptions) (search, extract int) {
	if opts.TavilyAPIKey == "" {
		return freeEngineResultBudget, keylessToolResultBudget
	}
	budget := resolveWebResponseBudget(opts.MaxTavilyResponseBytes)
	return budget, budget
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
	//
	// When both MaxReadBytes and MaxToolResultBytes are 0 (uncapped), the tools
	// have no byte budget at all, which means grep on a large monorepo can
	// accumulate unbounded memory before the dispatcher ceiling check fires.
	// Use the dispatcher's output ceiling floor (256 KiB) as a safety backstop
	// so accumulation stops before OOM. This is not a truncation default — it is
	// the same bound the dispatcher would enforce after the fact — but applied
	// inside the tool so the memory is never allocated.
	readClassMaxBytes := opts.MaxReadBytes
	if opts.MaxToolResultBytes > 0 {
		// These tools account for their own truncation notice inside the
		// budget, so no reserve is needed: clamping to the loop cap means the
		// loop never has to tail-cut them at all.
		readClassMaxBytes = min(readClassMaxBytes, opts.MaxToolResultBytes)
	}
	if readClassMaxBytes <= 0 {
		readClassMaxBytes = 256 << 20 // 256 MiB safety backstop
	}
	register(&readFileTool{ws: ws, maxBytes: readMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&listDirTool{ws: ws, maxEntries: opts.MaxListDirEntries, maxBytes: readClassMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&grepTool{ws: ws, maxMatches: 0, maxBytes: readClassMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&globTool{ws: ws, maxMatches: 0, maxBytes: readClassMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&writeFileTool{ws: ws, maxWriteKB: opts.MaxWriteKB, maxBytes: readClassMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&searchReplaceTool{ws: ws, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&runCommandTool{ws: ws, allowlist: allowlist, timeoutSec: opts.RunTimeoutSec, maxOut: opts.MaxOutputBytes, redactArgs: RedactToolArgs(), envExact: envExact, envPrefix: envPrefix, envKeywordBlock: opts.EnvAllowKeywordBlocklist, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	// Web-tool budgets. These are NOT clamped to MaxToolResultBytes the way the
	// read-class budgets above are: this number is enforced by REFUSING an
	// over-bound response, so clamping it to a small history cap would turn an
	// operator's soft ceiling on stored results into a hard failure of every
	// web search. The loop still applies its own cap to what it stores.
	searchBudget, extractBudget := tavilyToolBudgets(opts)
	register(&webSearchTool{ws: ws, maxFetchKB: webFetchKB, httpClient: &http.Client{Timeout: 15 * time.Second}, tavilyKey: opts.TavilyAPIKey, maxResultBytes: searchBudget})
	register(&fetchURLTool{ws: ws, maxLocalBytes: opts.MaxReadBytes, maxFetchKB: webFetchKB, httpClient: &http.Client{Timeout: 15 * time.Second}, fetchClient: newSafeFetchHTTPClient(15 * time.Second)})
	register(&extractTool{tavilyKey: opts.TavilyAPIKey, httpClient: &http.Client{Timeout: 15 * time.Second}, maxResultBytes: extractBudget})

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
			limit:    0,
		})
	}
}
