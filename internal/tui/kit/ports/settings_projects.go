package ports

import "context"

// ProjectView is the project-scoped configuration read by the Projects section.
// Informational path fields are read-only; configuration options can be modified
// and persisted to the project's .mivia/mivia.toml.
type ProjectView struct {
	WorkspacePath   string
	ConfigPath      string
	EnvFile         string
	BranchPrefix    string
	SystemPrompt    string
	Temperature     string // "default", "0.0", "0.2", "0.5", "0.7", "1.0", or custom
	MaxTokens       string // "default", "4096", "8192", "16384", "32768", "65536", "128000", or custom
	MaxPromptTokens string // "default", "50000", "100000", "200000", "400000", or custom
	MaxSteps        string // "default", "20", "50", "100", "unlimited (0)", or custom
	RunTimeoutSec   int    // Tool execution timeout in seconds
	StoreBackend    string // "sqlite" | "memory"
	StorePath       string // SQLite database file path
	Sandbox         bool   // Bubblewrap sandbox for verifier gates
	RedactToolArgs  bool   // Redact tool args from operator-visible output
}

// ProjectEdit is a closed union of project-level configuration mutations.
type ProjectEdit interface{ isProjectEdit() }

type SetProjectEnvFile struct{ Path string }
type SetProjectBranchPrefix struct{ Prefix string }
type SetProjectSystemPrompt struct{ Prompt string }
type SetProjectTemperature struct{ Value string }
type SetProjectMaxTokens struct{ Value string }
type SetProjectMaxPromptTokens struct{ Value string }
type SetProjectMaxSteps struct{ Value string }
type SetProjectRunTimeout struct{ Seconds int }
type SetProjectStoreBackend struct{ Backend string }
type SetProjectStorePath struct{ Path string }
type SetProjectSandbox struct{ On bool }
type SetProjectRedactToolArgs struct{ On bool }

func (SetProjectEnvFile) isProjectEdit()         {}
func (SetProjectBranchPrefix) isProjectEdit()    {}
func (SetProjectSystemPrompt) isProjectEdit()    {}
func (SetProjectTemperature) isProjectEdit()     {}
func (SetProjectMaxTokens) isProjectEdit()       {}
func (SetProjectMaxPromptTokens) isProjectEdit() {}
func (SetProjectMaxSteps) isProjectEdit()        {}
func (SetProjectRunTimeout) isProjectEdit()      {}
func (SetProjectStoreBackend) isProjectEdit()    {}
func (SetProjectStorePath) isProjectEdit()       {}
func (SetProjectSandbox) isProjectEdit()         {}
func (SetProjectRedactToolArgs) isProjectEdit()  {}

// ProjectSettings is the Projects section's read/write surface.
type ProjectSettings interface {
	Project() ProjectView
	Apply(ctx context.Context, scope Scope, e ProjectEdit) (SaveHandle, error)
}
