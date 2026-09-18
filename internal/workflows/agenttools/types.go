// Workflow tool types for the agent surface: tool names, result budgets,
// request/response payloads and the Engine/RepoFactory seams. The tools
// call the shared workflow ledger for reads and an injected Engine for
// mutations. This package must not import controller/agents/skills so the
// tools wrapper package can import it without an import cycle.
package agenttools

import "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// InvocationRunID returns the stable workflow run ID for a caller key.
func InvocationRunID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "wfr-inv-" + hex.EncodeToString(sum[:16])
}

// Tool names are model-facing and project/language-generic (rule 60).
const (
	ToolWorkflowRun      = "workflow_run"
	ToolWorkflowStatus   = "workflow_status"
	ToolWorkflowEvents   = "workflow_events"
	ToolWorkflowInspect  = "workflow_inspect"
	ToolWorkflowListRuns = "workflow_list_runs"
	ToolWorkflowDeliver  = "workflow_deliver"
	ToolWorkflowCancel   = "workflow_cancel"
	ToolWorkflowDelete   = "workflow_delete"
)

// AllToolNames returns the eight Phase 7 workflow tool names in stable order.
func AllToolNames() []string {
	return []string{
		ToolWorkflowRun,
		ToolWorkflowStatus,
		ToolWorkflowEvents,
		ToolWorkflowInspect,
		ToolWorkflowListRuns,
		ToolWorkflowDeliver,
		ToolWorkflowCancel,
		ToolWorkflowDelete,
	}
}

// Result budgets bound tool JSON (INV-AG-25). Framing stays inside the budget.
const (
	DefaultStatusBudgetBytes  = 256 << 10
	DefaultEventsBudgetBytes  = 256 << 10
	DefaultInspectBudgetBytes = 512 << 10
	DefaultListBudgetBytes    = 128 << 10
	DefaultRunBudgetBytes     = 16 << 10
	DefaultDeliverBudgetBytes = 32 << 10
	DefaultCancelBudgetBytes  = 16 << 10
	DefaultDeleteBudgetBytes  = 16 << 10
	DefaultEventsPageSize     = 50
	DefaultListRunsPageSize   = 50
)

// StartRequest admits a new workflow run or resumes an interrupted one.
type StartRequest struct {
	// Workflow is the discovered workflow name (required for a new run).
	Workflow string
	// Inputs are validated name→value pairs from the tool call.
	Inputs map[string]any
	// InvocationKey identifies one caller request across retries. When set for
	// a new run, the engine derives a stable run ID and admits it once.
	InvocationKey string
	// AllowPublish is the explicit publication gate for the workflow_deliver
	// tool and the CLI --allow-publish flag. The session harness does NOT
	// consult it for auto-delivery: a workflow whose [delivery] policy is
	// active is published automatically (the policy is the publication
	// grant), so a delivery-capable run is never stranded by a missing flag.
	AllowPublish bool
	// Resume, when true, resumes RunID from the durable ledger snapshot.
	Resume bool
	// RunID is required when Resume is true.
	RunID string
	// Force clears a stale claim before resume (operator-confirmed).
	Force bool
}

// StartResult is the immediate response from workflow_run (non-blocking).
type StartResult struct {
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
	Workflow string `json:"workflow,omitempty"`
	Resumed  bool   `json:"resumed,omitempty"`
}

// DeliverResult is the response from workflow_deliver.
type DeliverResult struct {
	RunID   string `json:"run_id"`
	Status  string `json:"status"`
	URL     string `json:"url,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Refused bool   `json:"refused,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// CancelResult is the response from workflow_cancel.
type CancelResult struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// DeleteResult is the response from workflow_delete. Status is the run's
// status BEFORE deletion; Deleted is always true on success (an error is
// returned otherwise), so the tool output is self-documenting for the agent.
type DeleteResult struct {
	RunID   string `json:"run_id"`
	Status  string `json:"status"`
	Deleted bool   `json:"deleted"`
}

// Engine performs mutating workflow operations. Reads use ledger.Repository only.
type Engine interface {
	// Start admits a run and advances it in a background goroutine.
	// It returns as soon as the run ID is durable (non-blocking).
	Start(ctx context.Context, req StartRequest) (StartResult, error)
	// Cancel settles a non-terminal run to canceled (idempotent).
	Cancel(ctx context.Context, runID string) (CancelResult, error)
	// Deliver publishes a delivery_pending run when allow_publish is true.
	Deliver(ctx context.Context, runID string, allowPublish bool) (DeliverResult, error)
	// Delete removes a run from the durable ledger. Settled runs (terminal or
	// delivery_pending) are always deletable; with force, a non-terminal run
	// (pending/running/waiting_approval) is deletable too — the crash-recovery
	// override for runs stranded by a dead executor. A fresh claim held by a
	// live executor is refused either way; only an expired lease is taken over.
	Delete(ctx context.Context, runID string, force bool) (DeleteResult, error)
}

// RepoFactory opens a workflow ledger repository. The closer releases resources.
type RepoFactory func(ctx context.Context) (ledger.Repository, func(), error)

// ErrRepoUnset is returned when tools register before a ledger is wired.
var ErrRepoUnset = errRepoUnset("workflow ledger is not configured for this session")

type errRepoUnset string

func (e errRepoUnset) Error() string { return string(e) }

// UnsetRepoFactory is used when tools register before a ledger is available.
func UnsetRepoFactory(context.Context) (ledger.Repository, func(), error) {
	return nil, func() {}, ErrRepoUnset
}
