// Package composition owns session wiring: registries, dispatchers, hooks,
// MCP merge, session construction. internal/cli must not hold this logic; it
// parses arguments and renders output.
package composition

import (
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// RegistryInput carries every value the CLI or tests supply to build the
// default tool registry. Keep it a plain struct. No methods. No behavior.
// Fields mirror tools.DefaultOptions field for field: BuildRegistry is the
// only place that translates one into the other.
type RegistryInput struct {
	Workspace *workspace.Root

	RunAllowlist, RunAllowlistOnly, RunBlocklist, DisableTools                                   []string
	RunTimeoutSec, MaxReadBytes, MaxEditFileBytes, MaxOutputBytes, MaxWriteKB, MaxListDirEntries int

	MaxToolResultBytes                           int
	MaxTavilyResponseBytes                       int
	MaxFetchKB                                   int
	MemoryBackstopBytes                          int
	TavilyAPIKey                                 string
	EnvAllowlist, EnvAllowlistOnly, EnvBlocklist []string
	EnvAllowKeywordBlocklist                     []string
	SecretPathPatterns, SecretPathExceptions     []string

	WritePathDenylist    []string
	SearchIgnorePatterns []string

	MaxInspectRepositoryBytes int

	DiagnosticsCommands map[string][]string

	WorkflowTools []tools.Tool

	Memory memory.Store
}

// BuildRegistry constructs the tool registry. It produces a registry
// identical to what the cli path built before the composition-root move
// (internal/cli's buildWorkflowToolOpts/configureChatWorkspace and
// workflowDefaultRegistry). It never returns a non-nil error today because
// tools.NewDefaultRegistry cannot fail; the error return is kept so a future
// validation step does not change this function's signature.
func BuildRegistry(in RegistryInput) (*tools.Registry, error) {
	opts := tools.DefaultOptions{
		Workspace:                 in.Workspace,
		RunAllowlist:              in.RunAllowlist,
		RunAllowlistOnly:          in.RunAllowlistOnly,
		RunBlocklist:              in.RunBlocklist,
		DisableTools:              in.DisableTools,
		RunTimeoutSec:             in.RunTimeoutSec,
		MaxReadBytes:              in.MaxReadBytes,
		MaxEditFileBytes:          in.MaxEditFileBytes,
		MaxOutputBytes:            in.MaxOutputBytes,
		MaxWriteKB:                in.MaxWriteKB,
		MaxListDirEntries:         in.MaxListDirEntries,
		MaxToolResultBytes:        in.MaxToolResultBytes,
		MaxTavilyResponseBytes:    in.MaxTavilyResponseBytes,
		MaxFetchKB:                in.MaxFetchKB,
		MemoryBackstopBytes:       in.MemoryBackstopBytes,
		TavilyAPIKey:              in.TavilyAPIKey,
		EnvAllowlist:              in.EnvAllowlist,
		EnvAllowlistOnly:          in.EnvAllowlistOnly,
		EnvBlocklist:              in.EnvBlocklist,
		EnvAllowKeywordBlocklist:  in.EnvAllowKeywordBlocklist,
		SecretPathPatterns:        in.SecretPathPatterns,
		SecretPathExceptions:      in.SecretPathExceptions,
		WritePathDenylist:         in.WritePathDenylist,
		SearchIgnorePatterns:      in.SearchIgnorePatterns,
		MaxInspectRepositoryBytes: in.MaxInspectRepositoryBytes,
		DiagnosticsCommands:       in.DiagnosticsCommands,
		WorkflowTools:             in.WorkflowTools,
		Memory:                    in.Memory,
	}
	return tools.NewDefaultRegistry(opts), nil
}
