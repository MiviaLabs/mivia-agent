package cli

// delivery_fixture_helpers_test.go duplicates cliworkflow's delivery test
// fixtures (workflow_deliver_command_test.go, workflow_deliver_test.go) for
// the stack drive settle tests that stay in cli.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
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

func (r *recordingPRClient) IsMerged(context.Context, string, string) (bool, error) {
	return false, nil
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

// setWorkflowAgentTools writes both workflow agents with the given tool.
func setWorkflowAgentTools(t *testing.T, root, tool string) {
	t.Helper()
	for _, name := range []string{"one", "two"} {
		path := filepath.Join(root, ".mivia", "agents", name+".toml")
		body := "name = \"" + name + "\"\ndescription = \"workflow agent\"\ntools = [\"" + tool + "\"]\nmax_turns = 2\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// appendWorkflowDeliveryPolicy appends a delivery policy block.
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

// newDeliveryFixture builds the standard delivery-pending fixture.
func newDeliveryFixture(t *testing.T) (root, storePath, config string, recorder *recordingPRClient) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)
	root = t.TempDir()
	storePath = filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	setWorkflowAgentTools(t, root, "write_file")
	appendWorkflowDeliveryPolicy(t, root, "draft")
	initWorkflowGitRepoWithOrigin(t, root)
	recorder = &recordingPRClient{}
	originalNewPR := cliworkflow.WorkflowDeliverNewPR
	cliworkflow.WorkflowDeliverNewPR = func() delivery.PRClient { return recorder }
	t.Cleanup(func() { cliworkflow.WorkflowDeliverNewPR = originalNewPR })
	return root, storePath, filepath.Join(root, "config.toml"), recorder
}

// openDeliveryStore opens the fixture store and returns its repository.
func openDeliveryStore(t *testing.T, storePath string) workflowledger.Repository {
	t.Helper()
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return workflowledger.NewStorageRepository(store)
}
