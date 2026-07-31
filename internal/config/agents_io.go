package config

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func loadAgentDir(dir string, source AgentSource) ([]LoadedAgentFile, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	root, err := openAgentsRoot(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open agents directory %s: %w", dir, err)
	}
	defer root.Close()

	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read agents directory %s: %w", dir, err)
	}
	var out []LoadedAgentFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".toml") {
			continue
		}
		data, err := readRegularAgent(root, name)
		if err != nil {
			return nil, fmt.Errorf("agent file %s: %w", filepath.Join(dir, name), err)
		}
		if len(data) > maxAgentFileBytes {
			return nil, fmt.Errorf("agent file %s exceeds %d bytes", filepath.Join(dir, name), maxAgentFileBytes)
		}
		spec, canonical, err := ParseAgentFileTOML(data, name)
		if err != nil {
			return nil, err
		}
		out = append(out, LoadedAgentFile{
			Name:   canonical,
			Source: source,
			Path:   filepath.Join(dir, name),
			Spec:   spec,
		})
	}
	return out, nil
}

// openAgentsRoot pins the agents directory and rejects a symbolic link at its
// boundary, matching the skills discovery contract.
func openAgentsRoot(path string) (*os.Root, error) {
	clean := filepath.Clean(path)
	if st, err := os.Lstat(clean); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("agents directory must not be a symbolic link")
	}
	parent, err := os.OpenRoot(filepath.Dir(clean))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	base := filepath.Base(clean)
	info, err := parent.Lstat(base)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("agents directory must not be a symbolic link")
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("agents directory is not a real directory")
	}
	root, err := parent.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	opened, err := root.Lstat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("agents directory changed while opening")
	}
	return root, nil
}

// readRegularAgent refuses links and verifies the opened file still matches
// the inspected file so a workspace cannot redirect agent content.
func readRegularAgent(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("agent file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("agent file is not a regular file")
	}
	if !hasSingleLink(info) {
		return nil, fmt.Errorf("agent file has multiple links")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("agent file changed while reading")
	}
	if !hasSingleLink(opened) {
		return nil, fmt.Errorf("agent file links changed while reading")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAgentFileBytes+1))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func sameResolvedDir(a, b string) (bool, error) {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false, nil
	}
	ra, err := resolveDir(a)
	if err != nil {
		return false, err
	}
	rb, err := resolveDir(b)
	if err != nil {
		return false, err
	}
	return ra == rb, nil
}

func resolveDir(path string) (string, error) {
	clean := filepath.Clean(path)
	if _, err := os.Lstat(clean); err == nil {
		ev, err := filepath.EvalSymlinks(clean)
		if err == nil {
			return filepath.Clean(ev), nil
		}
	}
	// Walk up to an existing ancestor for not-yet-created agent dirs.
	cur := clean
	for {
		if cur == "." || cur == string(filepath.Separator) {
			abs, err := filepath.Abs(clean)
			if err != nil {
				return "", err
			}
			return filepath.Clean(abs), nil
		}
		if _, err := os.Stat(cur); err == nil {
			ev, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			suffix, err := filepath.Rel(cur, clean)
			if err != nil {
				return filepath.Clean(filepath.Join(ev, filepath.Base(clean))), nil
			}
			return filepath.Clean(filepath.Join(ev, suffix)), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			abs, err := filepath.Abs(clean)
			if err != nil {
				return "", err
			}
			return filepath.Clean(abs), nil
		}
		cur = parent
	}
}

func sameFilePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = filepath.Clean(a)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = filepath.Clean(b)
	}
	return filepath.Clean(ra) == filepath.Clean(rb)
}
