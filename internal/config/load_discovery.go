package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// Config file discovery and layering: which files are read, in what order,
// and how each layer's raw bytes are decoded onto the accumulating File.
// The precedence chain is base candidate -> workspace overlay ->
// user-level provider fallback (providers_user_fallback.go), with
// [verifiers] deliberately resolved from the workspace file alone.

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
	// Probe the raw bytes for the [subagents] keys whose explicit 0 differs
	// from their absence (inline_output_bytes "always use refs",
	// spawn_stagger_ms "disabled"): the main decode sees 0 either way, and
	// resolveSubagentConfig must preserve the operator's explicit choice.
	probeSubagentPresence(data, file)
	// Raw bytes are the only place model keys still exist; see auditModelKeys.
	if err := auditModelKeys(data); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}

// firstProviderCandidate scans DefaultConfigCandidates() in order and returns
// the first existing candidate whose config actually declares a provider
// ([provider].name or any [providers.*] entry). A workspace mivia.toml that
// only carries workspace concerns (workflows, verifiers, MCP) must not shadow
// the user config and kill the first-time-user flow: without this filter, the
// workspace file becomes the base config, AutoBootstrapUserConfig never fires
// (it only triggers when NO candidate exists), and resolveProvider fails with
// a baffling "[providers.openrouter]: models must be non-empty" naming a
// provider the user never configured.
//
// When no existing candidate declares a provider: if allowBootstrap is true
// the return is "" so loadFile can auto-bootstrap the user config (the
// provider-less workspace file still applies through the overlay path);
// otherwise the first existing candidate is returned unchanged, preserving
// the pre-existing found=true/resolveProvider-error behavior for callers
// that did not opt into bootstrapping. Candidates that exist but fail to
// decode are treated as provider-less; their parse error still surfaces
// later, when the file is loaded as the base or as the workspace overlay.
func firstProviderCandidate(allowBootstrap bool) string {
	candidates := DefaultConfigCandidates()
	firstExisting := ""
	for _, cand := range candidates {
		if strings.TrimSpace(cand) == "" {
			continue
		}
		if info, err := os.Stat(cand); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if firstExisting == "" {
			firstExisting = cand
		}
		data, err := os.ReadFile(cand)
		if err != nil {
			continue
		}
		var file File
		if err := decodeConfigInto(data, cand, &file); err != nil {
			continue
		}
		if strings.TrimSpace(file.Provider.Name) != "" || len(file.Providers) > 0 {
			return cand
		}
	}
	if allowBootstrap {
		return ""
	}
	return firstExisting
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
// loadedFile is loadFile's result. BaseMCP is the [mcp] table decoded from
// the BASE file alone, captured before any workspace overlay is merged into
// File - see refuseUntrustedMCPTable, which judges trust on BaseMCP rather
// than the merged File.MCP so that an overlay-declared table (one of the two
// TRUSTED paths) is never mistaken for one the untrusted base file declared.
type loadedFile struct {
	File       File
	ConfigPath string
	Found      bool
	BaseMCP    MCPConfig
}

func loadFile(opts LoadOptions) (loadedFile, error) {
	path := ExpandPath(opts.ConfigPath)
	if path == "" {
		path = firstProviderCandidate(opts.AutoBootstrapUserConfig)
	}
	if path == "" && opts.AutoBootstrapUserConfig && strings.TrimSpace(opts.ConfigPath) == "" {
		bootstrapped, err := autoBootstrapUserConfig()
		switch {
		case errors.Is(err, errUserConfigExists):
			// The candidate scan reported "no provider anywhere" only because
			// the existing user config is provider-less or undecodable. Load
			// it as the base anyway so the file's own error surfaces below,
			// rather than a bootstrap message that names neither the problem
			// nor the remedy every other command prints.
			path = UserConfigPath()
		case err != nil:
			return loadedFile{}, err
		default:
			path = bootstrapped
		}
	}
	if path == "" {
		if !opts.AllowMissingConfig {
			return loadedFile{}, fmt.Errorf("no config file found (tried %s); set MIVIA_CONFIG or create %s", strings.Join(DefaultConfigCandidates(), ", "), filepath.Join(workspace.Namespace, "mivia.toml"))
		}
		return loadedFile{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return loadedFile{ConfigPath: path}, fmt.Errorf("read config %s: %w", path, err)
	}
	var file File
	if err := decodeConfigInto(data, path, &file); err != nil {
		return loadedFile{ConfigPath: path}, err
	}

	// baseMCP is the [mcp] table as decoded from the base file alone, before
	// any workspace overlay below can add or change it. refuseUntrustedMCPTable
	// judges trust against this snapshot, not the merged file.MCP, so that an
	// overlay declared at a TRUSTED path (the workspace's own .mivia/mivia.toml)
	// is never mistaken for a table the untrusted base file declared itself.
	baseMCP := file.MCP

	if overlayPath, ok := workspaceOverlayConfigPath(opts.WorkspaceRoot, path); ok {
		overlayData, err := os.ReadFile(overlayPath)
		if err != nil {
			return loadedFile{ConfigPath: path}, fmt.Errorf("read workspace config %s: %w", overlayPath, err)
		}
		if err := decodeConfigInto(overlayData, overlayPath, &file); err != nil {
			return loadedFile{ConfigPath: path}, err
		}
	}

	// mergeProviderFallback layers ~/.mivia/mivia.toml's [provider]/
	// [providers.*] underneath whatever the base config (+ workspace overlay
	// above) already declared, so the user-level catalog/credentials remain
	// available even when an explicit --config/$MIVIA_CONFIG or a provider-
	// less workspace file was selected as the base. See its own doc comment.
	if err := mergeProviderFallback(&file, path); err != nil {
		return loadedFile{ConfigPath: path}, err
	}

	// [verifiers] deliberately does NOT layer: evidence-gate profiles are the
	// WORKSPACE'S property, so they come only from the workspace's own
	// .mivia/mivia.toml — the same file `mivia workflows validate` reads.
	// Resolving them from a user-level base config would let validation pass
	// on one machine and fail on another, and would let a user file define
	// the commands that judge a project's gates.
	verifiers, err := LoadWorkspaceVerifiers(opts.WorkspaceRoot)
	if err != nil {
		return loadedFile{ConfigPath: path}, err
	}
	file.Verifiers = verifiers

	return loadedFile{File: file, ConfigPath: path, Found: true, BaseMCP: baseMCP}, nil
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

// loadSelectedWorktreeConfig reads the selected config file again to preserve
// the difference between an absent branch_prefix and an explicit empty value.
func loadSelectedWorktreeConfig(path string, found bool) (WorktreeConfig, error) {
	if !found {
		return resolveWorktreeConfig(WorktreeConfig{})
	}
	return loadWorktreeConfigPath(path)
}
