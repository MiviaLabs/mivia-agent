package template

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaxTemplateBytes is the maximum allowed size for a single template file.
const MaxTemplateBytes = 32768

// LoadTemplates reads all .md files from the given directory.
// Returns a map of basename -> content, or an error if the directory is invalid.
// Path traversal is rejected: all resolved paths must remain under baseDir.
func LoadTemplates(baseDir string) (map[string]string, error) {
	if strings.TrimSpace(baseDir) == "" {
		return nil, fmt.Errorf("template directory is empty")
	}
	clean := filepath.Clean(baseDir)
	if strings.Contains(clean, "..") {
		return nil, fmt.Errorf("template directory path contains traversal")
	}

	root, err := openTemplateRoot(clean)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open template directory %s: %w", baseDir, err)
	}
	defer root.Close()

	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read template directory %s: %w", baseDir, err)
	}

	result := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		data, err := readTemplateFile(root, name)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
		result[name] = string(data)
	}
	return result, nil
}

// ValidateReferences checks that all template references in stepTemplates
// exist in the loaded templates map. Returns a list of missing template names.
func ValidateReferences(loaded map[string]string, stepTemplates []string) []string {
	var missing []string
	for _, t := range stepTemplates {
		if strings.TrimSpace(t) == "" {
			continue
		}
		if _, ok := loaded[t]; !ok {
			missing = append(missing, t)
		}
	}
	return missing
}

// openTemplateRoot pins the template directory and rejects symbolic links.
func openTemplateRoot(path string) (*os.Root, error) {
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("template directory must not be a symbolic link")
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	base := filepath.Base(path)
	info, err := parent.Lstat(base)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("template directory is not a real directory")
	}
	return parent.OpenRoot(base)
}

// readTemplateFile reads a single template with symlink rejection and size cap.
func readTemplateFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("template must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("template is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, _ := io.ReadAll(io.LimitReader(file, MaxTemplateBytes+1))
	if len(data) > MaxTemplateBytes {
		return nil, fmt.Errorf("template %s exceeds %d bytes", name, MaxTemplateBytes)
	}
	return data, nil
}
