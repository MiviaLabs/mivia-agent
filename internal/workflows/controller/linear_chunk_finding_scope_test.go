package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// chunkScopeWorkflow is repairWorkflow plus an enabled stacking config, so
// chunk-mode reserved inputs are legal and the chunk finding-scope filter can
// arm. plan_step and implement_step both name "implement", the only producer
// step in this graph.
func chunkScopeWorkflow(t *testing.T, maxLoop int) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "chunk-repair", InitialStep: "implement",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 16},
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure",
				Context: []definition.ContextBinding{{From: "inputs.task", As: "task"}}},
			{ID: "review", Kind: "agent_gate", Agent: "rev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"ready": "yes"}}},
			{From: "review", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
			{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "review_repair", MaxIterations: maxLoop},
		},
		Stacking: &definition.Stacking{PlanStep: "implement", ImplementStep: "implement"},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// chunkModeInputs builds the reserved chunk-mode input set around one
// chunk_plan JSON payload.
func chunkModeInputs(planJSON string) map[string]any {
	return map[string]any{
		"task": "x", "stack_mode": "chunk", "chunk": "c1",
		"pr_base": "master", "stack_part": "1/1", "chunk_plan": planJSON,
	}
}

// runeutilPlan declares the two files of one chunk of the live incident.
const runeutilPlan = `{"id":"c1","title":"runeutil","files":["internal/runeutil/runeutil.go","internal/runeutil/runeutil_test.go"]}`

// runChunkScopeTest drives one chunk-mode repair run with the given runner
// outputs and returns the settled run, its error, and the repository.
func runChunkScopeTest(t *testing.T, planJSON string, runner *scriptedRunner) (*LinearController, workflowledger.Repository, error) {
	t.Helper()
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, chunkScopeWorkflow(t, -1), map[string]StepRuntime{
		"implement":           {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":              {Agent: agents.ResolvedAgent{Name: "rev"}},
		"decompose":           {Agent: agents.ResolvedAgent{Name: "dev"}, Digest: "sha256:" + strings.Repeat("a", 64)},
		"chunk_plan_validate": {Agent: agents.ResolvedAgent{Name: "dev"}, Digest: "sha256:" + strings.Repeat("b", 64)},
	}, chunkModeInputs(planJSON), "wfr-chunk-scope", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := ctrl.Run(context.Background())
	return ctrl, repo, runErr
}

// scopeFinding is the findings-entry shape the filter reads.
func scopeFinding(id, required string) string {
	return `{"id":"` + id + `","severity":"high","reason":"r","claim":"c","evidence":"e","required":"` + required + `"}`
}

// TestChunkFindingScopeDropsSiblingPackageFinding reproduces the live
// incident: the review demands packages that belong to sibling chunks. The
// filter must drop the finding before routing, flip the verdict to approved,
// and let the run succeed without a repair round. The persisted review output
// must carry the filtered shape so later rounds and the convergence gate read
// what the engine acted on.
func TestChunkFindingScopeDropsSiblingPackageFinding(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"ready":"yes","summary":"v1","files_changed":["internal/runeutil/runeutil.go"]}`),
		"review#1": json.RawMessage(`{"verdict":"changes_requested","findings":[` +
			scopeFinding("R0-1", "Implement internal/pathutil with SplitExt(p string) (base, ext string) and internal/envutil with ParseBool(s string, def bool) bool, each with table-driven tests") +
			`],"inspected":["internal"]}`),
		// Reached only if the filter wrongly keeps the finding.
		"implement#2": json.RawMessage(`{"ready":"yes","summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[],"inspected":["internal"]}`),
	}}
	ctrl, repo, runErr := runChunkScopeTest(t, runeutilPlan, runner)
	if runErr != nil {
		t.Fatalf("run error = %v, want success via the flipped verdict", runErr)
	}
	runner.mu.Lock()
	calls := len(runner.calls)
	runner.mu.Unlock()
	if calls != 2 { // implement#1 + review#1 only: no repair round
		t.Fatalf("runner calls = %d, want 2 (finding dropped, no repair round)", calls)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.StepID != "review" {
			continue
		}
		raw, loadErr := repo.LoadContent(context.Background(), a.OutputRef)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		var persisted map[string]any
		if err := json.Unmarshal(raw, &persisted); err != nil {
			t.Fatal(err)
		}
		if verdict := persisted["verdict"]; verdict != "approved" {
			t.Fatalf("persisted review verdict = %v, want approved (filtered shape)", verdict)
		}
		if findings, ok := persisted["findings"].([]any); !ok || len(findings) != 0 {
			t.Fatalf("persisted findings = %#v, want empty", persisted["findings"])
		}
	}
}

// TestChunkFindingScopeKeepsInScopeFinding pins that a demand on a declared
// file loops normally: the filter never touches fixable findings.
func TestChunkFindingScopeKeepsInScopeFinding(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"ready":"yes","summary":"v1"}`),
		"review#1": json.RawMessage(`{"verdict":"changes_requested","findings":[` +
			scopeFinding("R0-1", "Fix the TrimSpace handling in `internal/runeutil/runeutil.go` so Runes trims only trailing spaces") +
			`],"inspected":["internal/runeutil/runeutil.go"]}`),
		"implement#2": json.RawMessage(`{"ready":"yes","summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[],"inspected":["internal"]}`),
	}}
	ctrl, repo, runErr := runChunkScopeTest(t, runeutilPlan, runner)
	if runErr != nil {
		t.Fatalf("run error = %v, want success through the repair loop", runErr)
	}
	counters, err := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(counters) != 1 || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v, want review_repair=1 (back-edge taken)", counters)
	}
}

// TestChunkFindingScopeKeepsReferenceStyleFinding pins the Step-0 challenge
// fix: a demand that only REFERENCES a foreign file ("match the conventions
// used in ...") must stay. The foreign token shares no parent with the
// declared files, so it is a reference, not a sibling-chunk demand.
func TestChunkFindingScopeKeepsReferenceStyleFinding(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"ready":"yes","summary":"v1"}`),
		"review#1": json.RawMessage(`{"verdict":"changes_requested","findings":[` +
			scopeFinding("R0-1", "Make the Runes helper match the error conventions already used in internal/errors/errors.go") +
			`],"inspected":["internal/runeutil/runeutil.go"]}`),
		"implement#2": json.RawMessage(`{"ready":"yes","summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[],"inspected":["internal"]}`),
	}}
	ctrl, repo, runErr := runChunkScopeTest(t, runeutilPlan, runner)
	if runErr != nil {
		t.Fatalf("run error = %v, want success through the repair loop", runErr)
	}
	counters, err := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(counters) != 1 || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v, want review_repair=1 (reference finding kept)", counters)
	}
}

// TestChunkFindingScopeKeepsMixedDemand pins that a finding naming both a
// declared file and a sibling package stays: the in-scope half is fixable.
func TestChunkFindingScopeKeepsMixedDemand(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"ready":"yes","summary":"v1"}`),
		"review#1": json.RawMessage(`{"verdict":"changes_requested","findings":[` +
			scopeFinding("R0-1", "Fix Runes in internal/runeutil/runeutil.go and add the matching helper in internal/pathutil") +
			`],"inspected":["internal/runeutil/runeutil.go"]}`),
		"implement#2": json.RawMessage(`{"ready":"yes","summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[],"inspected":["internal"]}`),
	}}
	ctrl, repo, runErr := runChunkScopeTest(t, runeutilPlan, runner)
	if runErr != nil {
		t.Fatalf("run error = %v, want success through the repair loop", runErr)
	}
	counters, err := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(counters) != 1 || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v, want review_repair=1 (mixed finding kept)", counters)
	}
}

// TestChunkFindingScopeInactiveWithoutChunkMode pins the arming gate: the
// same sibling-package finding in a run without the chunk-mode reserved
// inputs loops normally. Only chunk-mode runs of a stacking workflow with
// hard lines get the filter.
func TestChunkFindingScopeInactiveWithoutChunkMode(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"ready":"yes","summary":"v1"}`),
		"review#1": json.RawMessage(`{"verdict":"changes_requested","findings":[` +
			scopeFinding("R0-1", "Implement internal/pathutil with SplitExt and internal/envutil with ParseBool") +
			`],"inspected":["internal"]}`),
		"implement#2": json.RawMessage(`{"ready":"yes","summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[],"inspected":["internal"]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, chunkScopeWorkflow(t, -1), map[string]StepRuntime{
		"implement":           {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":              {Agent: agents.ResolvedAgent{Name: "rev"}},
		"decompose":           {Agent: agents.ResolvedAgent{Name: "dev"}, Digest: "sha256:" + strings.Repeat("a", 64)},
		"chunk_plan_validate": {Agent: agents.ResolvedAgent{Name: "dev"}, Digest: "sha256:" + strings.Repeat("b", 64)},
	}, map[string]any{"task": "x"}, "wfr-no-chunk-mode", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if _, runErr := ctrl.Run(context.Background()); runErr != nil {
		t.Fatalf("run error = %v, want success through the repair loop", runErr)
	}
	counters, countersErr := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if countersErr != nil {
		t.Fatal(countersErr)
	}
	if len(counters) != 1 || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v, want review_repair=1 (filter unarmed)", counters)
	}
}

// TestChunkFindingScopeBlockedDemandKept pins the Step-0 challenge fix: a
// finding that demands a write-blocklisted path is never dropped, so the
// honest blocked cause still fails the run instead of a silent approval.
func TestChunkFindingScopeBlockedDemandKept(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"ready":"yes","summary":"v1"}`),
		"review#1": json.RawMessage(`{"verdict":"changes_requested","findings":[` +
			scopeFinding("R0-1", "Add the missing binding by editing internal/runeutil/extra.go and update .mivia/policy/hook.json with the new entry") +
			`],"inspected":["internal"]}`),
	}}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, chunkScopeWorkflow(t, -1), map[string]StepRuntime{
		"implement":           {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":              {Agent: agents.ResolvedAgent{Name: "rev"}},
		"decompose":           {Agent: agents.ResolvedAgent{Name: "dev"}, Digest: "sha256:" + strings.Repeat("a", 64)},
		"chunk_plan_validate": {Agent: agents.ResolvedAgent{Name: "dev"}, Digest: "sha256:" + strings.Repeat("b", 64)},
	}, chunkModeInputs(runeutilPlan), "wfr-blocked-demand", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetWritePathBlocklist([]string{".mivia/policy/hook.json"}); err != nil {
		t.Fatal(err)
	}
	_, runErr := ctrl.Run(context.Background())
	if runErr == nil || !strings.Contains(runErr.Error(), "write-blocklisted") {
		t.Fatalf("run error = %v, want the honest blocked cause", runErr)
	}
}

// TestChunkFindingScopeKeepsSlashlessDeclaredDemand pins the audit fix:
// a chunk that declares a top-level file (Makefile, go.mod) keeps a
// finding that demands work on it, even when the same finding also names
// a sibling path. The slash-less demand is fixable in the chunk.
func TestChunkFindingScopeKeepsSlashlessDeclaredDemand(t *testing.T) {
	plan := `{"id":"c1","files":["Makefile","internal/a/a.go"]}`
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"ready":"yes","summary":"v1"}`),
		"review#1": json.RawMessage(`{"verdict":"changes_requested","findings":[` +
			scopeFinding("R0-1", "Add the install target to Makefile and implement internal/a/extra.go with tests") +
			`],"inspected":["Makefile"]}`),
		"implement#2": json.RawMessage(`{"ready":"yes","summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[],"inspected":["Makefile"]}`),
	}}
	ctrl, repo, runErr := runChunkScopeTest(t, plan, runner)
	if runErr != nil {
		t.Fatalf("run error = %v, want success through the repair loop", runErr)
	}
	counters, err := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(counters) != 1 || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v, want review_repair=1 (slash-less declared demand kept)", counters)
	}
}

// TestChunkFindingScopeKeepsGluedDeclaredDemand pins the audit fix: review
// prose glues text onto a declared path ("runeutil.go's handling",
// "runeutil.go:Fix"). The glued token must still classify in scope, so the
// finding stays.
func TestChunkFindingScopeKeepsGluedDeclaredDemand(t *testing.T) {
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"ready":"yes","summary":"v1"}`),
		"review#1": json.RawMessage(`{"verdict":"changes_requested","findings":[` +
			scopeFinding("R0-1", "Fix internal/runeutil/runeutil.go's handling of nil input") +
			`],"inspected":["internal/runeutil/runeutil.go"]}`),
		"implement#2": json.RawMessage(`{"ready":"yes","summary":"v2"}`),
		"review#2":    json.RawMessage(`{"verdict":"approved","findings":[],"inspected":["internal"]}`),
	}}
	ctrl, repo, runErr := runChunkScopeTest(t, runeutilPlan, runner)
	if runErr != nil {
		t.Fatalf("run error = %v, want success through the repair loop", runErr)
	}
	counters, err := repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(counters) != 1 || counters[0].Iterations != 1 {
		t.Fatalf("loop counters = %+v, want review_repair=1 (glued declared token kept)", counters)
	}
}

// --- unit level ---

func TestChunkDeclaredFilesNormalizesAndCounts(t *testing.T) {
	raw := `{"id":"c1","files":["./internal/a/a.go","/internal/a/a_test.go","","internal/a/","internal/b/b.go","Makefile"]}`
	declared := chunkDeclaredFiles(raw)
	want := map[string]bool{"internal/a/a.go": true, "internal/a/a_test.go": true, "internal/a": true, "internal/b/b.go": true, "Makefile": true}
	if len(declared) != len(want) {
		t.Fatalf("declared = %#v, want %#v", declared, want)
	}
	for f := range want {
		if !declared[f] {
			t.Fatalf("declared missing normalized entry %q: %#v", f, declared)
		}
	}
	if got := chunkDeclaredFiles(`{"id":"c1","files":["","./"]}`); len(got) != 0 {
		t.Fatalf("empty-after-normalize plan = %#v, want no declared files", got)
	}
	if got := chunkDeclaredFiles("not json"); len(got) != 0 {
		t.Fatalf("undecodable plan = %#v, want no declared files", got)
	}
}

func TestDemandedPathTokens(t *testing.T) {
	got := demandedPathTokens(
		"Fix `internal/runeutil/runeutil.go:40-52` and ./internal/a/a.go and see https://example.com/x and " +
			"import github.com/stretchr/testify and the retry helper and internal/b/b.go#L7")
	want := []string{"internal/runeutil/runeutil.go", "internal/a/a.go", "internal/b/b.go"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokens = %#v, want %#v", got, want)
		}
	}
}

func TestClassifyChunkToken(t *testing.T) {
	declared := chunkDeclaredFiles(`{"files":["internal/runeutil/runeutil.go"]}`)
	cases := []struct {
		token string
		want  chunkTokenClass
	}{
		{"internal/runeutil/runeutil.go", chunkTokenInScope},
		{"internal/runeutil", chunkTokenInScope},                  // ancestor dir of a declared file
		{"internal/runeutil/runeutil_test.go", chunkTokenSibling}, // undeclared file in the declared dir
		{"internal/pathutil", chunkTokenSibling},                  // sibling package dir
		{"internal", chunkTokenInScope},                           // root ancestor: conservative keep
		{"cmd/tool/main.go", chunkTokenForeign},
		{"internal/errors/errors.go", chunkTokenForeign}, // reference-style foreign file
	}
	for _, tc := range cases {
		if got := classifyChunkToken(tc.token, declared); got != tc.want {
			t.Errorf("classifyChunkToken(%q) = %v, want %v", tc.token, got, tc.want)
		}
	}
}
