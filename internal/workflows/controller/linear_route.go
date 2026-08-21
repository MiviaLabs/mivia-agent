package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// selectRoute chooses the next step from closed structural match criteria.
// Infrastructure failures use on_failure and never enter a repair loop.
// Back-edges check per-loop caps before the route is returned; the counter
// increments only after a durable successful completion (see recordLoopAfterComplete).
func (c *LinearController) selectRoute(ctx context.Context, step definition.Step, status workflowledger.AttemptStatus, output map[string]any) (RouteDecision, error) {
	if status != workflowledger.AttemptStatusSucceeded {
		return failureRoute(step), nil
	}
	decision, err := definition.Match(step.ID, "succeeded", output, c.Workflow.Transitions)
	if err != nil {
		route := RouteDecision{
			ToStepID:        failureTarget(step),
			TransitionIndex: decision.TransitionIndex,
			MatchDigest:     decision.MatchDigest,
			DecisionJSON:    append([]byte(nil), decision.DecisionJSON...),
		}
		return route, fmt.Errorf("transition match failed: %w", err)
	}
	route := RouteDecision{
		ToStepID:        decision.ToStepID,
		TransitionIndex: decision.TransitionIndex,
		MatchDigest:     decision.MatchDigest,
		DecisionJSON:    append([]byte(nil), decision.DecisionJSON...),
		Loop:            decision.Loop,
		MaxIterations:   decision.MaxIterations,
	}
	if decision.Loop != "" {
		if err := c.checkLoopCap(ctx, decision.Loop, decision.MaxIterations); err != nil {
			return c.loopExhaustionRoute(ctx, step, decision, c.loopExhaustedRouteError(ctx, err, step.ID))
		}
	}
	return route, nil
}

// selectEvidenceFailureRoute selects an explicit failed transition for an
// evidence gate. A verifier result can fail a check without an infrastructure
// error. That result is repairable only when the workflow declares one exact
// failed transition. Missing or ambiguous transitions fail closed.
func (c *LinearController) selectEvidenceFailureRoute(ctx context.Context, step definition.Step, output map[string]any) (RouteDecision, error) {
	decision, err := definition.Match(step.ID, "failed", output, c.Workflow.Transitions)
	if err != nil {
		route := RouteDecision{
			ToStepID:        failureTarget(step),
			TransitionIndex: decision.TransitionIndex,
			MatchDigest:     decision.MatchDigest,
			DecisionJSON:    append([]byte(nil), decision.DecisionJSON...),
		}
		return route, fmt.Errorf("failed evidence transition match failed: %w", err)
	}
	route := RouteDecision{
		ToStepID:        decision.ToStepID,
		TransitionIndex: decision.TransitionIndex,
		MatchDigest:     decision.MatchDigest,
		DecisionJSON:    append([]byte(nil), decision.DecisionJSON...),
		Loop:            decision.Loop,
		MaxIterations:   decision.MaxIterations,
	}
	if decision.Loop != "" {
		if err := c.checkLoopCap(ctx, decision.Loop, decision.MaxIterations); err != nil {
			return c.loopExhaustionRoute(ctx, step, decision, c.loopExhaustedRouteError(ctx, err, step.ID))
		}
	}
	return route, nil
}

func failureRoute(step definition.Step) RouteDecision {
	return RouteDecision{ToStepID: failureTarget(step), TransitionIndex: -1}
}

// loopExhaustionRoute decides the exhausted-loop route (R2 Phase 2). A
// transition that declares a partial_target is honored when the ledger still
// holds verified outputs: the run advances to the target with run.salvage
// bound as evidence, and the exhausted attempt is persisted with that route.
// Without a partial_target (or without salvage) the route is the terminal
// failureTarget and the error carries the salvage hint.
func (c *LinearController) loopExhaustionRoute(ctx context.Context, step definition.Step, decision definition.Decision, exhausted error) (RouteDecision, error) {
	route := RouteDecision{
		ToStepID:        failureTarget(step),
		TransitionIndex: decision.TransitionIndex,
		MatchDigest:     decision.MatchDigest,
		DecisionJSON:    append([]byte(nil), decision.DecisionJSON...),
	}
	if decision.PartialTarget == "" {
		return route, exhausted
	}
	var loopErr *loopExhaustedError
	if !errors.As(exhausted, &loopErr) || len(loopErr.Salvage) == 0 {
		return route, exhausted
	}
	route.ToStepID = decision.PartialTarget
	route.PartialAccept = true
	return route, nil
}

// loopExhaustedRouteError attaches the refused step and the salvaged verified
// outputs to the loop-exhaustion hint without losing the typed error, so
// callers can recover the structured fields with errors.As while the message
// names the step. Salvage is best-effort: a salvage failure must never change
// the loop-exhaustion outcome.
func (c *LinearController) loopExhaustedRouteError(ctx context.Context, err error, stepID string) error {
	var loopErr *loopExhaustedError
	if errors.As(err, &loopErr) {
		loopErr.StepID = stepID
		if salvaged, salErr := c.salvageLoopSuccesses(ctx); salErr == nil {
			loopErr.Salvage = salvaged
			loopErr.Findings = c.decodeSalvagedFindings(ctx, salvaged)
		}
		return loopErr
	}
	return fmt.Errorf("%w (step %q)", err, stepID)
}

func failureTarget(step definition.Step) string {
	if step.OnFailure != "" {
		return step.OnFailure
	}
	return "failure"
}

// SalvagedAttempt names one durable step output preserved when a repair loop
// exhausts, so the verified work survives the terminal failure (R2 Phase 2:
// partial-accept foundation - the outputs are content-addressed and recoverable
// by ref from the failure evidence).
type SalvagedAttempt struct {
	StepID       string `json:"step_id"`
	AttemptNo    int    `json:"attempt_no"`
	OutputRef    string `json:"output_ref,omitempty"`
	OutputDigest string `json:"output_digest,omitempty"`
}

// maxSalvageSummaryItems bounds the refs listed in the failure message; the
// structured Salvage slice on the typed error is not bounded by it.
const maxSalvageSummaryItems = 4

// salvageLoopSuccesses collects the last succeeded attempt per step from the
// durable ledger. Deterministic order: sorted by step ID.
func (c *LinearController) salvageLoopSuccesses(ctx context.Context) ([]SalvagedAttempt, error) {
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return nil, err
	}
	last := make(map[string]workflowledger.StepAttempt)
	for _, a := range attempts {
		if a.Status != workflowledger.AttemptStatusSucceeded {
			continue
		}
		if cur, ok := last[a.StepID]; !ok || a.AttemptNo > cur.AttemptNo {
			last[a.StepID] = a
		}
	}
	out := make([]SalvagedAttempt, 0, len(last))
	for stepID, a := range last {
		out = append(out, SalvagedAttempt{StepID: stepID, AttemptNo: a.AttemptNo, OutputRef: a.OutputRef, OutputDigest: a.OutputDigest})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StepID < out[j].StepID })
	return out, nil
}

// loopExhaustedError is the deterministic recovery hint produced when a
// repair loop spends its budget. Its message names the loop, the cap, the
// iterations spent, and the step whose route was refused, so a human or a
// resumed run can recover the verified work without re-reading the whole
// ledger (R2 Phase 1: the routing stays terminal, the evidence is enriched).
//
// Findings carries the last review verdict(s) decoded from the salvaged
// attempts, when any salvaged output is a panel member or synthesis report
// (verdict/host_verdict field present). Without this, an exhausted
// review_repair loop is undiagnosable after the fact: the terminal error
// named only content-addressed refs, and the run's worktree may already be
// reused by the time a human investigates, so the last rejection reason was
// otherwise unrecoverable.
type loopExhaustedError struct {
	LoopName      string
	Iterations    int
	MaxIterations int
	StepID        string
	Salvage       []SalvagedAttempt
	Findings      []string
}

func (e *loopExhaustedError) Error() string {
	msg := fmt.Sprintf("loop %q exhausted: max_iterations=%d (iterations=%d) (step %q)", e.LoopName, e.MaxIterations, e.Iterations, e.StepID)
	if len(e.Salvage) == 0 {
		return msg
	}
	n := min(len(e.Salvage), maxSalvageSummaryItems)
	parts := make([]string, 0, n)
	for _, s := range e.Salvage[:n] {
		ref := s.OutputRef
		if ref == "" {
			ref = s.OutputDigest
		}
		parts = append(parts, fmt.Sprintf("%s#%d:%s", s.StepID, s.AttemptNo, ref))
	}
	msg += fmt.Sprintf(" (salvaged: %s)", strings.Join(parts, ", "))
	if len(e.Findings) > 0 {
		msg += fmt.Sprintf(" (last review verdicts: %s)", strings.Join(e.Findings, "; "))
	}
	return msg
}

// decodeSalvagedFindings best-effort decodes each salvaged attempt's stored
// output as a panel synthesis report (host_verdict) or a panel member report
// (verdict), and renders a short, human-readable summary line per decodable
// attempt. A salvaged output that decodes as neither (an ordinary step, not a
// review step) is silently skipped - this is diagnostic enrichment, never a
// reason to fail the exhaustion path itself.
func (c *LinearController) decodeSalvagedFindings(ctx context.Context, salvaged []SalvagedAttempt) []string {
	var lines []string
	for _, s := range salvaged {
		if s.OutputRef == "" {
			continue
		}
		raw, err := c.Repo.LoadContent(ctx, s.OutputRef)
		if err != nil {
			continue
		}
		if line, ok := renderPanelFinalReportLine(s, raw); ok {
			lines = append(lines, line)
			continue
		}
		if line, ok := renderPanelMemberReportLine(s, raw); ok {
			lines = append(lines, line)
		}
	}
	return lines
}

func renderPanelFinalReportLine(s SalvagedAttempt, raw []byte) (string, bool) {
	var final PanelFinalReport
	if err := json.Unmarshal(raw, &final); err != nil || final.HostVerdict == "" {
		return "", false
	}
	included := 0
	for _, d := range final.Dispositions {
		if d.Disposition == PanelDispositionIncluded {
			included++
		}
	}
	return fmt.Sprintf("%s#%d:%s (%d/%d findings included)", s.StepID, s.AttemptNo, final.HostVerdict, included, len(final.Dispositions)), true
}

func renderPanelMemberReportLine(s SalvagedAttempt, raw []byte) (string, bool) {
	var member PanelMemberReport
	if err := json.Unmarshal(raw, &member); err != nil || member.Verdict == "" {
		return "", false
	}
	if len(member.Findings) == 0 {
		return fmt.Sprintf("%s#%d:%s", s.StepID, s.AttemptNo, member.Verdict), true
	}
	f := member.Findings[0]
	more := ""
	if len(member.Findings) > 1 {
		more = fmt.Sprintf(" +%d more", len(member.Findings)-1)
	}
	return fmt.Sprintf("%s#%d:%s [%s] %s%s", s.StepID, s.AttemptNo, member.Verdict, f.Severity, f.Title, more), true
}

// checkLoopCap refuses a back-edge when the durable counter already hit the cap.
// max_iterations = -1 means unlimited per-loop. It does not increment.
func (c *LinearController) checkLoopCap(ctx context.Context, loopName string, maxIterations int) error {
	counters, err := c.Repo.GetLoopCounters(ctx, c.RunID)
	if err != nil {
		return err
	}
	current := 0
	for _, lc := range counters {
		if lc.LoopName == loopName {
			current = lc.Iterations
			break
		}
	}
	if maxIterations >= 0 && current >= maxIterations {
		return &loopExhaustedError{LoopName: loopName, Iterations: current, MaxIterations: maxIterations}
	}
	return nil
}

// recordLoopAfterComplete increments the loop counter after a successful durable
// route completion. Crash before this call under-counts (safer than over-count).
func (c *LinearController) recordLoopAfterComplete(ctx context.Context, route RouteDecision) error {
	if route.Loop == "" {
		return nil
	}
	if _, err := c.Repo.IncrementLoopCounter(ctx, c.RunID, route.Loop); err != nil {
		return fmt.Errorf("increment loop %q: %w", route.Loop, err)
	}
	return nil
}

// completeSucceededRoute persists the attempt outcome then records a taken loop.
// If the outcome is durable but loop accounting fails, it returns a
// loopAccountError so callers do not fail the whole run for under-count.
func (c *LinearController) completeSucceededRoute(ctx context.Context, attempt workflowledger.StepAttempt, result AgentStepResult, route RouteDecision) error {
	if err := CompleteExistingStepResult(ctx, c.Repo, attempt, result, workflowledger.AttemptStatusSucceeded, route); err != nil {
		return err
	}
	if err := c.recordLoopAfterComplete(ctx, route); err != nil {
		return &loopAccountError{err: err}
	}
	return nil
}

// loopAccountError means the step route was persisted but the loop counter
// write failed. Callers must not mark the run failed for this alone.
type loopAccountError struct{ err error }

func (e *loopAccountError) Error() string {
	if e == nil || e.err == nil {
		return "loop counter write failed after durable route"
	}
	return e.err.Error()
}

func (e *loopAccountError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func isLoopAccountError(err error) bool {
	var target *loopAccountError
	return errors.As(err, &target)
}

// enforceGlobalAttemptCap refuses a new attempt when the run already hit the
// global max_step_attempts ceiling. max_step_attempts <= 0 means unlimited.
func (c *LinearController) enforceGlobalAttemptCap(attempts []workflowledger.StepAttempt) error {
	limit := c.Workflow.Limits.MaxStepAttempts
	if limit <= 0 {
		return nil
	}
	if len(attempts) >= limit {
		return fmt.Errorf("run exceeded max_step_attempts %d (exceeded max attempts)", limit)
	}
	return nil
}

func resultOutputMap(result AgentStepResult) (map[string]any, error) {
	if m, ok := result.ValidatedOutput.(map[string]any); ok {
		return m, nil
	}
	if len(result.Output) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(result.Output, &m); err != nil {
		// Non-object output (scalar, array, null) is a valid child result when
		// no output schema is in force; status-only transitions still route on
		// an empty map instead of failing the whole run.
		return map[string]any{}, nil
	}
	return m, nil
}

func settleAfterRoute(ctx context.Context, c *LinearController, run workflowledger.RunSnapshot, route RouteDecision) (workflowledger.RunSnapshot, bool, error) {
	run, err := c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	if workflowledger.IsTerminalStepID(route.ToStepID) {
		return c.reconcileTerminalRoute(ctx, run)
	}
	return run, false, nil
}
