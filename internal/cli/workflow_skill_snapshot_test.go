package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestWorkflowSkillSnapshotRejectsChangedSkillOnResume(t *testing.T) {
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{{Skill: "workflow-safe"}}}
	initial := skills.NewRegistry()
	if err := initial.Register(skills.Definition{Name: "workflow-safe", Instructions: "admitted instruction", Tools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: workflowledger.SnapshotSchemaVersion, DefinitionTOML: []byte("workflow"), DefinitionDigest: "digest"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = pinWorkflowSkills(raw, wf, initial)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWorkflowSkillSnapshot(wf, initial, &prior); err != nil {
		t.Fatalf("admitted skill rejected: %v", err)
	}
	changed := skills.NewRegistry()
	if err := changed.Register(skills.Definition{Name: "workflow-safe", Instructions: "changed instruction", Tools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	err = verifyWorkflowSkillSnapshot(wf, changed, &prior)
	if err == nil || !strings.Contains(err.Error(), "changed since admission") {
		t.Fatalf("changed skill error = %v", err)
	}
}

func TestWorkflowSkillBytesWrapsResourceSnapshotError(t *testing.T) {
	def := skills.Definition{
		Name:      "bad-resource",
		Resources: []skills.ResourceDescriptor{{ID: "missing", Summary: "missing resource file"}},
	}
	_, err := workflowSkillBytes(def)
	if err == nil {
		t.Fatal("expected error for skill with unavailable resources")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bad-resource") {
		t.Fatalf("error %q does not contain skill name", msg)
	}
	if !strings.Contains(msg, "restore the skill resource files") {
		t.Fatalf("error %q does not contain recovery guidance", msg)
	}
}

func TestWorkflowSkillSnapshotRejectsChangedPanelMemberSkillOnResume(t *testing.T) {
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{{
		ID: "review", Kind: "agent_panel", Panel: &definition.AgentPanel{Members: []definition.PanelMember{{ID: "security", Skill: "review"}}},
	}}}
	initial := skills.NewRegistry()
	if err := initial.Register(skills.Definition{Name: "review", Instructions: "admitted instruction"}); err != nil {
		t.Fatal(err)
	}
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: workflowledger.SnapshotSchemaVersion, DefinitionTOML: []byte("workflow"), DefinitionDigest: "digest",
		PanelBindings: map[string]workflowledger.PanelBindingSnapshot{"review/security": {StepID: "review", MemberID: "security"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = pinWorkflowSkills(raw, wf, initial)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWorkflowSkillSnapshot(wf, initial, &prior); err != nil {
		t.Fatalf("verify admitted panel skill: %v", err)
	}
	changed := skills.NewRegistry()
	if err := changed.Register(skills.Definition{Name: "review", Instructions: "changed instruction"}); err != nil {
		t.Fatal(err)
	}
	if err := verifyWorkflowSkillSnapshot(wf, changed, &prior); err == nil {
		t.Fatal("verifyWorkflowSkillSnapshot accepted a changed panel skill")
	}
}

func TestWorkflowSkillSnapshotRejectsChangedResourceOnResume(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workflow-safe")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":             "---\nname: workflow-safe\n---\nadmitted instruction",
		"resources.toml":       "format = 1\n[[resources]]\nid = \"checklist\"\npath = \"private-checklist.md\"\nsummary = \"Run checks\"\n",
		"private-checklist.md": "original resource body",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{{Skill: "workflow-safe"}}}
	initial := loadWorkflowSnapshotSkills(t, root)
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: workflowledger.SnapshotSchemaVersion, DefinitionTOML: []byte("workflow"), DefinitionDigest: "digest"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = pinWorkflowSkills(raw, wf, initial)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-checklist.md") {
		t.Fatal("snapshot exposes the resource path")
	}
	prior, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWorkflowSkillSnapshot(wf, initial, &prior); err != nil {
		t.Fatalf("admitted resource rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private-checklist.md"), []byte("changed resource body"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := loadWorkflowSnapshotSkills(t, root)
	err = verifyWorkflowSkillSnapshot(wf, changed, &prior)
	if err == nil || !strings.Contains(err.Error(), "changed since admission") {
		t.Fatalf("changed resource error = %v", err)
	}
}

// R1: dispatch under a workflow pin executes the PINNED bytes. A resource or
// instruction edited on disk after admission is never executed: the
// activation serves the admitted snapshots from memory.
func TestWorkflowSkillDispatchServesPinnedBytes(t *testing.T) {
	root := t.TempDir()
	skillReg := resourceSkillRegistryAt(t, root)
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{{Skill: "review"}}}
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: workflowledger.SnapshotSchemaVersion, DefinitionTOML: []byte("workflow"), DefinitionDigest: "digest"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = pinWorkflowSkills(raw, wf, skillReg)
	if err != nil {
		t.Fatal(err)
	}
	handler := newSkillInjectionHandler(t, skillReg)
	handler.opts.WorkflowSkillSnapshots = make(map[string]workflowledger.RefSnapshot)
	if err := installWorkflowSkillSnapshots(handler.opts.WorkflowSkillSnapshots, raw); err != nil {
		t.Fatal(err)
	}
	// Edit the on-disk resource and instructions AFTER admission.
	if err := os.WriteFile(filepath.Join(root, "review", "template.md"), []byte("CHANGED"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "review", "SKILL.md"), []byte("---\nname: review\n---\nChanged instructions.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt, _, registry, closeActivation, err := handler.prepareInvokeSurface(runtime.Request{Skill: "review"})
	defer closeActivation()
	if err != nil {
		t.Fatalf("prepareInvokeSurface() error = %v", err)
	}
	if !strings.Contains(prompt, "Review the change.") {
		t.Fatalf("prompt must carry the ADMITTED instructions, got: %q", prompt)
	}
	if strings.Contains(prompt, "Changed instructions") {
		t.Fatalf("prompt must not carry live post-admission edits, got: %q", prompt)
	}
	if !strings.Contains(prompt, "Report template") {
		t.Fatalf("prompt must render the pinned resource catalogue summary, got: %q", prompt)
	}
	tool, ok := registry.Get(tools.SkillResourceToolName)
	if !ok {
		t.Fatal("skill resource tool is missing from the activated registry")
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"template"}`))
	if err != nil {
		t.Fatalf("resource read: %v", err)
	}
	if !strings.Contains(out, "TEMPLATE") {
		t.Fatalf("resource tool must serve the pinned bytes, got: %q", out)
	}
	if strings.Contains(out, "CHANGED") {
		t.Fatalf("resource tool must not serve live post-admission bytes, got: %q", out)
	}
}

// R1: a tampered or corrupt in-memory pin fails closed at dispatch and routes
// the operator at the recovery options.
func TestWorkflowSkillDispatchRejectsTamperedPin(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original instructions"})
	handler := newSkillInjectionHandler(t, reg)
	handler.opts.WorkflowSkillSnapshots = map[string]workflowledger.RefSnapshot{
		"audit": {Digest: "sha256:0000", Bytes: []byte("{}")},
	}
	_, _, _, closeActivation, err := handler.prepareInvokeSurface(runtime.Request{Skill: "audit"})
	defer closeActivation()
	if err == nil || !strings.Contains(err.Error(), "--accept-skill-change") {
		t.Fatalf("tampered pin error must route at recovery options, got: %v", err)
	}
}

// F10: dispatch-time workflow skill errors route the operator at recovery
// options instead of returning a bare message.
func TestWorkflowSkillDispatchRejectsNotAdmitted(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original instructions"})
	handler := newSkillInjectionHandler(t, reg)
	handler.opts.WorkflowSkillSnapshots = map[string]workflowledger.RefSnapshot{}

	_, _, _, closeActivation, err := handler.prepareInvokeSurface(runtime.Request{Skill: "audit"})
	defer closeActivation()
	if err == nil || !strings.Contains(err.Error(), "is not admitted") || !strings.Contains(err.Error(), "--accept-skill-change") {
		t.Fatalf("not admitted error must route at recovery options, got: %v", err)
	}
}

func TestWorkflowSkillDispatchRejectsNotDeclared(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"other": "other instructions"})
	handler := newSkillInjectionHandler(t, reg)
	handler.opts.WorkflowSkillSnapshots = map[string]workflowledger.RefSnapshot{
		"audit": {Digest: "sha256:0000", Bytes: []byte("{}")},
	}

	_, _, _, closeActivation, err := handler.prepareInvokeSurface(runtime.Request{Skill: "audit"})
	defer closeActivation()
	if err == nil || !strings.Contains(err.Error(), "is not declared") || !strings.Contains(err.Error(), "--accept-skill-change") {
		t.Fatalf("not declared error must route at recovery options, got: %v", err)
	}
}

func TestWorkflowSkillDispatchRejectsUnauthorizedSkill(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original instructions"})
	handler := newSkillInjectionHandler(t, reg)
	handler.opts.WorkflowSkillSnapshots = map[string]workflowledger.RefSnapshot{
		"audit": {Digest: "sha256:0000", Bytes: []byte("{}")},
	}
	handler.opts.SkillReg = nil

	_, _, _, closeActivation, err := handler.prepareInvokeSurface(runtime.Request{Skill: "audit"})
	defer closeActivation()
	if err == nil || !strings.Contains(err.Error(), "is not authorized") || !strings.Contains(err.Error(), "--accept-skill-change") {
		t.Fatalf("unauthorized skill error must route at recovery options, got: %v", err)
	}
}

// R2: --accept-skill-change re-pins the in-memory prior; when the dispatcher
// pins install from that accepted prior (not the durable record), dispatch
// executes the accepted bytes.
func TestAcceptSkillChangeReachesDispatch(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original instructions"})
	wf := skillTestWorkflow(map[string]string{"s1": "audit"})
	prior := skillPinnedSnapshot(t, wf, reg)

	changed := skillTestRegistry(map[string]string{"audit": "changed instructions"})
	if _, err := acceptWorkflowSkillChanges(prior, wf, changed); err != nil {
		t.Fatalf("accept: %v", err)
	}
	// The resume path marshals the ACCEPTED in-memory prior for dispatch-pin
	// installation; the durable record (pre-acceptance bytes) stays untouched.
	raw, err := workflowledger.MarshalSnapshot(*prior)
	if err != nil {
		t.Fatal(err)
	}
	handler := newSkillInjectionHandler(t, changed)
	handler.opts.WorkflowSkillSnapshots = make(map[string]workflowledger.RefSnapshot)
	if err := installWorkflowSkillSnapshots(handler.opts.WorkflowSkillSnapshots, raw); err != nil {
		t.Fatal(err)
	}
	prompt, _, _, closeActivation, err := handler.prepareInvokeSurface(runtime.Request{Skill: "audit"})
	defer closeActivation()
	if err != nil {
		t.Fatalf("dispatch after acceptance must succeed: %v", err)
	}
	if !strings.Contains(prompt, "changed instructions") {
		t.Fatalf("dispatch must execute the accepted bytes, got: %q", prompt)
	}
}

// R3: the remaining-steps derivation starts at the PlanResume-derived active
// step and follows declared transitions and on_failure routes, so a completed
// step outside the reachable set does not block resume, while a looped-back
// step stays in scope.
func TestWorkflowRemainingSteps(t *testing.T) {
	ctx := context.Background()
	wf := &compiler.CompiledWorkflow{
		Steps: []definition.Step{
			{ID: "s1", Skill: "audit"},
			{ID: "s2", Skill: "audit"},
			{ID: "s3", Skill: "audit", OnFailure: "s2"},
		},
		Transitions: []definition.Transition{
			{From: "s1", To: "s2"},
			{From: "s2", To: "s3"},
			{From: "s3", To: "success"},
		},
		StepIDs: map[string]bool{"s1": true, "s2": true, "s3": true},
	}
	newRun := func(t *testing.T, runID, active string) workflowledger.Repository {
		t.Helper()
		repo := workflowledger.NewMemoryRepository()
		t.Cleanup(func() { _ = repo.Close() })
		run := workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending, ActiveStepID: active}
		if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
			t.Fatal(err)
		}
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
			t.Fatal(err)
		}
		return repo
	}

	// Active s2: s1 completed and is unreachable; s2 and s3 remain.
	repo := newRun(t, "wfr-remaining-mid", "s2")
	remaining, err := workflowRemainingSteps(ctx, repo, "wfr-remaining-mid", wf)
	if err != nil {
		t.Fatal(err)
	}
	if remaining["s1"] || !remaining["s2"] || !remaining["s3"] {
		t.Fatalf("remaining = %v, want s2+s3 only", remaining)
	}

	// Active s3 with an on_failure route back to s2: both remain in scope.
	repo = newRun(t, "wfr-remaining-loop", "s3")
	remaining, err = workflowRemainingSteps(ctx, repo, "wfr-remaining-loop", wf)
	if err != nil {
		t.Fatal(err)
	}
	if remaining["s1"] || !remaining["s2"] || !remaining["s3"] {
		t.Fatalf("remaining = %v, want s2+s3 (loop back-edge)", remaining)
	}

	// An active step unknown to the graph falls back to nil: check all steps.
	repo = newRun(t, "wfr-remaining-unknown", "ghost")
	remaining, err = workflowRemainingSteps(ctx, repo, "wfr-remaining-unknown", wf)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != nil {
		t.Fatalf("remaining = %v, want nil (check-all fallback)", remaining)
	}
}

func loadWorkflowSnapshotSkills(t *testing.T, root string) *skills.Registry {
	t.Helper()
	registry, warnings, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	return registry
}

// --- R2: --accept-skill-change ---
// --- R3: scope guard to remaining steps ---
// --- R4: recovery-routed error messages ---

func skillTestDefinition(name, instructions string) skills.Definition {
	return skills.Definition{Name: name, Instructions: instructions, Tools: []string{"read_file"}}
}

func skillTestRegistry(defs map[string]string) *skills.Registry {
	reg := skills.NewRegistry()
	for name, instructions := range defs {
		_ = reg.Register(skillTestDefinition(name, instructions))
	}
	return reg
}

func skillTestWorkflow(stepSkills map[string]string) *compiler.CompiledWorkflow {
	ids := make([]string, 0, len(stepSkills))
	for id := range stepSkills {
		ids = append(ids, id)
	}
	steps := make([]definition.Step, 0, len(stepSkills))
	for _, id := range ids {
		steps = append(steps, definition.Step{ID: id, Kind: "agent", Skill: stepSkills[id], Agent: "test"})
	}
	return &compiler.CompiledWorkflow{Steps: steps}
}

func skillPinnedSnapshot(t *testing.T, wf *compiler.CompiledWorkflow, reg *skills.Registry) *workflowledger.Snapshot {
	t.Helper()
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: workflowledger.SnapshotSchemaVersion, DefinitionTOML: []byte("workflow"), DefinitionDigest: "digest"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = pinWorkflowSkills(raw, wf, reg)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &snapshot
}

func TestSkillPinAndResumeVerification(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original instructions"})
	wf := skillTestWorkflow(map[string]string{"s1": "audit"})
	prior := skillPinnedSnapshot(t, wf, reg)

	if err := verifyWorkflowSkillSnapshot(wf, reg, prior); err != nil {
		t.Fatalf("unchanged skill must verify: %v", err)
	}

	drifted := skillTestRegistry(map[string]string{"audit": "changed instructions"})
	if err := verifyWorkflowSkillSnapshot(wf, drifted, prior); err == nil {
		t.Fatal("drifted skill must fail closed")
	}

	empty := skillTestRegistry(map[string]string{})
	if err := verifyWorkflowSkillSnapshot(wf, empty, prior); err == nil || !strings.Contains(err.Error(), "missing on resume") {
		t.Fatalf("deleted skill must fail closed, got %v", err)
	}
}

// R3: When remaining steps are provided, only skills referenced by those steps
// are checked. A skill that drifted but is only used by completed steps must
// not block resume.
func TestVerifyWorkflowSkillSnapshotScopesToRemainingSteps(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original instructions"})
	wf := skillTestWorkflow(map[string]string{"s1": "audit", "s2": "audit"})
	prior := skillPinnedSnapshot(t, wf, reg)

	drifted := skillTestRegistry(map[string]string{"audit": "changed instructions"})

	// nil remaining steps: guard checks ALL steps (no scoping info available).
	// This is the safe default used by the build-phase call before PlanResume.
	if err := verifyWorkflowSkillSnapshotScoped(wf, drifted, prior, nil); err == nil {
		t.Fatal("nil remaining must check all steps")
	}

	// Empty remaining: no steps left to run, guard passes.
	if err := verifyWorkflowSkillSnapshotScoped(wf, drifted, prior, map[string]bool{}); err != nil {
		t.Fatalf("empty remaining steps must pass: %v", err)
	}

	// s1 remains: s1 references "audit", so drift is caught.
	if err := verifyWorkflowSkillSnapshotScoped(wf, drifted, prior, map[string]bool{"s1": true}); err == nil {
		t.Fatal("remaining step using drifted skill must fail")
	}

	// s2 remains: same skill, same failure.
	if err := verifyWorkflowSkillSnapshotScoped(wf, drifted, prior, map[string]bool{"s2": true}); err == nil {
		t.Fatal("remaining step using drifted skill must fail")
	}

	// Original registry with remaining steps: must pass.
	if err := verifyWorkflowSkillSnapshotScoped(wf, reg, prior, map[string]bool{"s1": true}); err != nil {
		t.Fatalf("unchanged skill must verify with remaining steps: %v", err)
	}
}

// R4: When the scoped guard fires, the error message names the skill, the
// remaining steps that use it, and recovery options.
func TestSkillSnapshotScopedErrorIncludesRecoveryGuidance(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original instructions", "other-skill": "other instructions"})
	wf := skillTestWorkflow(map[string]string{"s1": "audit", "s2": "audit", "s3": "other-skill"})
	prior := skillPinnedSnapshot(t, wf, reg)

	drifted := skillTestRegistry(map[string]string{"audit": "changed instructions", "other-skill": "other instructions"})
	remaining := map[string]bool{"s1": true, "s2": true, "s3": true}

	err := verifyWorkflowSkillSnapshotScoped(wf, drifted, prior, remaining)
	if err == nil {
		t.Fatal("drifted skill must fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"audit"`) {
		t.Fatalf("error must name the drifted skill, got: %s", msg)
	}
	if !strings.Contains(msg, "s1") && !strings.Contains(msg, "s2") {
		t.Fatalf("error must name remaining steps using the skill, got: %s", msg)
	}
	if !strings.Contains(msg, "--accept-skill-change") {
		t.Fatalf("error must include --accept-skill-change option, got: %s", msg)
	}
	if !strings.Contains(msg, "restore") {
		t.Fatalf("error must include restore option, got: %s", msg)
	}
}

// R2: acceptWorkflowSkillChanges re-pins drifted skills so verification passes.
func TestAcceptWorkflowSkillChanges(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original instructions"})
	wf := skillTestWorkflow(map[string]string{"s1": "audit"})
	prior := skillPinnedSnapshot(t, wf, reg)

	// Unchanged: acceptance is a no-op.
	drifted, err := acceptWorkflowSkillChanges(prior, wf, reg)
	if err != nil || len(drifted) != 0 {
		t.Fatalf("no drift expected, got %v, %v", drifted, err)
	}

	// Drifted skill: acceptance rewrites the pin.
	changed := skillTestRegistry(map[string]string{"audit": "changed instructions"})
	if err := verifyWorkflowSkillSnapshot(wf, changed, prior); err == nil {
		t.Fatal("drift must fail closed before acceptance")
	}
	drifted, err = acceptWorkflowSkillChanges(prior, wf, changed)
	if err != nil || len(drifted) != 1 || drifted[0] != "audit" {
		t.Fatalf("accept = %v, %v", drifted, err)
	}
	if err := verifyWorkflowSkillSnapshot(wf, changed, prior); err != nil {
		t.Fatalf("verification must pass after acceptance: %v", err)
	}

	// A deleted skill cannot be accepted.
	if _, err := acceptWorkflowSkillChanges(prior, wf, skillTestRegistry(map[string]string{})); err == nil {
		t.Fatal("acceptance must refuse a missing skill")
	}
}

// R2: applyAcceptedSkillChanges reports drifted skills to stderr.
func TestApplyAcceptedSkillChangesReportsDrift(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original instructions"})
	wf := skillTestWorkflow(map[string]string{"s1": "audit"})
	prior := skillPinnedSnapshot(t, wf, reg)

	changed := skillTestRegistry(map[string]string{"audit": "changed instructions"})
	if err := verifyWorkflowSkillSnapshot(wf, changed, prior); err == nil {
		t.Fatal("must fail before acceptance")
	}

	var stderr strings.Builder
	if err := applyAcceptedSkillChanges(prior, wf, changed, &stderr); err != nil {
		t.Fatalf("apply must succeed: %v", err)
	}
	if !strings.Contains(stderr.String(), "audit") {
		t.Fatalf("stderr must name the accepted skill, got: %q", stderr.String())
	}

	if err := verifyWorkflowSkillSnapshot(wf, changed, prior); err != nil {
		t.Fatalf("must verify after acceptance: %v", err)
	}
}

// Integration: scoped guard with realistic step completion pattern.
// Two steps s1 (completed) and s2 (remaining) both use the same skill.
// Drift only affects s1 (completed) → resume must succeed.
// Drift affects s2 (remaining) → resume must fail with guidance.
func TestSkillSnapshotScopedWithCompletedAndRemainingSteps(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original instructions"})
	wf := skillTestWorkflow(map[string]string{"s1": "audit", "s2": "audit"})
	prior := skillPinnedSnapshot(t, wf, reg)

	drifted := skillTestRegistry(map[string]string{"audit": "changed instructions"})

	// s1 is completed, s2 is remaining. The skill drifted but only s2 uses it.
	// Since s2 is remaining, the guard should catch the drift.
	remaining := map[string]bool{"s2": true}
	if err := verifyWorkflowSkillSnapshotScoped(wf, drifted, prior, remaining); err == nil {
		t.Fatal("drifted skill used by remaining step must fail")
	}

	// Both steps completed: no remaining steps use the skill, guard passes.
	remaining = map[string]bool{}
	if err := verifyWorkflowSkillSnapshotScoped(wf, drifted, prior, remaining); err != nil {
		t.Fatalf("no remaining steps using skill must pass: %v", err)
	}

	// Only s1 remaining (s2 completed): skill used by remaining step, must fail.
	remaining = map[string]bool{"s1": true}
	if err := verifyWorkflowSkillSnapshotScoped(wf, drifted, prior, remaining); err == nil {
		t.Fatal("drifted skill used by remaining s1 must fail")
	}
}

// Integration: multi-skill workflow where only one skill drifts.
// The error must name the drifted skill, not the stable one.
func TestSkillSnapshotScopedMultiSkillOnlyNamesDrifted(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original", "review": "original"})
	wf := skillTestWorkflow(map[string]string{"s1": "audit", "s2": "review"})
	prior := skillPinnedSnapshot(t, wf, reg)

	drifted := skillTestRegistry(map[string]string{"audit": "changed", "review": "original"})
	remaining := map[string]bool{"s1": true, "s2": true}

	err := verifyWorkflowSkillSnapshotScoped(wf, drifted, prior, remaining)
	if err == nil {
		t.Fatal("drifted skill must fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"audit"`) {
		t.Fatalf("error must name drifted skill 'audit', got: %s", msg)
	}
	if strings.Contains(msg, `"review"`) {
		t.Fatalf("error must not name stable skill 'review', got: %s", msg)
	}
}

// F8: acceptWorkflowSkillChanges must fail closed when a panel skill binding
// was stripped from the snapshot, instead of fabricating a zero-value binding.
func TestAcceptWorkflowSkillChangesRejectsMissingPanelBinding(t *testing.T) {
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{{
		ID: "review", Kind: "agent_panel", Panel: &definition.AgentPanel{Members: []definition.PanelMember{{ID: "security", Skill: "review"}}},
	}}}
	reg := skillTestRegistry(map[string]string{"review": "original instructions"})

	// Seed the panel binding so pinWorkflowSkills can build the admission snapshot.
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: workflowledger.SnapshotSchemaVersion, DefinitionTOML: []byte("workflow"), DefinitionDigest: "digest",
		PanelBindings: map[string]workflowledger.PanelBindingSnapshot{"review/security": {StepID: "review", MemberID: "security"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = pinWorkflowSkills(raw, wf, reg)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a stripped snapshot: the skill pin exists but the panel binding
	// entry was removed after admission.
	delete(prior.PanelBindings, "review/security")

	_, err = acceptWorkflowSkillChanges(&prior, wf, reg)
	if err == nil {
		t.Fatal("acceptWorkflowSkillChanges accepted a missing panel binding")
	}
	if !strings.Contains(err.Error(), "panel binding \"review/security\" is missing") {
		t.Fatalf("error must report missing panel binding, got: %v", err)
	}
}

// Integration: acceptance followed by scoped verification passes.
// Simulates the full resume flow: accept → build → verify.
func TestAcceptThenScopedVerifyPasses(t *testing.T) {
	reg := skillTestRegistry(map[string]string{"audit": "original"})
	wf := skillTestWorkflow(map[string]string{"s1": "audit"})
	prior := skillPinnedSnapshot(t, wf, reg)

	drifted := skillTestRegistry(map[string]string{"audit": "changed"})

	// Step 1: acceptance rewrites the pin.
	if _, err := acceptWorkflowSkillChanges(prior, wf, drifted); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// Step 2: scoped verification with remaining steps passes.
	remaining := map[string]bool{"s1": true}
	if err := verifyWorkflowSkillSnapshotScoped(wf, drifted, prior, remaining); err != nil {
		t.Fatalf("scoped verify after accept must pass: %v", err)
	}
}

// N1: A skill admitted before the ResourceSnapshot Summary field existed
// carries the legacy pin shape. Resume verification must accept either the
// current shape (with Summary) or the legacy shape (without Summary), so an
// upgrade does not report drift on every historical run.
func TestWorkflowSkillSnapshotAcceptsLegacyPreSummaryPin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "legacy-skill")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: legacy-skill\n---\nlegacy instructions",
		"resources.toml": "format = 1\n[[resources]]\nid = \"checklist\"\npath = \"checklist.md\"\nsummary = \"Run checks\"\n",
		"checklist.md":   "legacy resource body",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	reg := loadWorkflowSnapshotSkills(t, root)
	wf := skillTestWorkflow(map[string]string{"s1": "legacy-skill"})

	def, ok := reg.Get("legacy-skill")
	if !ok {
		t.Fatal("legacy-skill not found in registry")
	}
	current, legacy, err := workflowSkillBytesCurrentAndLegacy(def)
	if err != nil {
		t.Fatalf("workflowSkillBytesCurrentAndLegacy: %v", err)
	}
	if bytes.Equal(current, legacy) {
		t.Fatal("current and legacy bytes must differ when Summary is present")
	}

	// Build a snapshot pinned with the legacy (pre-Summary) shape.
	prior := &workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte("workflow"),
		DefinitionDigest: "digest",
		Skills: map[string]workflowledger.RefSnapshot{
			"legacy-skill": {Digest: digestBytes(legacy), Bytes: legacy},
		},
	}

	if err := verifyWorkflowSkillSnapshot(wf, reg, prior); err != nil {
		t.Fatalf("legacy pre-Summary pin must verify against current skill: %v", err)
	}

	// A genuinely changed skill must still fail closed.
	changed := loadWorkflowSnapshotSkills(t, root)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: legacy-skill\n---\nchanged instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed = loadWorkflowSnapshotSkills(t, root)
	if err := verifyWorkflowSkillSnapshot(wf, changed, prior); err == nil {
		t.Fatal("changed skill must still fail closed with a legacy pin")
	}
}
