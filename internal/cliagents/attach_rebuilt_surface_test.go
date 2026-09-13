package cliagents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestAttachRebuiltSurface_NoCompleterIsSilentNoop pins the no-completer
// guard, distinct from the nil-sess/nil-state guard above it: a session
// with no wired completer at all must not attempt a rebuild.
func TestAttachRebuiltSurface_NoCompleterIsSilentNoop(t *testing.T) {
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, nil)
	state := &AgentSessionState{}
	published, _, err := AttachRebuiltSurface(sess, res, state)
	if err != nil {
		t.Fatalf("AttachRebuiltSurface with no completer: %v", err)
	}
	if published {
		t.Fatal("AttachRebuiltSurface with no completer must not report a publication")
	}
}

// TestAttachRebuiltSurface_ReloadsSkillsWhenMissing pins the SkillRegFull-nil
// branch: a rebuild with no cached skill registry must load one (defaulting
// root to "." when WorkspaceRoot is empty) rather than publish with none.
func TestAttachRebuiltSurface_ReloadsSkillsWhenMissing(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file"})
	fixture.state.SkillRegFull = nil

	published, _, err := AttachRebuiltSurface(fixture.sess, fixture.res, fixture.state)
	if err != nil {
		t.Fatalf("AttachRebuiltSurface: %v", err)
	}
	if !published {
		t.Fatal("AttachRebuiltSurface did not report a publication")
	}
	if fixture.state.SkillRegFull == nil {
		t.Fatal("AttachRebuiltSurface left SkillRegFull nil after a rebuild")
	}
}

// TestAttachRebuiltSurface_DefaultsEmptyWorkspaceRootToDot pins the
// WorkspaceRoot == "" fallback: a rebuild with no cached skill registry
// AND no workspace root configured must still succeed, loading skills
// against "." rather than erroring or crashing on an empty root.
func TestAttachRebuiltSurface_DefaultsEmptyWorkspaceRootToDot(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file"})
	fixture.state.SkillRegFull = nil
	fixture.state.WorkspaceRoot = ""

	published, _, err := AttachRebuiltSurface(fixture.sess, fixture.res, fixture.state)
	if err != nil {
		t.Fatalf("AttachRebuiltSurface with an empty WorkspaceRoot: %v", err)
	}
	if !published {
		t.Fatal("AttachRebuiltSurface did not report a publication")
	}
	if fixture.state.SkillRegFull == nil {
		t.Fatal("AttachRebuiltSurface left SkillRegFull nil after a rebuild")
	}
}

// TestAttachRebuiltSurface_WidenerErrorSurfaces pins the final
// NewSurfaceWidener(...)(...) error wrap: a state with no available tool
// base (mirroring TestBuildWidenedWithRejectsUnavailableToolBase's own
// precondition) makes buildWidenedWith fail inside the widener closure.
func TestAttachRebuiltSurface_WidenerErrorSurfaces(t *testing.T) {
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	state := &AgentSessionState{
		SkillRegFull: skills.NewRegistry(),
		TierPlan:     ToolTierPlan{Tiers: tools.Tiers{Core: []string{"placeholder"}}},
	}
	published, _, err := AttachRebuiltSurface(sess, res, state)
	if err == nil {
		t.Fatal("AttachRebuiltSurface accepted a state with no available tool base")
	}
	if published {
		t.Fatal("AttachRebuiltSurface reported a publication despite the widener error")
	}
	if !strings.Contains(err.Error(), "attach rebuilt surface") {
		t.Fatalf("err = %v, want the attach-rebuilt-surface wrap", err)
	}
}

// TestAttachRebuiltSurface_PinsTheAdvertisedUnion pins that an attach - as
// opposed to a tool admission - decides the binding's wire tools[] array.
// A pooled session (SessionPool.wireEntryLocked, the only caller) used to
// keep a nil snapshot, because TryPublishAgentSurface never writes one by
// design. A nil snapshot is what the agent loop's surface rotation turns
// into an EMPTY tools[] on every step after the first, so the session ran
// its whole turn with no tools and no descriptions.
func TestAttachRebuiltSurface_PinsTheAdvertisedUnion(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})

	// A pooled session is built by the pool, not by the launch attach, so
	// it reaches wireEntryLocked with no pinned snapshot at all. Clear the
	// fixture's launch-attach pin to reproduce exactly that shape.
	fixture.sess.SetAdvertisedToolSpecs(nil)
	if specs := fixture.sess.AdvertisedToolSpecs(); specs != nil {
		t.Fatalf("clearing the pin left %d specs behind", len(specs))
	}
	published, _, err := AttachRebuiltSurface(fixture.sess, fixture.res, fixture.state)
	if err != nil {
		t.Fatalf("AttachRebuiltSurface: %v", err)
	}
	if !published {
		t.Fatal("AttachRebuiltSurface did not report a publication")
	}
	specs := fixture.sess.AdvertisedToolSpecs()
	if len(specs) == 0 {
		t.Fatal("AttachRebuiltSurface published a surface but pinned no advertised tools[]")
	}
	// The union is built from the pre-scope base, so the DEFERRED tier is
	// advertised too - a union built from the published core-tier registry
	// would silently drop it.
	names := make(map[string]bool, len(specs))
	for _, spec := range specs {
		fn, _ := spec["function"].(map[string]any)
		name, _ := fn["name"].(string)
		names[name] = true
	}
	for _, want := range []string{"read_file", "grep"} {
		if !names[want] {
			t.Fatalf("advertised union is missing %q; got %v", want, names)
		}
	}
}

// TestPinRebuiltAdvertisedToolSpecs_NoBaseIsANoop pins the unavailable-base
// guard. A publication cannot reach it (buildWidenedWith fails first with
// "tool base is unavailable"), but advertisedToolSpecs dereferences the base
// registry, so the guard is what keeps a state with no base from panicking
// rather than simply pinning nothing.
func TestPinRebuiltAdvertisedToolSpecs_NoBaseIsANoop(t *testing.T) {
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	state := &AgentSessionState{}

	pinRebuiltAdvertisedToolSpecs(sess, state)

	if specs := sess.AdvertisedToolSpecs(); specs != nil {
		t.Fatalf("pinned %d specs with no tool base available", len(specs))
	}
}

// TestAttachRebuiltSurface_PinRefreshesPrefixIdentity pins the pairing
// SetAdvertisedToolSpecs' own doc requires. The cached prefix identity
// hashes the advertised snapshot, so a pin that does not refresh it leaves
// the cache describing the nil snapshot: the session's next identity
// comparison then reports a "tools" prefix reset for a wire array that has
// been identical since request 0 (INV-68-2).
func TestAttachRebuiltSurface_PinRefreshesPrefixIdentity(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.sess.SetAdvertisedToolSpecs(nil)
	fixture.sess.RefreshPrefixIdentity()
	stale := fixture.sess.PrefixIdentity().ToolSchemaDigest

	published, _, err := AttachRebuiltSurface(fixture.sess, fixture.res, fixture.state)
	if err != nil {
		t.Fatalf("AttachRebuiltSurface: %v", err)
	}
	if !published {
		t.Fatal("AttachRebuiltSurface did not report a publication")
	}
	current := fixture.sess.PrefixIdentity().ToolSchemaDigest
	if current == stale {
		t.Fatal("the cached tool-schema digest still describes the pre-pin snapshot: the pin did not refresh the prefix identity")
	}
	// The cache is now current, so a second refresh is a no-op. This is what
	// distinguishes "refreshed" from "changed for some other reason".
	fixture.sess.RefreshPrefixIdentity()
	if again := fixture.sess.PrefixIdentity().ToolSchemaDigest; again != current {
		t.Fatalf("digest moved on a second refresh (%q -> %q): the cache was not current after the pin", current, again)
	}
}
