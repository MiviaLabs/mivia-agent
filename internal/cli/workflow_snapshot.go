package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// verifyWorkflowSkillSnapshot checks all workflow-referenced skills against
// the pinned snapshot. It is the unscoped default (nil remaining set); the
// resume build path calls verifyWorkflowSkillSnapshotScoped with the
// PlanResume-derived remaining steps so drift on a step that can never run
// again does not block the resume (R3).
func verifyWorkflowSkillSnapshot(wf *compiler.CompiledWorkflow, registry *skills.Registry, prior *workflowledger.Snapshot) error {
	return verifyWorkflowSkillSnapshotScoped(wf, registry, prior, nil)
}

// verifyWorkflowSkillSnapshotScoped checks workflow-referenced skills against
// the pinned snapshot, scoped to the given set of remaining step IDs. When
// remaining is nil, all steps are checked (backward-compatible default). When
// remaining is an empty map, no steps are checked (all completed). When a
// skill drift is detected the error names the skill, the remaining steps that
// reference it, and the operator's recovery options (R3+R4).
func verifyWorkflowSkillSnapshotScoped(wf *compiler.CompiledWorkflow, registry *skills.Registry, prior *workflowledger.Snapshot, remaining map[string]bool) error {
	if wf == nil || registry == nil {
		return fmt.Errorf("workflow skill registry is incomplete")
	}
	if prior == nil {
		return nil
	}
	// Build the step→skill map for remaining-step error messages.
	stepSkills := workflowSkillStepMap(wf)
	for _, ref := range workflowSkillReferences(wf) {
		if remaining != nil && !remaining[ref.stepID] {
			continue
		}
		definition, ok := registry.Get(ref.name)
		if !ok {
			return fmt.Errorf("workflow skill %q is missing on resume; restore the skill or use a fresh run", ref.name)
		}
		bytes, err := workflowSkillBytes(definition)
		if err != nil {
			return err
		}
		pinned, ok := prior.Skills[ref.name]
		if !ok || pinned.Digest != digestBytes(bytes) || string(pinned.Bytes) != string(bytes) {
			steps := remainingStepIDsUsingSkill(stepSkills, ref.name, remaining)
			return fmt.Errorf("workflow skill %q changed since admission (used by step(s): %s); recover with: --accept-skill-change, restore the skill content, or start a fresh run",
				ref.name, formatStepList(steps))
		}
		if ref.panelKey != "" {
			binding, ok := prior.PanelBindings[ref.panelKey]
			if !ok || binding.SkillDigest != digestBytes(bytes) {
				return fmt.Errorf("panel binding %q skill %q changed since admission; recover with: --accept-skill-change, restore the skill content, or start a fresh run", ref.panelKey, ref.name)
			}
		}
	}
	return nil
}

// acceptWorkflowSkillChanges re-pins drifted skills in the in-memory prior
// snapshot so verification passes. The durable admission record is never touched:
// acceptance is per-invocation, mirroring acceptWorkflowVerifierChanges.
func acceptWorkflowSkillChanges(prior *workflowledger.Snapshot, wf *compiler.CompiledWorkflow, registry *skills.Registry) ([]string, error) {
	if prior == nil {
		return nil, nil
	}
	var drifted []string
	for _, ref := range workflowSkillReferences(wf) {
		definition, ok := registry.Get(ref.name)
		if !ok {
			return nil, fmt.Errorf("cannot accept skill change: %q is not declared; restore the skill", ref.name)
		}
		bytes, err := workflowSkillBytes(definition)
		if err != nil {
			return nil, err
		}
		digest := digestBytes(bytes)
		// A skill referenced by several refs (a plain step and a panel member,
		// or two panel members) is pinned once by name; the pin rewrite appends
		// to drifted only when it actually changes. Every ref that carries a
		// panelKey still re-pins its binding regardless, so a stale SkillDigest
		// never survives acceptance.
		if current, ok := prior.Skills[ref.name]; !ok || string(current.Bytes) != string(bytes) {
			drifted = append(drifted, ref.name)
		}
		if prior.Skills == nil {
			prior.Skills = make(map[string]workflowledger.RefSnapshot)
		}
		prior.Skills[ref.name] = workflowledger.RefSnapshot{Digest: digest, Bytes: bytes}
		if ref.panelKey != "" {
			if prior.PanelBindings == nil {
				prior.PanelBindings = make(map[string]workflowledger.PanelBindingSnapshot)
			}
			binding, ok := prior.PanelBindings[ref.panelKey]
			if !ok {
				return nil, fmt.Errorf("panel binding %q is missing", ref.panelKey)
			}
			binding.SkillDigest = digest
			prior.PanelBindings[ref.panelKey] = binding
		}
	}
	return drifted, nil
}

// applyAcceptedSkillChanges runs the acceptance rewrite for a resume that
// passed --accept-skill-change and reports each accepted skill to stderr.
func applyAcceptedSkillChanges(prior *workflowledger.Snapshot, wf *compiler.CompiledWorkflow, registry *skills.Registry, stderr io.Writer) error {
	drifted, err := acceptWorkflowSkillChanges(prior, wf, registry)
	if err != nil {
		return err
	}
	for _, name := range drifted {
		fmt.Fprintf(stderr, "accepting changed skill %q for this resume; the admission pin is unchanged\n", name)
	}
	if len(drifted) == 0 {
		fmt.Fprintln(stderr, "no skill definitions drifted; --accept-skill-change had no effect")
	}
	return nil
}

// workflowRemainingSteps derives the set of steps a resume may still execute:
// the PlanResume-derived active step plus every step reachable from it through
// declared transitions (including partial targets) and on_failure routes. A
// step outside this set can never run again, so skill drift on it cannot
// affect the resumed run (R3). When the active step is unknown to the
// (synthesized) run graph the result is nil: the guard then checks all steps,
// the fail-closed default.
func workflowRemainingSteps(ctx context.Context, repo workflowledger.Repository, runID string, wf *compiler.CompiledWorkflow) (map[string]bool, error) {
	plan, err := workflowledger.PlanResume(ctx, repo, runID)
	if err != nil {
		return nil, err
	}
	active := plan.Run.ActiveStepID
	if wf == nil || active == "" || !wf.StepIDs[active] {
		return nil, nil
	}
	edges := make(map[string][]string, len(wf.Steps))
	for _, step := range wf.Steps {
		if step.OnFailure != "" {
			edges[step.ID] = append(edges[step.ID], step.OnFailure)
		}
	}
	for _, tr := range wf.Transitions {
		edges[tr.From] = append(edges[tr.From], tr.To)
		if tr.PartialTarget != "" {
			edges[tr.From] = append(edges[tr.From], tr.PartialTarget)
		}
	}
	remaining := map[string]bool{active: true}
	queue := []string{active}
	for len(queue) > 0 {
		step := queue[0]
		queue = queue[1:]
		for _, next := range edges[step] {
			if remaining[next] || !wf.StepIDs[next] {
				continue
			}
			remaining[next] = true
			queue = append(queue, next)
		}
	}
	return remaining, nil
}

// workflowSkillStepMap returns a map from skill name to step IDs that
// reference it.
func workflowSkillStepMap(wf *compiler.CompiledWorkflow) map[string][]string {
	m := make(map[string][]string)
	if wf == nil {
		return m
	}
	for _, step := range wf.Steps {
		if step.Skill != "" {
			m[step.Skill] = append(m[step.Skill], step.ID)
		}
		if step.Kind == "agent_panel" && step.Panel != nil {
			for _, member := range step.Panel.Members {
				if member.Skill != "" {
					m[member.Skill] = append(m[member.Skill], step.ID+"/"+member.ID)
				}
			}
		}
	}
	return m
}

// remainingStepIDsUsingSkill returns the step IDs from the remaining set that
// reference the given skill.
func remainingStepIDsUsingSkill(stepSkills map[string][]string, skillName string, remaining map[string]bool) []string {
	ids := stepSkills[skillName]
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if remaining == nil || remaining[id] {
			out = append(out, id)
		}
	}
	return out
}

// formatStepList formats a list of step IDs for error messages.
func formatStepList(ids []string) string {
	if len(ids) == 0 {
		return "(none)"
	}
	s := ids[0]
	for _, id := range ids[1:] {
		s += ", " + id
	}
	return s
}

func pinWorkflowSkills(raw []byte, wf *compiler.CompiledWorkflow, registry *skills.Registry) ([]byte, error) {
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return nil, err
	}
	if snapshot.Skills == nil {
		snapshot.Skills = make(map[string]workflowledger.RefSnapshot)
	}
	for _, ref := range workflowSkillReferences(wf) {
		definition, ok := registry.Get(ref.name)
		if !ok {
			return nil, fmt.Errorf("workflow skill %q is missing", ref.name)
		}
		bytes, err := workflowSkillBytes(definition)
		if err != nil {
			return nil, err
		}
		digest := digestBytes(bytes)
		snapshot.Skills[ref.name] = workflowledger.RefSnapshot{Digest: digest, Bytes: bytes}
		if ref.panelKey != "" {
			binding, ok := snapshot.PanelBindings[ref.panelKey]
			if !ok {
				return nil, fmt.Errorf("panel binding %q is missing", ref.panelKey)
			}
			binding.SkillDigest = digest
			snapshot.PanelBindings[ref.panelKey] = binding
		}
	}
	return workflowledger.MarshalSnapshot(snapshot)
}

type workflowSkillReference struct {
	name     string
	stepID   string
	panelKey string
}

func workflowSkillReferences(wf *compiler.CompiledWorkflow) []workflowSkillReference {
	if wf == nil {
		return nil
	}
	refs := make([]workflowSkillReference, 0)
	for _, step := range wf.Steps {
		if step.Skill != "" {
			refs = append(refs, workflowSkillReference{name: step.Skill, stepID: step.ID})
		}
		if step.Kind != "agent_panel" || step.Panel == nil {
			continue
		}
		for _, member := range step.Panel.Members {
			if member.Skill != "" {
				refs = append(refs, workflowSkillReference{name: member.Skill, stepID: step.ID, panelKey: step.ID + "/" + member.ID})
			}
		}
	}
	return refs
}

func installWorkflowSkillSnapshots(destination map[string]workflowledger.RefSnapshot, raw []byte) error {
	if destination == nil {
		return fmt.Errorf("workflow skill snapshot destination is nil")
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return err
	}
	return installWorkflowSkillSnapshotsFromSnapshot(destination, &snapshot)
}

// installWorkflowSkillSnapshotsFromSnapshot installs the skill pins the
// dispatcher enforces at dispatch time directly from a decoded snapshot (the
// in-memory prior on resume, which may have been re-pinned by
// --accept-skill-change). Installing from the struct avoids re-marshalling the
// whole snapshot, so round-trip fidelity of unrelated fields cannot skew the
// pins, and it guarantees the dispatcher sees exactly the pins verification
// accepted.
func installWorkflowSkillSnapshotsFromSnapshot(destination map[string]workflowledger.RefSnapshot, snapshot *workflowledger.Snapshot) error {
	if destination == nil {
		return fmt.Errorf("workflow skill snapshot destination is nil")
	}
	if snapshot == nil {
		return nil
	}
	for name, pinned := range snapshot.Skills {
		if pinned.Digest == "" || digestBytes(pinned.Bytes) != pinned.Digest {
			return fmt.Errorf("workflow skill %q snapshot is invalid", name)
		}
		destination[name] = workflowledger.RefSnapshot{Digest: pinned.Digest, Bytes: append([]byte(nil), pinned.Bytes...)}
	}
	return nil
}

func workflowSkillBytes(definition skills.Definition) ([]byte, error) {
	resources, err := definition.SnapshotResources(context.Background())
	if err != nil {
		return nil, fmt.Errorf("workflow skill %q resource snapshot failed; recover with: restore the skill resource files or start a fresh run: %w", definition.Name, err)
	}
	view := workflowSkillSnapshotView{
		Name: definition.Name, Version: definition.Version, Scope: definition.Scope, Permission: definition.Permission,
		Description: definition.Description, ShortDescription: definition.ShortDescription, ArgsHint: definition.ArgsHint,
		Instructions: definition.Instructions, UserInvocable: definition.UserInvocable, Triggers: definition.Triggers,
		Tools: definition.Tools, Resources: resources, Timeout: int64(definition.Timeout), Budget: definition.Budget,
		InputSchema: definition.InputSchema, OutputSchema: definition.OutputSchema,
	}
	return json.Marshal(view)
}

// workflowSkillSnapshotView is the pinned byte shape of one admitted skill.
// workflowSkillBytes marshals it at admission; hydrateWorkflowSkillSnapshot
// unmarshals it at dispatch so a workflow run executes the ADMITTED bytes,
// never whatever the skill source holds at dispatch time (R1).
type workflowSkillSnapshotView struct {
	Name, Version, Scope, Permission, Description, ShortDescription, ArgsHint, Instructions string
	UserInvocable                                                                           bool
	Triggers, Tools                                                                         []string
	Resources                                                                               []skills.ResourceSnapshot
	Timeout                                                                                 int64
	Budget                                                                                  int
	InputSchema, OutputSchema                                                               map[string]any
}

// hydrateWorkflowSkillSnapshot rebuilds the executable skill definition and
// its resource snapshots from the pinned admission bytes. The pin's digest is
// re-verified here so a tampered in-memory pin fails closed at dispatch. The
// hydrated definition carries no source location: resource activation must go
// through skills.ActivateSnapshot, which serves the pinned bytes from memory.
func hydrateWorkflowSkillSnapshot(name string, pinned workflowledger.RefSnapshot) (skills.Definition, []skills.ResourceSnapshot, error) {
	invalid := fmt.Errorf("workflow skill %q snapshot is invalid; recover with: --accept-skill-change (re-pins the current skill definitions) or start a fresh run", name)
	if pinned.Digest == "" || digestBytes(pinned.Bytes) != pinned.Digest {
		return skills.Definition{}, nil, invalid
	}
	var view workflowSkillSnapshotView
	if err := json.Unmarshal(pinned.Bytes, &view); err != nil || view.Name != name {
		return skills.Definition{}, nil, invalid
	}
	definition := skills.Definition{
		Name: view.Name, Version: view.Version, Scope: view.Scope, Permission: view.Permission,
		Description: view.Description, ShortDescription: view.ShortDescription, ArgsHint: view.ArgsHint,
		Instructions: view.Instructions, UserInvocable: view.UserInvocable, Triggers: view.Triggers,
		Tools: view.Tools, Timeout: time.Duration(view.Timeout), Budget: view.Budget,
		InputSchema: view.InputSchema, OutputSchema: view.OutputSchema,
	}
	for _, resource := range view.Resources {
		definition.Resources = append(definition.Resources, skills.ResourceDescriptor{ID: resource.ID, Summary: resource.Summary})
	}
	return definition, view.Resources, nil
}

const (
	workflowGoModBaselineRef = "go-mod-baseline"
	workflowGoSumBaselineRef = "go-sum-baseline"
)

// workflowModuleBaseline captures (fresh run) or restores (resume) the pinned
// Go module baseline. needBaseline comes from workflowNeedsGoBaseline: the
// declared go_module_baseline flag on a fresh run, the PINNED definitions on
// resume.
func workflowModuleBaseline(needBaseline bool, root string, prior *workflowledger.Snapshot) (*verifier.GoModuleBaseline, error) {
	if !needBaseline {
		return nil, nil
	}
	if prior == nil {
		return verifier.CaptureGoModuleBaseline(root)
	}
	goMod, ok := prior.Verifiers[workflowGoModBaselineRef]
	if !ok || digestBytes(goMod.Bytes) != goMod.Digest {
		return nil, fmt.Errorf("workflow module baseline is invalid")
	}
	goSum, ok := prior.Verifiers[workflowGoSumBaselineRef]
	if !ok || digestBytes(goSum.Bytes) != goSum.Digest {
		return nil, fmt.Errorf("workflow module checksum baseline is invalid")
	}
	return &verifier.GoModuleBaseline{GoMod: append([]byte(nil), goMod.Bytes...), GoSum: append([]byte(nil), goSum.Bytes...)}, nil
}

func pinWorkflowModuleBaseline(raw []byte, baseline *verifier.GoModuleBaseline) ([]byte, error) {
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return nil, err
	}
	if baseline == nil || len(baseline.GoMod) == 0 {
		return nil, fmt.Errorf("workflow verifier module baseline is empty")
	}
	if snapshot.Verifiers == nil {
		snapshot.Verifiers = make(map[string]workflowledger.RefSnapshot)
	}
	snapshot.Verifiers[workflowGoModBaselineRef] = workflowledger.RefSnapshot{Digest: digestBytes(baseline.GoMod), Bytes: append([]byte(nil), baseline.GoMod...)}
	snapshot.Verifiers[workflowGoSumBaselineRef] = workflowledger.RefSnapshot{Digest: digestBytes(baseline.GoSum), Bytes: append([]byte(nil), baseline.GoSum...)}
	return workflowledger.MarshalSnapshot(snapshot)
}
