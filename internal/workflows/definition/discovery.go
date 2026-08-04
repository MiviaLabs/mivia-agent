package definition

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// DiscoverWorkflows finds all .toml workflow definitions beneath
// <workspaceRoot>/.mivia/workflows/ using safe file discovery (symlink
// rejection, TOCTOU protection). Returns an empty slice (not an error) when
// the workflows directory does not exist.
func DiscoverWorkflows(workspaceRoot string) ([]DiscoveredWorkflow, error) {
	dir := workspace.NamespacePath(workspaceRoot, "workflows")
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}

	root, err := openWorkflowsRoot(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open workflows directory %s: %w", dir, err)
	}
	defer root.Close()

	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read workflows directory %s: %w", dir, err)
	}

	var out []DiscoveredWorkflow
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".toml") {
			continue
		}
		data, err := readRegularWorkflowFile(root, name)
		if err != nil {
			return nil, fmt.Errorf("workflow file %s: %w", filepath.Join(dir, name), err)
		}
		if len(data) > MaxWorkflowFileBytes {
			return nil, fmt.Errorf("workflow file %s exceeds %d bytes",
				filepath.Join(dir, name), MaxWorkflowFileBytes)
		}
		out = append(out, DiscoveredWorkflow{
			Name: strings.TrimSuffix(name, ".toml"),
			Path: filepath.Join(dir, name),
			Raw:  data,
		})
	}
	return out, nil
}

// openWorkflowsRoot pins the workflows directory and rejects a symbolic link
// at its boundary, matching the agents discovery contract.
func openWorkflowsRoot(path string) (*os.Root, error) {
	clean := filepath.Clean(path)

	// First pass: Lstat before opening parent.
	if st, err := os.Lstat(clean); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workflows directory must not be a symbolic link")
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
		return nil, fmt.Errorf("workflows directory must not be a symbolic link")
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workflows directory is not a real directory")
	}

	root, err := parent.OpenRoot(base)
	if err != nil {
		return nil, err
	}

	// TOCTOU: verify the opened root matches the inspected directory.
	opened, err := root.Lstat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("workflows directory changed while opening")
	}
	return root, nil
}

// readRegularWorkflowFile refuses links and verifies the opened file still
// matches the inspected file so a workspace cannot redirect workflow content.
func readRegularWorkflowFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workflow file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workflow file is not a regular file")
	}

	// Inline nlink check (equivalent to hasSingleLink in agents_io).
	if nlink := fileNlink(info); nlink > 0 && nlink != 1 {
		return nil, fmt.Errorf("workflow file has multiple links")
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
		return nil, fmt.Errorf("workflow file changed while reading")
	}

	// Inline nlink check on the opened file descriptor.
	if nlink := fileNlink(opened); nlink > 0 && nlink != 1 {
		return nil, fmt.Errorf("workflow file links changed while reading")
	}

	data, err := io.ReadAll(io.LimitReader(file, MaxWorkflowFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxWorkflowFileBytes {
		return nil, fmt.Errorf("workflow file exceeds %d bytes", MaxWorkflowFileBytes)
	}
	return data, nil
}
