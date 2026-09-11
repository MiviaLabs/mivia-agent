// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records. It
// depends on cli* packages; nothing in internal/ui* may import it.
//
// This file (service.go) is the Service's ports.AutomationSettings
// surface: definition edits (Upsert/Remove/SetEnabled) through a
// load-modify-save round trip, and Apply's TriggerAutomation /
// ResumeAutomationRun variants, which admit synchronously and then
// execute on a Service goroutine. The async run machinery (startAsync,
// Watch, CancelRun, Close, claim heartbeat) lives in service_run.go,
// the ports value mappers in service_map.go, admission and terminal
// writes in executor_run.go, and executeRun - reached by both RunOnce
// and Apply - in executor.go/executor_run.go.
package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// SessionSpawner is the capability set the executor needs from the
// session pool. The parameter types are PLAIN funcs, never
// uiadapter.BindFunc: a composition-root adapter in internal/newtui
// converts. A named func type is not assignable to a plain func
// parameter, and this package must never import internal/uiadapter.
//
// SetApprovalOverride runs after CreateFreshInDir returns (never inside
// the bind closure - see uiadapter.SetApprovalOverride's own doc
// comment on the clobber ordering). The executor installs the
// automation's unattended approval posture (DenyGate/AutoApproveGate)
// directly on the spawned session, keyed by its conversation id.
type SessionSpawner interface {
	CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error)
	SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error
}

// Config holds the executor's tunables. TurnTimeout bounds every
// headless-driven turn ("Headless Session Safety" in
// docs/design/automations.md); zero means the executor's turnTimeout()
// applies defaultTurnTimeout, 10 minutes. ClaimRefreshInterval is the
// period of the fenced-claim heartbeat a live run sends while a step
// is in flight; zero means defaultSweepMaxAge/3, so a healthy run is
// never swept as interrupted ("Interrupted Run Sweep").
type Config struct {
	TurnTimeout          time.Duration
	ClaimRefreshInterval time.Duration
	// SkillRegistry, when set, is the executor's fallback source for a
	// live *skills.Registry: skillRegistryFor (skillstep.go) consults it
	// for a StepSkill dispatch whose spawned session carries no registry
	// of its own on its current binding, and upsert consults it to
	// validate a StepSkill's Ref at Apply time (matching StepSlash's own
	// registry-validation posture). It is a plain func value, called
	// fresh on every use rather than cached, so a later composition root
	// can swap the live registry (e.g. after a workspace skill reload)
	// without reconstructing the Service. Nil means "no registry
	// available" - the same posture every automation.Service had before
	// this field existed.
	SkillRegistry func() *skills.Registry
}

// Service is the automation backend. It satisfies
// ports.AutomationSettings, which is how the settings UI reaches it
// without any UI package importing this one ("Settings UI").
//
// Lock order: s.mu is a leaf lock. Never hold s.mu across an s.db call
// or a runWatcher.mu acquisition.
type Service struct {
	root  string
	db    *storage.SQLite
	spawn SessionSpawner
	cfg   Config

	mu        sync.Mutex
	watchers  map[string][]*runWatcher
	running   map[string]activeRun
	closed    bool
	wg        sync.WaitGroup
	baseCtx   context.Context
	cancelAll context.CancelFunc
}

// New builds a Service rooted at workspace root, optionally backed by
// db (nil means no run persistence: Runs/Run stay empty and a fire
// cannot be admitted) and spawn (nil is valid only when no run will
// ever execute). Rejects an empty root: Automations()/Apply() always resolve automations.toml at
// ports.ScopeProject (scopeForLoad below), and automationsFilePath
// itself already refuses an empty workspaceRoot for that scope
// (store.go) - a Service built with one could never load or save
// anything, so failing fast here is more honest than a Service that
// silently does nothing.
func New(root string, db *storage.SQLite, spawn SessionSpawner, cfg Config) (*Service, error) {
	if root == "" {
		return nil, fmt.Errorf("automation: New requires a non-empty workspace root")
	}
	baseCtx, cancelAll := context.WithCancel(context.Background())
	return &Service{
		root:      root,
		db:        db,
		spawn:     spawn,
		cfg:       cfg,
		watchers:  make(map[string][]*runWatcher),
		running:   make(map[string]activeRun),
		baseCtx:   baseCtx,
		cancelAll: cancelAll,
	}, nil
}

// Compile-time proof of the inverted dependency: automation
// imports ports and implements AutomationSettings; ports never imports
// automation.
var _ ports.AutomationSettings = (*Service)(nil)

// scopeForLoad is the scope Automations()/Apply() read/write from:
// always ports.ScopeProject, because the service is constructed with one
// workspace root (see New's root parameter) and has no user-scope
// resolution wired. "File Locations" documents user+project as the
// eventual model.
const scopeForLoad = ports.ScopeProject

// Automations returns every defined automation, mapped from the store's
// on-disk Spec shape.
func (s *Service) Automations() []ports.Automation {
	specs, err := LoadSpecs(scopeForLoad, s.root)
	if err != nil {
		return nil
	}
	out := make([]ports.Automation, 0, len(specs))
	for _, spec := range specs {
		out = append(out, specToAutomation(spec))
	}
	return out
}

// Runs returns automationID's run history via the runstore, most
// recently started first, mapped to ports.Run. A store error (including
// "no run store configured" on a nil db) is treated as empty rather than
// propagated: ports.AutomationSettings.Runs has no error return.
func (s *Service) Runs(automationID string, limit int) []ports.Run {
	runs, err := s.listRuns(context.Background(), automationID, limit)
	if err != nil || len(runs) == 0 {
		return nil
	}
	out := make([]ports.Run, 0, len(runs))
	for _, r := range runs {
		out = append(out, runToPorts(r))
	}
	return out
}

// Run looks up one run by id via the runstore, mapped to ports.Run.
func (s *Service) Run(runID string) (ports.Run, bool) {
	r, ok, err := s.getRun(context.Background(), runID)
	if err != nil || !ok {
		return ports.Run{}, false
	}
	return runToPorts(r), true
}

// Apply handles automation-definition edits (Upsert/Remove/SetEnabled)
// via a load-modify-save round trip through the store. TriggerAutomation
// and ResumeAutomationRun run their admission (spec lookup, claim, run
// row) synchronously inside the save handle, so a refused fire surfaces
// as SaveFailed, and then start execution on a Service-owned goroutine:
// the caller (the TUI event loop) must never block on a model turn.
// Run progress is streamed through Watch.
func (s *Service) Apply(ctx context.Context, scope ports.Scope, e ports.AutomationEdit) (ports.SaveHandle, error) {
	switch v := e.(type) {
	case ports.UpsertAutomation:
		return s.runSaveHandle(func() error { return s.upsert(v.Automation) }), nil
	case ports.RemoveAutomation:
		return s.runSaveHandle(func() error { return s.remove(v.ID) }), nil
	case ports.SetAutomationEnabled:
		return s.runSaveHandle(func() error { return s.setEnabled(v.ID, v.On) }), nil
	case ports.TriggerAutomation:
		return s.runSaveHandle(func() error { return s.triggerAsync(ctx, v.ID) }), nil
	case ports.ResumeAutomationRun:
		return s.runSaveHandle(func() error { return s.resumeAsync(ctx, v.RunID) }), nil
	case ports.CancelAutomationRun:
		return s.runSaveHandle(func() error { return s.CancelRun(v.RunID) }), nil
	default:
		return nil, fmt.Errorf("automation: unknown edit %T", e)
	}
}

// triggerAsync admits a manual fire and starts its execution in the
// background. A lost claim is a named refusal here (ErrRunAlreadyActive),
// unlike RunOnce's silent RunSkipped no-op.
func (s *Service) triggerAsync(ctx context.Context, automationID string) error {
	adm, err := s.admitRun(ctx, automationID, ports.TriggerManual)
	if err != nil {
		return err
	}
	return s.startAsync(adm, s.executeRun)
}

// resumeAsync admits a resume and starts its execution in the
// background. A run that is already complete at admission needs no
// goroutine.
func (s *Service) resumeAsync(ctx context.Context, runID string) error {
	adm, err := s.admitResume(ctx, runID)
	if err != nil {
		return err
	}
	if adm.complete {
		return nil
	}
	return s.startAsync(adm, s.executeResume)
}

func (s *Service) upsert(a ports.Automation) error {
	specs, err := LoadSpecs(scopeForLoad, s.root)
	if err != nil {
		return err
	}
	spec := automationToSpec(a)
	found := -1
	for i := range specs {
		if specs[i].ID == spec.ID {
			found = i
			break
		}
	}
	if err := ValidateSpec(spec, s.configSkillRegistry()); err != nil {
		return err
	}
	if found >= 0 {
		specs[found] = spec
	} else {
		specs = append(specs, spec)
	}
	return SaveSpecs(scopeForLoad, s.root, specs)
}

func (s *Service) remove(id string) error {
	specs, err := LoadSpecs(scopeForLoad, s.root)
	if err != nil {
		return err
	}
	out := specs[:0]
	found := false
	for _, spec := range specs {
		if spec.ID == id {
			found = true
			continue
		}
		out = append(out, spec)
	}
	if !found {
		return fmt.Errorf("automation %q not found", id)
	}
	return SaveSpecs(scopeForLoad, s.root, out)
}

func (s *Service) setEnabled(id string, on bool) error {
	specs, err := LoadSpecs(scopeForLoad, s.root)
	if err != nil {
		return err
	}
	found := false
	for i := range specs {
		if specs[i].ID == id {
			specs[i].Enabled = on
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("automation %q not found", id)
	}
	return SaveSpecs(scopeForLoad, s.root, specs)
}

// saveHandle is a synchronous ports.SaveHandle: apply runs immediately
// and the whole event sequence (Pending -> Validating -> Saved|Failed)
// is queued onto a small buffered channel before returning, mirroring
// internal/uiadapter's own saveHandle shape (settings.go's
// newSaveHandle) without importing that package.
type saveHandle struct {
	id     string
	events chan ports.SaveEvent
}

func (h *saveHandle) ID() string                     { return h.id }
func (h *saveHandle) Events() <-chan ports.SaveEvent { return h.events }
func (h *saveHandle) Cancel()                        {}

func (s *Service) runSaveHandle(apply func() error) ports.SaveHandle {
	ch := make(chan ports.SaveEvent, 4)
	ch <- ports.SaveEvent{State: ports.SavePending}
	ch <- ports.SaveEvent{State: ports.SaveValidating}
	if err := apply(); err != nil {
		ch <- ports.SaveEvent{State: ports.SaveFailed, Message: err.Error()}
	} else {
		ch <- ports.SaveEvent{State: ports.SaveSaved}
	}
	close(ch)
	return &saveHandle{id: "automation-save", events: ch}
}
