package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
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
func configureChatWorkspace(sess *chat.Session, root string, useTools bool, res *config.Resolved) (func(), error) {
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
	// workspace. State every declared command once at startup, before any tool
	// call: a configured command that runs programs is a disclosure, not a
	// hidden capability (the same contract as the lifecycle-hooks armedNotice).
	// Names are sorted so the line is deterministic across runs.
	if len(tc.DiagnosticsCommands) > 0 {
		names := make([]string, 0, len(tc.DiagnosticsCommands))
		for name := range tc.DiagnosticsCommands {
			names = append(names, name)
		}
		slices.Sort(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			parts = append(parts, fmt.Sprintf("%s=[%s]", name, strings.Join(tc.DiagnosticsCommands[name], " ")))
		}
		fmt.Fprintf(os.Stderr, "diagnostics: configured commands: %s\n", strings.Join(parts, ", "))
	}
	// Phase 7: attach in-process workflow tools when .mivia/workflows/ exists.
	// Pass res so the store path matches prepareWorkflowRun / CLI commands.
	// The bus provider is read when a controller attaches, so sess.EventBus
	// (created later by runTUI) still receives workflow progress events.
	wireWorkflowToolOptions(&opts, ws.Abs, res, func() *events.Bus { return sess.EventBus })
	if err := wireSessionMemory(&opts, root, res); err != nil {
		return func() {}, err
	}
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
