package cliagents

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// configureChatWorkspace installs the session tool registry for chat.
// res supplies tool policy and the subagent store path so Phase 7 workflow
// tools open the same ledger as CLI workflow commands.
//
// The returned function closes the session-owned memory store, if one was
// opened. The CLI process never needs it (exit reclaims the handle), but a
// test or embedding that tears the session down must release the SQLite
// handle: Windows cannot delete a database file that is still open.
// stashMemoryOnState is plan 77's E1: the store configureChatWorkspace just
// opened becomes the single source of truth every SystemPrompt-composing
// call site reads from - never a second Open of the same file. state may
// be nil (a caller that doesn't participate in prompt-level core-memory
// injection); that's a safe no-op, not an error -
// coreMemoryBlockForState(nil) returns "".
func stashMemoryOnState(state *AgentSessionState, store memory.Store, res *config.Resolved) {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.Memory = store
	if res != nil {
		state.MemoryConfig = res.Memory
	}
}

// buildWorkflowToolOpts builds the base tool options for a chat workspace:
// the workspace root and the tool policy. It carries no workflow wiring -
// the event bus provider (and with it the parked-run recovery sweep, F14) is
// decided by the caller in configureChatWorkspace.
func buildWorkflowToolOpts(root string, fullDisk bool, res *config.Resolved) (*tools.DefaultOptions, error) {
	var ws *workspace.Root
	var err error
	if fullDisk {
		ws, err = workspace.OpenFullDisk(root)
	} else {
		ws, err = workspace.Open(root)
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	var tc config.ToolsConfig
	var tavilyKey string
	if res != nil {
		tc = res.Tools
		tavilyKey = res.TavilyAPIKey
	}
	opts := &tools.DefaultOptions{
		Workspace:                 ws,
		TavilyAPIKey:              tavilyKey,
		RunAllowlist:              tc.RunAllowlist,
		RunAllowlistOnly:          tc.RunAllowlistOnly,
		RunBlocklist:              tc.RunBlocklist,
		DisableTools:              tc.DisableTools,
		EnvAllowlist:              tc.EnvAllowlist,
		EnvAllowlistOnly:          tc.EnvAllowlistOnly,
		EnvBlocklist:              tc.EnvBlocklist,
		EnvAllowKeywordBlocklist:  tc.EnvAllowKeywordBlocklist,
		RunTimeoutSec:             tc.RunTimeoutSec,
		MaxReadBytes:              tc.MaxReadBytes,
		MaxEditFileBytes:          tc.MaxEditFileBytes,
		MaxWriteKB:                tc.MaxWriteKB,
		MaxOutputBytes:            tc.MaxOutputBytes,
		MaxListDirEntries:         tc.MaxListDirEntries,
		MaxToolResultBytes:        tc.MaxToolResultBytes,
		MaxTavilyResponseBytes:    tc.MaxTavilyResponseBytes,
		MaxFetchKB:                tc.MaxFetchKB,
		MemoryBackstopBytes:       tc.MemoryBackstopMB << 20,
		SecretPathPatterns:        tc.SecretPathPatterns,
		SecretPathExceptions:      tc.SecretPathExceptions,
		SearchIgnorePatterns:      tc.SearchIgnorePatterns,
		MaxInspectRepositoryBytes: tc.MaxInspectRepositoryBytes,
		DiagnosticsCommands:       tc.DiagnosticsCommands,
	}
	return opts, nil
}

func ConfigureChatWorkspace(sess *chat.Session, root string, useTools bool, res *config.Resolved, state *AgentSessionState, quiet bool, fullDisk bool, runRecoverySweep bool) (func(), error) {
	if !useTools {
		return func() {}, nil
	}
	opts, err := buildWorkflowToolOpts(root, fullDisk, res)
	if err != nil {
		return func() {}, err
	}
	var busProvider func() *events.Bus
	if runRecoverySweep {
		busProvider = func() *events.Bus { return sess.EventBus }
	}
	// The session's ledger repo is the same value NewSessionDispatcher will
	// receive, so the workflow engine stamps exactly the instance the
	// session's orchestration tools carry. Nil (state without an adopted
	// repo) keeps child-run registration skipped.
	var sessionRepo ledger.LedgerRepository
	if state != nil {
		sessionRepo = state.LedgerRepo
	}
	WireWorkflowToolOptionsVar(opts, opts.Workspace.Abs, res, busProvider, quiet, sessionRepo)
	if err := WireSessionMemory(opts, root, res); err != nil {
		return func() {}, err
	}
	stashMemoryOnState(state, opts.Memory, res)
	// Register the live confinement re-arm BEFORE the registry build so the
	// operator's Settings -> General toggle can flip the very root the
	// session's tools hold, without a restart. State is nil for some test
	// harnesses; persistence-only then.
	if state != nil {
		state.SetFullDiskReArm(opts.Workspace.SetUnrestricted)
	}
	// composition.BuildRegistry cannot fail today (see its doc comment); the
	// error return is discarded here rather than propagated through a new,
	// untestable branch, matching the pre-move tools.NewDefaultRegistry call
	// this replaces, which had no error return at all.
	registry, _ := composition.BuildRegistry(registryInputFromDefaultOptions(opts))
	sess.Tools = registry
	sess.RefreshPrefixIdentity()
	return func() {
		if opts.Memory != nil {
			_ = opts.Memory.Close()
		}
	}, nil
}

// registryInputFromDefaultOptions copies opts field by field into a
// composition.RegistryInput. opts has already been through
// WireWorkflowToolOptionsVar and WireSessionMemory by the time this runs, so it
// carries the full set of fields the pre-refactor tools.NewDefaultRegistry
// call site saw.
func registryInputFromDefaultOptions(opts *tools.DefaultOptions) composition.RegistryInput {
	return composition.RegistryInput{
		Workspace:                 opts.Workspace,
		RunAllowlist:              opts.RunAllowlist,
		RunAllowlistOnly:          opts.RunAllowlistOnly,
		RunBlocklist:              opts.RunBlocklist,
		DisableTools:              opts.DisableTools,
		RunTimeoutSec:             opts.RunTimeoutSec,
		MaxReadBytes:              opts.MaxReadBytes,
		MaxEditFileBytes:          opts.MaxEditFileBytes,
		MaxOutputBytes:            opts.MaxOutputBytes,
		MaxWriteKB:                opts.MaxWriteKB,
		MaxListDirEntries:         opts.MaxListDirEntries,
		MaxToolResultBytes:        opts.MaxToolResultBytes,
		MaxTavilyResponseBytes:    opts.MaxTavilyResponseBytes,
		MaxFetchKB:                opts.MaxFetchKB,
		MemoryBackstopBytes:       opts.MemoryBackstopBytes,
		TavilyAPIKey:              opts.TavilyAPIKey,
		EnvAllowlist:              opts.EnvAllowlist,
		EnvAllowlistOnly:          opts.EnvAllowlistOnly,
		EnvBlocklist:              opts.EnvBlocklist,
		EnvAllowKeywordBlocklist:  opts.EnvAllowKeywordBlocklist,
		SecretPathPatterns:        opts.SecretPathPatterns,
		SecretPathExceptions:      opts.SecretPathExceptions,
		WritePathDenylist:         opts.WritePathDenylist,
		SearchIgnorePatterns:      opts.SearchIgnorePatterns,
		MaxInspectRepositoryBytes: opts.MaxInspectRepositoryBytes,
		DiagnosticsCommands:       opts.DiagnosticsCommands,
		WorkflowTools:             opts.WorkflowTools,
		Memory:                    opts.Memory,
	}
}

// logDiagnosticsCommandsOnce states every configured get_diagnostics command
// once at startup, before any tool call: a configured command that runs
// programs is a disclosure, not a hidden capability (the same contract as the
// lifecycle-hooks armedNotice). Names are sorted so the line is deterministic
// across runs. quiet (--quiet) suppresses the line; the tool itself is still
// registered whenever the workspace declares commands.
func LogDiagnosticsCommandsOnce(w io.Writer, tc config.ToolsConfig, quiet bool) {
	if w == nil || quiet || len(tc.DiagnosticsCommands) == 0 {
		return
	}
	names := make([]string, 0, len(tc.DiagnosticsCommands))
	for name := range tc.DiagnosticsCommands {
		names = append(names, name)
	}
	slices.Sort(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=[%s]", name, strings.Join(tc.DiagnosticsCommands[name], " ")))
	}
	fmt.Fprintf(w, "diagnostics: configured commands: %s\n", strings.Join(parts, ", "))
}
