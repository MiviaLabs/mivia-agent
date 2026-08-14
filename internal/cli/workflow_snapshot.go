package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

func verifyWorkflowSkillSnapshot(wf *compiler.CompiledWorkflow, registry *skills.Registry, prior *workflowledger.Snapshot) error {
	if wf == nil || registry == nil {
		return fmt.Errorf("workflow skill registry is incomplete")
	}
	if prior == nil {
		return nil
	}
	for _, ref := range workflowSkillReferences(wf) {
		definition, ok := registry.Get(ref.name)
		if !ok {
			return fmt.Errorf("workflow skill %q is missing on resume", ref.name)
		}
		bytes, err := workflowSkillBytes(definition)
		if err != nil {
			return err
		}
		pinned, ok := prior.Skills[ref.name]
		if !ok || pinned.Digest != digestBytes(bytes) || string(pinned.Bytes) != string(bytes) {
			return fmt.Errorf("workflow skill %q changed since admission", ref.name)
		}
		if ref.panelKey != "" {
			binding, ok := prior.PanelBindings[ref.panelKey]
			if !ok || binding.SkillDigest != digestBytes(bytes) {
				return fmt.Errorf("panel binding %q skill changed since admission", ref.panelKey)
			}
		}
	}
	return nil
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
	panelKey string
}

func workflowSkillReferences(wf *compiler.CompiledWorkflow) []workflowSkillReference {
	if wf == nil {
		return nil
	}
	refs := make([]workflowSkillReference, 0)
	for _, step := range wf.Steps {
		if step.Skill != "" {
			refs = append(refs, workflowSkillReference{name: step.Skill})
		}
		if step.Kind != "agent_panel" || step.Panel == nil {
			continue
		}
		for _, member := range step.Panel.Members {
			if member.Skill != "" {
				refs = append(refs, workflowSkillReference{name: member.Skill, panelKey: step.ID + "/" + member.ID})
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
		return nil, err
	}
	view := struct {
		Name, Version, Scope, Permission, Description, ShortDescription, ArgsHint, Instructions string
		UserInvocable                                                                           bool
		Triggers, Tools                                                                         []string
		Resources                                                                               []skills.ResourceSnapshot
		Timeout                                                                                 int64
		Budget                                                                                  int
		InputSchema, OutputSchema                                                               map[string]any
	}{
		Name: definition.Name, Version: definition.Version, Scope: definition.Scope, Permission: definition.Permission,
		Description: definition.Description, ShortDescription: definition.ShortDescription, ArgsHint: definition.ArgsHint,
		Instructions: definition.Instructions, UserInvocable: definition.UserInvocable, Triggers: definition.Triggers,
		Tools: definition.Tools, Resources: resources, Timeout: int64(definition.Timeout), Budget: definition.Budget,
		InputSchema: definition.InputSchema, OutputSchema: definition.OutputSchema,
	}
	return json.Marshal(view)
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
