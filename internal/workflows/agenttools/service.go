package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Service is the in-process host for the seven workflow tools.
// Read methods use only ledger projections. Mutating methods call Engine.
type Service struct {
	mu     sync.Mutex
	engine Engine
	repo   RepoFactory

	// resultBudget overrides (0 = package default).
	statusBudget  int
	eventsBudget  int
	inspectBudget int
	listBudget    int
}

// ServiceOptions configures a Service.
type ServiceOptions struct {
	// Engine performs run/cancel/deliver. Nil refuses mutating calls.
	Engine Engine
	// Repo opens the workflow ledger. Required for read tools.
	Repo RepoFactory
	// Optional result budget overrides (bytes). Zero keeps package defaults.
	StatusBudgetBytes  int
	EventsBudgetBytes  int
	InspectBudgetBytes int
	ListBudgetBytes    int
}

// NewService builds a Service from options.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Repo == nil {
		return nil, fmt.Errorf("workflow tool service requires a repository factory")
	}
	s := &Service{
		engine:        opts.Engine,
		repo:          opts.Repo,
		statusBudget:  opts.StatusBudgetBytes,
		eventsBudget:  opts.EventsBudgetBytes,
		inspectBudget: opts.InspectBudgetBytes,
		listBudget:    opts.ListBudgetBytes,
	}
	return s, nil
}

// SetEngine replaces the mutating engine (e.g. after session dispatcher attach).
func (s *Service) SetEngine(engine Engine) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.engine = engine
	s.mu.Unlock()
}

func (s *Service) getEngine() Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.engine
}

func (s *Service) openRepo(ctx context.Context) (workflowledger.Repository, func(), error) {
	if s == nil || s.repo == nil {
		return nil, nil, fmt.Errorf("workflow tool service has no repository factory")
	}
	return s.repo(ctx)
}

// Run starts or resumes a workflow run without waiting for terminal state.
func (s *Service) Run(ctx context.Context, req StartRequest) (StartResult, error) {
	engine := s.getEngine()
	if engine == nil {
		return StartResult{}, fmt.Errorf("workflow engine is not configured")
	}
	if req.Resume {
		if strings.TrimSpace(req.RunID) == "" {
			return StartResult{}, fmt.Errorf("resume requires run_id")
		}
	} else if strings.TrimSpace(req.Workflow) == "" {
		return StartResult{}, fmt.Errorf("workflow name is required")
	}
	return engine.Start(ctx, req)
}

// Cancel settles a non-terminal run to canceled.
func (s *Service) Cancel(ctx context.Context, runID string) (CancelResult, error) {
	if strings.TrimSpace(runID) == "" {
		return CancelResult{}, fmt.Errorf("run_id is required")
	}
	engine := s.getEngine()
	if engine == nil {
		return CancelResult{}, fmt.Errorf("workflow engine is not configured")
	}
	return engine.Cancel(ctx, runID)
}

// Deliver publishes a delivery_pending run when allow_publish is true.
func (s *Service) Deliver(ctx context.Context, runID string, allowPublish bool) (DeliverResult, error) {
	if strings.TrimSpace(runID) == "" {
		return DeliverResult{}, fmt.Errorf("run_id is required")
	}
	if !allowPublish {
		return DeliverResult{
			RunID:   runID,
			Refused: true,
			Reason:  "delivery requires allow_publish=true",
		}, nil
	}
	engine := s.getEngine()
	if engine == nil {
		return DeliverResult{}, fmt.Errorf("workflow engine is not configured")
	}
	return engine.Deliver(ctx, runID, allowPublish)
}

// Status returns a deep run overview from ledger projections only.
func (s *Service) Status(ctx context.Context, runID string) (StatusView, error) {
	if strings.TrimSpace(runID) == "" {
		return StatusView{}, fmt.Errorf("run_id is required")
	}
	repo, closeFn, err := s.openRepo(ctx)
	if err != nil {
		return StatusView{}, err
	}
	defer closeFn()
	return buildStatusView(ctx, repo, runID)
}

// Events returns a paged audit trail from the ledger.
func (s *Service) Events(ctx context.Context, runID string, limit, offset int) (EventsPage, error) {
	if strings.TrimSpace(runID) == "" {
		return EventsPage{}, fmt.Errorf("run_id is required")
	}
	if limit < 0 || offset < 0 {
		return EventsPage{}, fmt.Errorf("limit and offset must be >= 0")
	}
	if limit == 0 {
		limit = DefaultEventsPageSize
	}
	repo, closeFn, err := s.openRepo(ctx)
	if err != nil {
		return EventsPage{}, err
	}
	defer closeFn()
	// Confirm the run exists before listing.
	if _, err := repo.GetRun(ctx, runID); err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return EventsPage{}, fmt.Errorf("workflow run %q not found", runID)
		}
		return EventsPage{}, err
	}
	events, err := repo.ListEvents(ctx, runID, limit, offset)
	if err != nil {
		return EventsPage{}, err
	}
	out := make([]EventView, 0, len(events))
	for _, ev := range events {
		out = append(out, EventView{
			Seq:       ev.Sequence,
			Timestamp: formatTime(ev.CreatedAt),
			Kind:      ev.Kind,
			Detail:    ev.Summary,
		})
	}
	return EventsPage{RunID: runID, Events: out, Limit: limit, Offset: offset, Count: len(out)}, nil
}

// Inspect returns one step attempt's validated output and route decision.
func (s *Service) Inspect(ctx context.Context, runID, step string, attemptNo int) (InspectView, error) {
	if strings.TrimSpace(runID) == "" {
		return InspectView{}, fmt.Errorf("run_id is required")
	}
	if strings.TrimSpace(step) == "" {
		return InspectView{}, fmt.Errorf("step is required")
	}
	if attemptNo < 1 {
		return InspectView{}, fmt.Errorf("attempt must be >= 1")
	}
	repo, closeFn, err := s.openRepo(ctx)
	if err != nil {
		return InspectView{}, err
	}
	defer closeFn()
	if _, err := repo.GetRun(ctx, runID); err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return InspectView{}, fmt.Errorf("workflow run %q not found", runID)
		}
		return InspectView{}, err
	}
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return InspectView{}, err
	}
	var found *workflowledger.StepAttempt
	for i := range attempts {
		if attempts[i].StepID == step && attempts[i].AttemptNo == attemptNo {
			found = &attempts[i]
			break
		}
	}
	if found == nil {
		return InspectView{}, fmt.Errorf("attempt %s#%d not found on run %q", step, attemptNo, runID)
	}
	return buildInspectView(ctx, repo, runID, *found)
}

// ListRuns lists active and historical runs with optional status filter.
func (s *Service) ListRuns(ctx context.Context, statusFilter string, limit, offset int) (ListRunsView, error) {
	if limit < 0 || offset < 0 {
		return ListRunsView{}, fmt.Errorf("limit and offset must be >= 0")
	}
	if limit == 0 {
		limit = DefaultListRunsPageSize
	}
	repo, closeFn, err := s.openRepo(ctx)
	if err != nil {
		return ListRunsView{}, err
	}
	defer closeFn()
	var statuses []workflowledger.RunStatus
	if statusFilter != "" {
		statuses = []workflowledger.RunStatus{workflowledger.RunStatus(statusFilter)}
	}
	runs, err := repo.ListRuns(ctx, statuses...)
	if err != nil {
		return ListRunsView{}, err
	}
	// Apply offset/limit in-process (repository may return all matching).
	if offset > len(runs) {
		offset = len(runs)
	}
	end := offset + limit
	if end > len(runs) {
		end = len(runs)
	}
	page := runs[offset:end]
	now := time.Now()
	items := make([]RunListItem, 0, len(page))
	for _, r := range page {
		item := RunListItem{
			RunID:     r.RunID,
			Workflow:  r.WorkflowName,
			Status:    string(r.Status),
			StartedAt: formatTime(r.StartedAt),
		}
		if !r.StartedAt.IsZero() {
			item.Age = now.UTC().Sub(r.StartedAt.UTC()).Truncate(time.Second).String()
		}
		items = append(items, item)
	}
	return ListRunsView{Runs: items, Limit: limit, Offset: offset, Count: len(items)}, nil
}

// encodeJSON marshals v and enforces a result budget (fail closed, no cut).
func encodeJSON(v any, budget int) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if budget > 0 && len(data) > budget {
		return "", fmt.Errorf("workflow tool result exceeds %d bytes (got %d)", budget, len(data))
	}
	return string(data), nil
}

func (s *Service) budget(kind string) int {
	switch kind {
	case "status":
		if s.statusBudget > 0 {
			return s.statusBudget
		}
		return DefaultStatusBudgetBytes
	case "events":
		if s.eventsBudget > 0 {
			return s.eventsBudget
		}
		return DefaultEventsBudgetBytes
	case "inspect":
		if s.inspectBudget > 0 {
			return s.inspectBudget
		}
		return DefaultInspectBudgetBytes
	case "list":
		if s.listBudget > 0 {
			return s.listBudget
		}
		return DefaultListBudgetBytes
	default:
		return DefaultStatusBudgetBytes
	}
}
