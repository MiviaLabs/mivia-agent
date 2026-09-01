package chat

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// liveSurface installs a registry advertising exactly the named tools.
func liveSurface(sess *Session, names ...string) {
	registry := tools.NewRegistry()
	for _, name := range names {
		registry.Register(fixedBodyTool{name: name})
	}
	sess.mu.Lock()
	sess.Tools = registry
	sess.mu.Unlock()
}

// TestLoadRefusesWhileATurnIsActive is the exclusion Session.Load was missing.
//
// Every other surface-mutating entry point refuses while a turn is running:
// /agent takes BeginSurfaceSwitch, /model refuses on activeTurns. Load took
// nothing, so its admission replay raced the turn's own publication: the replay
// clears the admitted set, decides from a snapshot taken outside the lock that
// guards the surface, and writes the stale decision back over whatever the turn
// published in between. The registry then advertises tools the session does not
// report - and the operator note names the wrong ones.
//
// The same missing exclusion destroys a live turn's pendingAdmission (a
// load_tools call answered "callable from your next turn" is silently voided)
// and bumps turnID under a turn boundary that already captured RequireTurnID,
// fencing out its publication. All three are asserted here.
func TestLoadRefusesWhileATurnIsActive(t *testing.T) {
	// A context-bound session with no binding factory: publishLoadedMessages
	// is the one publication path with no activeTurns check of its own,
	// which is exactly why the exclusion has to live in Load.
	sess, _ := contextCatalogSession(t)
	sess.mu.Lock()
	sess.bindingFactory = nil
	sess.mu.Unlock()
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)
	sess.SetAdmissionBinding("reader", "digest-1")
	// Saved with grep admitted; the live turn then widened to grep+glob.
	seedSavedAdmission(t, sess, "named", "grep")
	liveSurface(sess, "core_tool", "grep", "glob")
	stage := &AdmissionStage{Names: []string{"apply_patch"}}
	sess.mu.Lock()
	sess.admittedTools = []string{"grep", "glob"}
	sess.pendingAdmission = stage
	sess.activeTurns = 1
	turnID := sess.turnID
	sess.mu.Unlock()

	loadErr := sess.Load("named")
	if loadErr == nil {
		t.Fatal("Load mutated the session surface while a turn was active")
	}
	if !strings.Contains(loadErr.Error(), "while work is active") {
		t.Fatalf("Load error = %q, want the busy-session refusal", loadErr)
	}
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep", "glob"}) {
		t.Fatalf("admitted = %v, want [grep glob]: the turn's writeback was clobbered", got)
	}
	if !liveSurfaceHas(sess, "glob") {
		t.Fatal("the refused load narrowed the live surface underneath the turn")
	}
	sess.mu.RLock()
	pending, afterTurnID := sess.pendingAdmission, sess.turnID
	sess.mu.RUnlock()
	if pending != stage {
		t.Fatalf("pendingAdmission = %v, want the live turn's stage preserved", pending)
	}
	if afterTurnID != turnID {
		t.Fatalf("turnID advanced %d -> %d: the turn boundary's publication is now fenced out", turnID, afterTurnID)
	}
	if calls, _ := widener.snapshot(); len(calls) != 0 {
		t.Fatalf("the refused load still republished the surface: %v", calls)
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != 0 {
		t.Fatalf("a refused load announced %v; it changed nothing", notes)
	}
}

// TestLoadExcludesTurnsWhileItRepublishes proves the refusal is a real
// exclusion and not just an entry check: no turn may start for as long as the
// load is rebuilding the surface. The probe runs inside the widener, which the
// replay calls synchronously with the surface half-swapped.
func TestLoadExcludesTurnsWhileItRepublishes(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	sess.SetAdmissionBinding("reader", "digest-1")
	var probed bool
	var turnErr error
	sess.SetSurfaceWidener(func(admitted []string, req AgentSurfacePublication) (bool, error) {
		probed = true
		if _, done, err := sess.beginPlainTurn("probe"); err != nil {
			turnErr = err
		} else {
			done()
		}
		registry := tools.NewRegistry()
		registry.Register(fixedBodyTool{name: "core_tool"})
		for _, name := range admitted {
			registry.Register(fixedBodyTool{name: name})
		}
		req.Registry = registry
		return sess.TryPublishAgentSurface(req), nil
	})
	seedSavedAdmission(t, sess, "named", "grep")

	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if !probed {
		t.Fatal("the replay never rebuilt the surface; the probe proved nothing")
	}
	if turnErr == nil {
		t.Fatal("a turn started while the load was republishing the tool surface")
	}
	if !liveSurfaceHas(sess, "grep") {
		t.Fatal("the load did not restore the admitted tool")
	}
	// The exclusion must be released, not leaked.
	if _, done, err := sess.beginPlainTurn("after"); err != nil {
		t.Fatalf("a turn is still refused after the load finished: %v", err)
	} else {
		done()
	}
}

// TestLoadRestoresTheAdmittedSurfaceWhenIdle guards against the exclusion being
// implemented with a flag that also blocks the load's own republication.
func TestLoadRestoresTheAdmittedSurfaceWhenIdle(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)
	sess.SetAdmissionBinding("reader", "digest-1")
	seedSavedAdmission(t, sess, "named", "grep")
	liveSurface(sess, "core_tool")
	admitTools(sess)

	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want [grep] restored from the record", got)
	}
	if !liveSurfaceHas(sess, "grep") {
		t.Fatal("the resumed surface does not advertise the admitted tool")
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != 0 {
		t.Fatalf("a successful resume announced %v", notes)
	}
}

// TestLoadIsMutuallyExclusiveWithASurfaceSwitch: a load publishes a surface of
// its own, so it cannot overlap an /agent switch in either direction.
func TestLoadIsMutuallyExclusiveWithASurfaceSwitch(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})

	releaseLoad, err := sess.BeginSessionLoad()
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if _, err := sess.BeginSessionLoad(); err == nil {
		t.Fatal("a second concurrent load was admitted")
	}
	if _, err := sess.BeginSurfaceSwitch(); err == nil || !strings.Contains(err.Error(), "loading") {
		t.Fatalf("switch during a load = %v, want a refusal naming the load", err)
	}
	releaseLoad()

	releaseSwitch, err := sess.BeginSurfaceSwitch()
	if err != nil {
		t.Fatalf("switch after the load released: %v", err)
	}
	if _, err := sess.BeginSessionLoad(); err == nil || !strings.Contains(err.Error(), "surface is changing") {
		t.Fatalf("load during a switch = %v, want a refusal naming the switch", err)
	}
	releaseSwitch()
	if release, err := sess.BeginSessionLoad(); err != nil {
		t.Fatalf("load after the switch released: %v", err)
	} else {
		release()
	}
}

// TestAgentTurnRefusesWhileASessionIsLoading covers the agent-turn half of the
// exclusion; the plain-turn half is covered by TestLoadRefusesWhileATurnIsActive.
func TestAgentTurnRefusesWhileASessionIsLoading(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	release, err := sess.BeginSessionLoad()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := sess.SendUser(context.Background(), "question", io.Discard); err == nil ||
		!strings.Contains(err.Error(), "loading") {
		t.Fatalf("agent turn during a load = %v, want a refusal naming the load", err)
	}
}
