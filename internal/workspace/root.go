// Package workspace confines filesystem access to a root directory.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Root is a resolved workspace directory.
type Root struct {
	// Abs is the absolute, cleaned, symlink-evaluated root path.
	Abs string

	// LexicalAbs is the absolute, cleaned root path exactly as the caller
	// named it, BEFORE symlink evaluation; the blocklist's lexical check
	// derives workspace-relative names from it so an absolute path spelled
	// through a symlink alias of the root maps to the same relative name as
	// the direct spelling.
	LexicalAbs string

	// Unrestricted lifts the workspace escape check: file tools may resolve
	// paths anywhere on the filesystem, not only under Abs. Set only by the
	// operator-level `mivia chat --full-disk` flag (OpenFullDisk) - never by
	// workspace config, so a repository cannot grant itself full disk access.
	// The operator-invocation-only provenance is the security property; keep
	// it that way.
	Unrestricted bool
}

// SameExistingPath reports whether two existing paths name the same file.
func SameExistingPath(a, b string) (bool, error) {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false, fmt.Errorf("path must not be empty")
	}
	infoA, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	infoB, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(infoA, infoB), nil
}

// Open resolves rootPath (default ".") to an absolute workspace root.
func Open(rootPath string) (*Root, error) {
	if strings.TrimSpace(rootPath) == "" {
		rootPath = "."
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("workspace abs: %w", err)
	}
	lexicalAbs := abs
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		// If root does not exist yet, keep Abs without eval.
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("workspace eval: %w", err)
		}
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace stat: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("workspace is not a directory: %s", abs)
	}
	return &Root{Abs: abs, LexicalAbs: lexicalAbs}, nil
}

// OpenFullDisk resolves rootPath like Open but sets Unrestricted, allowing
// file tools to operate anywhere on the filesystem. Must only be called when
// the operator explicitly requested full disk access (e.g. --full-disk flag),
// never from workspace config.
func OpenFullDisk(rootPath string) (*Root, error) {
	r, err := Open(rootPath)
	if err != nil {
		return nil, err
	}
	r.Unrestricted = true
	return r, nil
}

// Resolve maps a user path (relative or absolute) into the workspace.
// Rejects escapes via .. or symlinks outside the root.
func (r *Root) Resolve(userPath string) (string, error) {
	if r == nil || r.Abs == "" {
		return "", fmt.Errorf("nil workspace")
	}
	if strings.TrimSpace(userPath) == "" {
		return "", fmt.Errorf("empty path")
	}
	var candidate string
	if filepath.IsAbs(userPath) {
		candidate = filepath.Clean(userPath)
	} else {
		candidate = filepath.Clean(filepath.Join(r.Abs, userPath))
	}
	// EvalSymlinks for existing paths; for non-existing, check parent chain.
	resolved, err := evalExistingPrefix(candidate)
	if err != nil {
		return "", err
	}
	if !r.Unrestricted && !isUnder(r.Abs, resolved) {
		return "", fmt.Errorf("path %q escapes workspace %s", userPath, r.Abs)
	}
	return resolved, nil
}

func evalExistingPrefix(path string) (string, error) {
	// Walk up until we find an existing path to EvalSymlinks.
	cur := path
	var suffix []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			ev, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", fmt.Errorf("eval symlinks: %w", err)
			}
			if len(suffix) == 0 {
				return ev, nil
			}
			// Re-join missing suffix components (cleaned).
			for i := len(suffix) - 1; i >= 0; i-- {
				ev = filepath.Join(ev, suffix[i])
			}
			return filepath.Clean(ev), nil
		}
		dir, base := filepath.Dir(cur), filepath.Base(cur)
		if dir == cur {
			return "", fmt.Errorf("cannot resolve path %s", path)
		}
		suffix = append(suffix, base)
		cur = dir
	}
}

func isUnder(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(path, root+sep)
}

// Rel returns a path relative to the workspace for display.
func (r *Root) Rel(absPath string) string {
	rel, err := filepath.Rel(r.Abs, absPath)
	if err != nil {
		return filepath.ToSlash(absPath)
	}
	return filepath.ToSlash(rel)
}

// LexicalRel returns the clean workspace-relative form of userPath without
// resolving symlinks. Rel reports where a request lands; LexicalRel reports
// the path the caller named, so a deny list can refuse a blocked name even
// when an in-workspace symlink would redirect the write elsewhere; a name
// that cannot be expressed as a workspace-relative path is an error and
// callers treat that as denied (fail closed).
func (r *Root) LexicalRel(userPath string) (string, error) {
	if r == nil || r.LexicalAbs == "" {
		return "", fmt.Errorf("nil workspace")
	}
	if strings.TrimSpace(userPath) == "" {
		return "", fmt.Errorf("empty path")
	}
	cleaned := filepath.Clean(userPath)
	if !filepath.IsAbs(cleaned) {
		return filepath.ToSlash(cleaned), nil
	}
	if !r.Unrestricted && !isUnder(r.LexicalAbs, cleaned) {
		return "", fmt.Errorf("path %q escapes workspace %s", userPath, r.LexicalAbs)
	}
	if !isUnder(r.LexicalAbs, cleaned) {
		// Unrestricted mode: outside the workspace — return the cleaned
		// absolute path so callers (e.g. write-denylist) have a usable key
		// rather than silently re-confining the operation.
		return filepath.ToSlash(cleaned), nil
	}
	rel, err := filepath.Rel(r.LexicalAbs, cleaned)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
