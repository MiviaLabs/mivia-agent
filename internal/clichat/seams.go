// Package clichat holds the interactive REPL session loop, the session
// stack, and the terminal UI rendering support that the loop needs. It is
// extracted from internal/cli and must never import internal/cli; the cli
// package wires the seam vars below at process start.
package clichat

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// FlagValueFunc stands for cli.flagValue: it returns the value of the first
// occurrence of any named flag and the remaining arguments. Wired by
// internal/cli/clichat_wiring.go; tests wire it in TestMain.
var FlagValueFunc func(args []string, names ...string) (string, []string, bool, error)

// FlagVarFunc stands for cli.flagVar: like FlagValueFunc but for repeatable
// string flags. Wired by internal/cli/clichat_wiring.go.
var FlagVarFunc func(args []string, names ...string) ([]string, []string, bool, error)

// HookSessionState is the read surface of the cli hook session that this
// package needs: runnable hook groups plus a warning sink.
type HookSessionState interface {
	// RunnableGroups returns the hook groups that tool calls may run.
	RunnableGroups() []hooks.Group
	// NoteRunWarnings records bounded diagnostics from executed hooks.
	NoteRunWarnings(warnings []string)
}

// InstallHookSessionFunc stands for cli.installHookSession. Wired by
// internal/cli/clichat_wiring.go; tests wire it in TestMain.
var InstallHookSessionFunc func(workspaceRoot string, staleBypass, quiet bool) (func(), error)

// CurrentHookSessionFunc stands for cli.currentHookSession.
var CurrentHookSessionFunc func() HookSessionState

// HookSessionConfiguredFunc stands for cli.hookSessionConfigured.
var HookSessionConfiguredFunc func() bool

// HandleSlashHooksFunc stands for cli.handleSlashHooks.
var HandleSlashHooksFunc func(fields []string, term *Terminal) (bool, bool, error)

// MemoryOfFunc stands for cli.memoryOf.
var MemoryOfFunc func(state *AgentSessionState) memory.Store

// MemoryConfigOfFunc stands for cli.memoryConfigOf.
var MemoryConfigOfFunc func(state *AgentSessionState) config.MemoryConfig

// TUILauncherFunc stands for the TUI launcher that cli owns. Wired by
// internal/cli/tui_launcher.go.
var TUILauncherFunc func(sess *chat.Session, res *config.Resolved, toolsOn bool, agentState *AgentSessionState, resumeSessionName string) error

// OpenStackLedgerFunc stands for cli.openStackLedger.
var OpenStackLedgerFunc func(root, configPath string) (*workflowledger.Store, workflowledger.Repository, func(), error)

// ResolveStackIDFunc stands for cli.resolveStackID.
var ResolveStackIDFunc func(repo workflowledger.Repository, workflowName, stackFlag string) (string, error)

// ParseStackWorkflowArgsFunc stands for cli.parseStackWorkflowArgs.
var ParseStackWorkflowArgsFunc func(args []string) (name, stackFlag string, rest []string, err error)

// NewEmptyDelegateToolFunc stands for a zero-value cli delegate tool, used by
// the session tool catalog.
var NewEmptyDelegateToolFunc func() tools.Tool
