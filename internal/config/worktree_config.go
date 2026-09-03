package config

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// WorktreeConfig controls worktree branch settings.
type WorktreeConfig struct {
	// BranchPrefix is the prefix for branches that mivia creates for worktrees.
	BranchPrefix string `toml:"branch_prefix"`
}

// DefaultWorktreeBranchPrefix is the prefix for branches that mivia creates
// when the project config does not set [worktrees].branch_prefix.
const DefaultWorktreeBranchPrefix = "mivia/"

// LoadWorktreeConfig reads the worktree settings from the main repository.
// It does not use general config discovery. A linked worktree therefore uses
// the main repository setting, not its current directory or MIVIA_CONFIG.
func LoadWorktreeConfig(mainRepoRoot string) (WorktreeConfig, error) {
	path := workspace.NamespacePath(mainRepoRoot, "mivia.toml")
	return loadWorktreeConfigPath(path)
}

func loadWorktreeConfigPath(path string) (WorktreeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return resolveWorktreeConfig(WorktreeConfig{})
		}
		return WorktreeConfig{}, fmt.Errorf("read worktree config %s: %w", path, err)
	}

	var file struct {
		Worktrees struct {
			BranchPrefix *string `toml:"branch_prefix"`
		} `toml:"worktrees"`
	}
	if err := toml.Unmarshal(data, &file); err != nil {
		return WorktreeConfig{}, fmt.Errorf("parse worktree config %s: %w", path, err)
	}
	if file.Worktrees.BranchPrefix == nil {
		return resolveWorktreeConfig(WorktreeConfig{})
	}
	prefix := *file.Worktrees.BranchPrefix
	if prefix == "" {
		return WorktreeConfig{}, validateWorktreeBranchPrefix(prefix)
	}
	return resolveWorktreeConfig(WorktreeConfig{BranchPrefix: prefix})
}

// resolveWorktreeConfig applies defaults and validates the Git branch prefix.
// Load uses this function after it resolves the selected config file.
func resolveWorktreeConfig(cfg WorktreeConfig) (WorktreeConfig, error) {
	if cfg.BranchPrefix == "" {
		cfg.BranchPrefix = DefaultWorktreeBranchPrefix
	}
	if err := validateWorktreeBranchPrefix(cfg.BranchPrefix); err != nil {
		return WorktreeConfig{}, err
	}
	return cfg, nil
}

func validateWorktreeBranchPrefix(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("[worktrees].branch_prefix must not be empty")
	}
	if !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("[worktrees].branch_prefix must end with /")
	}
	if err := validateGitBranchName(prefix + "mivia-worktree"); err != nil {
		return fmt.Errorf("[worktrees].branch_prefix %q is invalid: %w", prefix, err)
	}
	return nil
}

// validateGitBranchName implements the refname restrictions Git applies to a
// branch name. The caller adds a safe final component to a branch prefix.
func validateGitBranchName(name string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("not a branch name")
	}
	if strings.Contains(name, "@{") || strings.Contains(name, "..") || strings.Contains(name, "//") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("not a valid Git ref")
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("~^:?*[\\", r) {
			return fmt.Errorf("not a valid Git ref")
		}
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("not a valid Git ref")
		}
	}
	return nil
}
