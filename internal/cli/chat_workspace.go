package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
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
func stashMemoryOnState(state *agentSessionState, store memory.Store, res *config.Resolved) {
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

func configureChatWorkspace(sess *chat.Session, root string, useTools bool, res *config.Resolved, state *agentSessionState, quiet bool) (func(), error) {
	if !useTools {
		return func() {}, nil
	}
	ws, err := workspace.Open(root)
	if err != nil {
		return func() {}, fmt.Errorf("workspace: %w", err)
	}
	var tc config.ToolsConfig
	var tavilyKey string
	if res != nil {
		tc = res.Tools
		tavilyKey = res.TavilyAPIKey
	}
	opts := tools.DefaultOptions{
		Workspace:                ws,
		TavilyAPIKey:             tavilyKey,
		RunAllowlist:             tc.RunAllowlist,
		RunAllowlistOnly:         tc.RunAllowlistOnly,
		RunBlocklist:             tc.RunBlocklist,
		DisableTools:             tc.DisableTools,
		EnvAllowlist:             tc.EnvAllowlist,
		EnvAllowlistOnly:         tc.EnvAllowlistOnly,
		EnvBlocklist:             tc.EnvBlocklist,
		EnvAllowKeywordBlocklist: tc.EnvAllowKeywordBlocklist,
		RunTimeoutSec:            tc.RunTimeoutSec,
		MaxReadBytes:             tc.MaxReadBytes,
		MaxWriteKB:               tc.MaxWriteKB,
		MaxOutputBytes:           tc.MaxOutputBytes,
		MaxListDirEntries:        tc.MaxListDirEntries,
		MaxToolResultBytes:       tc.MaxToolResultBytes,
		MaxTavilyResponseBytes:   tc.MaxTavilyResponseBytes,
		MaxFetchKB:               tc.MaxFetchKB,
		// MiB → bytes; resolveToolsConfig already settled 0 → default 256.
		MemoryBackstopBytes: tc.MemoryBackstopMB << 20,
		// RedactToolArgs is NOT plumbed here - the single source of truth
		// is the package atomic set by tools.SetRedactToolArgs at line 40.
		SecretPathPatterns:        tc.SecretPathPatterns,
		SecretPathExceptions:      tc.SecretPathExceptions,
		SearchIgnorePatterns:      tc.SearchIgnorePatterns,
		MaxInspectRepositoryBytes: tc.MaxInspectRepositoryBytes,
		DiagnosticsCommands:       tc.DiagnosticsCommands,
	}
	// get_diagnostics runs the operator-configured diagnostics commands on this
	// workspace (see DiagnosticsCommands above). The startup disclosure that
	// states which commands are configured lives in logDiagnosticsCommandsOnce,
	// called from runConfiguredChatOnce beside the other startup notices.
	// Phase 7: attach in-process workflow tools when .mivia/workflows/ exists.
	// Pass res so the store path matches prepareWorkflowRun / CLI commands.
	// The bus provider is read when a controller attaches, so sess.EventBus
	// (created later by runTUI) still receives workflow progress events.
	wireWorkflowToolOptions(&opts, ws.Abs, res, func() *events.Bus { return sess.EventBus }, quiet)
	if err := wireSessionMemory(&opts, root, res); err != nil {
		return func() {}, err
	}
	stashMemoryOnState(state, opts.Memory, res)
	sess.Tools = tools.NewDefaultRegistry(opts)
	// The attach-time tool surface is wire-affecting and not one of the four
	// identity-capture triggers; keep the cached prefix identity fresh so the
	// first switch/publication compares fresh against fresh (audit RC-1).
	sess.RefreshPrefixIdentity()
	return func() {
		if opts.Memory != nil {
			_ = opts.Memory.Close()
		}
	}, nil
}

// logDiagnosticsCommandsOnce states every configured get_diagnostics command
// once at startup, before any tool call: a configured command that runs
// programs is a disclosure, not a hidden capability (the same contract as the
// lifecycle-hooks armedNotice). Names are sorted so the line is deterministic
// across runs. quiet (--quiet) suppresses the line; the tool itself is still
// registered whenever the workspace declares commands.
func logDiagnosticsCommandsOnce(w io.Writer, tc config.ToolsConfig, quiet bool) {
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
