package agentkit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/agentkitdata"
)

// Version returns a content hash of all embedded instructions.
func Version() string { return agentkitdata.Version() }

// AgentInstructions returns the embedded AGENTS.md content.
func AgentInstructions() string {
	return agentkitdata.ReadFile("AGENTS.md")
}

// Rule returns embedded .ai/rules/<name>.md content.
func Rule(name string) (string, error) {
	path := fmt.Sprintf(".ai/rules/%s.md", name)
	content := agentkitdata.ReadFile(path)
	if content == "" {
		return "", fmt.Errorf("rule %s not found", name)
	}
	return content, nil
}

// Doctrine returns embedded .ai/doctrines/<name>.md content.
func Doctrine(name string) (string, error) {
	path := fmt.Sprintf(".ai/doctrines/%s.md", name)
	content := agentkitdata.ReadFile(path)
	if content == "" {
		return "", fmt.Errorf("doctrine %s not found", name)
	}
	return content, nil
}

// Skill returns embedded .ai/skills/<name>/SKILL.md content.
func Skill(name string) (string, error) {
	path := fmt.Sprintf(".ai/skills/%s/SKILL.md", name)
	content := agentkitdata.ReadFile(path)
	if content == "" {
		return "", fmt.Errorf("skill %s not found", name)
	}
	return content, nil
}

// WriteInstructions writes all embedded instructions to dir/.
// Does NOT overwrite existing files.
func WriteInstructions(dir string) ([]string, error) {
	allFiles, err := agentkitdata.ReadAllFiles()
	if err != nil {
		return nil, fmt.Errorf("read embedded files: %w", err)
	}

	var written []string
	for path, data := range allFiles {
		fullPath := filepath.Join(dir, path)
		if _, err := os.Stat(fullPath); err == nil {
			continue
		}
		parentDir := filepath.Dir(fullPath)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return written, fmt.Errorf("create dir %s: %w", parentDir, err)
		}
		if err := os.WriteFile(fullPath, data, 0644); err != nil {
			return written, fmt.Errorf("write %s: %w", path, err)
		}
		written = append(written, path)
	}
	return written, nil
}

// HasLocalOverride checks if dir/.ai/ exists with any .md files.
// Also checks if dir/AGENTS.md exists specifically (root-level).
func HasLocalOverride(dir string) bool {
	// Check root-level AGENTS.md (most agents look for this first)
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
		return true
	}
	// Check .ai/ directory for any .md files
	aiDir := filepath.Join(dir, ".ai")
	entries, err := os.ReadDir(aiDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			return true
		}
		if e.IsDir() {
			sub, err := os.ReadDir(filepath.Join(aiDir, e.Name()))
			if err != nil {
				continue
			}
			for _, s := range sub {
				if !s.IsDir() && strings.HasSuffix(s.Name(), ".md") {
					return true
				}
			}
		}
	}
	return false
}

// EnsureInstructions checks if dir/.ai/ exists. If not, writes embedded files.
func EnsureInstructions(dir string) error {
	if HasLocalOverride(dir) {
		return nil
	}
	_, err := WriteInstructions(dir)
	return err
}

// Resolve returns instruction content: local file first, else embedded.
func Resolve(dir, relPath string) (string, error) {
	localPath := filepath.Join(dir, relPath)
	if data, err := os.ReadFile(localPath); err == nil {
		return string(data), nil
	}
	content := agentkitdata.ReadFile(relPath)
	return content, nil
}
