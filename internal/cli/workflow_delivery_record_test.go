package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestRecordDeliveryRefusalReplacesTransientFailure is the DC-9 regression
// test for recordDeliveryRefusal: delivery.Deliver returns refusals BEFORE
// writing any record, so a pre-existing failed record is an EARLIER attempt's
// transient in-flight failure (delivery/errors.go markFailed). settleDeliveryRefusal
// must surface the refusal reason, not that stale transient text, as the
// terminal delivery_failed run's cause.
func TestRecordDeliveryRefusalReplacesTransientFailure(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	key := delivery.DeliveryKey(runID, run.WorkflowDigest)

	// An earlier attempt recorded a transient in-flight failure in the exact
	// shape delivery/errors.go markFailed writes: failed + content-addressed
	// ErrorRef.
	transient := "transient: origin unreachable, retrying later"
	transientRef := "sha256:" + workflowledger.DigestHex([]byte(transient))
	if err := repo.StoreContent(ctx, transientRef, []byte(transient)); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID:          runID,
		IdempotencyKey: key,
		Provider:       "github",
		Status:         "failed",
		ErrorRef:       transientRef,
	}); err != nil {
		t.Fatal(err)
	}

	refusal := &delivery.RefusalError{Reason: "refused: delivery base was rewritten since admission"}
	if err := recordDeliveryRefusal(ctx, repo, runID, refusal); err != nil {
		t.Fatal(err)
	}

	rec, err := repo.GetDeliveryByIdempotencyKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "failed" {
		t.Fatalf("record status = %q, want failed", rec.Status)
	}
	if rec.ErrorRef == "" || rec.ErrorRef == transientRef {
		t.Fatalf("record ErrorRef = %q, want the refusal's content ref, not the earlier transient ref %q", rec.ErrorRef, transientRef)
	}
	body, err := repo.LoadContent(ctx, rec.ErrorRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "delivery base was rewritten") {
		t.Fatalf("recorded failure body = %q, want the refusal reason", body)
	}
	if strings.Contains(string(body), "origin unreachable") {
		t.Fatalf("recorded failure body = %q, still shows the earlier transient in-flight failure", body)
	}
}

// TestRecordDeliveryRefusalPreservesIdentity pins the preservation contract of
// recordDeliveryRefusal (mirroring delivery/errors.go markFailed): the refusal
// write must never clobber the earlier record's identity fields, or the next
// retry would refuse the run's own PR as foreign and lose crash-resume data.
func TestRecordDeliveryRefusalPreservesIdentity(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	key := delivery.DeliveryKey(runID, run.WorkflowDigest)

	// A prior attempt pushed the run's PR and recorded its full identity.
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID:          runID,
		IdempotencyKey: key,
		Mode:           "draft",
		BaseRef:        "main",
		HeadRef:        "wf/" + run.WorktreeName,
		Provider:       "github",
		Status:         "pushed",
		CommitSHA:      "c0ffee",
		TreeSHA:        "tree",
		DiffRef:        "diff",
		RemoteID:       "42",
		URL:            "https://github.com/x/y/pull/42",
	}); err != nil {
		t.Fatal(err)
	}

	if err := recordDeliveryRefusal(ctx, repo, runID, &delivery.RefusalError{Reason: "refused: gate violated"}); err != nil {
		t.Fatal(err)
	}

	rec, err := repo.GetDeliveryByIdempotencyKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "failed" {
		t.Fatalf("record status = %q, want failed", rec.Status)
	}
	if rec.RemoteID != "42" || rec.URL != "https://github.com/x/y/pull/42" {
		t.Fatalf("refused record = %+v, want RemoteID 42 and URL preserved", rec)
	}
	if rec.CommitSHA != "c0ffee" || rec.TreeSHA != "tree" || rec.DiffRef != "diff" {
		t.Fatalf("refused record = %+v, want CommitSHA/TreeSHA/DiffRef preserved", rec)
	}
	if rec.Mode != "draft" || rec.BaseRef != "main" || rec.HeadRef != "wf/"+run.WorktreeName || rec.Provider != "github" {
		t.Fatalf("refused record = %+v, want Mode/BaseRef/HeadRef/Provider preserved", rec)
	}
}

// TestRecordDeliveryRefusalWithoutPriorRecord proves the refusal is recorded
// even when no prior delivery record exists (the plain refusal path), so the
// terminal delivery_failed run always carries its cause.
func TestRecordDeliveryRefusalWithoutPriorRecord(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	key := delivery.DeliveryKey(runID, run.WorkflowDigest)

	if err := recordDeliveryRefusal(ctx, repo, runID, &delivery.RefusalError{Reason: "refused: origin unresolved"}); err != nil {
		t.Fatal(err)
	}

	rec, err := repo.GetDeliveryByIdempotencyKey(ctx, key)
	if err != nil {
		t.Fatalf("no delivery record after the refusal: %v", err)
	}
	if rec.Status != "failed" {
		t.Fatalf("record status = %q, want failed", rec.Status)
	}
	if rec.ErrorRef == "" {
		t.Fatal("record ErrorRef is empty: the refusal reason must be recorded")
	}
	body, err := repo.LoadContent(ctx, rec.ErrorRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "origin unresolved") {
		t.Fatalf("recorded refusal body = %q, want the refusal reason", body)
	}
}
