package uiadapter

// Per-worktree tool-registry adoption for the session pool: canonical-root
// memoization, compute-then-adopt outside p.mu, resolver installation, and
// the launch-root/full-disk posture probes. Split from session_pool.go to
// keep it under the go-structure soft cap; the lock contract is unchanged -
// callers hold p.mu, and adoptWorktreeToolsLocked may briefly release it.

import (
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

// toolRootFor picks the root a new session's tools are scoped to. A bind
// that resolved a root wins over the caller's dir, because a worktree
// session may be SAVED in a subdirectory while its tools belong to the
// worktree root the binding already validated.
//
// The override is checked, not silent: it applies only when dir sits at or
// under boundRoot. That is exactly the containment StartInRoute enforces
// before it returns a root, so a mismatch means the two arguments describe
// different worktrees and the narrower dir is the fail-safe choice - this
// widens scope only along a path already validated, never blindly.
func toolRootFor(boundRoot, dir string) string {
	if boundRoot == "" {
		return dir
	}
	if dir == "" {
		return boundRoot
	}
	root, cleanDir := filepath.Clean(boundRoot), filepath.Clean(dir)
	if cleanDir == root || strings.HasPrefix(cleanDir, root+string(filepath.Separator)) {
		return root
	}
	return dir
}

// adoptWorktreeToolsLocked swaps sess.Tools to a registry rebuilt for the
// canonical worktree root wtDir, memoized per root. Empty dir, tools off,
// unresolvable roots, and the launch root itself all keep the inherited
// registry: the swap must never widen scope nor strand a session whose
// binding already validated. Construction happens OUTSIDE p.mu
// (compute-then-adopt): release, build serialized under buildSer, re-lock,
// then the memo-race winner publishes while losers close their own side
// resources and serialize through buildSer; a concurrent publish in our
// unlocked window is detected after re-locking, discarding duplicates so
// exactly one registry (and closer) exists per canonical root. Every skip
// keeps inherited tools rather than stranding the session mid-creation.
// Callers hold p.mu.
func (p *SessionPool) adoptWorktreeToolsLocked(sess *chat.Session, wtDir string) string {
	if wtDir == "" || p.res == nil || !p.toolsOn || sess == nil {
		return ""
	}
	canonical, cerr := worktreeroute.CanonicalDir(wtDir)
	if cerr != nil {
		return toolScopeNotResolved
	}
	if launch := p.launchRootLocked(); launch != "" && samePath(launch, canonical) {
		return ""
	}
	if reg, ok := p.regByRoot[canonical]; ok {
		p.adoptRegistry(sess, reg)
		return ""
	}

	fullDisk := p.authoritativeFullDiskLocked()

	// The session half of the workflow wiring travels to this root too: a
	// workflow started from a worktree session must publish progress on that
	// session's bus and register its child runs against the repository the
	// session's own inspect/cancel tools compare against.
	wiring := cliagents.WorkflowSessionWiring{Bus: p.sessionBusProviderLocked(), SessionRepo: p.agentState.LedgerRepoValue()}
	build := func(rootWorkspace, rootMemory string, fd bool, res *config.Resolved, w cliagents.WorkflowSessionWiring) (*tools.Registry, func(), error) {
		if cliagents.BuildToolsForRootHookForTest != nil {
			return cliagents.BuildToolsForRootHookForTest(rootWorkspace, rootMemory, fd, res)
		}
		return cliagents.BuildToolsForRoot(rootWorkspace, rootMemory, fd, res, w)
	}
	p.mu.Unlock()
	p.buildSer.Lock()
	reg, closeFn, buildErr := build(
		canonical, canonicalRepoRoot(canonical), fullDisk, p.res, wiring)
	p.buildSer.Unlock()
	p.mu.Lock()

	if buildErr != nil {
		// Keep inherited tools; the binding itself remains valid.
		return toolScopeRebuildFailedPrefix + ": " + buildErr.Error()
	}
	if p.regByRoot == nil {
		p.regByRoot = map[string]*tools.Registry{}
	}
	// buildSer serializes builders, but another goroutine can still have
	// published during our unlocked window; keep exactly one registry (and
	// one closer) per root.
	if existing, ok := p.regByRoot[canonical]; ok {
		closeFn() // discard our duplicate handles
		p.adoptRegistry(sess, existing)
		return ""
	}
	// Register this root's re-arm so a LATER Settings -> General toggle
	// reaches it too, not only the launch root - and re-sync it to
	// whatever the authoritative posture became while the build above ran
	// unlocked, closing the race an operator toggle mid-build would
	// otherwise leave this root on the wrong side of.
	if p.agentState != nil {
		p.agentState.SetFullDiskReArm(reg.SetWorkspaceUnrestricted)
	}
	p.regByRoot[canonical] = reg
	p.regCloses = append(p.regCloses, closeFn)
	p.adoptRegistry(sess, reg)
	return ""
}

// adoptRegistry installs reg onto sess together with the identity refresh
// ConfigureChatWorkspace performs after its own installs, and wires the
// session's ToolBaseResolver to a private Clone of reg: dispatcher-rebuild
// paths (entryBase in cliagents) then re-scope from THIS entry's root
// instead of the shared launch base. The clone is taken once per adoption;
// per-call cloning would let one session's deferred-tool registration
// grow the memoized registry (runDeferredToolNow registers into
// resolver()), while sharing the pointer would leak growth across
// same-root siblings. chat_repl.go installs the same stable-pointer
// pattern for the CLI attach path.
//
// ToolBaseResolver hands the deferred-tool path this PRE-scope base, which
// the operator's mandatory denylist has not been applied to - every other
// layer refuses a denied name, so ToolDenylist is what closes this one door
// (mirrors chat_repl.go's scopeAttachedToolSurface, which sets it alongside
// its own ToolBaseResolver for the same reason).
func (p *SessionPool) adoptRegistry(sess *chat.Session, reg *tools.Registry) {
	base := reg.Clone()
	sess.ToolBaseResolver = func() *tools.Registry { return base }
	if p.agentState != nil {
		sess.ToolDenylist = p.agentState.Global.MandatoryToolDenylistAdditions
	}
	sess.Tools = reg
	sess.RefreshPrefixIdentity()
}

// launchRootLocked returns the workspace root the pool's first member was
// opened against, derived once from that member's registry; empty means
// unknown and disables the skip rule. Callers hold p.mu.
func (p *SessionPool) launchRootLocked() string {
	if !p.launchRootDone {
		for _, existing := range p.sessions {
			if existing.Tools == nil {
				continue
			}
			p.launchRootVal = existing.Tools.WorkspaceRoot()
			p.launchRootDone = true
			break
		}
	}
	return p.launchRootVal
}

// preferredInheritanceSessionLocked returns the launch session when the pool
// also contains worktree-scoped sessions. Plain /new and /resume must inherit
// the launch registry; map iteration order is not stable.
func (p *SessionPool) preferredInheritanceSessionLocked() *chat.Session {
	if launch := p.launchRootLocked(); launch != "" {
		for _, sess := range p.sessions {
			if sess != nil && sess.Tools != nil && samePath(sess.Tools.WorkspaceRoot(), launch) {
				return sess
			}
		}
	}
	for _, sess := range p.sessions {
		if sess != nil && sess.Tools != nil {
			return sess
		}
	}
	return nil
}

// authoritativeFullDiskLocked reports the full-disk posture a newly built
// worktree registry should start with: the agentState's authoritative value
// when the pool has one (kept current by ApplyFullDisk's fan-out, and by
// each new root's own SetFullDiskReArm sync), or - for a pool built without
// one (some embeddings/tests wire tools directly) - the pool's own launch
// session, deterministically identified via preferredInheritanceSessionLocked,
// never an arbitrary "first" entry visited by iterating the unordered
// p.sessions map (bug-audit "new worktree posture depends on random map
// iteration" - the defect the removed anyMemberUnrestricted had). Callers
// hold p.mu.
func (p *SessionPool) authoritativeFullDiskLocked() bool {
	if p.agentState != nil {
		return p.agentState.FullDiskOn()
	}
	if launch := p.preferredInheritanceSessionLocked(); launch != nil && launch.Tools != nil {
		return launch.Tools.WorkspaceUnrestricted()
	}
	return false
}

// sessionBusProviderLocked returns the pool's event bus provider. Every
// pooled session inherits the launch session's bus (inheritEntryStateLocked),
// so one provider is unambiguous for the whole pool. Callers hold p.mu.
func (p *SessionPool) sessionBusProviderLocked() func() *events.Bus {
	sess := p.preferredInheritanceSessionLocked()
	if sess == nil {
		return nil
	}
	return func() *events.Bus { return sess.EventBus }
}

// samePath compares two paths after cleaning.
func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// Scope of the rebuilt registries: direct dispatch uses sess.Tools, and
// the dispatcher-rebuild paths - agent switch, model switch, MCP merge,
// admission replay - resolve their base through cliagents.entryBase,
// which prefers the ToolBaseResolver adoptRegistry installs above. Both
// surfaces therefore re-scope from THIS entry's root (pinned by
// TestApplySessionAgentUsesResolverBaseForRebuild). SCOPE LIMIT that
// remains: skills (state.SkillRegFull), the dispatcher's WorkspaceRoot
// option, and project memory stay launch-scoped by design - memory
// deliberately so, see canonicalRepoRoot below.

// canonicalRepoRoot anchors shared project state (memory) to the MAIN
// repository checkout: prompt injection reads AgentSessionState.Memory
// stashed at launch, so a per-worktree memory.db would split read/write
// targets - and a disposable worktree would lose memories on removal.
func canonicalRepoRoot(worktreeDir string) string {
	root, err := worktreeroute.Root(worktreeDir)
	if err == nil && root != "" {
		return root
	}
	// Fallback keeps the memory store local to the worktree when the vcs
	// probe cannot establish a main root (non-git or unreachable).
	return worktreeDir
}
