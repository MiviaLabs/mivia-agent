package ledger

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// Coverage repair for the flagged panel-phase projection branches: the
// synthesis clone on replay and the legacy (work-fingerprint-optional)
// validation path the projector uses.

func TestApplyPanelPhaseClonesSynthesisOnReplay(t *testing.T) {
	proj := &Projection{Attempts: []StepAttempt{{
		AttemptID: "attempt", Version: 1,
		PanelExecution: &PanelExecution{Phase: PanelPhaseMembersAdmitted},
	}}}
	synthesis := &PanelSynthesisExecution{Work: validSynthesisTask(t, "wfr-proj", "attempt")}
	payload, err := marshalPanelPhase(panelPhasePayload{
		AttemptID: "attempt", Version: 2, Phase: PanelPhaseSynthesisAdmitted,
		Synthesis: synthesis, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyPanelPhase(proj, storage.Event{Kind: "wf_panel_phase", Payload: payload}); err != nil {
		t.Fatalf("applyPanelPhase: %v", err)
	}
	got := proj.Attempts[0].PanelExecution.Synthesis
	if got == nil || got == synthesis {
		t.Fatal("synthesis must be cloned, not aliased")
	}
	if proj.Attempts[0].PanelExecution.Phase != PanelPhaseSynthesisAdmitted {
		t.Fatalf("phase = %s", proj.Attempts[0].PanelExecution.Phase)
	}
}

func TestApplyPanelPhaseLegacySynthesisWorkWithoutFingerprint(t *testing.T) {
	proj := &Projection{Attempts: []StepAttempt{{
		AttemptID: "attempt", Version: 1,
		PanelExecution: &PanelExecution{Phase: PanelPhaseMembersAdmitted},
	}}}
	work := validSynthesisTask(t, "wfr-proj", "attempt")
	work.WorkFingerprint = ""
	if work.ValidateLegacy() != nil {
		t.Fatal("fixture must be legacy-valid without a work fingerprint")
	}
	synthesis := &PanelSynthesisExecution{Work: work}
	payload, err := marshalPanelPhase(panelPhasePayload{
		AttemptID: "attempt", Version: 2, Phase: PanelPhaseSynthesisAdmitted,
		Synthesis: synthesis, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyPanelPhase(proj, storage.Event{Kind: "wf_panel_phase", Payload: payload}); err != nil {
		t.Fatalf("applyPanelPhase(legacy work): %v", err)
	}
}

func TestPanelPhaseSynthesisAdmissionFailsClosedWithoutValidator(t *testing.T) {
	SetPanelContentValidator(nil)
	defer SetPanelContentValidator(testPanelContentValidator)

	repo := newMemoryRepo(t)
	ctx := ContextWithClaimHolder(context.Background(), "holder")
	run := runID(t)
	snap, raw := newRun(t, run)
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	attempt := StepAttempt{AttemptID: "attempt", RunID: run, StepID: "panel", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "attempt")}
	storePanelExecution(t, repo, attempt.PanelExecution)
	// A stub validator lets the members phase persist; nil-ing it afterwards
	// must still fail closed at synthesis admission.
	SetPanelContentValidator(func(context.Context, Repository, string, string, PanelTaskSpec) error { return nil })
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	SetPanelContentValidator(nil)
	if err := repo.ClaimRun(ctx, run, "holder"); err != nil {
		t.Fatal(err)
	}
	synthesis := &PanelSynthesisExecution{Work: validSynthesisTask(t, run, attempt.AttemptID)}
	if err := repo.CompareAndSetPanelPhase(ctx, run, attempt.AttemptID, 1, PanelPhaseMembersAdmitted, PanelPhaseSynthesisAdmitted, synthesis); err == nil {
		t.Fatal("synthesis admission without a registered validator must be refused")
	}
}
