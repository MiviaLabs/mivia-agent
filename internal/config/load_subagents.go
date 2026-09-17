package config

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/pelletier/go-toml/v2"
)

// The [subagents] presence contract: the raw-byte probe that records which
// presence-sensitive keys a layer actually declared, and the resolution that
// consumes those flags. Probe and resolver live together on purpose - an
// explicit 0 is a real configuration for both inline_output_bytes ("always
// use refs") and spawn_stagger_ms ("staggering disabled"), and splitting the
// only writer of those flags from their only reader is how the overlay
// regression pinned by TestInlineOutputBytesExplicitZeroSurvivesWorkspaceOverlay
// became possible in the first place.

// rejectNegativeSubagentKnobs fails the load for the two [subagents] knobs
// whose resolution has no defined meaning for a negative value, matching the
// sibling [tools] max_output_bytes / max_list_dir_entries checks in Load.
//
// Both are typo guards, not repairs of a reachable defect. A negative value
// already dead-ends at a downstream guard today - BelowInlineThreshold treats
// any threshold <= 0 as "always use refs", and the dispatch loop only staggers
// when SpawnStagger > 0 - so the observable behavior is exactly that of an
// explicit 0. That is the problem: `spawn_stagger_ms = -150` silently means
// "staggering disabled" when the operator plainly meant 150ms, and nothing
// reports the typo. Rejecting at load turns a silent reinterpretation into a
// named error, on the one layer that knows what the operator wrote.
//
// This is deliberately per-key, not a blanket "no negatives under [subagents]".
// DefaultTotalTimeoutSec documents negative as a real value (an explicit
// operator opt-out of the last-resort termination bound), and MaxDepth /
// MaxFanout / NestedSteps route through separate policy. Only add a knob here
// once its negative case is genuinely meaningless.
func rejectNegativeSubagentKnobs(cfg SubagentConfig) error {
	if cfg.InlineOutputBytes < 0 {
		return fmt.Errorf("[subagents]: inline_output_bytes must not be negative (0 means always use refs)")
	}
	if cfg.SpawnStaggerMs < 0 {
		return fmt.Errorf("[subagents]: spawn_stagger_ms must not be negative (0 disables staggering)")
	}
	return nil
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

// probeSubagentPresence re-parses data for the [subagents] keys whose
// explicit-zero value differs from their absence, and records which ones this
// layer actually declared. The main struct decode cannot express that
// difference: it sees 0 either way.
//
// Presence is per-key and monotonic across layers. A layer sets a flag only
// when its own bytes carry that key; a layer that omits the key never clears
// a flag an earlier layer set. That is what lets an operator's explicit
// inline_output_bytes = 0 in the base config survive a workspace overlay that
// is silent about it (the later layer still wins on VALUE, matching the struct
// decode - only absence is non-destructive). The two flags stay independent:
// a layer declaring only spawn_stagger_ms must not mark inline_output_bytes
// present.
//
// The unmarshal cannot fail, but not simply because "the main decode already
// accepted these bytes": that decode runs with EnableUnmarshalerInterface(), so
// it is not a plain superset of this probe. It holds because decodeConfigInto
// returns early on a failed File decode, this probe declares only [subagents]
// *int fields (go-toml/v2 skips keys it has no field for, untyped), and the
// package's one custom unmarshaler, ModelSpec.UnmarshalTOML, is strictly MORE
// restrictive than the decoding it replaces. Do not grow this struct outside
// [subagents]: crossing that asymmetry is what would make the error branch
// reachable. It is handled regardless - on error neither flag is set, so a
// partial decode cannot invent presence.
func probeSubagentPresence(data []byte, file *File) {
	var probe struct {
		Subagents struct {
			InlineOutputBytes *int `toml:"inline_output_bytes"`
			SpawnStaggerMs    *int `toml:"spawn_stagger_ms"`
		} `toml:"subagents"`
	}
	if err := toml.Unmarshal(data, &probe); err != nil {
		return
	}
	if probe.Subagents.InlineOutputBytes != nil {
		file.Subagents.inlineOutputBytesSet = true
	}
	if probe.Subagents.SpawnStaggerMs != nil {
		file.Subagents.spawnStaggerMsSet = true
	}
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
