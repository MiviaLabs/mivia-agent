package cliagents

import (
	"fmt"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ClassicAgentState is the root agent context for the classic REPL/one-shot
// chat path. The TUI stores the same pointer on TUIModel.agentState.
var ClassicAgentState *AgentSessionState

// AgentSessionState is the mid-session mutable agent context. Startup and
// /agent switch share this so model-switch rebuilds keep the selected agent.
type AgentSessionState struct {
	mu                 sync.Mutex
	Global             config.AgentsGlobal
	Selected           *agents.ResolvedAgent
	AllowProjectSkills bool
	Registry           *agents.AgentRegistry
	WorkspaceRoot      string
	// ToolBase is the post-dispatcher, pre-scope registry for re-scoping.
	// Nil when tools are off.
	ToolBase *tools.Registry
	// MCPManager owns the session-wide MCP clients. Agent switches borrow it to
	// discover newly selected server tools without starting another client.
	MCPManager *mcp.Manager
	// SkillScope is the immutable per-instance skill policy for the selected
	// root agent, including the final live tool registry snapshot (plan 43).
	// Set at dispatcher attach and agent switch; read by the TUI slash path.
	SkillScope AgentSkillScope
	// TierPlan is the frozen core/deferred tool split for the current agent
	// binding (plan tools/05 D8). Computed once per binding; never recomputed
	// while it lives, so the prompt index it feeds stays byte-stable.
	TierPlan ToolTierPlan
	// SkillRegFull is the current binding's unfiltered skill registry. Surface
	// widening reuses it so admitting a tool performs no skill disk I/O.
	SkillRegFull *skills.Registry
	// LedgerRepo is the session-lifetime ledger repository every surface rebuild
	// passes to NewSessionDispatcherVar. It exists so no dispatcher ever OWNS a
	// ledger store: a republished surface carries the live remainder spool, the
	// spool captured its ContentStore at construction, and publication closes
	// the dispatcher it replaced. A per-dispatcher store would therefore be
	// closed out from under the spool by the first tool admission. Nil means the
	// caller supplied a shared store (the dispatcher borrows it) or tools are off.
	LedgerRepo ledger.LedgerRepository
	// ownedLedgerStore is the durable repository this session opened and must
	// close at cleanup. Nil when LedgerRepo is the process-wide memory default.
	ownedLedgerStore *ledger.StorageLedgerRepository
	// LastSchemaMass is the most recent advertised schema-mass measurement for
	// this session's surface (plan tools/05 D5 telemetry). It is written by the
	// three publications that can change the split or the admitted tail: attach,
	// /agent switch and tool admission. A /model rebuild republishes the same
	// frozen tiers with the same admitted tail, so it deliberately leaves this
	// measurement alone rather than re-emitting an identical one.
	LastSchemaMass   SchemaMass
	BaselinePrompt   string
	BaselineMaxSteps int
	BaselineCaptured bool
	// Memory is the session-lifetime memory store, opened once by
	// configureChatWorkspace and never closed here - the same store
	// tools.DefaultOptions.Memory wires into memory_save/memory_search (plan
	// 77, E1). Nil when memory is disabled or tools are off. Callers hold
	// state.mu (applySessionAgent does), so the field is read directly,
	// matching the LedgerRepo convention above.
	Memory memory.Store
	// MemoryConfig is the resolved [memory] section, read alongside Memory
	// to build the core-tier injection block (coreMemoryBlock).
	MemoryConfig config.MemoryConfig
	// fullDisk is the shared, operator-wide full-disk posture and re-arm
	// list. It is a POINTER so Fork gives every per-session state a private
	// Selected/SkillScope/TierPlan (bug-audit "pooled worktree sessions
	// share mutable agent state") while every fork still drives and
	// observes the SAME confinement posture: full-disk access is an
	// operator setting, not a per-session one, and toggling it from
	// whichever session happens to be active must still reach every live
	// worktree root (bug-audit "full-disk access does not reach active
	// worktree registries"). Never nil after newFullDiskState.
	fullDisk *fullDiskState
}

// fullDiskState is the operator-wide full-disk posture: every live
// workspace root's confinement re-arm, and the authoritative on/off value.
// Shared by pointer across every AgentSessionState Fork produces so an
// operator toggle from any pooled session reaches every other one.
type fullDiskState struct {
	mu     sync.Mutex
	on     bool
	reArms []func(on bool)
}

func newFullDiskState() *fullDiskState {
	return &fullDiskState{}
}

// fullDiskStateLocked returns this state's shared full-disk posture,
// lazily initializing it. Most states are built as struct literals (tests,
// startup wiring), not through a constructor, so fullDisk is nil until
// first use; every accessor below routes through this instead of assuming
// newFullDiskState already ran. Callers hold s.mu.
func (s *AgentSessionState) fullDiskStateLocked() *fullDiskState {
	if s.fullDisk == nil {
		s.fullDisk = newFullDiskState()
	}
	return s.fullDisk
}

// seedFullDisk records the launch-time full-disk posture (the operator's
// `--full-disk` flag or persisted [workspace_access] full_disk setting)
// before any re-arm has registered, so the first SetFullDiskReArm call does
// not stomp a "born unrestricted" root back to false. Only
// ConfigureChatWorkspace, which knows that launch value, calls it.
func (s *AgentSessionState) seedFullDisk(on bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	fd := s.fullDiskStateLocked()
	s.mu.Unlock()
	fd.mu.Lock()
	fd.on = on
	fd.mu.Unlock()
}

// FullDiskOn reports the authoritative full-disk posture: the value ApplyFullDisk
// last set, or the seeded launch value before any toggle. A newly built worktree
// registry uses this - not a peer session's live value - to decide its own
// initial posture, so worktree creation is deterministic regardless of
// session-map iteration order.
func (s *AgentSessionState) FullDiskOn() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	fd := s.fullDiskStateLocked()
	s.mu.Unlock()
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return fd.on
}

// SetFullDiskReArm registers one live confinement re-arm (ConfigureChatWorkspace
// for the launch root, SessionPool for each worktree root it rebuilds) and
// immediately synchronizes it to the current authoritative posture, so a root
// joining after the operator already toggled full-disk access does not start
// out of step.
func (s *AgentSessionState) SetFullDiskReArm(fn func(on bool)) {
	if s == nil || fn == nil {
		return
	}
	s.mu.Lock()
	fd := s.fullDiskStateLocked()
	s.mu.Unlock()
	fd.mu.Lock()
	on := fd.on
	fd.reArms = append(fd.reArms, fn)
	fd.mu.Unlock()
	fn(on)
}

// ApplyFullDisk drives every live re-arm, reporting whether at least one was
// wired. Nil-receiver and empty-list safe: without a chat workspace there is
// nothing to re-arm and the setting stays persistence-only (next launch).
func (s *AgentSessionState) ApplyFullDisk(on bool) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	fd := s.fullDiskStateLocked()
	s.mu.Unlock()
	fd.mu.Lock()
	fd.on = on
	fns := append([]func(bool){}, fd.reArms...)
	fd.mu.Unlock()
	if len(fns) == 0 {
		return false
	}
	for _, fn := range fns {
		fn(on)
	}
	return true
}

// Fork returns a new AgentSessionState for one pooled session entry,
// carrying this state's CURRENT selection and admission plan as that
// entry's own private starting point. Every session-independent facility -
// tool base, MCP manager, ledger, memory store, skill registry, workspace
// root, and the operator-wide full-disk posture - is shared with the state
// it forked from (the full-disk fields by shared pointer, so a toggle from
// any fork still reaches every worktree root; see fullDiskState). Only
// Selected, SkillScope, TierPlan, LastSchemaMass and the Baseline* fields
// are private to the fork from this point on: a later /agent switch or
// deferred-tool admission in ONE pooled session no longer rewrites another
// session's policy through one shared pointer (bug-audit "pooled worktree
// sessions share mutable agent state"). The fork never inherits
// ownedLedgerStore - only the state that opened a durable ledger may close
// it.
func (s *AgentSessionState) Fork() *AgentSessionState {
	if s == nil {
		return &AgentSessionState{fullDisk: newFullDiskState()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return &AgentSessionState{
		Global:             s.Global,
		Selected:           s.Selected,
		AllowProjectSkills: s.AllowProjectSkills,
		Registry:           s.Registry,
		WorkspaceRoot:      s.WorkspaceRoot,
		ToolBase:           s.ToolBase,
		MCPManager:         s.MCPManager,
		SkillScope:         s.SkillScope,
		TierPlan:           s.TierPlan,
		SkillRegFull:       s.SkillRegFull,
		LedgerRepo:         s.LedgerRepo,
		LastSchemaMass:     s.LastSchemaMass,
		BaselinePrompt:     s.BaselinePrompt,
		BaselineMaxSteps:   s.BaselineMaxSteps,
		BaselineCaptured:   s.BaselineCaptured,
		Memory:             s.Memory,
		MemoryConfig:       s.MemoryConfig,
		fullDisk:           s.fullDiskStateLocked(),
	}
}

// DisplayName is the status dialog's "agent" row: the locked, nil-safe read
// of the currently selected agent's name. Exported for internal/legacytui's
// dialog rendering, which cannot lock the unexported mu field directly.
func (s *AgentSessionState) DisplayName() string {
	if s == nil {
		return config.RootAgentName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Selected == nil {
		return config.RootAgentName
	}
	return s.Selected.Name
}

// DisplaySource is the status dialog's "source" row: the locked, nil-safe
// read of the currently selected agent's provenance source. Exported for
// internal/legacytui's dialog rendering, which cannot lock the unexported mu
// field directly.
func (s *AgentSessionState) DisplaySource() string {
	if s == nil {
		return string(config.AgentSourceBuiltIn)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Selected == nil {
		return string(config.AgentSourceBuiltIn)
	}
	return string(s.Selected.Provenance.Source)
}

// Context returns a snapshot of the agent session context. Thread-safe.
func (s *AgentSessionState) Context() AgentSessionContext {
	if s == nil {
		return AgentSessionContext{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return AgentSessionContext{
		Global:             s.Global,
		Selected:           s.Selected,
		Registry:           s.Registry,
		AllowProjectSkills: s.AllowProjectSkills,
	}
}

// ledgerRepo is the session-owned ledger repository for callers that do NOT
// hold s.mu. Surface builds read the field directly under the lock.
func (s *AgentSessionState) ledgerRepo() ledger.LedgerRepository {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LedgerRepo
}

// OwnedLedgerStore returns the session-owned durable ledger store or nil.
// Used by tests outside this package that cannot read the unexported field.
func (s *AgentSessionState) OwnedLedgerStore() *ledger.StorageLedgerRepository {
	if s == nil {
		return nil
	}
	return s.ownedLedgerStore
}

// AdoptLedgerRepo sets the session-lifetime ledger repository and owned store.
// Used by cli.adoptSessionLedgerRepo so the field stays package-private here.
func (s *AgentSessionState) AdoptLedgerRepo(repo ledger.LedgerRepository, owned *ledger.StorageLedgerRepository) {
	if s == nil {
		return
	}
	s.LedgerRepo = repo
	s.ownedLedgerStore = owned
}

// ReleaseOwnedLedgerRepo closes and forgets the store adoptSessionLedgerRepo
// opened. Used by cli.releaseSessionLedgerRepo on the error path.
func (s *AgentSessionState) ReleaseOwnedLedgerRepo() {
	if s == nil {
		return
	}
	if s.ownedLedgerStore != nil {
		_ = s.ownedLedgerStore.Close()
	}
	s.LedgerRepo = nil
	s.ownedLedgerStore = nil
}

// CloseOwnedLedgerStore closes the session's durable ledger store.
// Called by sessionSurfaceCleanup after the dispatcher is torn down.
func (s *AgentSessionState) CloseOwnedLedgerStore() {
	if s == nil || s.ownedLedgerStore == nil {
		return
	}
	_ = s.ownedLedgerStore.Close()
	s.ownedLedgerStore = nil
}

// memoryStore and memoryConfig mirror ledgerRepo for callers that do NOT
// hold s.mu (plan 77, E1/E2).
func (s *AgentSessionState) memoryStore() memory.Store {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Memory
}

func (s *AgentSessionState) memoryConfig() config.MemoryConfig {
	if s == nil {
		return config.MemoryConfig{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.MemoryConfig
}

// Lock acquires the session state mutex. Use with Unlock for cross-package
// atomic reads/writes (tests, legacytui). Production paths hold mu directly.
func (s *AgentSessionState) Lock() { s.mu.Lock() }

// Unlock releases the session state mutex acquired by Lock.
func (s *AgentSessionState) Unlock() { s.mu.Unlock() }

// SetSkillScope stores the selected root agent's skill policy. Writers that
// already hold s.mu (applySessionAgent) assign the field directly.
func (s *AgentSessionState) SetSkillScope(scope AgentSkillScope) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.SkillScope = scope
	s.mu.Unlock()
}

// SkillScopeSnapshot returns a copy of the current root skill policy for the
// TUI slash path. A nil state or unset scope yields the open zero value.
func (s *AgentSessionState) SkillScopeSnapshot() AgentSkillScope {
	if s == nil {
		return AgentSkillScope{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SkillScope
}

// AgentListRow is one selectable entry for the /agent dialog and listings.
type AgentListRow struct {
	Name        string
	Description string
	Current     bool
}

// restoreRootSurface switches the session back to the compiled root surface:
// no selected definition, the baseline prompt and step budget, and a tool
// surface rebuilt from the pre-scope base through the same build path a named
// switch uses (with no selected definition). Callers hold state.mu via
// ApplySessionAgent.
func restoreRootSurface(sess *chat.Session, res *config.Resolved, state *AgentSessionState) error {
	if !state.BaselineCaptured {
		// The session never left the root surface.
		state.Selected = nil
		return nil
	}
	if err := ensureRootMCPTools(sess, res, state); err != nil {
		return fmt.Errorf("MCP tools: %w", err)
	}
	var candidate *agentSurface
	var err error
	if sess.Tools != nil && state.ToolBase != nil {
		candidate, err = buildAgentScopedSurface(sess, res, state, nil)
		if err != nil {
			return fmt.Errorf("root surface: %w", err)
		}
	}
	state.Selected = nil
	if candidate == nil {
		// The session's own AgentSettings carry the active prompt; res is
		// the pool's SHARED launch config and must keep holding the
		// original baseline for every future fresh entry, not this
		// session's current selection (bug-audit "pooled worktree sessions
		// share mutable agent state" - a /agent switch in one session
		// silently rewrote the launch baseline every sibling and future
		// /new session started from).
		sess.SetAgentSettings(state.BaselinePrompt, state.BaselineMaxSteps, CoreMemoryBlockForState(state))
		return nil
	}
	candidate.commitTo(state)
	prompt := promptWithDeferredIndex(state.BaselinePrompt, state.TierPlan)
	commitAgentSwitchSurface(sess, res, state, candidate, config.RootAgentName, prompt, state.BaselineMaxSteps)
	return nil
}

// ApplySessionAgent switches the root agent for the idle session. busy is the
// TUI waiting flag; active turns and switch guards are checked on sess.
// It reuses ToolBase for re-scope and rebuilds the dispatcher like model switch.
func ApplySessionAgent(sess *chat.Session, res *config.Resolved, state *AgentSessionState, name string, busy bool) error {
	if sess == nil || state == nil {
		return fmt.Errorf("agent switch requires a session and agent state")
	}
	// Selection and all session-owned surfaces are one logical transaction.
	// Candidate construction is intentionally inside this lock so two /agent
	// requests cannot publish different surfaces under one selected name.
	state.mu.Lock()
	defer state.mu.Unlock()
	if busy {
		return fmt.Errorf("finish current work first")
	}
	release, err := sess.BeginSurfaceSwitch()
	if err != nil {
		return err
	}
	defer release()
	if err := sess.CheckSwitchAllowed(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("agent name is empty")
	}
	if name == config.RootAgentName {
		// The root identity is compiled and never a registry member: this
		// restores the root surface instead of going through Select.
		return restoreRootSurface(sess, res, state)
	}
	if state.Registry == nil {
		return fmt.Errorf("no agents loaded")
	}
	selected, err := agents.Select(state.Registry, name)
	if err != nil {
		return err
	}
	if err := ensureSelectedMCPTools(sess, state, selected); err != nil {
		return fmt.Errorf("MCP tools: %w", err)
	}
	if !state.BaselineCaptured {
		state.BaselinePrompt, state.BaselineMaxSteps = sess.AgentSettings()
		state.BaselineCaptured = true
	}
	prompt, maxSteps := selectedAgentSettings(&selected, state)
	var candidate *agentSurface
	if sess.Tools != nil && state.ToolBase != nil {
		candidate, err = buildAgentScopedSurface(sess, res, state, &selected)
		if err != nil {
			return err
		}
		WarnDisabledAgentTools(&selected, DisabledForAgent(&selected, entryBase(sess, state)))
		warnAdvertisedToolsTruncated(&selected, candidate.advertisedDropped)
	}
	// Commit selection and every session-owned surface only after all candidate
	// construction and validation has succeeded.
	sel := selected
	state.Selected = &sel
	if candidate != nil {
		candidate.commitTo(state)
	}
	if candidate == nil {
		// See restoreRootSurface's matching comment: res is the pool's
		// SHARED launch config, never this session's private prompt.
		sess.SetAgentSettings(prompt, maxSteps, CoreMemoryBlockForState(state))
		return nil
	}
	prompt = promptWithDeferredIndex(prompt, state.TierPlan)
	commitAgentSwitchSurface(sess, res, state, candidate, sel.Name, prompt, maxSteps)
	return nil
}

// commitAgentSwitchSurface publishes a successfully built agent-switch
// candidate and wires its admission state. A new binding starts from its own
// core tier: admissions never carry across an /agent switch (plan tools/05
// D4).
func commitAgentSwitchSurface(sess *chat.Session, res *config.Resolved, state *AgentSessionState, candidate *agentSurface, agentName, prompt string, maxSteps int) {
	sess.ResetAdmissions()
	sess.PublishAgentSurface(prompt, maxSteps, candidate.registry, candidate.dispatcher, candidate.skillReg, CoreMemoryBlockForState(state), candidate.advertised)
	sess.SetRemainderSpool(RemainderSpoolFromRegistryVar(candidate.registry))
	recordSchemaMassLocked(sess, state, candidate.plan, nil, agentName, "agent_switch")
	if state.TierPlan.Deferred() {
		sess.SetSurfaceWidener(NewSurfaceWidener(sess, res, state))
		sess.SetAdmissionBinding(agentName, state.TierPlan.Digest)
	} else {
		sess.SetSurfaceWidener(nil)
		sess.SetAdmissionBinding("", "")
	}
}

func selectedAgentSettings(selected *agents.ResolvedAgent, state *AgentSessionState) (string, int) {
	if selected != nil && strings.TrimSpace(selected.SystemPrompt) != "" {
		return selected.SystemPrompt, selectedMaxTurns(selected, state.BaselineMaxSteps)
	}
	if selected != nil && selected.MaxTurns != nil {
		return state.BaselinePrompt, *selected.MaxTurns
	}
	return state.BaselinePrompt, state.BaselineMaxSteps
}

func selectedMaxTurns(selected *agents.ResolvedAgent, baseline int) int {
	if selected != nil && selected.MaxTurns != nil {
		return *selected.MaxTurns
	}
	return baseline
}
