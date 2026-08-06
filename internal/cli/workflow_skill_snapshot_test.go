package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
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

func TestWorkflowSkillSnapshotRejectsChangedResourceBeforeActivation(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(root, "review", "template.md"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, closeActivation, err := handler.prepareInvokeSurface(runtime.Request{Skill: "review"})
	defer closeActivation()
	if err == nil || !strings.Contains(err.Error(), "changed after admission") {
		t.Fatalf("prepareInvokeSurface() error = %v", err)
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
