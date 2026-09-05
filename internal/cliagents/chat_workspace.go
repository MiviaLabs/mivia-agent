package cliagents

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
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

// BuildToolsForRoot builds the production tool registry for ONE root,
// without touching any session: workspace + workflow tools wire against
// rootWorkspace, the project memory store opens at rootMemory. Callers
// that split roots pass worktree dir / main repo root respectively; the
// chat REPL passes the same root twice. The returned closer releases the
// memory store (nil-safe when memory wiring produced no store). The
// builder never mutates a session - installing registry and prefix
// identity stays with the caller. ConfigureChatWorkspace runs the same
// wiring through buildToolsForRootWired and then performs the installs.
//
// BuildToolsForRootHookForTest redirects registry construction in tests
// (race-window replay, failure injection). Production leaves it nil.
var BuildToolsForRootHookForTest func(rootWorkspace, rootMemory string, fullDisk bool, res *config.Resolved) (*tools.Registry, func(), error)

// WorkflowSessionWiring is the session-scoped half of a per-root registry
// build: the event bus a workflow publishes progress on, and the ledger
// repository its child runs are registered against. Both belong to the
// SESSION, not to the root, so every root a session can run a workflow from
// needs them - the launch checkout and each worktree the pool rebuilds.
// Passing them as one value keeps the two build sites from drifting apart
// field by field, which is how the worktree build came to pass nil for both.
type WorkflowSessionWiring struct {
	Bus         func() *events.Bus
	SessionRepo ledger.LedgerRepository
}

// buildToolsForRootWired is the ONE wiring path both public entry points
// share: workflow options for rootWorkspace, workflow-var wiring, session
// memory at rootMemory, then registry composition. Keeping a single body
// stops the two callers drifting field by field (they did once).
// composition.BuildRegistry cannot fail today (see its doc comment).
func buildToolsForRootWired(rootWorkspace, rootMemory string, fullDisk bool, res *config.Resolved, busProvider func() *events.Bus, runSweep, quiet bool, sessionRepo ledger.LedgerRepository) (*tools.Registry, *tools.DefaultOptions, func(), error) {
	noClose := func() {}
	opts, err := buildWorkflowToolOpts(rootWorkspace, fullDisk, res)
	if err != nil {
		return nil, nil, noClose, err
	}
	WireWorkflowToolOptionsVar(opts, opts.Workspace.Abs, res, busProvider, runSweep, quiet, sessionRepo)
	if err := WireSessionMemory(opts, rootMemory, res); err != nil {
		return nil, nil, noClose, err
	}
	registry, _ := composition.BuildRegistry(registryInputFromDefaultOptions(opts))
	closeFn := func() {
		if opts.Memory != nil {
			_ = opts.Memory.Close()
		}
	}
	return registry, opts, closeFn, nil
}

// busProvider and sweep stay launch-side on purpose: parked-run recovery
// belongs to the process's own workspace, not to every worktree root a
// pool rebuilds for.
func BuildToolsForRoot(rootWorkspace, rootMemory string, fullDisk bool, res *config.Resolved, wiring WorkflowSessionWiring) (*tools.Registry, func(), error) {
	// runSweep is false here and only here: parked-run recovery is the
	// launch workspace's job (see the comment above). Progress publishing
	// and child-run ownership are the SESSION's and travel with it to every
	// root it can run a workflow from.
	registry, _, closeFn, err := buildToolsForRootWired(rootWorkspace, rootMemory, fullDisk, res, wiring.Bus, false, true, wiring.SessionRepo)
	return registry, closeFn, err
}

func ConfigureChatWorkspace(sess *chat.Session, root string, useTools bool, res *config.Resolved, state *AgentSessionState, quiet bool, fullDisk bool, runRecoverySweep bool) (func(), error) {
	if !useTools {
		return func() {}, nil
	}
	// The bus is wired whenever the session has one, so a workflow started
	// here publishes progress; the recovery SWEEP is gated separately on
	// runRecoverySweep, which only a genuine interactive launch sets.
	busProvider := func() *events.Bus { return sess.EventBus }
	// The session's ledger repo is the same value NewSessionDispatcher will
	// receive, so the workflow engine stamps exactly the instance the
	// session's orchestration tools carry. Nil (state without an adopted
	// repo) keeps child-run registration skipped.
	var sessionRepo ledger.LedgerRepository
	if state != nil {
		sessionRepo = state.LedgerRepo
	}
	registry, opts, closeFn, err := buildToolsForRootWired(root, root, fullDisk, res, busProvider, runRecoverySweep, quiet, sessionRepo)
	if err != nil {
		return func() {}, err
	}
	stashMemoryOnState(state, opts.Memory, res)
	// Seed the authoritative posture from the launch value BEFORE
	// registering the re-arm, so SetFullDiskReArm's immediate sync call
	// re-applies the same value this root was just opened with instead of
	// stomping a "born unrestricted" root back to false. State is nil for
	// some test harnesses; persistence-only then.
	if state != nil {
		state.seedFullDisk(fullDisk)
		state.SetFullDiskReArm(opts.Workspace.SetUnrestricted)
	}
	sess.Tools = registry
	sess.RefreshPrefixIdentity()
	return closeFn, nil
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

// CreateManagedWorktreeForPool creates a managed worktree in the given
// store. Bridge for the TUI pool's worktree-session creation path —
// uiadapter cannot import internal/cliworktree directly (UI isolation).
func CreateManagedWorktreeForPool(store *storage.SQLite, root, name string) error {
	_, err := cliworktree.CreateManagedWorktreeInStore(store, root, name, "", config.DefaultWorktreeBranchPrefix)
	return err
}

// VerifyWorktreeMarker confirms the on-disk marker under root names
// exactly the live instance a session just bound. The DB row alone cannot
// see a worktree removed and recreated out-of-band at the same path with
// the state still active; the marker is the physical identity the REPL's
// repository binding already checks (bindManagedWorktreeSessionExpected).
// Bridge for the TUI bind path - uiadapter cannot import
// internal/cliworktree directly (UI isolation).
func VerifyWorktreeMarker(root string, want contextstate.WorktreeInstance) error {
	got, err := cliworktree.ReadWorktreeMarker(root)
	if err != nil {
		return fmt.Errorf("worktree %q marker: %w", want.Worktree, err)
	}
	if got != want {
		return fmt.Errorf("worktree %q marker names instance %s, binding expects %s - the directory was replaced out-of-band",
			want.Worktree, got.ID, want.ID)
	}
	return nil
}
