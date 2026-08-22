package clichat

// This file exists to support internal/legacytui's test suite across the
// package boundary. It exports package-private helpers that
// internal/legacytui's relocated tests still need. Each export is a thin
// identity wrapper (or alias) around a cli-only symbol with real internal
// callers; wrapping keeps every existing call site untouched.
//
// Treat these exports as test-support API, not general-purpose public API:
// they exist for internal/legacytui's tests, not for arbitrary callers, and
// carry no compatibility promise beyond that use.

import (
	"io"
	"os"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ChatInvocation is chatInvocation, exported for internal/legacytui.
type ChatInvocation = chatInvocation

// SessionRouting is sessionRouting, exported for internal/legacytui.
type SessionRouting = sessionRouting

// KeyRegistry is keyRegistry, exported for internal/legacytui.
var KeyRegistry = keyRegistry

// ForbiddenKeys is forbiddenKeys, exported for internal/legacytui.
var ForbiddenKeys = forbiddenKeys

// Binding is binding, exported for internal/legacytui.
type BindingExport = Binding

// ScopeGlobal is scopeGlobal, exported for internal/legacytui.
var ScopeGlobal = scopeGlobal

// ScopeComposer is scopeComposer, exported for internal/legacytui.
var ScopeComposer = scopeComposer

// ScopeSuggest is scopeSuggest, exported for internal/legacytui.
var ScopeSuggest = scopeSuggest

// ScopeScrollback is scopeScrollback, exported for internal/legacytui.
var ScopeScrollback = scopeScrollback

// ScopeDashboard is scopeDashboard, exported for internal/legacytui.
var ScopeDashboard = scopeDashboard

// ScopeOverlay is scopeOverlay, exported for internal/legacytui.
var ScopeOverlay = scopeOverlay

// ScopeSessions is scopeSessions, exported for internal/legacytui.
var ScopeSessions = scopeSessions

// ScopeWorkflows is scopeWorkflows, exported for internal/legacytui.
var ScopeWorkflows = scopeWorkflows

// ScopeWelcome is scopeWelcome, exported for internal/legacytui.
var ScopeWelcome = scopeWelcome

// ScopeHistory is scopeHistory, exported for internal/legacytui.
var ScopeHistory = scopeHistory

// ScopeQueue is scopeQueue, exported for internal/legacytui.
var ScopeQueue = scopeQueue

// KeyLabel is keyLabel, exported for internal/legacytui.
func KeyLabel(b Binding) string { return keyLabel(b) }

// SetupSessionContext is setupSessionContext, exported for internal/legacytui.
func SetupSessionContext(sess *chat.Session, root string, res *config.Resolved) (*storage.SQLite, error) {
	return setupSessionContext(sess, root, res)
}

// OpenContextStorePath is openContextStorePath, exported for internal/legacytui.
func OpenContextStorePath(path string) (*storage.SQLite, error) {
	return openContextStorePath(path)
}

// ContextWorkspaceID is contextWorkspaceID, exported for internal/legacytui.
func ContextWorkspaceID(root string) string {
	return contextWorkspaceID(root)
}

// ValidateWorkspaceRestart is validateWorkspaceRestart, exported for internal/legacytui.
func ValidateWorkspaceRestart(restart workspaceRestartError, invocation ChatInvocation) error {
	return validateWorkspaceRestart(restart, invocation)
}

// BindManagedWorktreeSessionExpected is bindManagedWorktreeSessionExpected, exported for internal/legacytui.
func BindManagedWorktreeSessionExpected(sess *chat.Session, repositoryRoot, workspaceRoot, storePath string, expected contextstate.WorktreeInstance) error {
	return bindManagedWorktreeSessionExpected(sess, repositoryRoot, workspaceRoot, storePath, expected)
}

// HandleSlash is handleSlash, exported for internal/legacytui.
func HandleSlash(line string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal) (bool, bool, error) {
	return handleSlash(line, sess, res, toolsOn, term)
}

// ValidateKeyRegistry is validateKeyRegistry, exported for internal/legacytui.
func ValidateKeyRegistry(rs []binding) []error {
	return validateKeyRegistry(rs)
}

// SkillScopeFromAgent is cliagents.SkillScopeFromAgent, exported for internal/legacytui.
func SkillScopeFromAgent(selected *agents.ResolvedAgent) AgentSkillScope {
	return cliagents.SkillScopeFromAgent(selected)
}

// SkillScopeFromAgentAndRegistry is cliagents.SkillScopeFromAgentAndRegistry, exported for internal/legacytui.
func SkillScopeFromAgentAndRegistry(selected *agents.ResolvedAgent, reg *tools.Registry) AgentSkillScope {
	return cliagents.SkillScopeFromAgentAndRegistry(selected, reg)
}

// FilterSkillsForScope is cliagents.FilterSkillsForScope, exported for internal/legacytui.
func FilterSkillsForScope(reg *skills.Registry, scope AgentSkillScope) *skills.Registry {
	return cliagents.FilterSkillsForScope(reg, scope)
}

// RenderOneChatBlock is renderOneChatBlock, exported for internal/legacytui.
func RenderOneChatBlock(block ChatBlock, model string, width int, thinkingExpandDefault bool) []string {
	return renderOneChatBlock(block, model, width, thinkingExpandDefault)
}

// SummarizeToolDetail is summarizeToolDetail, exported for internal/legacytui.
func SummarizeToolDetail(name, detail, result string) string {
	return summarizeToolDetail(name, detail, result)
}

// RenderThinkingBlock is renderThinkingBlock, exported for internal/legacytui.
func RenderThinkingBlock(text string, collapsed bool, scrollOffset int, thinkingExpandDefault bool, width int) []string {
	return renderThinkingBlock(text, collapsed, scrollOffset, thinkingExpandDefault, width)
}

// HighlightCodeBlock is highlightCodeBlock, exported for internal/legacytui.
func HighlightCodeBlock(lang, code string) string {
	return highlightCodeBlock(lang, code)
}

// FormatUserMessageCard is formatUserMessageCard, exported for internal/legacytui.
func FormatUserMessageCard(text string, width int, sentAt time.Time) []string {
	return formatUserMessageCard(text, width, sentAt)
}

// FormatUserBubbleTime is formatUserBubbleTime, exported for internal/legacytui.
func FormatUserBubbleTime(t time.Time) string {
	return formatUserBubbleTime(t)
}

// OrchestrationSwitchGuard is orchestrationSwitchGuard, exported for internal/legacytui.
func OrchestrationSwitchGuard(sessionID string) func() error {
	return cliorchestrate.OrchestrationSwitchGuard(sessionID)
}

// AttachSessionDispatcher is attachSessionDispatcher, exported for internal/legacytui.
func AttachSessionDispatcher(sess *chat.Session, root, model string, cfg config.SubagentConfig, state *AgentSessionState, skillReg *skills.Registry, routing SessionRouting) (func(), error) {
	return attachSessionDispatcher(sess, root, model, cfg, state, skillReg, routing)
}

// EmitSubagentProgress is emitSubagentProgress, exported for internal/legacytui.
func EmitSubagentProgress(e agent.Event) {
	emitSubagentProgress(e)
}

// BuildSkillCatalogue is cliagents.BuildSkillCatalogue, exported for internal/legacytui.
func BuildSkillCatalogue(workspaceRoot string) (map[string]agents.SkillCatalogueEntry, []string) {
	return cliagents.BuildSkillCatalogue(workspaceRoot)
}

// NewAgentTaskHandler is newAgentTaskHandler, exported for internal/legacytui.
func NewAgentTaskHandler(definition agents.ResolvedAgent, digest string, full *tools.Registry, d *runtime.Dispatcher, opts SessionDispatcherOpts) *agentTaskHandler {
	return newAgentTaskHandler(definition, digest, full, d, opts)
}

// NewSessionDispatcherMinimal is newSessionDispatcherMinimal, exported for internal/legacytui.
func NewSessionDispatcherMinimal(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, toolResultCapBytes int, skillReg ...*skills.Registry) (*runtime.Dispatcher, error) {
	return newSessionDispatcherMinimal(reg, comp, model, cfg, toolResultCapBytes, skillReg...)
}

// ResolveTaskRoute is cliorchestrate.ResolveTaskRoute, exported for internal/legacytui.
func ResolveTaskRoute(reg *agents.AgentRegistry, skillReg *skills.Registry, agentName, skillName string) (cliorchestrate.TaskRoute, error) {
	return cliorchestrate.ResolveTaskRoute(reg, skillReg, agentName, skillName)
}

// RepositorySessionStorePath is repositorySessionStorePath, exported for internal/legacytui.
func RepositorySessionStorePath(root string, invocation ChatInvocation, r *config.Resolved) (string, error) {
	return repositorySessionStorePath(root, invocation, r)
}

// ReplHelpContent is replHelpContent, exported for internal/legacytui.
func ReplHelpContent() []helpSection {
	return replHelpContent()
}

// RenderReplHelpInline is renderReplHelpInline, exported for internal/legacytui.
func RenderReplHelpInline() string {
	return renderReplHelpInline()
}

// RegisterManagedWorktreeInStore is registerManagedWorktreeInStore, exported for internal/legacytui.
func RegisterManagedWorktreeInStore(store *storage.SQLite, root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	return cliworktree.RegisterManagedWorktreeInStore(store, root, wt)
}

// RecoverManagedWorktreeRemoval is cliworktree.RecoverManagedWorktreeRemoval, exported for internal/legacytui.
func RecoverManagedWorktreeRemoval(root, name, branchPrefix string) (bool, error) {
	return cliworktree.RecoverManagedWorktreeRemoval(root, name, branchPrefix)
}

// OpenWorkflowStore is cliworkflow.OpenWorkflowStore, exported for internal/legacytui.
func OpenWorkflowStore(root string, cfg config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
	return cliworkflow.OpenWorkflowStore(root, cfg)
}

// OneShot is oneShot, exported for internal/legacytui.
func OneShot(sess *chat.Session, prompt string, toolsOn bool, res *config.Resolved, quiet bool) error {
	return oneShot(sess, prompt, toolsOn, res, quiet)
}

// ProcessLineChat is processLineChat, exported for internal/legacytui.
func ProcessLineChat(line string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal, renderer *ChatRenderer, input *InputBuffer, modelShort string) error {
	return processLineChat(line, sess, res, toolsOn, term, renderer, input, modelShort)
}

// NewREPLRuntime is newREPLRuntime, exported for internal/legacytui.
func NewREPLRuntime(sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal) *replRuntime {
	return newREPLRuntime(sess, res, toolsOn, term)
}

// HandleSlashSessions is handleSlashSessions, exported for internal/legacytui.
func HandleSlashSessions(cmd, line string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	return handleSlashSessions(cmd, line, sess, term)
}

// HandleSlashAgent is handleSlashAgent, exported for internal/legacytui.
func HandleSlashAgent(fields []string, sess *chat.Session, res *config.Resolved, term *Terminal, state *AgentSessionState) (bool, bool, error) {
	return handleSlashAgent(fields, sess, res, term, state)
}

// HandleSlashInfo is handleSlashInfo, exported for internal/legacytui.
func HandleSlashInfo(cmd string, fields []string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal) (bool, bool, error) {
	return handleSlashInfo(cmd, fields, sess, res, toolsOn, term)
}

// DialogRectFor is dialogRect, exported for internal/legacytui.
func DialogRectFor(termW, termH int, p DialogPrefs, contentW, contentH int) Rect {
	return dialogRect(termW, termH, p, contentW, contentH)
}

// ConfigureChatWorkspace is cliagents.ConfigureChatWorkspace, exported for internal/legacytui.
func ConfigureChatWorkspace(sess *chat.Session, root string, useTools bool, res *config.Resolved, state *AgentSessionState, quiet bool, fullDisk bool, runRecoverySweep bool) (func(), error) {
	return cliagents.ConfigureChatWorkspace(sess, root, useTools, res, state, quiet, fullDisk, runRecoverySweep)
}

// BuildModelBinding is cliagents.BuildModelBinding, exported for internal/legacytui.
func BuildModelBinding(sess *chat.Session, res *config.Resolved, root, providerName, model string, state *AgentSessionState) (chat.ModelBinding, error) {
	return cliagents.BuildModelBinding(sess, res, root, providerName, model, state)
}

// ApplyWorkflowStoreRoot is cliworkflow.ApplyWorkflowStoreRoot, exported for internal/legacytui.
func ApplyWorkflowStoreRoot(res *config.Resolved, root string) {
	cliworkflow.ApplyWorkflowStoreRoot(res, root)
}

// ApplyPrivacyPolicy is applyPrivacyPolicy, exported for internal/legacytui.
func ApplyPrivacyPolicy(res *config.Resolved) {
	applyPrivacyPolicy(res)
}

// TuiHelpCommands is tuiHelpCommands, exported for internal/legacytui.
func TuiHelpCommands() []helpSection {
	return tuiHelpCommands()
}

// SendLineMode is sendLineMode, exported for internal/legacytui.
func SendLineMode(sess *chat.Session, line string, sigCh <-chan os.Signal, jsonMode bool) error {
	return sendLineMode(sess, line, sigCh, jsonMode)
}

// CreateManagedWorktree is createManagedWorktree, exported for internal/legacytui.
func CreateManagedWorktree(root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, error) {
	return cliworktree.CreateManagedWorktree(root, name, baseRef, branchPrefix)
}

// BeginManagedWorktreeRemoval is cliworktree.BeginManagedWorktreeRemoval, exported for internal/legacytui.
func BeginManagedWorktreeRemoval(root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	return cliworktree.BeginManagedWorktreeRemoval(root, wt)
}

// WorktreeMarkerPath is cliworktree.WorktreeMarkerPath, exported for internal/legacytui.
func WorktreeMarkerPath(root string) string {
	return cliworktree.WorktreeMarkerPath(root)
}

// WriteWorktreeMarker is cliworktree.WriteWorktreeMarker, exported for internal/legacytui.
func WriteWorktreeMarker(root string, instance contextstate.WorktreeInstance) error {
	return cliworktree.WriteWorktreeMarker(root, instance)
}

// CreateManagedWorktreeInStore is cliworktree.CreateManagedWorktreeInStore, exported for internal/legacytui.
func CreateManagedWorktreeInStore(store *storage.SQLite, root, name, baseRef, branchPrefix string) (*vcs.WorktreeInfo, error) {
	return cliworktree.CreateManagedWorktreeInStore(store, root, name, baseRef, branchPrefix)
}

// BeginManagedWorktreeRemovalInStore is cliworktree.BeginManagedWorktreeRemovalInStore, exported for internal/legacytui.
func BeginManagedWorktreeRemovalInStore(store *storage.SQLite, root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	return cliworktree.BeginManagedWorktreeRemovalInStore(store, root, wt)
}

// SetupChatSessionContext is setupChatSessionContext, exported for internal/legacytui.
func SetupChatSessionContext(sess *chat.Session, workspaceRoot string, invocation ChatInvocation, res *config.Resolved) (*storage.SQLite, error) {
	return setupChatSessionContext(sess, workspaceRoot, invocation, res)
}

// SetupRepositorySessionContext is setupRepositorySessionContext, exported for internal/legacytui.
func SetupRepositorySessionContext(sess *chat.Session, repositoryRoot, storePath string, res *config.Resolved) (*storage.SQLite, error) {
	return setupRepositorySessionContext(sess, repositoryRoot, storePath, res)
}

// RegisterWorktreeRoute is cliworktree.RegisterWorktreeRoute, exported for internal/legacytui.
func RegisterWorktreeRoute(root string, wt *vcs.WorktreeInfo) error {
	return cliworktree.RegisterWorktreeRoute(root, wt)
}

// EnableSessionContext is enableSessionContext, exported for internal/legacytui.
func EnableSessionContext(sess *chat.Session, root string, store *storage.SQLite, res *config.Resolved) error {
	return enableSessionContext(sess, root, store, res)
}

// OpenContextStore is openContextStore, exported for internal/legacytui.
func OpenContextStore(root string, cfg config.SubagentConfig) (*storage.SQLite, error) {
	return openContextStore(root, cfg)
}

// ClassicAgentStatePtr is &cliagents.ClassicAgentState, exported for internal/legacytui.
var ClassicAgentStatePtr = &cliagents.ClassicAgentState

// NewTestTerminal builds a Terminal that writes to w, for tests that need a
// Terminal without opening a real tty. Exported for internal/legacytui.
func NewTestTerminal(w io.Writer) *Terminal {
	return &Terminal{out: w}
}

// ContextDispatcherFor is contextDispatcherFor, exported for internal/legacytui.
func ContextDispatcherFor(sess *chat.Session, cfg config.SubagentConfig) ContextDispatcherWiring {
	return contextDispatcherFor(sess, cfg)
}

// OrchestrationRepoForDispatcher is cliorchestrate.OrchestrationRepoForDispatcher, exported
// for internal/legacytui.
func OrchestrationRepoForDispatcher(d *runtime.Dispatcher) ledger.LedgerRepository {
	return cliorchestrate.OrchestrationRepoForDispatcher(d)
}

// NewChatInvocationWorkspacePath builds a ChatInvocation with only
// workspacePath set, for internal/legacytui tests that need one without a
// full CLI parse. chatInvocation's fields are unexported (chat_command.go),
// so a constructor is the only way to set one from outside the package.
func NewChatInvocationWorkspacePath(workspacePath string) ChatInvocation {
	return chatInvocation{workspacePath: workspacePath}
}

// NewChatInvocationRepositorySessionStorePath builds a ChatInvocation with
// only repositorySessionStorePath set, for internal/legacytui tests.
func NewChatInvocationRepositorySessionStorePath(path string) ChatInvocation {
	return chatInvocation{repositorySessionStorePath: path}
}

// WorkGroupWindowRows is workGroupWindowRows, exported for internal/legacytui.
const WorkGroupWindowRows = workGroupWindowRows

// REPLRuntime is replRuntime, exported for internal/legacytui.
type REPLRuntime = replRuntime

// RestoreREPLRuntime builds a replRuntime for sess/res/term via
// newREPLRuntime (toolsOn=false: no caller of this export exercises the
// tool-enabled REPL path) and returns the runtime's resulting short model
// name. newREPLRuntime itself runs the restore step (the auto-save "restored
// previous session" notice) as part of construction. Exported for
// internal/legacytui.
func RestoreREPLRuntime(sess *chat.Session, res *config.Resolved, term *Terminal) string {
	r := newREPLRuntime(sess, res, false, term)
	return r.modelShort
}

// SlashSurfacePlain is slashSurfacePlain, exported for internal/legacytui.
const SlashSurfacePlain = slashSurfacePlain

// LoadSessionSkills is cliagents.LoadSessionSkills, exported for internal/legacytui.
func LoadSessionSkills(root string, allowProject bool) (*skills.Registry, []string, error) {
	return cliagents.LoadSessionSkills(root, allowProject)
}

// SkillTurnPreamble is skillTurnPreamble, exported for internal/legacytui.
const SkillTurnPreamble = skillTurnPreamble

// AnsiBgDiffAdd and AnsiBgDiffDel are ansiBgDiffAdd/ansiBgDiffDel, exported
// for internal/legacytui.
const (
	AnsiBgDiffAdd = ansiBgDiffAdd
	AnsiBgDiffDel = ansiBgDiffDel
)

// LoadChatSkills is loadChatSkills, exported for internal/legacytui.
func LoadChatSkills(wsRoot string) (*skills.Registry, error) {
	return loadChatSkills(wsRoot)
}

// LoadAgentDefinitions is cliagents.LoadAgentDefinitions, exported for internal/legacytui.
func LoadAgentDefinitions(workspaceRoot, agentFlag string, skillReg *skills.Registry) (cliagents.AgentLoadResult, error) {
	return cliagents.LoadAgentDefinitions(workspaceRoot, agentFlag, skillReg)
}

// SnapshotWorktreeDialogBinding is snapshotWorktreeDialogBinding, exported
// for internal/legacytui.
func SnapshotWorktreeDialogBinding(store *storage.SQLite, principal contextstate.Principal, worktree vcs.WorktreeInfo) cliworktree.WorktreeDialogBinding {
	return cliworktree.SnapshotWorktreeDialogBinding(store, principal, worktree)
}

// WriteWorktreeList is cliworktree.WriteWorktreeList, exported for internal/legacytui.
func WriteWorktreeList(stdout io.Writer, worktrees []vcs.WorktreeInfo, deleting []contextstate.WorktreeInstanceInfo) {
	cliworktree.WriteWorktreeList(stdout, worktrees, deleting)
}

// RunWorktreeWithIO is cliworktree.RunWorktreeWithIO, exported for internal/legacytui.
func RunWorktreeWithIO(args []string, stdout io.Writer) error {
	return cliworktree.RunWorktreeWithIO(args, stdout)
}

// AdoptManagedWorktree is cliworktree.AdoptManagedWorktree, exported for internal/legacytui.
func AdoptManagedWorktree(root string, wt *vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	return cliworktree.AdoptManagedWorktree(root, wt)
}

// CurrentAgentName is cliagents.CurrentAgentName, exported for internal/legacytui.
func CurrentAgentName(state *AgentSessionState) string {
	return cliagents.CurrentAgentName(state)
}

// FormatAgentSet is cliagents.FormatAgentSet, exported for internal/legacytui.
func FormatAgentSet(name string) string {
	return cliagents.FormatAgentSet(name)
}

// FormatAgentCurrent is cliagents.FormatAgentCurrent, exported for internal/legacytui.
func FormatAgentCurrent(name string, reg *agents.AgentRegistry) string {
	return cliagents.FormatAgentCurrent(name, reg)
}

// ApplySessionAgent is cliagents.ApplySessionAgent, exported for internal/legacytui.
func ApplySessionAgent(sess *chat.Session, res *config.Resolved, state *AgentSessionState, name string, busy bool) error {
	return cliagents.ApplySessionAgent(sess, res, state, name, busy)
}

// SwitchModelCommand is cliagents.SwitchModelCommand, exported for internal/legacytui.
func SwitchModelCommand(sess *chat.Session, res *config.Resolved, providerName, model string) (reasoning.Level, error) {
	return cliagents.SwitchModelCommand(sess, res, providerName, model)
}

// SessionIdentity is cliagents.SessionIdentity, exported for internal/legacytui.
func SessionIdentity(sess *chat.Session, state *AgentSessionState, generation uint64) *events.Identity {
	return cliagents.SessionIdentity(sess, state, generation)
}
