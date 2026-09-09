package config

import (
	"os"
	"path/filepath"
	"strings"
)

// mergeProviderFallback makes ~/.mivia/mivia.toml the global provider layer
// underneath EVERY base config - including one selected via an explicit
// --config/$MIVIA_CONFIG path, which today wins outright and never sees the
// user file at all (see loadFile's own doc comment on the workspace
// overlay, which has the same "explicit path wins" starting point but for a
// different pair of files). Only [provider] and [providers.*] are
// inherited from the user file; every other section ([chat], [tools],
// [approvals], [subagents], ...) is a base-config-only concern and is never
// read here. This is deliberately narrower than the workspace overlay: a
// user-level file supplies credentials/catalog, never workspace or tool
// policy.
//
// Called once per load, after the (optional) workspace overlay has already
// been decoded onto file, so the fallback fills in strictly BELOW both the
// base config and the workspace overlay - neither ever loses to it.
//
// basePath is the already-resolved base config path (loadFile's `path`,
// pre-overlay); when it is empty (no base config at all - the
// AllowMissingConfig path) or clean-equal to the user config path, this is a
// no-op: the user file either has nothing to layer under, or it already IS
// the base file being decoded (self-merge would be a pointless no-op at best
// and confusing at worst). The comparison is raw filepath.Clean equality
// only - no symlink resolution, no case-insensitive normalization on a
// case-insensitive filesystem - mirroring the existing caveat on
// workspaceOverlayConfigPath's own basePath comparison in this same file.
func mergeProviderFallback(file *File, basePath string) error {
	userConfigPath := UserConfigPath()
	if userConfigPath == "" {
		return nil
	}
	if basePath != "" && filepath.Clean(basePath) == filepath.Clean(userConfigPath) {
		return nil
	}
	data, err := os.ReadFile(userConfigPath)
	if err != nil {
		// Missing or unreadable: the user file simply contributes nothing.
		// This is not an error path - most callers have no user config at
		// all, and an unreadable one (permissions, transient FS issue) must
		// not turn a perfectly good explicit/workspace config load into a
		// hard failure over a layer that was only ever going to fill gaps.
		return nil
	}
	var userFile File
	if err := decodeConfigInto(data, userConfigPath, &userFile); err != nil {
		return err
	}
	// A user file that declares no provider at all (fresh checkout, or one
	// that only ever held other settings) has nothing to contribute; skip
	// the per-field/per-key merge below entirely rather than let empty
	// zero-value fields "fill" something that was never actually set.
	if userFile.Provider.Name == "" && len(userFile.Providers) == 0 {
		return nil
	}

	// (a) [provider] section: per-field fill, never override. Each field is
	// independent - an operator may have set default_model/name in the base
	// config but left the stream timeouts (or vice versa) to the user file.
	if file.Provider.Name == "" {
		file.Provider.Name = userFile.Provider.Name
	}
	if file.Provider.PromptCache == "" {
		file.Provider.PromptCache = userFile.Provider.PromptCache
	}
	if file.Provider.StreamIdleTimeoutSeconds == nil {
		file.Provider.StreamIdleTimeoutSeconds = userFile.Provider.StreamIdleTimeoutSeconds
	}
	if file.Provider.StreamFirstByteTimeoutSeconds == nil {
		file.Provider.StreamFirstByteTimeoutSeconds = userFile.Provider.StreamFirstByteTimeoutSeconds
	}
	if file.Provider.StreamContentIdleTimeoutSeconds == nil {
		file.Provider.StreamContentIdleTimeoutSeconds = userFile.Provider.StreamContentIdleTimeoutSeconds
	}

	// (b) [providers.*]: per-key fill. A base stanza for a given provider
	// name is NEVER overridden, even partially - the base's own [providers.X]
	// table (endpoint, key env, and critically its model catalog) is treated
	// as a complete, deliberate declaration, not a set of fields to merge
	// field-by-field the way (a) does for the single [provider] section.
	// Only provider names entirely absent from the base are filled in from
	// the user file. Keys are compared case-insensitively using the same
	// ToLower+TrimSpace convention normalizeProviderConfigs uses when it
	// later collapses raw TOML table keys to canonical provider names.
	if file.Providers == nil {
		file.Providers = map[string]ProviderConfig{}
	}
	existing := make(map[string]bool, len(file.Providers))
	for k := range file.Providers {
		existing[strings.ToLower(strings.TrimSpace(k))] = true
	}
	for k, v := range userFile.Providers {
		norm := strings.ToLower(strings.TrimSpace(k))
		if existing[norm] {
			continue
		}
		file.Providers[k] = v
		existing[norm] = true
	}
	return nil
}
