package cli

// clichat_aliases.go re-exports symbols that moved to internal/clichat so
// staying consumers (internal/legacytui) compile without per-file import
// updates. Use the clichat-qualified form in new code. These aliases are
// intentional shims while the extraction stabilises.

import clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"

// BindManagedWorktreeSessionExpected re-exports the clichat.BindManagedWorktreeSessionExpected function.
var BindManagedWorktreeSessionExpected = clichat.BindManagedWorktreeSessionExpected

// ChatInvocation re-exports the clichat.ChatInvocation type.
type ChatInvocation = clichat.ChatInvocation

// DisplaySessionName re-exports the clichat.DisplaySessionName function.
var DisplaySessionName = clichat.DisplaySessionName

// EnableSessionContext re-exports the clichat.EnableSessionContext function.
var EnableSessionContext = clichat.EnableSessionContext

// HandleSlashSessions re-exports the clichat.HandleSlashSessions function.
var HandleSlashSessions = clichat.HandleSlashSessions

// LoadContextSessionResult re-exports the clichat.LoadContextSessionResult function.
var LoadContextSessionResult = clichat.LoadContextSessionResult

// NewChatInvocationRepositorySessionStorePath re-exports the clichat.NewChatInvocationRepositorySessionStorePath function.
var NewChatInvocationRepositorySessionStorePath = clichat.NewChatInvocationRepositorySessionStorePath

// NewChatInvocationWorkspacePath re-exports the clichat.NewChatInvocationWorkspacePath function.
var NewChatInvocationWorkspacePath = clichat.NewChatInvocationWorkspacePath

// NewSessionDispatcherMinimal re-exports the clichat.NewSessionDispatcherMinimal function.
var NewSessionDispatcherMinimal = clichat.NewSessionDispatcherMinimal

// RepositorySessionStorePath re-exports the clichat.RepositorySessionStorePath function.
var RepositorySessionStorePath = clichat.RepositorySessionStorePath

// SessionEffortBusyRefusal re-exports the clichat.SessionEffortBusyRefusal constant.
const SessionEffortBusyRefusal = clichat.SessionEffortBusyRefusal

// SessionIdentity re-exports the clichat.SessionIdentity function.
var SessionIdentity = clichat.SessionIdentity

// SetupChatSessionContext re-exports the clichat.SetupChatSessionContext function.
var SetupChatSessionContext = clichat.SetupChatSessionContext

// SetupRepositorySessionContext re-exports the clichat.SetupRepositorySessionContext function.
var SetupRepositorySessionContext = clichat.SetupRepositorySessionContext

// SetupSessionContext re-exports the clichat.SetupSessionContext function.
var SetupSessionContext = clichat.SetupSessionContext

// SlashSurfacePlain re-exports the clichat.SlashSurfacePlain constant.
const SlashSurfacePlain = clichat.SlashSurfacePlain

// SlashSurfaceTUI re-exports the clichat.SlashSurfaceTUI constant.
const SlashSurfaceTUI = clichat.SlashSurfaceTUI

// ApplySessionAgent re-exports the clichat.ApplySessionAgent function.
var ApplySessionAgent = clichat.ApplySessionAgent

// SaveSessionResult re-exports the clichat.SaveSessionResult function.
var SaveSessionResult = clichat.SaveSessionResult

// DeleteSessionResult re-exports the clichat.DeleteSessionResult function.
var DeleteSessionResult = clichat.DeleteSessionResult

// InjectSkillResourceTool re-exports the clichat.InjectSkillResourceTool function.
var InjectSkillResourceTool = clichat.InjectSkillResourceTool

// LoadSessionResult re-exports the clichat.LoadSessionResult function.
var LoadSessionResult = clichat.LoadSessionResult

// NewSessionDispatcher re-exports the clichat.NewSessionDispatcher function.
var NewSessionDispatcher = clichat.NewSessionDispatcher

// AttachSessionDispatcher re-exports the clichat.AttachSessionDispatcher function.
var AttachSessionDispatcher = clichat.AttachSessionDispatcher

// SessionRouting re-exports the clichat.SessionRouting type.
type SessionRouting = clichat.SessionRouting

// LoadSessionSkills re-exports the clichat.LoadSessionSkills function.
var LoadSessionSkills = clichat.LoadSessionSkills
