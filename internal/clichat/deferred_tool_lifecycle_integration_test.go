package clichat

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// --- D4/R2-5: stage and admission lifecycle -----------------------------

func TestAgentSwitchResetsAdmissions(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("precondition: admitted = %v", got)
	}
	writer := agents.ResolvedAgent{Name: "writer", SystemPrompt: "W", EffectiveTools: []string{"read_file", "grep"}}
	if err := fixture.state.Registry.Publish(writer); err != nil {
		t.Fatal(err)
	}
	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, "writer", false); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v, want empty after an /agent switch", got)
	}
}

func TestStagedAdmissionDiesWhenTheBindingIsReplaced(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	stage, ok := fixture.sess.PendingAdmission()
	if !ok {
		t.Fatal("no pending stage")
	}
	writer := agents.ResolvedAgent{Name: "writer", SystemPrompt: "W", EffectiveTools: []string{"read_file", "grep"}}
	if err := fixture.state.Registry.Publish(writer); err != nil {
		t.Fatal(err)
	}
	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, "writer", false); err != nil {
		t.Fatalf("switch: %v", err)
	}
	fixture.sess.PublishPendingAdmission()
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("a stage from generation %d published into a replaced binding: %v", stage.SurfaceGeneration, got)
	}
}

// TestBackgroundWorkDefersTheAdmission is R2-2: while an owner-registered
// switch guard refuses, publishing would close a dispatcher background
// orchestration still holds. The stage waits instead, and says so.
func TestBackgroundWorkDefersTheAdmission(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.sess.SetSwitchGuard(func() error { return fmt.Errorf("background run active") })

	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v while background work held the dispatcher", got)
	}
	if _, ok := fixture.sess.PendingAdmission(); !ok {
		t.Fatal("the stage must stay pending for the next qualifying boundary")
	}
	notes := fixture.sess.TakeAdmissionNotes()
	if len(notes) == 0 || !strings.Contains(notes[0], "grep") {
		t.Fatalf("notes = %v, want a bounded deferral note naming grep", notes)
	}

	// Once the guard clears, the next turn boundary publishes it.
	fixture.sess.SetSwitchGuard(nil)
	completer.mu.Lock()
	completer.turns = []provider.Response{{Content: "done"}}
	completer.calls = 0
	completer.mu.Unlock()
	if _, err := fixture.sess.SendUser(context.Background(), "again", io.Discard); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want [grep] at the next allowed boundary", got)
	}
}

func TestDeferralNotesAreBounded(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.sess.SetSwitchGuard(func() error { return fmt.Errorf("busy") })
	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	for i := 0; i < 10; i++ {
		fixture.sess.PublishPendingAdmission()
	}
	if notes := fixture.sess.TakeAdmissionNotes(); len(notes) > 3 {
		t.Fatalf("%d deferral notes queued; the note must be bounded", len(notes))
	}
}

// --- D3: persistence and resume replay ----------------------------------

func TestAdmittedToolsSurviveSaveAndLoad(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	bindDeferredFixtureContext(t, fixture.sess)
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if err := fixture.sess.Save("snap"); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Drop the live surface back to core, then resume.
	fixture.sess.ResetAdmissions()
	if err := fixture.sess.Load("snap"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted after resume = %v, want [grep]", got)
	}
	if !slices.Contains(registryToolNames(fixture.sess.Tools), "grep") {
		t.Fatalf("resumed surface does not advertise grep: %v", registryToolNames(fixture.sess.Tools))
	}
}

// TestResumeDropsAStaleAdmittedSetWithANote is F6: when the tier split has
// changed under a saved session, the admitted names are dropped fail-closed
// and the user is told which ones.
func TestResumeDropsAStaleAdmittedSetWithANote(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	bindDeferredFixtureContext(t, fixture.sess)
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if err := fixture.sess.Save("snap"); err != nil {
		t.Fatalf("save: %v", err)
	}
	_ = fixture.sess.TakeAdmissionNotes()
	// The operator re-tiers the agent: the digest no longer matches.
	fixture.sess.SetAdmissionBinding("reader", "a-different-digest")
	fixture.sess.ResetAdmissions()
	if err := fixture.sess.Load("snap"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v, want the stale set dropped fail-closed", got)
	}
	notes := fixture.sess.TakeAdmissionNotes()
	if len(notes) == 0 || !strings.Contains(notes[0], "grep") {
		t.Fatalf("notes = %v, want a note naming the dropped tools", notes)
	}
}

// --- telemetry (D5) -----------------------------------------------------

func TestSchemaMassReportsWhatTheDeferredTierWithholds(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	mass := fixture.state.SchemaMassSnapshot()
	if mass.Locked != 2 {
		t.Fatalf("locked count = %d, want 2", mass.Locked)
	}
	if mass.LockedTokens <= 0 {
		t.Fatalf("locked tokens = %d, want the locked schema mass to be measured", mass.LockedTokens)
	}
	if mass.Tokens <= 0 {
		t.Fatalf("advertised tokens = %d, want a positive measurement", mass.Tokens)
	}
	if !strings.Contains(mass.String(), "locked") {
		t.Fatalf("operator line omits the locked tier: %q", mass.String())
	}
}
