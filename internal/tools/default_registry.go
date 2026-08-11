package tools

import (
	"net/http"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// defaultIgnorePatterns is the default list of directory names to skip
// during grep/glob walks. Configurable via [tools] search_ignore_patterns.
var defaultIgnorePatterns = []string{".git", "node_modules", "vendor"}

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
	MaxTavilyResponseBytes int
	// MaxFetchKB bounds the body read by fetch_url (KiB, [tools]
	// max_fetch_kb). 0 (from config) means unlimited; <=0 at tool construction
	// means unlimited. When the registry constructs the tool, this value is
	// already resolved by the config layer (unset-or-0 becomes the built-in
	// 4096 KiB default; an operator's positive value passes through), so it is
	// passed through as-is - the registry applies no default of its own.
	MaxFetchKB int
	// MemoryBackstopBytes is the OOM guard when MaxReadBytes is uncapped (0).
	// 0 means the built-in 256 MiB default ([tools] memory_backstop_mb). Not a
	// context-cost cap; cannot be disabled by setting 0.
	MemoryBackstopBytes                          int
	TavilyAPIKey                                 string
	EnvAllowlist, EnvAllowlistOnly, EnvBlocklist []string
	EnvAllowKeywordBlocklist                     []string
	SecretPathPatterns, SecretPathExceptions     []string
	// WritePathDenylist blocks writes to workspace-relative files or directories.
	// It does not affect read tools.
	WritePathDenylist    []string
	SearchIgnorePatterns []string
	// MaxInspectRepositoryBytes bounds inspect_repository's output envelope
	// ([tools] max_inspect_repository_bytes). 0 at construction resolves to
	// the built-in 64 KiB default; the config layer already resolves an
	// unset-or-0 knob to that same default before this struct is built.
	MaxInspectRepositoryBytes int

	// WorkflowTools are pre-built Phase 7 workflow tools. They register only
	// when the workspace has .mivia/workflows/ and no WorkflowToolsBuilder is
	// installed. Prefer tools.SetWorkflowToolsBuilder for production wiring so
	// this package does not import workflow/ledger (storage test import cycle).
	WorkflowTools []Tool

	// Memory is the durable agent memory backend. When nil, the memory tools
	// (memory_save, memory_search) are not registered. Wired by the CLI from
	// the resolved [memory] config; never constructed by a workspace file.
	Memory memory.Store
}

// defaultMemoryBackstopBytes is the OOM guard when MemoryBackstopBytes is unset.
const defaultMemoryBackstopBytes = 256 << 20

// effectiveMemoryBackstop returns the byte OOM guard for read/edit paths.
func effectiveMemoryBackstop(opts DefaultOptions) int {
	if opts.MemoryBackstopBytes <= 0 {
		return defaultMemoryBackstopBytes
	}
	return opts.MemoryBackstopBytes
}

// readResultReserve is headroom subtracted from a configured result cap when
// pre-clamping read_file's byte budget: it covers the "… lines X–Y of Z"
// window header plus the tool's own truncation notice, so the tool's whole
// output stays under the loop cap and the header stays honest by construction.
const readResultReserve = 128

// webFetchKB bounds the HTML body read by the free web-search engine chain
// (webSearchTool). It no longer bounds fetch_url: that tool reads up to the
// configurable MaxFetchKB from DefaultOptions (resolved by the config layer to
// its built-in 4096 KiB default unless the operator sets it). Unlike the
// provider-API bound it is compiled in: the free-engine path already truncates
// rather than refuses, so there is nothing for an operator to raise.
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
// so declaring it leaves this tool's own output ceiling - and the global one a
// keyless install derives - unchanged. The guard on the composed result makes
// the declaration true regardless of the estimate above.
const freeEngineResultBudget = 256 << 10

// searchToolBudget returns the result budget `search` declares and enforces.
// It is decided here, with the registry's other budget decisions, rather than
// inside the tool: the value turns on whether a provider key is configured,
// which is a composition fact, not a size fact.
//
// `search` registers unconditionally because it always can produce output: it
// uses Tavily when a key is configured and otherwise falls back to free search
// engines. Without a key that free-engine chain is the only output it can
// produce, so it declares the free-engine fetch bound and cannot inflate the
// SINGLE global dispatcher ceiling that every other tool shares. With a key it
// declares the provider bound.
//
// `extract` has no fallback and therefore cannot succeed without a key, so it
// is not registered at all in that case (conditional registration); no budget
// decision is needed for it here. When it IS registered - always with a key -
// it declares the same provider bound, resolved at its registration site.
func searchToolBudget(opts DefaultOptions) int {
	if opts.TavilyAPIKey == "" {
		return freeEngineResultBudget
	}
	return resolveWebResponseBudget(opts.MaxTavilyResponseBytes)
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
	envExact, envPrefix, envBlocked := resolveEnvAllowlist(opts.EnvAllowlist, opts.EnvAllowlistOnly, opts.EnvBlocklist)
	disabled := disabledToolNames(opts.DisableTools)
	r := NewRegistry()
	registerDefaultTools(r, opts, allowlist, envExact, envPrefix, envBlocked, secretPatterns, secretExceptions, disabled)
	return r
}

func normalizeDefaultOptions(opts *DefaultOptions) {
	if opts.RunTimeoutSec <= 0 {
		opts.RunTimeoutSec = 900
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

// registerEditTools registers the in-place edit tools. Their two bounds are
// derived from DIFFERENT knobs on purpose:
//
//   - the file-size guard is the READ bound (or the 256 MiB memory backstop
//     when uncapped), because these tools load the whole file into memory.
//     Clamping it by max_tool_result_bytes the way the read-class tools clamp
//     theirs would refuse to edit a 100 KiB source file just because the
//     operator keeps tool RESULTS small.
//   - the result budget is sized by the edit, not by the file: a header plus a
//     unified diff, so the compiled-in bound holds unless the operator's
//     result cap is tighter still.
func registerEditTools(register func(Tool), opts DefaultOptions, ws *workspace.Root, patterns, exceptions, writeDenylist []string) {
	maxFileBytes := opts.MaxReadBytes
	if maxFileBytes <= 0 {
		maxFileBytes = effectiveMemoryBackstop(opts)
	}
	maxResultBytes := searchReplaceResultMaxBytes
	if opts.MaxToolResultBytes > 0 {
		maxResultBytes = min(maxResultBytes, opts.MaxToolResultBytes)
	}
	register(&searchReplaceTool{ws: ws, maxFileBytes: maxFileBytes, maxBytes: maxResultBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns, writePathDenylist: writeDenylist})
	register(&multiEditTool{ws: ws, maxFileBytes: maxFileBytes, maxBytes: maxResultBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns, writePathDenylist: writeDenylist})
}

// composeIgnoreSource builds the shared ignore decision (built-in floor +
// search_ignore_patterns + root .gitignore) used by list_dir, grep, and glob.
func composeIgnoreSource(ws *workspace.Root, opts DefaultOptions) *gitignoreMatcher {
	ignorePatterns := make([]string, 0, len(defaultIgnorePatterns)+len(opts.SearchIgnorePatterns))
	ignorePatterns = append(ignorePatterns, defaultIgnorePatterns...)
	ignorePatterns = append(ignorePatterns, opts.SearchIgnorePatterns...)
	root := ""
	if ws != nil {
		root = ws.Abs
	}
	return newIgnoreSource(root, ignorePatterns)
}

// registerSearchTools registers grep and glob sharing the given ignore source.
func registerSearchTools(register func(Tool), ws *workspace.Root, maxBytes int, patterns, exceptions []string, ignore *gitignoreMatcher) {
	register(&grepTool{ws: ws, maxMatches: 0, maxBytes: maxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns, ignore: ignore})
	register(&globTool{ws: ws, maxMatches: 0, maxBytes: maxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns, ignore: ignore})
}

func registerDefaultTools(r *Registry, opts DefaultOptions, allowlist []string, envExact map[string]bool, envPrefix []string, envBlockedExact map[string]bool, patterns, exceptions []string, disabled map[string]bool) {
	register := func(tool Tool) {
		if !disabled[strings.ToLower(tool.Name())] {
			r.Register(tool)
		}
	}
	ws := opts.Workspace
	readMaxBytes, readClassMaxBytes := readClassBudgets(opts)
	// One ignore decision shared by list_dir, grep, and glob (D3).
	ignore := composeIgnoreSource(ws, opts)
	register(&readFileTool{ws: ws, maxBytes: readMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&listDirTool{ws: ws, maxEntries: opts.MaxListDirEntries, maxBytes: readClassMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns, ignore: ignore})
	registerSearchTools(register, ws, readClassMaxBytes, patterns, exceptions, ignore)
	register(&inspectRepositoryTool{ws: ws, maxBytes: inspectRepositoryBudget(opts), secretPathExceptions: exceptions, secretPathPatterns: patterns, ignore: ignore})
	register(&writeFileTool{ws: ws, maxWriteKB: opts.MaxWriteKB, maxBytes: readClassMaxBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns, writePathDenylist: opts.WritePathDenylist})
	register(&deleteFileTool{ws: ws, writePathDenylist: opts.WritePathDenylist, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	registerEditTools(register, opts, ws, patterns, exceptions, opts.WritePathDenylist)
	// run_command is advertised only when the allowlist is non-empty: an empty
	// allowlist means no program may run, so the tool cannot succeed and is
	// absent from the registry, not present and error-returning at Execute time.
	if len(allowlist) > 0 {
		register(&runCommandTool{
			ws: ws, allowlist: allowlist, timeoutSec: opts.RunTimeoutSec,
			maxOut: opts.MaxOutputBytes, memoryBackstop: effectiveMemoryBackstop(opts),
			redactArgs: RedactToolArgs(), envExact: envExact, envPrefix: envPrefix,
			envBlockedExact:      envBlockedExact,
			envKeywordBlock:      opts.EnvAllowKeywordBlocklist,
			secretPathExceptions: exceptions, secretPathPatterns: patterns,
		})
	}
	registerWebTools(register, opts, ws, patterns, exceptions)
	registerCodeNavTools(register, opts, ws, patterns, exceptions)
	registerMemoryTools(register, opts)
	registerWorkflowTools(register, opts)
}

// readClassBudgets resolves the two read-class byte budgets shared by the
// file tools.
//
// readMaxBytes is read_file's own budget, pre-clamped so its whole output
// (header + content + notice) fits under the loop's result cap; the loop then
// never tail-cuts below what the "… lines X–Y of Z" header claims. The config
// floor of 1024 keeps this positive.
//
// readClassMaxBytes covers the count-or-input-bounded tools (list_dir, grep,
// glob, write_file, search_replace, multi_edit): they account for their own
// truncation notice inside the budget, so clamping to the loop cap means the
// loop never has to tail-cut them at all.
//
// When both MaxReadBytes and MaxToolResultBytes are 0 (uncapped), the tools
// have no byte budget at all, which means grep on a large monorepo can
// accumulate unbounded memory before the dispatcher ceiling check fires.
// Use the dispatcher's output ceiling floor (256 KiB) as a safety backstop
// so accumulation stops before OOM. This is not a truncation default - it is
// the same bound the dispatcher would enforce after the fact - but applied
// inside the tool so the memory is never allocated.
func readClassBudgets(opts DefaultOptions) (int, int) {
	// Resolve uncapped MaxReadBytes to the OOM backstop first, then clamp by
	// MaxToolResultBytes. Ordering matters: min(0, cap) is 0, and the old
	// backstop fill after that clamp discarded the result cap entirely.
	readMaxBytes := opts.MaxReadBytes
	if readMaxBytes <= 0 {
		readMaxBytes = effectiveMemoryBackstop(opts)
	}
	if opts.MaxToolResultBytes > 0 {
		readMaxBytes = min(readMaxBytes, opts.MaxToolResultBytes-readResultReserve)
	}
	readClassMaxBytes := opts.MaxReadBytes
	if readClassMaxBytes <= 0 {
		readClassMaxBytes = effectiveMemoryBackstop(opts)
	}
	if opts.MaxToolResultBytes > 0 {
		readClassMaxBytes = min(readClassMaxBytes, opts.MaxToolResultBytes)
	}
	return readMaxBytes, readClassMaxBytes
}

// defaultInspectRepositoryBytes is the OOM/output guard when
// MaxInspectRepositoryBytes is unset at construction (0). The config layer
// normally resolves this first (built-in 64 KiB default), so 0 here means a
// caller built DefaultOptions directly rather than through config.
const defaultInspectRepositoryBytes = 64 << 10

// inspectRepositoryBudget resolves inspect_repository's result envelope
// bound, clamped like the other read-class tools by an operator's
// max_tool_result_bytes ceiling so the loop never has to tail-cut its JSON.
func inspectRepositoryBudget(opts DefaultOptions) int {
	budget := opts.MaxInspectRepositoryBytes
	if budget <= 0 {
		budget = defaultInspectRepositoryBytes
	}
	if opts.MaxToolResultBytes > 0 {
		budget = min(budget, opts.MaxToolResultBytes)
	}
	return budget
}

// registerWebTools registers the network-backed tools. Their budgets are NOT
// clamped to MaxToolResultBytes the way the read-class budgets are: these
// numbers are enforced by REFUSING an over-bound response, so clamping to a
// small history cap would turn an operator's soft ceiling on stored results
// into a hard failure of every web search. The loop still applies its own cap
// to what it stores.
func registerWebTools(register func(Tool), opts DefaultOptions, ws *workspace.Root, patterns, exceptions []string) {
	register(&webSearchTool{ws: ws, maxFetchKB: webFetchKB, httpClient: &http.Client{}, tavilyKey: opts.TavilyAPIKey, maxResultBytes: searchToolBudget(opts)})
	// fetch_url's MaxFetchKB is passed through as-is: the config layer already
	// resolved it (unset-or-0 -> the built-in 4096 KiB default, a positive
	// operator value preserved). No default lives here any more - a 0 that
	// reaches this point via direct DefaultOptions construction means
	// unlimited, which fetch_url itself handles.
	register(&fetchURLTool{ws: ws, maxLocalBytes: opts.MaxReadBytes, maxFetchKB: opts.MaxFetchKB, httpClient: &http.Client{}, fetchClient: newSafeFetchHTTPClient()})
	// extract has no free-engine fallback, so a keyless tool could never
	// succeed - and a tool is advertised only if it can succeed. Register it
	// solely when a provider key is configured (conditional registration): the
	// struct is then created with that key and declares the configured provider
	// bound. Without the key it is absent from the registry, not present and
	// error-returning.
	if opts.TavilyAPIKey != "" {
		register(&extractTool{tavilyKey: opts.TavilyAPIKey, httpClient: &http.Client{}, maxResultBytes: resolveWebResponseBudget(opts.MaxTavilyResponseBytes)})
	}
}

// registerMemoryTools registers the durable memory tools when a memory store
// is wired. The store is a session-level backend built by the CLI from the
// [memory] config; without it the tools cannot succeed and are absent.
func registerMemoryTools(register func(Tool), opts DefaultOptions) {
	if opts.Memory == nil {
		return
	}
	maxSearchBytes := memorySearchResultBytes
	if opts.MaxToolResultBytes > 0 {
		maxSearchBytes = min(maxSearchBytes, opts.MaxToolResultBytes)
	}
	register(&memorySaveTool{store: opts.Memory})
	register(&memorySearchTool{store: opts.Memory, maxBytes: maxSearchBytes})
}

// registerCodeNavTools registers the code-intelligence tools when the
// workspace has a root. Each self-truncates to maxBytes (valid JSON) and
// declares the same value as its Capability budget, so clamping to the
// configured cap keeps the loop from ever cutting its envelope.
//
// ONE analyzer is constructed here and handed to all three tools (plan
// tools/03 D3). The analyzer owns a cached workspace snapshot, so a shared
// instance is what makes the second query cheap; three instances would each
// pay their own full load and the cache would buy nothing across tools. Its
// lifetime is the registry's - a new registry (workspace or agent change)
// starts cold, and there is nothing to tear down.
func registerCodeNavTools(register func(Tool), opts DefaultOptions, ws *workspace.Root, patterns, exceptions []string) {
	if ws == nil || ws.Abs == "" {
		return
	}
	navMaxBytes := 100_000
	if opts.MaxToolResultBytes > 0 {
		navMaxBytes = min(navMaxBytes, opts.MaxToolResultBytes)
	}
	analyzer := codeintel.NewAnalyzer(ws.Abs)
	register(&findReferencesTool{
		finder:   analyzer,
		maxBytes: navMaxBytes,
		limit:    50,
	})
	register(&listSymbolsTool{
		ws:                   ws,
		searcher:             analyzer,
		outline:              codeintel.FileOutline,
		maxBytes:             navMaxBytes,
		limit:                codeintel.DefaultSymbolLimit,
		secretPathExceptions: exceptions,
		secretPathPatterns:   patterns,
	})
	register(&goToDefinitionTool{
		ws:       ws,
		resolver: analyzer,
		maxBytes: navMaxBytes,
	})
	register(&findSymbolContextTool{
		ws:       ws,
		resolver: analyzer,
		maxBytes: navMaxBytes,
	})
}
