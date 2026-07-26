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
	return &Root{Abs: abs}, nil
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
	if !isUnder(r.Abs, resolved) {
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
		return absPath
	}
	return rel
}
