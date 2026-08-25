package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// ExpandPath expands leading ~ to the user home directory.
func ExpandPath(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		if home, err := workspace.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := workspace.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// UserConfigPath returns the fixed user-level config path without checking the
// filesystem.
func UserConfigPath() string {
	return userPath("mivia.toml")
}

// UserEnvPath returns the fixed user-level env path without checking the
// filesystem.
func UserEnvPath() string {
	return userPath(".env")
}

// UserAuthPath returns the path to the local CLI auth token file
// (~/.mivia/auth.json) without checking the filesystem.
func UserAuthPath() string {
	return userPath("auth.json")
}

func userPath(name string) string {
	home, err := workspace.UserHomeDir()
	if err != nil {
		return ""
	}
	return workspace.NamespacePath(home, name)
}

// ProjectConfigPath returns workspaceRoot's own .mivia/mivia.toml path
// without checking the filesystem, mirroring UserConfigPath's shape for
// the project layer. Empty workspaceRoot returns "" - there is no
// project-scoped config to address without a workspace. Callers writing
// a project-scoped override must additionally check this path differs
// from whatever base config path is in play (see loadFile's own
// workspaceOverlayConfigPath guard): when they are the same file, "the
// project layer" is not a distinct target and treating it as one would
// silently read a value back as its own override.
func ProjectConfigPath(workspaceRoot string) string {
	if strings.TrimSpace(workspaceRoot) == "" {
		return ""
	}
	return workspace.NamespacePath(workspaceRoot, "mivia.toml")
}

// DefaultConfigCandidates returns config paths in search order.
func DefaultConfigCandidates() []string {
	var out []string
	if v := os.Getenv("MIVIA_CONFIG"); v != "" {
		out = append(out, ExpandPath(v))
	}
	if cwd, err := os.Getwd(); err == nil {
		out = append(out, workspace.NamespacePath(cwd, "mivia.toml"))
	}
	if path := UserConfigPath(); path != "" {
		out = append(out, path)
	}
	return out
}

// DefaultEnvCandidates returns env file paths when env_file is unset.
func DefaultEnvCandidates() []string {
	var out []string
	if cwd, err := os.Getwd(); err == nil {
		out = append(out, filepath.Join(cwd, ".env"))
	}
	if path := UserEnvPath(); path != "" {
		out = append(out, path)
	}
	return out
}

// FirstExisting returns the first path that exists as a regular file.
func FirstExisting(paths []string) (string, bool) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err == nil && st.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}
