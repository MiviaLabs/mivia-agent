package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// errListRepo answers every ListStepAttempts with a sentinel error, so a test
// can tell whether reviewMadeNoProgress SHORT-CIRCUITED or ran on. The guard's
// whole job is to decide that, and a returned error is the cheapest unambiguous
// proof that it did not stop early.
type errListRepo struct {
	workflowledger.Repository
}

var errReachedRepo = errors.New("guard did not short-circuit")

func (errListRepo) ListStepAttempts(context.Context, string) ([]workflowledger.StepAttempt, error) {
	return nil, errReachedRepo
}

func changesRequestedPanel() map[string]any {
	return panelReport("changes_requested", "f-1")
}

// TestConvergenceGuardShortCircuitsOnKindAndLoop pins BOTH arms of
//
//	(step.Kind != "agent_gate" && step.Kind != "agent_panel") || route.Loop == ""
//
// Each arm is checked on its own. A mutant that turns the "||" into "&&" makes
// neither arm sufficient alone, and one that flips either "!=" to "==" swaps
// which kinds are reviewers - both change exactly these answers.
func TestConvergenceGuardShortCircuitsOnKindAndLoop(t *testing.T) {
	c := &LinearController{Repo: errListRepo{}, RunID: "wfr-guard"}
	route := RouteDecision{Loop: "review", MaxIterations: 3}

	// A non-reviewer kind stops the guard even with a live loop.
	stop, err := c.reviewMadeNoProgress(context.Background(), definition.Step{ID: "s", Kind: "agent"}, route, changesRequestedPanel())
	if err != nil {
		t.Fatalf("a non-reviewer step reached the ledger (%v); the kind arm must stop it alone", err)
	}
	if stop {
		t.Fatal("a non-reviewer step reported a stall")
	}

	// A reviewer kind with NO loop stops it too.
	for _, kind := range []string{"agent_gate", "agent_panel"} {
		stop, err := c.reviewMadeNoProgress(context.Background(),
			definition.Step{ID: "s", Kind: kind}, RouteDecision{Loop: "", MaxIterations: 3}, changesRequestedPanel())
		if err != nil {
			t.Errorf("%s with no loop reached the ledger (%v); the loop arm must stop it alone", kind, err)
		}
		if stop {
			t.Errorf("%s with no loop reported a stall", kind)
		}
	}
}

// TestConvergenceGuardRunsForBothReviewerKinds is the complement: a reviewer
// kind on a live loop, asking for changes, must NOT short-circuit - it must go
// on to compare rounds. agent_panel is the case that matters most, since it is
// the only ACTIVE reviewer the shipped feature-delivery workflow has.
func TestConvergenceGuardRunsForBothReviewerKinds(t *testing.T) {
	c := &LinearController{Repo: errListRepo{}, RunID: "wfr-guard"}
	route := RouteDecision{Loop: "review", MaxIterations: 3}

	for _, tc := range []struct {
		kind   string
		output map[string]any
	}{
		{"agent_gate", map[string]any{"verdict": "changes_requested", "findings": []any{map[string]any{"id": "f-1"}}}},
		{"agent_panel", changesRequestedPanel()},
	} {
		_, err := c.reviewMadeNoProgress(context.Background(), definition.Step{ID: "s", Kind: tc.kind}, route, tc.output)
		if !errors.Is(err, errReachedRepo) {
			t.Errorf("%s on a live loop short-circuited (err = %v); it must compare rounds", tc.kind, err)
		}
	}
}

// TestConvergenceGuardStopsOnUnlimitedIterations keeps the third early return
// honest: an unbounded loop has no cap to report against, so the guard has
// nothing to add and must not read the ledger.
func TestConvergenceGuardStopsOnUnlimitedIterations(t *testing.T) {
	c := &LinearController{Repo: errListRepo{}, RunID: "wfr-guard"}
	route := RouteDecision{Loop: "review", MaxIterations: definition.UnlimitedIterations}
	stop, err := c.reviewMadeNoProgress(context.Background(), definition.Step{ID: "s", Kind: "agent_panel"}, route, changesRequestedPanel())
	if err != nil {
		t.Fatalf("an unlimited loop reached the ledger: %v", err)
	}
	if stop {
		t.Fatal("an unlimited loop reported a stall")
	}
}

// TestPanelFindingIDSetSkipsNonObjectDispositions pins the `continue` that
// steps over a disposition entry which is not an object. Dropping it would
// read a string or a number as a disposition and panic or mint a bogus id -
// and the list is model-authored, so a malformed entry is reachable.
func TestPanelFindingIDSetSkipsNonObjectDispositions(t *testing.T) {
	out := map[string]any{"dispositions": []any{
		"not-an-object",
		42,
		nil,
		map[string]any{"final_finding_id": "f-real"},
	}}
	got := findingIDSet(out)
	if len(got) != 1 || !got["f-real"] {
		t.Fatalf("findingIDSet = %v, want only the one well-formed disposition", got)
	}
}
