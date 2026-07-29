package agentkit

import (
	"fmt"
	"os"
	"path/filepath"

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

// Resolve returns instruction content: local file first, else embedded.
func Resolve(dir, relPath string) (string, error) {
	localPath := filepath.Join(dir, relPath)
	if data, err := os.ReadFile(localPath); err == nil {
		return string(data), nil
	}
	content := agentkitdata.ReadFile(relPath)
	return content, nil
}
