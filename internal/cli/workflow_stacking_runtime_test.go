package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// stackingRuntimeFixture writes the engine-reserved template and schema refs a
// synthesized stacking run needs and compiles a stacking workflow whose plan
// step runs on agent "plan-agent". With no stacking.agent set, the
// engine-synthesized steps (decompose, chunk_plan_validate) run on the plan
// step's agent, exactly like the product workflows.
func stackingRuntimeFixture(t *testing.T) (workflowBase string, wf *definition.CompiledWorkflow, registry *agents.AgentRegistry) {
	t.Helper()
	root := t.TempDir()
	workflowBase = filepath.Join(root, "workflows")
	for _, dir := range []string{
		filepath.Join(workflowBase, "templates"),
		filepath.Join(workflowBase, "schemas"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for ref, body := range map[string]string{
		"templates/decompose.md":            "decompose the plan",
		"templates/chunk-plan-validate.md":  "validate the chunk plan",
		"schemas/chunk-plan-v1.json":        `{"type":"object","additionalProperties":false}`,
		"schemas/chunk-plan-review-v1.json": `{"type":"object","additionalProperties":false}`,
	} {
		if err := os.WriteFile(filepath.Join(workflowBase, filepath.FromSlash(ref)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	enabled := true
	wfFile := &definition.WorkflowFile{
		Version: 1, Name: "stacked-runtime", InitialStep: "plan",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 12},
		Stacking: &definition.Stacking{
			Enabled:       &enabled,
			PlanStep:      "plan",
			ImplementStep: "implement",
			MaxChunks:     3,
			SoftLines:     20,
			HardLines:     40,
			MaxFiles:      2,
		},
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "plan-agent"},
			{ID: "implement", Kind: "agent", Agent: "impl-agent"},
			{ID: "verify", Kind: "agent_gate", Agent: "gate-agent"},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "implement", To: "verify", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	var err error
	wf, err = definition.Compile(wfFile)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Stacking == nil {
		t.Fatal("stacking config did not resolve")
	}
	registry = agents.NewRegistry()
	for _, name := range []string{"plan-agent", "impl-agent", "gate-agent"} {
		if err := registry.Publish(agents.ResolvedAgent{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	return workflowBase, wf, registry
}

// TestWorkflowRuntimeSynthesizedStepsCarryRoutingDigests is the regression test
// for stacking runs that failed with "agent task routing snapshot mismatch
// (resume not allowed)" on their first engine-synthesized step (decompose).
// The synthesized steps used to be injected into the controller WITHOUT a
// runtime: the controller has no agent registry, so the runtime carried a bare
// agent name and an empty routing digest, the runner stamped the empty digest
// on the task, and the agent handler refused the null routing snapshot. The
// run graph must be synthesized BEFORE the runtime build - where the registry
// is available - so decompose and chunk_plan_validate are resolved, pinned and
// dispatched exactly like declared steps.
func TestWorkflowRuntimeSynthesizedStepsCarryRoutingDigests(t *testing.T) {
	base, wf, registry := stackingRuntimeFixture(t)

	synth, err := workflowSynthesizeRunGraph(wf)
	if err != nil {
		t.Fatal(err)
	}
	if synth == wf {
		t.Fatal("stacking workflow must synthesize a run graph")
	}
	steps, snapshot, err := loadWorkflowRuntimes(filepath.Dir(base), base, synth, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	planAgent, _ := registry.Get("plan-agent")
	wantDigest, err := planAgent.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"decompose", "chunk_plan_validate"} {
		rt, ok := steps[id]
		if !ok {
			t.Fatalf("synthesized step %q has no runtime", id)
		}
		if rt.Agent.Name != "plan-agent" {
			t.Fatalf("synthesized step %q agent = %q; want plan-agent (stacking.agent unset)", id, rt.Agent.Name)
		}
		if rt.Digest != wantDigest {
			t.Fatalf("synthesized step %q digest = %q; want the registry-resolved %q", id, rt.Digest, wantDigest)
		}
		if strings.TrimSpace(rt.Template) == "" {
			t.Fatalf("synthesized step %q has no rendered template", id)
		}
		if rt.Schema == nil {
			t.Fatalf("synthesized step %q has no output schema", id)
		}
	}
	// The snapshot pins the synthesized steps' templates, schemas and agents so
	// resume re-validates them exactly like declared steps'.
	for _, ref := range []string{"templates/decompose.md", "templates/chunk-plan-validate.md"} {
		if got := snapshot.Templates[ref]; got.Digest == "" {
			t.Fatalf("snapshot did not pin template %q", ref)
		}
	}
	for _, ref := range []string{"schemas/chunk-plan-v1.json", "schemas/chunk-plan-review-v1.json"} {
		if got := snapshot.Schemas[ref]; got.Digest == "" {
			t.Fatalf("snapshot did not pin schema %q", ref)
		}
	}
	if got := snapshot.Agents["plan-agent"]; got.Digest != wantDigest {
		t.Fatalf("snapshot agent pin = %+v; want digest %q", got, wantDigest)
	}
}

// TestWorkflowRuntimeSynthesizedStepsResumeWithPinnedDigests proves the
// synthesized steps survive a resume round-trip: the snapshot recorded at
// admission pins their templates, schemas and routing digests, so rebuilding
// the runtime from the snapshot re-resolves the identical runtimes (stable
// digest and provider/model binding) instead of failing on missing pins.
func TestWorkflowRuntimeSynthesizedStepsResumeWithPinnedDigests(t *testing.T) {
	base, wf, registry := stackingRuntimeFixture(t)
	synth, err := workflowSynthesizeRunGraph(wf)
	if err != nil {
		t.Fatal(err)
	}
	opts := SessionDispatcherOpts{ProviderName: "openrouter", Model: "test/model", ModelCatalog: []config.ProviderModelGroup{{
		Provider: "openrouter", Selectable: true,
		Models: []config.ModelSpec{{Name: "test/model", ContextWindowTokens: 1000}},
	}}}
	prepared, err := prepareWorkflowRuntime(filepath.Dir(base), base, synth, registry, nil, nil, []byte("definition"), map[string]string{"task": "x"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := workflowledger.UnmarshalSnapshot(prepared.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := prepareWorkflowRuntime(filepath.Dir(base), base, synth, registry, &prior, prepared.Snapshot, nil, nil, opts)
	if err != nil {
		t.Fatalf("resume rebuild failed: %v", err)
	}
	for _, id := range []string{"decompose", "chunk_plan_validate"} {
		if a, b := prepared.Steps[id].Digest, rebuilt.Steps[id].Digest; a == "" || a != b {
			t.Fatalf("synthesized step %q digest across resume = %q -> %q; want stable and non-empty", id, a, b)
		}
		if a, b := prepared.Steps[id].ProviderName, rebuilt.Steps[id].ProviderName; a == "" || a != b {
			t.Fatalf("synthesized step %q provider across resume = %q -> %q; want stable", id, a, b)
		}
	}
}

// TestBuildWorkflowControllerSynthesizesStackingRuntimesBeforeAdmission is the
// end-to-end regression for the same failure, exercised through the real build
// path (buildWorkflowController -> prepareWorkflowRuntime -> NewLinearController
// with a registry-resolving agent load). If the build path ever stops
// synthesizing the run graph before building runtimes, admission must refuse
// the run - never admit a synthesized step without a routing digest.
func TestBuildWorkflowControllerSynthesizesStackingRuntimesBeforeAdmission(t *testing.T) {
	root, res, store, repo, _ := newWorkflowBuildFixture(t)

	// The engine-synthesized steps reference engine-reserved templates and
	// schemas; the runtime build must be able to read and pin them.
	workflowRoot := writeStackingEngineReservedFiles(t, root)

	enabled := true
	wfFile := &definition.WorkflowFile{
		Version: 1, Name: "stacked-build", InitialStep: "plan",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 12},
		Stacking: &definition.Stacking{
			Enabled:       &enabled,
			PlanStep:      "plan",
			ImplementStep: "implement",
			MaxChunks:     3,
			SoftLines:     20,
			HardLines:     40,
			MaxFiles:      2,
		},
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "one", Template: "templates/one.md", OutputSchema: "schemas/out.json", Context: []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 100}}},
			{ID: "implement", Kind: "agent", Agent: "two", Template: "templates/two.md", OutputSchema: "schemas/out.json", Context: []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 100}}},
			{ID: "verify", Kind: "agent_gate", Agent: "one", Template: "templates/one.md", OutputSchema: "schemas/out.json", Context: []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 100}}},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "implement", To: "verify", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := definition.Compile(wfFile)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Stacking == nil {
		t.Fatal("stacking config did not resolve")
	}

	built, err := buildWorkflowController(root, res, store, repo, compiled, workflowRoot, map[string]any{"task": "test"}, map[string]string{"task": "test"}, []byte("definition"), "wfr-stacking-e2e", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildWorkflowController() error = %v", err)
	}
	t.Cleanup(func() {
		built.Dispatcher.Close()
		built.Cleanup()
	})

	if _, ok := built.Controller.Workflow.StepIDs["decompose"]; !ok {
		t.Fatal("admitted workflow lacks the synthesized decompose step")
	}
	// decompose and chunk_plan_validate run on the plan step's agent, resolved
	// from the registry with the SAME routing digest as the plan step itself.
	for _, id := range []string{"decompose", "chunk_plan_validate"} {
		rt, ok := built.Controller.Steps[id]
		if !ok {
			t.Fatalf("admitted controller has no runtime for synthesized step %q", id)
		}
		if rt.Agent.Name != "one" {
			t.Fatalf("synthesized step %q agent = %q; want the plan step's agent", id, rt.Agent.Name)
		}
		if rt.Digest == "" || rt.Digest != built.Controller.Steps["plan"].Digest {
			t.Fatalf("synthesized step %q digest = %q; want the registry-resolved plan digest %q", id, rt.Digest, built.Controller.Steps["plan"].Digest)
		}
		if strings.TrimSpace(rt.Template) == "" {
			t.Fatalf("synthesized step %q has no rendered template", id)
		}
		if rt.Schema == nil {
			t.Fatalf("synthesized step %q has no output schema", id)
		}
	}
}

// writeStackingEngineReservedFiles writes the engine-reserved templates and
// schemas the synthesized decompose/chunk_plan_validate steps reference, so
// the runtime build can read and pin them like any declared step's files.
func writeStackingEngineReservedFiles(t *testing.T, root string) string {
	t.Helper()
	workflowRoot := filepath.Join(root, ".mivia", "workflows")
	for ref, body := range map[string]string{
		"templates/decompose.md":            "decompose the plan",
		"templates/chunk-plan-validate.md":  "validate the chunk plan",
		"schemas/chunk-plan-v1.json":        `{"type":"object","additionalProperties":false}`,
		"schemas/chunk-plan-review-v1.json": `{"type":"object","additionalProperties":false}`,
	} {
		if err := os.WriteFile(filepath.Join(workflowRoot, filepath.FromSlash(ref)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return workflowRoot
}
