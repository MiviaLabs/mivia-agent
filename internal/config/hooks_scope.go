package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// maxHookConfigBytes bounds the user config read for hooks. The general Load
// path has no bound because it reads whatever the operator pointed it at; this
// path feeds a trust decision, so it declares one.
const maxHookConfigBytes = 1 << 20

// HooksSource is every config file lifecycle hooks may come from.
//
// There are two, and they ADD rather than replace: the user config at its fixed
// path, and the workspace's own .mivia/mivia.toml. Hooks are the one setting
// mivia merges across layers, and they have to be - a project's formatter and a
// user's global gate are not competing answers to one question, they are two
// hooks, and letting the workspace file replace the user's would silently
// disarm a gate by opening a repository.
//
// Reading hooks from the workspace means a cloned repository can execute
// commands on first launch. That is a deliberate product decision, not an
// oversight: project-defined hooks are the point of the feature. It is stated
// here, in the `/hooks` listing, at startup and in the docs, because the one
// thing it must never be is a surprise. Cloning a repository is taking delivery
// of code you are about to run.
//
// $MIVIA_CONFIG still does not supply hooks. It names the GENERAL config, and a
// table in it is reported rather than loaded - the workspace file is the project
// surface, and a second one selected by an environment variable would make
// "which files can run commands here" depend on how mivia was launched.
type HooksSource struct {
	// Files are the hook-bearing configs, user config FIRST.
	//
	// Order is load-bearing rather than cosmetic: PreToolUse stops at the first
	// deny, so the user's own gates answer before a repository's do.
	Files []HookConfigFile
	// Warnings are user-visible startup diagnostics - one per config file that
	// declared hooks mivia will not load. A silently ignored hook is how
	// someone concludes hooks are broken.
	Warnings []string
}

// HookConfigFile is one file's hook bytes and where they came from.
type HookConfigFile struct {
	// Path is the file these bytes were read from.
	Path string
	// Data is the raw TOML. Parsing happens in internal/hooks; this type owns
	// provenance only.
	Data []byte
	// Project marks the workspace's own config, as opposed to the user's. Every
	// surface that shows a hook shows this, because "which of these came with
	// the repository" is the question a reader actually has.
	Project bool
}

// UserPath is the fixed user config path, for messages that name it.
func (s HooksSource) UserPath() string { return UserConfigPath() }

// LoadHooksSource resolves every config file lifecycle hooks may come from and
// reports the ones that declared hooks and were not loaded.
//
// A missing user config is not an error: hooks are optional. An unreadable or
// link-shaped user config IS an error - that file is the operator's own, and an
// ambiguous read of it fails closed rather than loading zero hooks silently.
func LoadHooksSource(workspaceRoot string) (HooksSource, error) {
	var src HooksSource
	if userPath := UserConfigPath(); userPath != "" {
		data, err := readTrustedHookConfig(userPath)
		if err != nil && !os.IsNotExist(err) {
			return HooksSource{}, err
		}
		if err == nil && hookGroupCount(data) > 0 {
			src.Files = append(src.Files, HookConfigFile{Path: userPath, Data: data})
		}
	}
	src.addProjectConfig(workspaceRoot)
	for _, candidate := range ignoredHookConfigCandidates(workspaceRoot) {
		data, err := readBoundedConfig(candidate)
		if err != nil {
			// An oversized candidate is reported rather than skipped: silence
			// there reads as "no hooks here".
			if errors.Is(err, errConfigTooLarge) {
				src.Warnings = append(src.Warnings, fmt.Sprintf(
					"not inspected for lifecycle hooks: %s exceeds %d bytes", candidate, maxHookConfigBytes))
			}
			continue
		}
		if n := hookGroupCount(data); n > 0 {
			src.Warnings = append(src.Warnings, fmt.Sprintf(
				"ignoring %d lifecycle hook group(s) in %s: $MIVIA_CONFIG names the general config and does not supply hooks",
				n, candidate))
		}
	}
	return src, nil
}

// addProjectConfig loads the workspace's own hook config.
//
// A fault in this file NEVER fails startup, and the asymmetry against the user
// config is deliberate: the user config is the operator's own file and a fault
// in it is theirs to fix, while ANY repository can ship a workspace file, and
// letting one break every session in that directory hands a cloned repo a
// denial of service. Faults are reported; the file contributes nothing.
func (s *HooksSource) addProjectConfig(workspaceRoot string) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return
	}
	path := workspace.NamespacePath(workspaceRoot, "mivia.toml")
	if path == "" || sameFilePath(path, UserConfigPath()) {
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		return
	}
	// A symlink here would let a repository point its hook source at a file
	// outside itself - including one nobody reviewing that repo ever opened.
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		s.Warnings = append(s.Warnings, fmt.Sprintf(
			"ignoring lifecycle hooks in %s: it must be a regular file, not a link", path))
		return
	}
	data, err := readBoundedConfig(path)
	if err != nil {
		if errors.Is(err, errConfigTooLarge) {
			s.Warnings = append(s.Warnings, fmt.Sprintf(
				"not inspected for lifecycle hooks: %s exceeds %d bytes", path, maxHookConfigBytes))
		}
		return
	}
	if hookGroupCount(data) == 0 {
		return
	}
	s.Files = append(s.Files, HookConfigFile{Path: path, Data: data, Project: true})
}

// errConfigTooLarge marks a candidate config mivia declined to read whole.
var errConfigTooLarge = errors.New("config file exceeds the inspection bound")

// readBoundedConfig reads at most maxHookConfigBytes from an UNTRUSTED config
// file. os.ReadFile would size the buffer from the file, so a cloned repo
// shipping a multi-gigabyte .mivia/mivia.toml would exhaust memory at startup
// before anything decided not to trust it.
func readBoundedConfig(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxHookConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxHookConfigBytes {
		return nil, errConfigTooLarge
	}
	return data, nil
}

// ignoredHookConfigCandidates lists config files that declare hooks mivia will
// not load. Only $MIVIA_CONFIG lands here now: it names the general config, and
// letting an environment variable add a third command-executing file would make
// "what can run here" depend on how mivia was launched.
func ignoredHookConfigCandidates(workspaceRoot string) []string {
	value := os.Getenv("MIVIA_CONFIG")
	if value == "" {
		return nil
	}
	candidate := ExpandPath(value)
	if candidate == "" || sameFilePath(candidate, UserConfigPath()) {
		return nil
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		// Already loaded as the project config; naming it ignored as well would
		// describe a session that does not exist.
		if project := workspace.NamespacePath(workspaceRoot, "mivia.toml"); sameFilePath(candidate, project) {
			return nil
		}
	}
	return []string{candidate}
}

func containsPath(paths []string, candidate string) bool {
	for _, p := range paths {
		if sameFilePath(p, candidate) {
			return true
		}
	}
	return false
}

// hookGroupCount reports how many [[hooks]] groups a config declares. A file
// that does not parse contributes none: a malformed workspace config is that
// file's own problem to report, and failing the user's startup over it would
// let any repo break every session.
func hookGroupCount(data []byte) int {
	var file struct {
		Hooks []map[string]any `toml:"hooks"`
	}
	if err := toml.Unmarshal(data, &file); err != nil {
		return 0
	}
	return len(file.Hooks)
}

// readTrustedHookConfig reads the user config with the same fail-closed shape
// the agent-definition reader uses: no symbolic link, regular file only, and a
// declared byte bound. A link at this path would let anything that can create
// one in ~/.mivia choose which file authorizes command execution.
func readTrustedHookConfig(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("user config %s must not be a symbolic link to supply lifecycle hooks", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("user config %s is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// Verify the file we opened is the file we inspected. Lstat-then-read would
	// let a replacement between the two calls decide which bytes authorize
	// command execution, which is the whole question this function answers.
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("user config %s changed while reading", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxHookConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxHookConfigBytes {
		return nil, fmt.Errorf("user config %s exceeds %d bytes", path, maxHookConfigBytes)
	}
	return data, nil
}
