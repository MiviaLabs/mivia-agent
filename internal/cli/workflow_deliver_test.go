package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// recordingPRClient records PR boundary calls instead of invoking gh.
type recordingPRClient struct {
	mu      sync.Mutex
	creates int
	finds   int
	drafts  int
}

func (r *recordingPRClient) FindByHead(ctx context.Context, repo, headBranch string) (*delivery.PRRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finds++
	return nil, nil
}

func (r *recordingPRClient) Create(ctx context.Context, repo string, in delivery.PRInput) (delivery.PRRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creates++
	if in.Draft {
		r.drafts++
	}
	return delivery.PRRef{RemoteID: "1", URL: "https://github.com/o/r/pull/1"}, nil
}

func (r *recordingPRClient) calls() (creates, finds int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creates, r.finds
}

// draftCreates reports how many Create calls carried the draft flag.
func (r *recordingPRClient) draftCreates() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.drafts
}

// appendWorkflowDeliveryPolicy adds a pull_request delivery policy to the
// two-step fixture workflow.
func appendWorkflowDeliveryPolicy(t *testing.T, root, mode string) {
	t.Helper()
	path := filepath.Join(root, ".mivia", "workflows", "two-step.toml")
	body := "\n[delivery]\nkind = \"pull_request\"\nmode = \"" + mode + "\"\nprovider = \"github\"\nbase = \"main\"\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// initWorkflowGitRepoWithOrigin commits the fixture files and wires a bare
// origin remote holding the base commit, so delivery admission can resolve
// the origin URL and verify the base.
func initWorkflowGitRepoWithOrigin(t *testing.T, root string) {
	t.Helper()
	initWorkflowGitRepo(t, root)
	origin := filepath.Join(root, "origin.git")
	for _, args := range [][]string{
		{"init", "--bare", origin},
		{"-C", root, "remote", "add", "origin", origin},
		{"-C", root, "push", "-u", "origin", "main"},
	} {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestExecuteWorkflowRunAllowPublishFlagParsing(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"ok\":true}"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(server.Close)

	for _, flag := range []string{"--allow-publish", "--allow-publish=true", "--allow-publish=false"} {
		t.Run(flag, func(t *testing.T) {
			root := t.TempDir()
			storePath := filepath.Join(root, "workflow.db")
			t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
			writeWorkflowRunFixture(t, root, server.URL, storePath)
			var stdout strings.Builder
			args := []string{"run", "two-step", flag, "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}
			if err := runWorkflowWithIO(args, &stdout, io.Discard); err != nil {
				t.Fatalf("runWorkflowWithIO(%q) error = %v", args, err)
			}
			if !strings.Contains(stdout.String(), "status=succeeded") {
				t.Fatalf("stdout = %q, want status=succeeded", stdout.String())
			}
			// The base fixture has no delivery policy, so no explanation is
			// printed and the run behaves like a normal run.
			if strings.Contains(stdout.String(), "--allow-publish") {
				t.Fatalf("stdout = %q, want no delivery explanation", stdout.String())
			}
		})
	}
}

func TestExecuteWorkflowRunRefusesDeliveryWithoutWriteCapable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "http://127.0.0.1:1", storePath)
	appendWorkflowDeliveryPolicy(t, root, "draft")

	err := runWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("workflow run error = %v, want a write-capability refusal mentioning the run worktree", err)
	}
}

func TestExecuteWorkflowRunDeliveryPendingExplanation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	setWorkflowAgentTools(t, root, "write_file")
	appendWorkflowDeliveryPolicy(t, root, "draft")
	initWorkflowGitRepoWithOrigin(t, root)

	prRecorder := &recordingPRClient{}
	originalNewPR := workflowDeliverNewPR
	workflowDeliverNewPR = func() delivery.PRClient { return prRecorder }
	t.Cleanup(func() { workflowDeliverNewPR = originalNewPR })

	var stdout strings.Builder
	err := runWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}, &stdout, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "status=delivery_pending") {
		t.Fatalf("stdout = %q, want status=delivery_pending", stdout.String())
	}
	if !strings.Contains(stdout.String(), "requires --allow-publish") {
		t.Fatalf("stdout = %q, want an --allow-publish explanation", stdout.String())
	}
	if !strings.Contains(stdout.String(), "deliver with: mivia workflow deliver") {
		t.Fatalf("stdout = %q, want a deliver command hint", stdout.String())
	}
	creates, finds := prRecorder.calls()
	if creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero without --allow-publish", creates, finds)
	}
}

// deliveryAgreementFixture builds a run snapshot whose definition carries the
// given delivery mode (empty omits the delivery block entirely).
func deliveryAgreementFixture(t *testing.T, mode string) (workflowledger.RunSnapshot, []byte) {
	t.Helper()
	deliveryBlock := ""
	if mode != "" {
		deliveryBlock = "\n[delivery]\nkind = \"pull_request\"\nmode = \"" + mode + "\"\nprovider = \"github\"\nbase = \"main\"\n"
	}
	toml := `version = 1
name = "delivery"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
` + deliveryBlock
	wf, _, err := definition.ParseWorkflowTOML([]byte(toml), "delivery.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte(toml),
		DefinitionDigest: compiled.Digest,
		Inputs:           map[string]string{"task": "x"},
		Agents:           map[string]workflowledger.AgentSnapshot{"one": {Digest: "one"}},
	}
	raw, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-delivery-agreement", WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(raw), InputDigest: workflowledger.InputDigest(snapshot.Inputs),
	}
	return run, raw
}

func TestValidateWorkflowResumeSnapshotDeliveryAgreement(t *testing.T) {
	remarshal := func(t *testing.T, run workflowledger.RunSnapshot, snapshot workflowledger.Snapshot) (workflowledger.RunSnapshot, []byte) {
		t.Helper()
		raw, err := workflowledger.MarshalSnapshot(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		run.SnapshotDigest = workflowledger.SnapshotDigest(raw)
		return run, raw
	}

	// A snapshot delivery policy matching the admitted definition is accepted.
	run, raw := deliveryAgreementFixture(t, "draft")
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Delivery = &workflowledger.DeliverySnapshot{Mode: "draft", Provider: "github", Base: "main"}
	run, raw = remarshal(t, run, snapshot)
	if _, _, _, err := validateWorkflowResumeSnapshot(run, raw); err != nil {
		t.Fatalf("matching delivery policy rejected: %v", err)
	}

	// A differing mode is rejected.
	snapshot.Delivery.Mode = "ready"
	run, raw = remarshal(t, run, snapshot)
	if _, _, _, err := validateWorkflowResumeSnapshot(run, raw); err == nil || !strings.Contains(err.Error(), "does not match the admitted definition") {
		t.Fatalf("differing delivery mode error = %v, want a snapshot/definition mismatch", err)
	}

	// A differing provider is rejected.
	snapshot.Delivery.Mode = "draft"
	snapshot.Delivery.Provider = "gitlab"
	run, raw = remarshal(t, run, snapshot)
	if _, _, _, err := validateWorkflowResumeSnapshot(run, raw); err == nil || !strings.Contains(err.Error(), "does not match the admitted definition") {
		t.Fatalf("differing delivery provider error = %v, want a snapshot/definition mismatch", err)
	}

	// A snapshot policy without any admitted definition policy is rejected.
	run, raw = deliveryAgreementFixture(t, "")
	snapshot, err = workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Delivery = &workflowledger.DeliverySnapshot{Mode: "draft", Provider: "github", Base: "main"}
	run, raw = remarshal(t, run, snapshot)
	if _, _, _, err := validateWorkflowResumeSnapshot(run, raw); err == nil || !strings.Contains(err.Error(), "does not match the admitted definition") {
		t.Fatalf("undeclared delivery policy error = %v, want a snapshot/definition mismatch", err)
	}
}
