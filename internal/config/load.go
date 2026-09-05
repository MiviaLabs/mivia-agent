package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	sdkenvfile "github.com/MiviaLabs/mivia-ai-sdk/envfile"
	"github.com/pelletier/go-toml/v2"
)

// LoadOptions controls config resolution.
type LoadOptions struct {
	ConfigPath string
	// WorkspaceRoot selects the project MCP configuration. Empty uses the
	// current working directory for backward compatibility.
	WorkspaceRoot      string
	ProviderOverride   string
	ModelOverride      string
	AllowMissingConfig bool
	// AutoBootstrapUserConfig silently writes a minimal default config to
	// UserConfigPath() (DefaultUserConfigTOML) when the normal search
	// (opts.ConfigPath, then DefaultConfigCandidates()) finds no config file
	// anywhere. It only fires when opts.ConfigPath was left empty - an
	// explicit --config/$MIVIA_CONFIG miss stays a real error (or, for
	// $MIVIA_CONFIG, falls through to the remaining candidates, unchanged
	// pre-existing behavior) rather than being silently papered over. It
	// does not require AllowMissingConfig to also be set, but callers should
	// normally set both: if HOME cannot be resolved (UserConfigPath() is
	// ""), bootstrap is a no-op and AllowMissingConfig alone then decides
	// whether that is a hard error or a found=false load.
	//
	// Defaults to false (zero value) for every existing caller; wire it to
	// true only where a config-file-writing side effect on a missing config
	// is actually wanted (currently: `mivia chat` only - see
	// internal/clichat/chat_command.go's runChat). Every read-only/internal/
	// test caller of config.Load keeps today's found=false behavior
	// unchanged.
	AutoBootstrapUserConfig bool
}

// Load resolves config + env credentials.
func Load(opts LoadOptions) (*Resolved, error) {
	file, configPath, found, err := loadFile(opts)
	if err != nil {
		return nil, err
	}
	worktreeCfg, err := loadSelectedWorktreeConfig(configPath, found)
	if err != nil {
		return nil, err
	}
	file.Worktrees = worktreeCfg
	maxTokens := 0
	if file.Chat.MaxTokens != nil {
		maxTokens = *file.Chat.MaxTokens
	}
	if file.Chat.MaxContextTokens != nil {
		return nil, fmt.Errorf("[chat]: max_context_tokens is no longer supported; use max_prompt_tokens")
	}
	if file.Chat.MaxPromptTokens != nil && (*file.Chat.MaxPromptTokens <= 0 || *file.Chat.MaxPromptTokens > maxContextWindowTokens) {
		return nil, fmt.Errorf("[chat]: max_prompt_tokens is out of range")
	}
	if file.Tools.MaxOutputBytes < 0 {
		return nil, fmt.Errorf("[tools]: max_output_bytes must not be negative")
	}
	if file.Tools.MaxListDirEntries < 0 {
		return nil, fmt.Errorf("[tools]: max_list_dir_entries must not be negative")
	}
	if err := normalizeProviderConfigs(&file, maxTokens); err != nil {
		return nil, err
	}
	root := opts.WorkspaceRoot
	if strings.TrimSpace(root) == "" {
		if isProjectConfigShape(configPath) {
			root = filepath.Dir(filepath.Dir(filepath.Clean(configPath)))
		} else if cwd, err := os.Getwd(); err == nil {
			root = cwd
		}
	}
	mcpConfig, mcpWarnings, err := loadRuntimeMCPConfig(root)
	if err != nil {
		return nil, err
	}
	if err := refuseUntrustedMCPTable(file, configPath, root, found); err != nil {
		return nil, err
	}
	projectConfigFound := ProjectConfigExists(root)
	memCfg, err := resolveMemoryConfig(file, configPath, root, projectConfigFound)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", configPath, err)
	}
	return resolveLoaded(file, configPath, found, opts, mcpConfig, mcpWarnings, memCfg)
}

func resolveLoaded(file File, configPath string, found bool, opts LoadOptions, mcpConfig MCPConfig, mcpWarnings []string, memCfg MemoryConfig) (*Resolved, error) {
	providerName, pc, model, err := resolveProvider(file, opts)
	if err != nil {
		return nil, err
	}
	envMap, envPath, envUsed, err := loadEnvMap(file.EnvFile)
	if err != nil {
		return nil, err
	}
	key, keySet := Lookup(pc.APIKeyEnv, envMap)
	activeProfile := activeModelProfile(pc, model)
	activePromptBudget := EffectivePromptTokens(activeProfile, file.Chat.MaxTokens, promptCap(file.Chat.MaxPromptTokens), 0)
	subagentCfg, storePath, err := resolveSubagentStoreBackend(resolveSubagentConfig(file.Subagents), configPath)
	if err != nil {
		return nil, err
	}
	storeBackend := subagentCfg.StoreBackend
	redactionPolicy, err := redact.Compile(
		file.Privacy.RedactionPatterns,
		file.Privacy.RedactionKeyNames,
		file.Privacy.RedactionPlaceholder,
	)
	if err != nil {
		return nil, fmt.Errorf("config %s: [privacy]: %w", configPath, err)
	}
	res := &Resolved{
		RedactionPolicy:         redactionPolicy,
		MaxSteps:                file.Chat.MaxSteps,
		MaxUnactedContinuations: resolveUnactedContinuations(file.Chat.MaxUnactedContinuations),
		ConfigPath:              configPath,
		EnvFilePath:             envPath,
		EnvFileUsed:             envUsed,
		ProviderName:            providerName,
		Model:                   model,
		Models:                  modelNames(pc.Models),
		ModelProfiles:           cloneModelSpecs(pc.Models),
		BaseURL:                 strings.TrimRight(pc.BaseURL, "/"),
		APIKeyEnv:               pc.APIKeyEnv,
		APIKeySet:               keySet && strings.TrimSpace(key) != "",
		APIKey:                  key,
		HTTPReferer:             pc.HTTPReferer,
		XTitle:                  pc.XTitle,
		SystemPrompt:            file.Chat.SystemPrompt,
		MaxPromptTokens:         file.Chat.MaxPromptTokens,
		MaxContextTokens:        activePromptBudget,
		Temperature:             file.Chat.Temperature,
		MaxTokens:               file.Chat.MaxTokens,
		ShowIterationNotices:    file.Chat.ShowIterationNotices != nil && *file.Chat.ShowIterationNotices,
		ShowPromptCacheNotices:  file.Chat.ShowPromptCacheNotices != nil && *file.Chat.ShowPromptCacheNotices,
		Subagents:               subagentCfg,
		Worktrees:               file.Worktrees,
		StoreBackend:            storeBackend,
		StorePath:               storePath,
		StorePathSet:            file.Subagents.StorePath != "",
		Privacy:                 resolvePrivacyConfig(file.Privacy),
		Context:                 resolveContextConfig(file.Context),
		Tools:                   resolveToolsConfig(file.Tools),
		Memory:                  memCfg,
		Harness:                 file.Harness,
		Approvals:               file.Approvals,
		Workflows:               file.Workflows,
		Verifiers:               cloneVerifierProfiles(file.Verifiers),
		MCP:                     mcpConfig,
		MCPWarnings:             append([]string(nil), mcpWarnings...),
		TavilyAPIKey:            resolveTavilyAPIKey(file.Integrations.Tavily, envMap),
		PromptCache:             resolvePromptCache(file.Provider.PromptCache),
		Sync:                    resolveSyncConfig(file.Sync),
	}
	applyTimeoutBudgets(res, file, subagentCfg)
	if !found {
		return nil, fmt.Errorf("no configured provider models available")
	}
	res.ProviderRuntimes, res.modelCatalog = resolveProviderRuntimes(file, envMap, providerName)
	if err := res.Validate(); err != nil {
		return nil, err
	}
	return res, nil
}

// applyTimeoutBudgets resolves every timeout budget on one Resolved value:
// the three [provider] stream watchdog bounds, the [chat] per-request
// deadline, and the derived provider HTTP wall (which must consume the chat
// and subagent budgets, so it resolves last). Split from resolveLoaded to
// keep that function inside the structure gate.
func applyTimeoutBudgets(res *Resolved, file File, subagentCfg SubagentConfig) {
	res.StreamIdleTimeout = resolveTimeoutSeconds(file.Provider.StreamIdleTimeoutSeconds, DefaultStreamIdleTimeoutSeconds)
	res.StreamFirstByteTimeout = resolveTimeoutSeconds(file.Provider.StreamFirstByteTimeoutSeconds, DefaultStreamFirstByteTimeoutSeconds)
	res.StreamContentIdleTimeout = resolveTimeoutSeconds(file.Provider.StreamContentIdleTimeoutSeconds, DefaultStreamContentIdleTimeoutSeconds)
	res.ChatRequestTimeout = resolveTimeoutSeconds(file.Chat.RequestTimeoutSeconds, DefaultChatRequestTimeoutSeconds)
	res.ProviderHTTPTimeout = resolveProviderHTTPTimeout(res.ChatRequestTimeout, subagentCfg)
}

// resolveSubagentStoreBackend normalizes and validates [subagents]
// store_backend like the sibling [memory] backend (resolveMemoryConfig) and
// returns the resolved store path (defaulted when sqlite has none). The
// backend is a closed enum; an unvalidated value such as "SQLite" previously
// survived to the CLI, where the exact "sqlite" equality checks silently
// selected the in-memory backend and lost orchestration history on process
// exit with no error or warning.
func resolveSubagentStoreBackend(subagentCfg SubagentConfig, configPath string) (SubagentConfig, string, error) {
	storeBackend := strings.ToLower(strings.TrimSpace(subagentCfg.StoreBackend))
	if storeBackend == "" {
		storeBackend = memory.BackendMemory
	}
	if storeBackend != memory.BackendMemory && storeBackend != "sqlite" {
		return subagentCfg, "", fmt.Errorf("config %s: [subagents] store_backend must be \"memory\" or \"sqlite\", got %q", configPath, subagentCfg.StoreBackend)
	}
	if storeBackend == "sqlite" && subagentCfg.StorePath == "" {
		subagentCfg.StorePath = defaultStorePath()
	}
	subagentCfg.StoreBackend = storeBackend
	return subagentCfg, subagentCfg.StorePath, nil
}

func loadRuntimeMCPConfig(workspaceRoot string) (MCPConfig, []string, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		var err error
		workspaceRoot, err = os.Getwd()
		if err != nil {
			return MCPConfig{}, nil, fmt.Errorf("get workspace directory: %w", err)
		}
	}
	cfg, warnings, err := LoadTrustedMCPConfig(workspaceRoot)
	return cfg, warnings, err
}

const DefaultTavilyAPIKeyEnv = "TAVILY_API_KEY"

func resolveTavilyAPIKey(tc TavilyConfig, envMap map[string]string) string {
	envName := tc.APIKeyEnv
	if envName == "" {
		envName = DefaultTavilyAPIKeyEnv
	}
	// Disabled explicitly
	if tc.Disable {
		return ""
	}
	key, ok := Lookup(envName, envMap)
	if ok && strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	return ""
}

// resolvePromptCache defaults an unset [provider] prompt_cache to "auto" so
// a config written before this field existed keeps loading unchanged.
// Anything else passes through unchanged for Resolved.Validate to reject.
func resolvePromptCache(raw string) string {
	if raw == "" {
		return "auto"
	}
	return raw
}

// DefaultStreamIdleTimeoutSeconds, DefaultStreamFirstByteTimeoutSeconds, and
// DefaultStreamContentIdleTimeoutSeconds mirror internal/provider's own
// watchdog defaults (provider.DefaultStreamIdleTimeout /
// DefaultStreamFirstByteTimeout / DefaultStreamContentIdleTimeout).
// Duplicated rather than imported: internal/provider already imports
// internal/config (ollama.go, provider.go), so config importing provider
// back would cycle. A mirror-pin test in internal/provider keeps the two
// sides equal.
const (
	DefaultStreamIdleTimeoutSeconds        = 100
	DefaultStreamFirstByteTimeoutSeconds   = 240
	DefaultStreamContentIdleTimeoutSeconds = 90
)

// MaxUnactedContinuationsCeiling clamps [chat] max_unacted_continuations.
// Every continuation is a full extra provider call on a turn that already
// answered, so a mistyped 500 must not turn one turn into 500 billable
// requests. The sibling empty-response retry is a compiled constant for the
// same reason; this knob is configurable but still bounded.
const MaxUnactedContinuationsCeiling = 3

// resolveUnactedContinuations normalizes the configured continuation bound:
// negative or zero is off, anything above the ceiling is clamped to it.
func resolveUnactedContinuations(configured int) int {
	if configured <= 0 {
		return 0
	}
	return min(configured, MaxUnactedContinuationsCeiling)
}

// DefaultChatRequestTimeoutSeconds is the per-LLM-request context deadline
// for a root AGENT turn when [chat] request_timeout_seconds is unset. It
// keeps the historical 15-minute value (chat.DefaultRequestTimeout mirrors
// it as a fallback for sessions built without config; internal/chat imports
// internal/config, so the mirror lives there, not here).
const DefaultChatRequestTimeoutSeconds = 900

// DefaultProviderHTTPTimeoutSeconds mirrors internal/provider's
// DefaultHTTPTimeout (15 minutes), duplicated for the same import-cycle
// reason as the stream watchdog mirrors above; the mirror-pin test in
// internal/provider keeps them equal. It is the floor of the derived
// http.Client wall. DefaultHTTPWallMarginSeconds is the headroom the wall
// keeps above every configured per-request budget.
const (
	DefaultProviderHTTPTimeoutSeconds = 900
	DefaultHTTPWallMarginSeconds      = 60
)

// resolveProviderHTTPTimeout derives the absolute http.Client wall from
// every configured per-request budget. The wall must stay above each budget
// plus the margin, so a spent budget reports as its own terminal context
// deadline and never as a transient transport fault (a wall hit looks
// retryable to the retry layer). Any new request-budget source must feed
// this max().
func resolveProviderHTTPTimeout(chatRequest time.Duration, subagents SubagentConfig) time.Duration {
	wall := time.Duration(DefaultProviderHTTPTimeoutSeconds) * time.Second
	margin := time.Duration(DefaultHTTPWallMarginSeconds) * time.Second
	if bound := chatRequest + margin; bound > wall {
		wall = bound
	}
	if bound := ResolvedSubagentRequestTimeout(subagents) + margin; bound > wall {
		wall = bound
	}
	return wall
}

// SaturatingSeconds converts a configured seconds count to a Duration,
// saturating at +/- MaxTimeoutSeconds (defaults.go) - the repo's one
// overflow-safety ceiling - so the multiply and any later margin addition
// cannot overflow. A TOML integer can carry up to math.MaxInt64 seconds,
// and a plain multiply by time.Second wraps negative above ~292 years.
// The sign is preserved: callers that treat a negative value as "off" see
// it unchanged. Every config-controlled seconds-to-Duration conversion
// must route through this helper, not a bare multiply.
func SaturatingSeconds(sec int) time.Duration {
	if sec > MaxTimeoutSeconds {
		sec = MaxTimeoutSeconds
	}
	if sec < -MaxTimeoutSeconds {
		sec = -MaxTimeoutSeconds
	}
	return time.Duration(sec) * time.Second
}

// resolveTimeoutSeconds defaults an unset (nil) [provider] timeout knob to
// def seconds, converting the resolved value to a time.Duration once so
// Resolved carries a ready-to-use bound instead of a raw *int.
func resolveTimeoutSeconds(raw *int, def int) time.Duration {
	if raw == nil || *raw <= 0 {
		return SaturatingSeconds(def)
	}
	return SaturatingSeconds(*raw)
}

const (
	maxProviderModels      = 128
	minContextWindowTokens = 1024
	maxContextWindowTokens = 10_000_000
)

func resolveProvider(file File, opts LoadOptions) (string, ProviderConfig, string, error) {
	if file.Providers == nil {
		file.Providers = map[string]ProviderConfig{}
	}
	name := strings.TrimSpace(file.Provider.Name)
	if name == "" {
		name = DefaultProvider
	}
	if opts.ProviderOverride != "" {
		name = strings.TrimSpace(opts.ProviderOverride)
	}
	name = strings.ToLower(name)
	descriptor, ok := providerregistry.Lookup(name)
	if !ok {
		return "", ProviderConfig{}, "", fmt.Errorf("unknown provider %q (supported: %s)", name, strings.Join(providerregistry.Names(), ", "))
	}
	pc := file.Providers[name]
	if len(pc.Models) == 0 {
		return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: models must be non-empty", name)
	}
	defaultModel := strings.TrimSpace(pc.DefaultModel)
	model := pc.Models[0].Name
	if defaultModel != "" {
		normalizedDefault, err := NormalizeModelName(defaultModel)
		if err != nil {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: default_model is invalid", name)
		}
		defaultModel = normalizedDefault
		if !slices.Contains(modelNames(pc.Models), normalizedDefault) {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: default_model is not in models (%s)", name, strings.Join(modelNames(pc.Models), ", "))
		}
		pc.DefaultModel = defaultModel
		model = defaultModel
	}
	if pc.BaseURL == "" {
		pc.BaseURL = descriptor.DefaultURL
	}
	if pc.APIKeyEnv == "" {
		pc.APIKeyEnv = descriptor.DefaultAPIKeyEnv
	}
	if strings.TrimSpace(opts.ModelOverride) != "" {
		override, err := NormalizeModelName(opts.ModelOverride)
		if err != nil {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: --model is invalid", name)
		}
		if !slices.Contains(modelNames(pc.Models), override) {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: --model is not in models (%s)", name, strings.Join(modelNames(pc.Models), ", "))
		}
		model = override
	}
	return name, pc, model, nil
}

func normalizeProviderConfigs(file *File, maxTokens int) error {
	if file.Providers == nil {
		file.Providers = map[string]ProviderConfig{}
	}
	seen := make(map[string]string, len(file.Providers))
	normalized := make(map[string]ProviderConfig, len(file.Providers))
	for rawName, pc := range file.Providers {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" {
			return fmt.Errorf("provider name is empty")
		}
		if previous, ok := seen[name]; ok && previous != rawName {
			return fmt.Errorf("provider names %q and %q collide by case", previous, rawName)
		}
		if _, ok := providerregistry.Lookup(name); !ok {
			return fmt.Errorf("unknown provider %q", name)
		}
		if pc.LegacyModel != nil {
			return fmt.Errorf("[providers.%s]: model is no longer supported; declare models", name)
		}
		models, err := normalizeModels(pc.Models, maxTokens, name)
		if err != nil {
			return fmt.Errorf("[providers.%s]: %w", name, err)
		}
		pc.Models = models
		if pc.BaseURL == "" {
			d, _ := providerregistry.Lookup(name)
			pc.BaseURL = d.DefaultURL
		}
		if pc.APIKeyEnv == "" {
			d, _ := providerregistry.Lookup(name)
			pc.APIKeyEnv = d.DefaultAPIKeyEnv
		}
		normalized[name] = pc
		seen[name] = rawName
	}
	file.Providers = normalized
	return nil
}

func modelNames(models []ModelSpec) []string {
	names := make([]string, len(models))
	for i, model := range models {
		names[i] = model.Name
	}
	return names
}

func cloneModelSpecs(models []ModelSpec) []ModelSpec {
	return slices.Clone(models)
}

// decodeConfigInto TOML-decodes data into file (only overwriting keys data
// explicitly sets - go-toml/v2 leaves fields absent from data untouched, see
// loadFile's doc comment), then re-runs the raw-byte probes that a plain
// struct decode cannot express (an explicit-vs-absent zero value, a legacy
// key name). Called once per layer in loadFile, base then overlay, so a
// later layer's explicit keys win over an earlier layer's the same way a
// second Decode call already would - the probes must follow that same
// per-layer, later-wins order to stay consistent with the struct fields
// they annotate. Like the struct decode, a probe never clears a presence
// flag an earlier layer set when this layer omits the key: absence
// preserves, an explicit later key wins.
func decodeConfigInto(data []byte, path string, file *File) error {
	dec := toml.NewDecoder(bytes.NewReader(data)).EnableUnmarshalerInterface()
	if err := dec.Decode(file); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	// Probe the raw bytes for an explicit [subagents] inline_output_bytes key:
	// the main decode cannot tell an explicit 0 from an absent key, and
	// resolveSubagentConfig must preserve an explicit 0 ("always use refs").
	probeInlineOutputBytes(data, file)
	// Same probe for [subagents] spawn_stagger_ms: an explicit 0 (disabled)
	// must survive resolution; an absent key takes the default.
	probeSpawnStaggerMs(data, file)
	// Raw bytes are the only place model keys still exist; see auditModelKeys.
	if err := auditModelKeys(data); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}

// loadFile resolves the base config (opts.ConfigPath, else the first of
// DefaultConfigCandidates() that exists) and, when opts.WorkspaceRoot names
// a directory with its own .mivia/mivia.toml distinct from that base file,
// layers it on top as an overlay - its explicit keys win, everything else
// keeps the base file's values (see decodeConfigInto).
//
// This matters whenever a base config was resolved from something other
// than the workspace's own file - most commonly an explicit --config/
// MIVIA_CONFIG pointed at a user-level provider catalog (mivia-agent-desktop
// pins one for every spawned thread, see resolve_user_config_path in
// src-tauri/src/commands/agent.rs) while chatting against a project that has
// its own workspace .mivia/mivia.toml. Before this, that explicit ConfigPath
// won outright and the workspace file's own settings - [subagents]
// store_path redirecting durable storage (chat sessions, workflow run
// history) elsewhere being the one that surfaced this, but the same applied
// to every other workspace-local override - were silently discarded, with
// no error: a workspace-relative context/workflow store the interactive TUI
// (which naturally resolves the workspace file via DefaultConfigCandidates'
// own cwd search, no explicit ConfigPath in play) had been writing to for
// months would appear completely empty from a caller that pins ConfigPath,
// because it was reading and writing a different SQLite file. The workspace
// file wins on overlap because it best knows this project's own storage/
// tooling needs; the base file supplies whatever the workspace file doesn't
// set (typically the provider/model catalog and API key wiring, which a
// per-project file rarely if ever redefines).
func loadFile(opts LoadOptions) (File, string, bool, error) {
	path := ExpandPath(opts.ConfigPath)
	if path == "" {
		path, _ = FirstExisting(DefaultConfigCandidates())
	}
	if path == "" && opts.AutoBootstrapUserConfig && strings.TrimSpace(opts.ConfigPath) == "" {
		bootstrapped, err := autoBootstrapUserConfig()
		if err != nil {
			return File{}, "", false, err
		}
		path = bootstrapped
	}
	if path == "" {
		if !opts.AllowMissingConfig {
			return File{}, "", false, fmt.Errorf("no config file found (tried %s); set MIVIA_CONFIG or create .mivia/mivia.toml", strings.Join(DefaultConfigCandidates(), ", "))
		}
		return File{}, "", false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, path, false, fmt.Errorf("read config %s: %w", path, err)
	}
	var file File
	if err := decodeConfigInto(data, path, &file); err != nil {
		return File{}, path, false, err
	}

	if overlayPath, ok := workspaceOverlayConfigPath(opts.WorkspaceRoot, path); ok {
		overlayData, err := os.ReadFile(overlayPath)
		if err != nil {
			return File{}, path, false, fmt.Errorf("read workspace config %s: %w", overlayPath, err)
		}
		if err := decodeConfigInto(overlayData, overlayPath, &file); err != nil {
			return File{}, path, false, err
		}
	}

	// [verifiers] deliberately does NOT layer: evidence-gate profiles are the
	// WORKSPACE'S property, so they come only from the workspace's own
	// .mivia/mivia.toml — the same file `mivia workflows validate` reads.
	// Resolving them from a user-level base config would let validation pass
	// on one machine and fail on another, and would let a user file define
	// the commands that judge a project's gates.
	verifiers, err := LoadWorkspaceVerifiers(opts.WorkspaceRoot)
	if err != nil {
		return File{}, path, false, err
	}
	file.Verifiers = verifiers

	return file, path, true, nil
}

// workspaceOverlayConfigPath returns workspaceRoot's own .mivia/mivia.toml
// path when it exists as a regular file and differs from basePath (already
// resolved and about to be/already loaded as the base config) - a workspace
// root with no config of its own, or one that IS the base config already
// (the common case of no explicit --config/MIVIA_CONFIG), has nothing to
// overlay.
func workspaceOverlayConfigPath(workspaceRoot, basePath string) (string, bool) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return "", false
	}
	candidate := workspace.NamespacePath(workspaceRoot, "mivia.toml")
	if candidate == basePath {
		return "", false
	}
	info, err := os.Stat(candidate)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return candidate, true
}

// probeSpawnStaggerMs re-parses data for an explicit [subagents]
// spawn_stagger_ms key and records its presence on file.Subagents, with the
// same presence-vs-value semantics as probeInlineOutputBytes below: a *int
// keeps an explicit 0 (stagger disabled) distinct from an absent key (default
// 150ms applies in resolveSubagentConfig), and presence is monotonic across
// base + overlay layers.
func probeSpawnStaggerMs(data []byte, file *File) {
	var probe struct {
		Subagents struct {
			SpawnStaggerMs *int `toml:"spawn_stagger_ms"`
		} `toml:"subagents"`
	}
	if err := toml.Unmarshal(data, &probe); err != nil {
		return // main decode reports parse errors; the probe only detects presence
	}
	if probe.Subagents.SpawnStaggerMs != nil {
		file.Subagents.spawnStaggerMsSet = true
	}
}

// probeInlineOutputBytes re-parses data for an explicit [subagents]
// inline_output_bytes key and records its presence on file.Subagents. A *int
// field keeps presence (nil = absent) distinct from value (0 is a real
// "always use refs" configuration). Presence is monotonic across the base
// and overlay layers: an explicit key in a layer (the later layer winning on
// value, matching the struct decode) sets inlineOutputBytesSet; a layer that
// omits the key leaves the flag as it was, so an operator-set 0 in the base
// config survives a workspace overlay that does not mention the key. The
// main decode already accepted data into the superset File struct, so
// re-unmarshalling the same bytes into this narrower probe struct cannot
// fail; the error is discarded rather than plumbed through as an untestable
// path.
func probeInlineOutputBytes(data []byte, file *File) {
	var probe struct {
		Subagents struct {
			InlineOutputBytes *int `toml:"inline_output_bytes"`
		} `toml:"subagents"`
	}
	_ = toml.Unmarshal(data, &probe)
	if probe.Subagents.InlineOutputBytes != nil {
		file.Subagents.inlineOutputBytesSet = true
	}
}

// loadSelectedWorktreeConfig reads the selected config file again to preserve
// the difference between an absent branch_prefix and an explicit empty value.
func loadSelectedWorktreeConfig(path string, found bool) (WorktreeConfig, error) {
	if !found {
		return resolveWorktreeConfig(WorktreeConfig{})
	}
	return loadWorktreeConfigPath(path)
}

func loadEnvMap(explicit string) (map[string]string, string, bool, error) {
	if explicit != "" {
		path := ExpandPath(explicit)
		m, err := sdkenvfile.Load(path)
		if err != nil {
			return nil, path, false, fmt.Errorf("load env_file %s: %w", path, err)
		}
		return m, path, true, nil
	}
	if p, ok := FirstExisting(DefaultEnvCandidates()); ok {
		m, err := sdkenvfile.Load(p)
		if err != nil {
			return nil, p, false, fmt.Errorf("load env file %s: %w", p, err)
		}
		return m, p, true, nil
	}
	return map[string]string{}, "", false, nil
}

// resolveSubagentConfig merges file config with defaults.
// Only the system prompt is defaulted; 0 means unlimited for all bounds
// (NestedSteps, MaxDepth, MaxFanout, MaxWorkers).
func resolveSubagentConfig(cfg SubagentConfig) SubagentConfig {
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = DefaultSubagentConfig.SystemPrompt
	}
	if !cfg.inlineOutputBytesSet && cfg.InlineOutputBytes == 0 {
		cfg.InlineOutputBytes = DefaultSubagentConfig.InlineOutputBytes
	}
	if cfg.SchemaRetryMax <= 0 { // 0 = use default 2, not "no retries"
		cfg.SchemaRetryMax = DefaultSubagentConfig.SchemaRetryMax
	} else if cfg.SchemaRetryMax > MaxSchemaRetryMax { // typo guard, see MaxSchemaRetryMax
		cfg.SchemaRetryMax = MaxSchemaRetryMax
	}
	if !cfg.spawnStaggerMsSet {
		cfg.SpawnStaggerMs = DefaultSubagentConfig.SpawnStaggerMs
	} else if cfg.SpawnStaggerMs > maxSpawnStaggerMs { // typo guard, see maxSpawnStaggerMs
		cfg.SpawnStaggerMs = maxSpawnStaggerMs
	}
	cfg.Messaging = resolveMessagingConfig(cfg.Messaging)
	return cfg
}
