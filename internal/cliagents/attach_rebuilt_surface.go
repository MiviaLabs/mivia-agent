package cliagents

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// AttachRebuiltSurface rebuilds and publishes a session's agent surface
// against its CURRENT tool base. It exists for hosts that swap sess.Tools to
// a freshly built registry after launch - the TUI session pool's worktree
// adoption (SessionPool.adoptWorktreeToolsLocked). Such a registry is built
// by BuildToolsForRoot/composition.BuildRegistry alone and therefore carries
// none of the dispatcher-owned session tools (dispatch_tasks, the messaging
// and ledger tools, load_tools): those are registered by the session
// dispatcher construction at launch onto the launch registry only. Without a
// surface publication the worktree session runs with no orchestration
// surface at all, and the only later path that would rebuild one (a model
// switch) is unreachable from a tool surface that lost load_tools.
//
// The rebuild reuses the exact admission-widening mechanism: the core tier of
// the frozen tier plan (recomputed from the base when the fork carries none),
// the same skill registry discipline, and TryPublishAgentSurface for the
// publication, so the session's prompt, memory block and fence handling are
// byte-identical to any other surface publication.
//
// It reports whether a surface was published, and how many tools the
// advertised union had to drop against tools.MaxAdvertisedTools. The caller
// owns reporting that count: this runs inside a live TUI, where writing to
// the terminal directly would corrupt the frame. A nil session or state, a
// session without a completer, or a process that never wired the dispatcher
// seam is a silent no-op: hosts that predate agent state or dispatcher
// wiring keep their previous behavior.
func AttachRebuiltSurface(sess *chat.Session, res *config.Resolved, state *AgentSessionState) (bool, int, error) {
	if sess == nil || state == nil {
		return false, 0, nil
	}
	if sess.CurrentBinding().Completer == nil {
		return false, 0, nil
	}
	// Without the process-level seams there is no dispatcher to build and no
	// spool to install: fail soft exactly like the admission widener's other
	// consumers instead of publishing a half-wired surface.
	if NewSessionDispatcherVar == nil || RemainderSpoolFromRegistryVar == nil {
		return false, 0, nil
	}
	// A fork created before the launch surface captured its tier plan (tests,
	// embeddings) would build an empty core tier and brick the session to the
	// catalog alone. Recompute from the live base - the same computation the
	// launch attach ran - instead of publishing a degenerate plan.
	state.mu.Lock()
	if len(state.TierPlan.Tiers.Core) == 0 {
		if base := entryBase(sess, state); base != nil {
			state.TierPlan = PlanToolTiers(base, state.Selected, res)
		}
	}
	if state.SkillRegFull == nil {
		root := state.WorkspaceRoot
		if root == "" {
			root = "."
		}
		skillReg, warnings, err := LoadSessionSkills(root, state.AllowProjectSkills)
		if err != nil {
			state.mu.Unlock()
			return false, 0, fmt.Errorf("load skills: %w", err)
		}
		WarnSkillLoad(warnings)
		state.SkillRegFull = FilterSkillRegistryForGate(skillReg, state.AllowProjectSkills)
	}
	state.mu.Unlock()
	prompt, maxSteps := sess.AgentSettings()
	published, err := NewSurfaceWidener(sess, res, state)(nil, chat.AgentSurfacePublication{
		Prompt:   prompt,
		MaxSteps: maxSteps,
	})
	if err != nil {
		return false, 0, fmt.Errorf("attach rebuilt surface: %w", err)
	}
	dropped := 0
	if published {
		dropped = pinRebuiltAdvertisedToolSpecs(sess, state)
	}
	return published, dropped, nil
}

// pinRebuiltAdvertisedToolSpecs pins the session's advertised tools[] array
// after a rebuilt surface is published, the same thing the interactive
// launch attach does through PinAttachAdvertisedToolSpecs.
//
// The publication itself cannot do it: it runs through
// TryPublishAgentSurface, whose contract is that an ADMISSION changes
// execution authority only and never the wire array (plan
// tools-advertising/01), so buildWidenedWith deliberately computes no
// advertised union. An ATTACH is not an admission - it is the moment the
// binding's wire array is decided - so the pin belongs here.
//
// Without it a pooled session (the only caller, through
// SessionPool.wireEntryLocked) carried a nil snapshot, which is the shape
// the SDK's wholesale Surface.Advertised replace turns into an empty
// tools[]. The agent-loop bridge now restates the rotated registry instead
// of clearing it, so the nil snapshot is no longer fatal; pinning here is
// what makes the pooled session's array match the interactive one - the
// full admissible union with real descriptions and the session-tool tail,
// byte-stable for the whole binding rather than tracking the live registry.
//
// Computed from the PRE-scope base and the frozen tier plan, like every
// other advertised-union build: the scoped registry the publication
// installed covers core-plus-admitted execution authority only, so a union
// built from it would drop the deferred tier the model is meant to see.
// Returns the number of tools the union had to drop against
// tools.MaxAdvertisedTools. It does NOT print that count the way the launch
// attach's PinAttachAdvertisedToolSpecs does: the launch attach runs before
// the TUI owns the terminal, while this runs inside it - and on the
// deliberately silent background-spawn path - so a raw os.Stderr write here
// would land on top of a bubbletea frame. The caller routes it instead.
func pinRebuiltAdvertisedToolSpecs(sess *chat.Session, state *AgentSessionState) int {
	state.mu.Lock()
	base, plan, agentReg := entryBase(sess, state), state.TierPlan, state.Registry
	state.mu.Unlock()
	if base == nil {
		return 0
	}
	advertised, dropped := advertisedToolSpecs(base, plan, agentReg)
	sess.SetAdvertisedToolSpecs(advertised)
	// SetAdvertisedToolSpecs writes the snapshot and nothing else; the cached
	// prefix identity hashes it (toolSchemaDigest), so without this the cache
	// still describes the nil snapshot and the session's next identity
	// comparison reports a "tools" prefix reset for a wire array that never
	// changed (INV-68-2). The launch attach pairs the two the same way.
	sess.RefreshPrefixIdentity()
	return dropped
}
