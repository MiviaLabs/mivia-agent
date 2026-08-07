package localengine

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func buildStepRuntimes(wf *compiler.CompiledWorkflow, base string) (map[string]controller.StepRuntime, error) {
	schemas, err := loadOutputSchemas(base, wf)
	if err != nil {
		return nil, err
	}
	return buildStepRuntimesFromSnapshot(wf, schemas)
}

// buildStepRuntimesFromSnapshot builds agent step runtimes exclusively from
// the admitted snapshot's schema bytes: no filesystem access. Resume uses this
// so a changed, deleted, or renamed schema file cannot alter an admitted run
// (CLI parity with loadStepReferences' prior-snapshot path). Fail closed when
// a step's output_schema is missing from the snapshot or its digest is invalid.
func buildStepRuntimesFromSnapshot(wf *compiler.CompiledWorkflow, schemas map[string]workflowledger.RefSnapshot) (map[string]controller.StepRuntime, error) {
	steps := make(map[string]controller.StepRuntime, len(wf.Steps))
	for _, step := range wf.Steps {
		if step.Kind != "agent" && step.Kind != "agent_gate" {
			continue
		}
		runtime := controller.StepRuntime{
			Agent:  agents.ResolvedAgent{Name: step.Agent},
			Digest: "sha256:agent-" + step.Agent,
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
		if step.OutputSchema == "" {
			continue
		}
		if _, ok := schemas[step.OutputSchema]; ok {
			continue // same ref used by multiple steps: read and pin once
		}
		data, err := loadOutputSchemaBytes(base, step.OutputSchema)
		if err != nil {
			return nil, fmt.Errorf("step %q: output_schema %q: %w", step.ID, step.OutputSchema, err)
		}
		schemas[step.OutputSchema] = workflowledger.RefSnapshot{
			Digest: digestRefBytes(data),
			Bytes:  append([]byte(nil), data...),
		}
	}
	return schemas, nil
}

// digestRefBytes returns the content digest in the same wire format the CLI
// uses for snapshot refs ("sha256:" + hex), so localengine snapshots stay
// byte-compatible with CLI snapshots.
func digestRefBytes(data []byte) string {
	return "sha256:" + workflowledger.DigestHex(data)
}

// loadOutputSchemaBytes reads one workflow output-schema reference with the
// admission guards: no path escape and a size cap. It follows symlinks like
// the previous os.ReadFile admission path; resume never reaches it (it reads
// the pinned snapshot bytes instead), so a symlinked schema on disk cannot
// change what an admitted run enforces.
func loadOutputSchemaBytes(base, ref string) ([]byte, error) {
	clean := filepath.Clean(ref)
	if clean == "." || filepath.IsAbs(ref) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("workflow reference %q escapes its directory", ref)
	}
	data, err := os.ReadFile(filepath.Join(base, clean))
	if err != nil {
		return nil, err
	}
	if len(data) > definition.MaxWorkflowFileBytes {
		return nil, fmt.Errorf("workflow reference %q exceeds %d bytes", ref, definition.MaxWorkflowFileBytes)
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
