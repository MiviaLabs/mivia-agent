package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// refsTestHarness builds a controller whose step binds three prior outputs:
// an OVERSIZED non-JSON blob, a SMALL non-JSON blob, and a small JSON value.
// It resolves the step's context and returns the controller plus the evidence
// and refs maps, so the focused tests below share one deterministic setup.
func refsTestHarness(t *testing.T) (map[string]any, map[string]ArtifactRef) {
	t.Helper()
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()

	big := []byte("not json at all " + strings.Repeat("x", 40<<10)) // > MaxEvidenceBindingBytes
	bigRef := "sha256:" + workflowledger.DigestHex(big)
	if err := repo.StoreContent(ctx, bigRef, big); err != nil {
		t.Fatal(err)
	}
	small := []byte("<plain>text</plain>") // small but not JSON
	smallRef := "sha256:" + workflowledger.DigestHex(small)
	if err := repo.StoreContent(ctx, smallRef, small); err != nil {
		t.Fatal(err)
	}
	j := []byte(`{"ok":true,"n":1}`)
	jRef := "sha256:" + workflowledger.DigestHex(j)
	if err := repo.StoreContent(ctx, jRef, j); err != nil {
		t.Fatal(err)
	}

	attempts := []workflowledger.StepAttempt{
		{AttemptID: "wfa-big-1", RunID: "wfr-refs", StepID: "big", AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, OutputRef: bigRef, OutputDigest: workflowledger.DigestHex(big)},
		{AttemptID: "wfa-small-2", RunID: "wfr-refs", StepID: "small", AttemptNo: 2, Status: workflowledger.AttemptStatusSucceeded, OutputRef: smallRef, OutputDigest: workflowledger.DigestHex(small)},
		{AttemptID: "wfa-json-3", RunID: "wfr-refs", StepID: "json", AttemptNo: 3, Status: workflowledger.AttemptStatusSucceeded, OutputRef: jRef, OutputDigest: workflowledger.DigestHex(j)},
	}
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), nil, nil, "wfr-refs", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	step := definition.Step{ID: "review", Kind: "agent", Context: []definition.ContextBinding{
		{From: "steps.big.output", As: "big"},
		{From: "steps.small.output", As: "small"},
		{From: "steps.json.output", As: "json"},
	}}
	_, evidence, refs, err := ctrl.contextForStep(ctx, step, attempts)
	if err != nil {
		t.Fatalf("non-JSON outputs must degrade to envelopes, not reject: %v", err)
	}
	return evidence, refs
}

// TestContextForStepOversizedNonJSONGetsEnvelope pins plan v3 P1: the size
// check runs BEFORE json.Unmarshal, so an oversized non-JSON prior output
// becomes a reference envelope instead of rejecting the run. The envelope
// must carry the artifact address and fit the binding cap once marshaled.
func TestContextForStepOversizedNonJSONGetsEnvelope(t *testing.T) {
	evidence, _ := refsTestHarness(t)
	envBig, ok := evidence["big"].(map[string]any)
	if !ok {
		t.Fatalf("evidence[big] = %#v (%T), want a reference envelope", evidence["big"], evidence["big"])
	}
	artifactBig, ok := envBig["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("envelope[big] = %#v, want an artifact key", envBig)
	}
	if artifactBig["step"] != "big" || artifactBig["attempt"] != 1 ||
		artifactBig["bytes"] != (40<<10)+16 {
		t.Fatalf("artifact[big] = %#v", artifactBig)
	}
	envRaw, err := json.Marshal(envBig)
	if err != nil || len(envRaw) > definition.MaxEvidenceBindingBytes {
		t.Fatalf("envelope[big] = %d bytes, want <= %d (err=%v)", len(envRaw), definition.MaxEvidenceBindingBytes, err)
	}
}

// TestContextForStepSmallNonJSONGetsEnvelope pins the plan v3 P1 fallback: a
// SMALL non-JSON output (json.Unmarshal failure) degrades to an envelope
// instead of rejecting the run, and the envelope points at the stored artifact.
func TestContextForStepSmallNonJSONGetsEnvelope(t *testing.T) {
	evidence, _ := refsTestHarness(t)
	envSmall, ok := evidence["small"].(map[string]any)
	if !ok {
		t.Fatalf("evidence[small] = %#v (%T), want a reference envelope", evidence["small"], evidence["small"])
	}
	artifactSmall, ok := envSmall["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("envelope[small] = %#v, want an artifact key", envSmall)
	}
	if artifactSmall["step"] != "small" || artifactSmall["attempt"] != 2 || artifactSmall["bytes"] != len("<plain>text</plain>") {
		t.Fatalf("artifact[small] = %#v", artifactSmall)
	}
}

// TestContextForStepSmallJSONStaysInline pins the plan v3 P1 boundary: a small
// JSON output stays inline and byte-identical to the parsed value.
func TestContextForStepSmallJSONStaysInline(t *testing.T) {
	evidence, _ := refsTestHarness(t)
	var parsed any
	if err := json.Unmarshal([]byte(`{"ok":true,"n":1}`), &parsed); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(evidence["json"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("inline evidence = %s, want %s", got, want)
	}
}

// TestContextForStepBuildsRefsMap pins plan v3 P3a: the refs map records
// step/attempt/ref/bytes/digest for EVERY resolved steps.X.output binding.
func TestContextForStepBuildsRefsMap(t *testing.T) {
	_, refs := refsTestHarness(t)
	if len(refs) != 3 {
		t.Fatalf("refs = %+v, want 3 entries", refs)
	}
	wantBytes := map[string]int{
		"big":   (40 << 10) + 16,
		"small": len("<plain>text</plain>"),
		"json":  len(`{"ok":true,"n":1}`),
	}
	for name, wantRef := range wantBytes {
		ref, ok := refs[name]
		if !ok {
			t.Fatalf("refs missing %q", name)
		}
		if ref.Bytes != wantRef || ref.Step == "" || ref.Ref == "" || ref.Digest == "" {
			t.Fatalf("refs[%s] = %+v, want bytes %d", name, ref, wantRef)
		}
	}
}

// TestAgentStepRequestRendersPromptAndPersistsPromptRef pins the controller-
// side prompt rendering (plan v3 P1+P3): on a fresh dispatch the prompt is
// rendered from the template plus the evidence-refs block, spec.EvidenceRefs
// carries the artifact references, and the prompt is persisted content-
// addressed under the attempt (the repo exposes PromptRef and the stored body
// equals the dispatched prompt, so a resume JOIN reuses it fingerprint-stable).
func TestAgentStepRequestRendersPromptAndPersistsPromptRef(t *testing.T) {
	wf := linearWorkflow(t)
	runner := &linearRunner{outputs: map[string]json.RawMessage{
		"first":  json.RawMessage(`{"ok":true}`),
		"second": json.RawMessage(`{"done":true}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	steps := map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}, Template: "first={{evidence.first}}"},
	}
	ctrl, err := NewLinearController(repo, runner, wf, steps, map[string]any{"task": "build"}, "wfr-prompt-fresh", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	if len(runner.calls) != 2 {
		runner.mu.Unlock()
		t.Fatalf("runner calls = %d, want 2", len(runner.calls))
	}
	secondCall := runner.calls[1]
	runner.mu.Unlock()

	if !strings.Contains(secondCall.Prompt, "Evidence refs: every prior-step output is stored in the workflow ledger") {
		t.Fatalf("prompt missing the evidence-refs header: %q", secondCall.Prompt)
	}
	if !strings.Contains(secondCall.Prompt, "workflow_inspect(run_id="+ctrl.RunID+", step=<step>, attempt=<attempt>, offset=<n>, limit=<n>)") {
		t.Fatalf("prompt missing the workflow_inspect pointer with the run ID: %q", secondCall.Prompt)
	}

	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %d err=%v", len(attempts), err)
	}
	var firstAttempt, secondAttempt workflowledger.StepAttempt
	for _, a := range attempts {
		switch a.StepID {
		case "first":
			firstAttempt = a
		case "second":
			secondAttempt = a
		}
	}
	if firstAttempt.OutputRef == "" {
		t.Fatalf("first attempt = %+v, want stored output", firstAttempt)
	}
	wantLine := fmt.Sprintf("- first: step=first attempt=%d ref=%s bytes=%d digest=%s",
		firstAttempt.AttemptNo, firstAttempt.OutputRef, len([]byte(`{"ok":true}`)), firstAttempt.OutputDigest)
	if !strings.Contains(secondCall.Prompt, wantLine) {
		t.Fatalf("prompt missing the per-binding line %q: %q", wantLine, secondCall.Prompt)
	}
	wantRef := ArtifactRef{Step: "first", Attempt: firstAttempt.AttemptNo, Ref: firstAttempt.OutputRef, Bytes: len([]byte(`{"ok":true}`)), Digest: firstAttempt.OutputDigest}
	if secondCall.EvidenceRefs["first"] != wantRef {
		t.Fatalf("EvidenceRefs[first] = %+v, want %+v", secondCall.EvidenceRefs["first"], wantRef)
	}

	if secondAttempt.PromptRef == "" {
		t.Fatalf("second attempt = %+v, want a persisted PromptRef", secondAttempt)
	}
	wantPromptRef := "sha256:" + workflowledger.DigestHex([]byte(secondCall.Prompt))
	if secondAttempt.PromptRef != wantPromptRef {
		t.Fatalf("PromptRef = %q, want %q", secondAttempt.PromptRef, wantPromptRef)
	}
	stored, err := repo.LoadContent(context.Background(), secondAttempt.PromptRef)
	if err != nil {
		t.Fatalf("stored prompt %q is not loadable: %v", secondAttempt.PromptRef, err)
	}
	if string(stored) != secondCall.Prompt {
		t.Fatalf("stored prompt = %q, want the dispatched prompt %q", stored, secondCall.Prompt)
	}
}

// TestAgentStepRequestJoinReusesStoredPrompt pins the join path: when the
// attempt already carries a PromptRef, agentStepRequest loads the STORED
// prompt and never re-renders — even a template that would fail to render is
// irrelevant because spec.Prompt equals the stored content exactly.
func TestAgentStepRequestJoinReusesStoredPrompt(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	snap, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: "wfr-join-prompt", Status: workflowledger.RunStatusPending, ActiveStepID: "one"}, snap); err != nil {
		t.Fatal(err)
	}
	promptBody := []byte("stored prompt body that a render could never produce")
	promptRef := "sha256:" + workflowledger.DigestHex(promptBody)
	if err := repo.StoreContent(ctx, promptRef, promptBody); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: "wfr-join-prompt", StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning, CoordinatorRunID: "coord-1", TaskID: "task-1", PromptRef: promptRef,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), nil, nil, "wfr-join-prompt", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	// The template references a binding that does not exist: if the join path
	// re-rendered, template.Render would fail. Loading the stored prompt must
	// succeed regardless.
	req, err := ctrl.agentStepRequest(ctx,
		workflowledger.RunSnapshot{RunID: ctrl.RunID},
		definition.Step{ID: "one", Kind: "agent"},
		StepRuntime{Template: "broken {{missing.binding}}"},
		attempt, []workflowledger.StepAttempt{attempt})
	if err != nil {
		t.Fatalf("join path must reuse the stored prompt without rendering: %v", err)
	}
	if req.Prompt != string(promptBody) {
		t.Fatalf("spec.Prompt = %q, want the stored prompt %q", req.Prompt, promptBody)
	}
	if req.EvidenceRefs != nil && len(req.EvidenceRefs) != 0 {
		t.Fatalf("EvidenceRefs = %+v, want nil/empty for a binding-free step", req.EvidenceRefs)
	}
}

// promptFaultRepository injects prompt-persistence failures (StoreContent of
// the prompt body or SetStepAttemptPrompt) to prove the controller's fail-soft
// contract. failStoreRef matches the content ref of the RENDERED PROMPT only,
// so the child output's StoreContent (same content store) still succeeds.
type promptFaultRepository struct {
	workflowledger.Repository
	failStoreRef string
	failSetRef   bool
}

func (r *promptFaultRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	if r.failStoreRef != "" && ref == r.failStoreRef {
		return errors.New("content store is down")
	}
	return r.Repository.StoreContent(ctx, ref, data)
}

func (r *promptFaultRepository) SetStepAttemptPrompt(ctx context.Context, runID, attemptID, promptRef string) error {
	if r.failSetRef {
		return errors.New("prompt ref store is down")
	}
	return r.Repository.SetStepAttemptPrompt(ctx, runID, attemptID, promptRef)
}

type promptRecordingRunner struct {
	mu    sync.Mutex
	calls []AgentStepRequest
}

func (r *promptRecordingRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Status: "completed", Output: json.RawMessage(`{"ok":true}`)}, nil
}

func promptWorkflow(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "prompt", InitialStep: "one",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Steps: []definition.Step{
			{ID: "one", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "inputs.task", As: "task"}}},
		},
		Transitions: []definition.Transition{
			{From: "one", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// TestAgentStepRequestPromptPersistenceFailSoft pins the fail-soft contract:
// a failure to store the prompt body OR to record the prompt ref NEVER fails
// the step — the prompt is still dispatched, the attempt completes, and the
// attempt's PromptRef stays empty.
func TestAgentStepRequestPromptPersistenceFailSoft(t *testing.T) {
	const runID = "wfr-prompt-failsoft"
	// The prompt is deterministic (template "task={{inputs.task}}" over the
	// same inputs and an empty refs block), so the test can name the exact
	// content ref the controller will try to store for it.
	promptBody := "task=x" + evidenceRefsBlock(runID, nil)
	promptRef := "sha256:" + workflowledger.DigestHex([]byte(promptBody))
	for _, tc := range []struct {
		name         string
		failStoreRef string
		failSetRef   bool
	}{
		{name: "store content", failStoreRef: promptRef},
		{name: "set prompt ref", failSetRef: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &promptFaultRepository{Repository: workflowledger.NewMemoryRepository(), failStoreRef: tc.failStoreRef, failSetRef: tc.failSetRef}
			runner := &promptRecordingRunner{}
			ctrl, err := NewLinearController(repo, runner, promptWorkflow(t), map[string]StepRuntime{
				"one": {Agent: agents.ResolvedAgent{Name: "dev"}, Template: "task={{inputs.task}}"},
			}, map[string]any{"task": "x"}, runID, []byte("snap"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := ctrl.Run(context.Background())
			if err != nil || got.Status != workflowledger.RunStatusSucceeded {
				t.Fatalf("run = %+v err=%v; prompt persistence failure must not fail the step", got, err)
			}
			runner.mu.Lock()
			defer runner.mu.Unlock()
			if len(runner.calls) != 1 || runner.calls[0].Prompt == "" {
				t.Fatalf("calls = %+v; want exactly one dispatched non-empty prompt", runner.calls)
			}
			attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("attempts = %d err=%v", len(attempts), err)
			}
			if attempts[0].PromptRef != "" {
				t.Fatalf("attempt = %+v; PromptRef must stay empty when persistence fails", attempts[0])
			}
		})
	}
}

// TestAgentStepRequestPostAppendCapRejectsOversizedPrompt pins the defense-
// in-depth re-check: template.Render already bounds the rendered body at
// maxStepContextBytes, so the evidence-refs block is the ONLY thing that can
// push the final prompt over the cap — and when it does, agentStepRequest
// fails the step (never dispatching an oversized prompt).
func TestAgentStepRequestPostAppendCapRejectsOversizedPrompt(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	prior := []byte(`{"content":"` + strings.Repeat("a", 20000) + `"}`)
	priorRef := "sha256:" + workflowledger.DigestHex(prior)
	if err := repo.StoreContent(ctx, priorRef, prior); err != nil {
		t.Fatal(err)
	}
	attempts := []workflowledger.StepAttempt{{
		AttemptID: "wfa-prior-1", RunID: "wfr-cap", StepID: "prior", AttemptNo: 1,
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: priorRef, OutputDigest: workflowledger.DigestHex(prior),
	}}
	// The rendered body must pass template.Render's own cap (<= maxStepContextBytes)
	// so ONLY the appended evidence-refs block pushes the final prompt over.
	big := strings.Repeat("x", maxStepContextBytes-140)
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), nil, map[string]any{"big": big}, "wfr-cap", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	step := definition.Step{ID: "review", Kind: "agent", Context: []definition.ContextBinding{
		{From: "inputs.big", As: "big", MaxBytes: maxStepContextBytes},
		{From: "steps.prior.output", As: "prior"},
	}}
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-review-1", RunID: "wfr-cap", StepID: "review", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning}
	_, err = ctrl.agentStepRequest(ctx,
		workflowledger.RunSnapshot{RunID: ctrl.RunID},
		step, StepRuntime{Template: "x={{inputs.big}}"}, attempt, attempts)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want post-append cap rejection containing %q", err, "exceeds")
	}
}

// transientPromptRunner fails the first N calls with a transient provider
// error and then succeeds, recording every request so the retry path's prompt
// reuse can be asserted.
type transientPromptRunner struct {
	mu       sync.Mutex
	calls    []AgentStepRequest
	failures int
	err      error
}

func (r *transientPromptRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	if len(r.calls) <= r.failures {
		return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Status: "failed"}, r.err
	}
	return AgentStepResult{CoordinatorRunID: req.CoordinatorRunID, TaskID: req.TaskID, Status: "completed", Output: json.RawMessage(`{"ok":true}`)}, nil
}

// TestAgentStepTransientRetryReusesSamePrompt pins the runner contract: the
// transient-retry re-dispatch (runStepWithTransientRetry) reuses the SAME
// spec — and therefore the SAME controller-rendered spec.Prompt — across every
// retry, and the attempt still persists its prompt ref exactly once.
func TestAgentStepTransientRetryReusesSamePrompt(t *testing.T) {
	orig := stepTransientRetryBackoff
	stepTransientRetryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { stepTransientRetryBackoff = orig })

	runner := &transientPromptRunner{
		failures: 2,
		err:      errors.New("zai: provider error (HTTP 429, code 1305: service temporarily overloaded)"),
	}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, linearWorkflow(t), map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "one"}, Template: "task={{inputs.task}}"},
		"second": {Agent: agents.ResolvedAgent{Name: "two"}},
	}, map[string]any{"task": "build"}, "wfr-retry-prompt", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	var prompts []string
	for _, call := range runner.calls {
		if call.StepID == "first" {
			prompts = append(prompts, call.Prompt)
		}
	}
	if len(prompts) != 3 {
		t.Fatalf("first-step calls = %d, want 3 (1 + 2 retries); all calls = %d", len(prompts), len(runner.calls))
	}
	if prompts[0] == "" || !strings.Contains(prompts[0], "Evidence refs:") {
		t.Fatalf("first prompt = %q, want the controller-rendered evidence-refs prompt", prompts[0])
	}
	for i, prompt := range prompts {
		if prompt != prompts[0] {
			t.Fatalf("retry %d changed the prompt: %q vs %q", i, prompt, prompts[0])
		}
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %d err=%v", len(attempts), err)
	}
	var firstAttempt workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == "first" {
			firstAttempt = a
		}
	}
	if firstAttempt.PromptRef != "sha256:"+workflowledger.DigestHex([]byte(prompts[0])) {
		t.Fatalf("first attempt = %+v, want PromptRef for the retried prompt", firstAttempt)
	}
}
