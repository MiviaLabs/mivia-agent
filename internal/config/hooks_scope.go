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

// HooksSource is the trusted, user-owned lifecycle-hook configuration surface.
//
// Lifecycle hooks execute arbitrary local commands, so the file they come from
// is a trust decision, not a lookup. mivia config does not merge across layers:
// DefaultConfigCandidates + FirstExisting read exactly ONE file, so a workspace
// .mivia/mivia.toml does not add to the user's configuration, it REPLACES it.
// A [[hooks]] table in a cloned repo would therefore be the only hook config
// that exists, supplied by that repo, executing on the user's machine.
//
// Hooks are consequently read only from UserConfigPath(), opened at its fixed
// path - never through Load, whose result depends on the working directory and
// on $MIVIA_CONFIG. This is the mechanism LoadAgentsGlobal already uses for the
// [agents] gate, for the reason stated there: a floor the agent can lower is
// not a floor.
type HooksSource struct {
	// Path is the fixed user config file hooks are read from. Empty when no
	// home directory resolves.
	Path string
	// Data is the raw TOML of the user config, nil when that file is absent.
	// Parsing is deliberately not done here: this type owns provenance only.
	Data []byte
	// Warnings are user-visible startup diagnostics - one per config file that
	// declared hooks mivia will not load. A silently ignored hook is how
	// someone concludes hooks are broken and reaches for a bypass flag.
	Warnings []string
}

// LoadHooksSource resolves the one config file lifecycle hooks may come from
// and reports every other config file that declared hooks and was ignored.
//
// A missing user config is not an error: hooks are optional. An unreadable or
// link-shaped user config IS an error - this file authorizes command execution,
// so an ambiguous read fails closed rather than loading zero hooks silently.
func LoadHooksSource(workspaceRoot string) (HooksSource, error) {
	src := HooksSource{Path: UserConfigPath()}
	if src.Path != "" {
		data, err := readTrustedHookConfig(src.Path)
		if err != nil && !os.IsNotExist(err) {
			return HooksSource{}, err
		}
		if err == nil {
			src.Data = data
		}
	}
	for _, candidate := range ignoredHookConfigCandidates(workspaceRoot, src.Path) {
		data, err := readBoundedConfig(candidate)
		if err != nil {
			// An oversized candidate is reported rather than skipped: it is
			// exactly the file a repo would use to hide a hook table behind a
			// read we refuse to do, and silence there reads as "no hooks here".
			if errors.Is(err, errConfigTooLarge) {
				src.Warnings = append(src.Warnings, fmt.Sprintf(
					"not inspected for lifecycle hooks: %s exceeds %d bytes", candidate, maxHookConfigBytes))
			}
			continue
		}
		if n := hookGroupCount(data); n > 0 {
			src.Warnings = append(src.Warnings, fmt.Sprintf(
				"ignoring %d lifecycle hook group(s) in %s: hooks load only from the user config %s",
				n, candidate, hookSourceLabel(src.Path)))
		}
	}
	return src, nil
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

// ignoredHookConfigCandidates lists every config file that mivia might load for
// general configuration but will never load hooks from. $MIVIA_CONFIG selects
// the general config; it deliberately does not relocate the hook source, so a
// hook table in it is ignored and must say so.
func ignoredHookConfigCandidates(workspaceRoot, userPath string) []string {
	var raw []string
	if v := os.Getenv("MIVIA_CONFIG"); v != "" {
		raw = append(raw, ExpandPath(v))
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		raw = append(raw, workspace.NamespacePath(workspaceRoot, "mivia.toml"))
	}
	if cwd, err := os.Getwd(); err == nil {
		raw = append(raw, workspace.NamespacePath(cwd, "mivia.toml"))
	}
	var out []string
	for _, candidate := range raw {
		if candidate == "" || sameFilePath(candidate, userPath) {
			continue
		}
		if containsPath(out, candidate) {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func containsPath(paths []string, candidate string) bool {
	for _, p := range paths {
		if sameFilePath(p, candidate) {
			return true
		}
	}
	return false
}

func hookSourceLabel(userPath string) string {
	if userPath == "" {
		return "~/.mivia/mivia.toml"
	}
	return userPath
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
