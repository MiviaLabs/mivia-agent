package localengine

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowtemplate "github.com/MiviaLabs/mivia-agent/internal/workflows/template"
)

func buildStepRuntimes(wf *compiler.CompiledWorkflow, base string) (map[string]controller.StepRuntime, error) {
	schemas, err := loadOutputSchemas(base, wf)
	if err != nil {
		return nil, err
	}
	templates, err := loadStepTemplates(base, wf)
	if err != nil {
		return nil, err
	}
	return buildStepRuntimesFromSnapshot(wf, schemas, templates, nil)
}

// buildStepRuntimesFromSnapshot builds agent step runtimes exclusively from
// the admitted snapshot's pinned bytes: no filesystem access. Resume uses this
// so a changed, deleted, or renamed reference cannot alter an admitted run
// (CLI parity with loadStepReferences' prior-snapshot path). A present pin
// with a missing or invalid digest fails closed.
//
// templates and agentPins may be incomplete for runs admitted before template
// and agent pinning existed: the whole-snapshot digest proves an absent pin is
// admission truth, not tampering, so a missing template pin degrades to the
// admitted (template-less) dispatch and nil agentPins keep the legacy
// synthetic digest the run was admitted with.
func buildStepRuntimesFromSnapshot(wf *compiler.CompiledWorkflow, schemas, templates map[string]workflowledger.RefSnapshot, agentPins map[string]workflowledger.AgentSnapshot) (map[string]controller.StepRuntime, error) {
	steps := make(map[string]controller.StepRuntime, len(wf.Steps))
	for _, step := range wf.Steps {
		if step.Kind != "agent" && step.Kind != "agent_gate" {
			continue
		}
		runtime := controller.StepRuntime{
			Agent:  agents.ResolvedAgent{Name: step.Agent},
			Digest: "sha256:agent-" + step.Agent,
		}
		if agentPins != nil {
			pin, ok := agentPins[step.Agent]
			if !ok || pin.Digest == "" {
				return nil, fmt.Errorf("step %q: snapshot agent %q admission is incomplete", step.ID, step.Agent)
			}
			runtime.Digest = pin.Digest
			runtime.ProviderName = pin.ProviderName
			runtime.Model = pin.Model
		}
		if step.Template != "" {
			ref, ok := templates[step.Template]
			if !ok {
				// The admission generation that pins agents also pins agent-step
				// templates. A missing pin with agent pins present is therefore a
				// snapshot integrity gap, not a legacy run. Older snapshots that
				// predate template pinning degrade to the admitted template-less
				// dispatch (the whole-snapshot digest proves the absence is admission
				// truth).
				if agentPins != nil {
					return nil, fmt.Errorf("step %q: template %q: missing from the run snapshot", step.ID, step.Template)
				}
			} else {
				if ref.Digest == "" || digestRefBytes(ref.Bytes) != ref.Digest {
					return nil, fmt.Errorf("step %q: template %q: snapshot digest is invalid", step.ID, step.Template)
				}
				runtime.Template = string(ref.Bytes)
			}
		}
		if step.OutputSchema != "" {
			ref, ok := schemas[step.OutputSchema]
			if !ok {
				return nil, fmt.Errorf("step %q: output_schema %q: not present in the run snapshot", step.ID, step.OutputSchema)
			}
			if ref.Digest == "" || digestRefBytes(ref.Bytes) != ref.Digest {
				return nil, fmt.Errorf("step %q: output_schema %q: snapshot digest is invalid", step.ID, step.OutputSchema)
			}
			var schema map[string]any
			if err := json.Unmarshal(ref.Bytes, &schema); err != nil {
				return nil, fmt.Errorf("step %q: output_schema %q: snapshot schema is invalid: %w", step.ID, step.OutputSchema, err)
			}
			runtime.Schema = schema
		}
		steps[step.ID] = runtime
	}
	return steps, nil
}

// loadOutputSchemas reads every output_schema referenced by the compiled
// workflow from the workspace and pins each one's raw bytes and content digest
// into the run snapshot. This is the admission read: startNew MUST read from
// the workspace here (that IS admission), and resume must never call it.
// Guards mirror loadOutputSchemaBytes (path traversal + size cap).
func loadOutputSchemas(base string, wf *compiler.CompiledWorkflow) (map[string]workflowledger.RefSnapshot, error) {
	schemas := make(map[string]workflowledger.RefSnapshot)
	for _, step := range wf.Steps {
		refs := []string{step.OutputSchema}
		if step.Panel != nil {
			for _, member := range step.Panel.Members {
				refs = append(refs, member.OutputSchema)
			}
		}
		for _, ref := range refs {
			if ref == "" {
				continue
			}
			if _, ok := schemas[ref]; ok {
				continue
			}
			data, err := loadOutputSchemaBytes(base, ref)
			if err != nil {
				return nil, fmt.Errorf("step %q: output_schema %q: %w", step.ID, ref, err)
			}
			schemas[ref] = workflowledger.RefSnapshot{Digest: digestRefBytes(data), Bytes: append([]byte(nil), data...)}
		}
	}
	return schemas, nil
}

// loadStepTemplates reads every template referenced by an agent or agent_gate
// step from the workspace and pins each one's raw bytes and content digest
// into the run snapshot (CLI parity with loadWorkflowRuntimes). This is an
// admission read: startNew MUST read from the workspace here, and resume must
// never call it. Guards mirror loadOutputSchemas (path traversal + size cap).
func loadStepTemplates(base string, wf *compiler.CompiledWorkflow) (map[string]workflowledger.RefSnapshot, error) {
	templates := make(map[string]workflowledger.RefSnapshot)
	for _, step := range wf.Steps {
		if step.Kind != "agent" && step.Kind != "agent_gate" {
			continue
		}
		if step.Template == "" {
			continue
		}
		if _, ok := templates[step.Template]; ok {
			continue
		}
		data, err := loadTemplateBytes(base, step.Template)
		if err != nil {
			return nil, fmt.Errorf("step %q: template %q: %w", step.ID, step.Template, err)
		}
		templates[step.Template] = workflowledger.RefSnapshot{Digest: digestRefBytes(data), Bytes: append([]byte(nil), data...)}
	}
	return templates, nil
}

// resolveStepAgents resolves every agent-bearing step's definition at
// admission and pins its routing snapshot (definition digest plus the
// definition-declared provider/model binding) into the run snapshot, so
// dispatch and resume verify against the admitted definition instead of a
// synthetic digest (CLI parity with workflowAgent).
func resolveStepAgents(wf *compiler.CompiledWorkflow, registry *agents.AgentRegistry) (map[string]workflowledger.AgentSnapshot, error) {
	pins := make(map[string]workflowledger.AgentSnapshot)
	for _, step := range wf.Steps {
		if step.Kind != "agent" && step.Kind != "agent_gate" {
			continue
		}
		if _, ok := pins[step.Agent]; ok {
			continue
		}
		agent, ok := registry.Get(step.Agent)
		if !ok {
			return nil, fmt.Errorf("step %q references unknown agent %q", step.ID, step.Agent)
		}
		digest, err := agent.DefinitionDigest()
		if err != nil {
			return nil, fmt.Errorf("step %q agent digest: %w", step.ID, err)
		}
		pin := workflowledger.AgentSnapshot{Digest: digest}
		// Pin the provider/model binding only as a complete pair: the engine
		// has no session provider to resolve a model-only declaration against,
		// and a half-pair would fail the dispatcher's binding completeness
		// check. An empty pair keeps session-following behavior.
		if agent.Provider != "" && agent.Model != "" {
			pin.ProviderName, pin.Model = agent.Provider, agent.Model
		}
		pins[step.Agent] = pin
	}
	return pins, nil
}

// verifyStepAgents re-resolves every pinned agent at resume and fails closed
// when the live definition digest no longer matches the admitted pin (CLI
// parity with workflowAgent's prior check). Provider/model stay pinned: they
// are admission data, not live policy.
func verifyStepAgents(wf *compiler.CompiledWorkflow, registry *agents.AgentRegistry, pinned map[string]workflowledger.AgentSnapshot) error {
	for _, step := range wf.Steps {
		if step.Kind != "agent" && step.Kind != "agent_gate" {
			continue
		}
		pin, ok := pinned[step.Agent]
		if !ok || pin.Digest == "" {
			return fmt.Errorf("step %q: snapshot agent %q admission is incomplete", step.ID, step.Agent)
		}
		agent, ok := registry.Get(step.Agent)
		if !ok {
			return fmt.Errorf("step %q references unknown agent %q", step.ID, step.Agent)
		}
		digest, err := agent.DefinitionDigest()
		if err != nil {
			return fmt.Errorf("step %q agent digest: %w", step.ID, err)
		}
		if digest != pin.Digest {
			return fmt.Errorf("agent %q changed since workflow admission; recover with: restore the agent definition to its admitted content or start a fresh run", step.Agent)
		}
	}
	return nil
}

func loadPanelSnapshotAssets(base string, wf *compiler.CompiledWorkflow, schemas map[string]workflowledger.RefSnapshot, registry *agents.AgentRegistry) (map[string]workflowledger.RefSnapshot, map[string]workflowledger.PanelBindingSnapshot, error) {
	templates := make(map[string]workflowledger.RefSnapshot)
	bindings := make(map[string]workflowledger.PanelBindingSnapshot)
	for _, step := range wf.Steps {
		if step.Kind != "agent_panel" || step.Panel == nil {
			continue
		}
		for _, member := range step.Panel.Members {
			agent, ok := registry.Get(member.Agent)
			if !ok {
				return nil, nil, fmt.Errorf("panel step %q member %q references unknown agent %q", step.ID, member.ID, member.Agent)
			}
			agentDigest, err := agent.DefinitionDigest()
			if err != nil {
				return nil, nil, fmt.Errorf("panel step %q member %q agent digest: %w", step.ID, member.ID, err)
			}
			data, err := loadTemplateBytes(base, member.Template)
			if err != nil {
				return nil, nil, fmt.Errorf("panel step %q member %q template %q: %w", step.ID, member.ID, member.Template, err)
			}
			templateRef := workflowledger.RefSnapshot{Digest: digestRefBytes(data), Bytes: append([]byte(nil), data...)}
			templates[member.Template] = templateRef
			schemaRef, ok := schemas[member.OutputSchema]
			if !ok {
				return nil, nil, fmt.Errorf("panel step %q member %q schema %q is missing", step.ID, member.ID, member.OutputSchema)
			}
			key := step.ID + "/" + member.ID
			if _, exists := bindings[key]; exists {
				return nil, nil, fmt.Errorf("duplicate panel binding %q", key)
			}
			bindings[key] = workflowledger.PanelBindingSnapshot{
				StepID: step.ID, MemberID: member.ID, AgentName: member.Agent,
				AgentDigest:  agentDigest,
				ProviderName: member.Provider, Model: member.Model,
				SkillDigest:    workflowledger.DigestHex([]byte(member.Skill)),
				TemplateDigest: templateRef.Digest, SchemaDigest: schemaRef.Digest,
			}
		}
	}
	return templates, bindings, nil
}

// digestRefBytes returns the content digest in the same wire format the CLI
// uses for snapshot refs ("sha256:" + hex), so localengine snapshots stay
// byte-compatible with CLI snapshots.
func digestRefBytes(data []byte) string {
	return "sha256:" + workflowledger.DigestHex(data)
}

// loadOutputSchemaBytes reads one workflow output-schema reference with the
// admission guards: no path escape, no symlink, and a size cap. Resume never
// reaches it because it reads the pinned snapshot bytes instead.
func loadOutputSchemaBytes(base, ref string) ([]byte, error) {
	return loadBoundedReferenceBytes(base, ref, definition.MaxWorkflowFileBytes)
}

func loadTemplateBytes(base, ref string) ([]byte, error) {
	return loadBoundedReferenceBytes(base, ref, workflowtemplate.MaxTemplateBytes)
}

func loadBoundedReferenceBytes(base, ref string, maxBytes int) ([]byte, error) {
	clean := filepath.Clean(ref)
	if clean == "." || filepath.IsAbs(ref) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("workflow reference %q escapes its directory", ref)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(clean)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workflow reference %q is not a regular file", ref)
	}
	file, err := root.Open(clean)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("workflow reference %q exceeds %d bytes", ref, maxBytes)
	}
	return data, nil
}

func validateInputs(inputs map[string]any, defs map[string]definition.InputDef) (map[string]any, map[string]string, error) {
	if inputs == nil {
		inputs = map[string]any{}
	}
	values := make(map[string]any, len(inputs))
	snapshot := make(map[string]string, len(inputs))
	for key, value := range inputs {
		def, ok := defs[key]
		if !ok {
			return nil, nil, fmt.Errorf("unknown workflow input %q", key)
		}
		str, err := inputToString(value, def.Type)
		if err != nil {
			return nil, nil, fmt.Errorf("workflow input %q: %w", key, err)
		}
		if def.MaxBytes > 0 && len(str) > def.MaxBytes {
			return nil, nil, fmt.Errorf("workflow input %q exceeds %d bytes", key, def.MaxBytes)
		}
		values[key] = value
		if def.Type == "string" {
			if s, ok := value.(string); ok {
				values[key] = s
			}
		}
		snapshot[key] = str
	}
	for key, def := range defs {
		if def.Required {
			if _, ok := values[key]; !ok {
				return nil, nil, fmt.Errorf("required workflow input %q is missing", key)
			}
		}
	}
	return values, snapshot, nil
}

func inputToString(value any, typ string) (string, error) {
	if typ == "string" {
		s, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("value is not a string")
		}
		return s, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func resolveLocalIdentity(root, runID string) (baseRef, baseCommit, worktree string, err error) {
	if root == "" {
		return "", "", "", fmt.Errorf("no workspace")
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", "", "", fmt.Errorf("no git repository at %s", root)
	}
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	// Resolve the real default branch and HEAD commit. Fabricating
	// "main"/"local-base" stamped delivery runs with a non-existent base
	// commit, so every delivery attempt refused with "base commit ... is not
	// an ancestor of HEAD".
	baseRef, err = git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", "", "", fmt.Errorf("resolve default branch: %w", err)
	}
	baseCommit, err = git("rev-parse", "HEAD")
	if err != nil {
		return "", "", "", fmt.Errorf("resolve HEAD commit: %w", err)
	}
	return baseRef, baseCommit, "workflow-" + runID, nil
}

func randomToken(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
}
