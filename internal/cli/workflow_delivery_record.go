package cli

import (
	"context"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/textutil"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// maxAutoDeliveryErrorBytes bounds one recorded end-of-run delivery failure
// text (mirrors the delivery package's per-failure bound).
const maxAutoDeliveryErrorBytes = 4 << 10

// recordAutoDeliveryFailure durably records an end-of-run auto-delivery
// failure so the run ledger explains why delivery did not settle: the error
// text is stored content-addressed and a failed delivery record carrying the
// ErrorRef is upserted, which `workflow status` and the workflow_status tool
// surface. In-flight delivery failures already write a failed record via the
// delivery package; this fills the gap for refusals and pre-flight errors
// that return before any record write. Best effort: the settled run status
// (delivery_failed on refusal, delivery_pending on a transient failure) is
// owned by the delivery path and is never changed here.
func recordAutoDeliveryFailure(ctx context.Context, repo workflowledger.Repository, runID string, deliverErr error) {
	if repo == nil || deliverErr == nil {
		return
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return
	}
	key := delivery.DeliveryKey(run.RunID, run.WorkflowDigest)
	var existing *workflowledger.DeliveryRecord
	if rec, gerr := repo.GetDeliveryByIdempotencyKey(ctx, key); gerr == nil {
		if rec.Status == "failed" && rec.ErrorRef != "" {
			return // the delivery attempt already recorded its failure durably
		}
		existing = &rec
	}
	rec := workflowledger.DeliveryRecord{
		RunID:          run.RunID,
		IdempotencyKey: key,
		Provider:       "github",
		Status:         "failed",
	}
	// Best effort: carry the crashed attempt's commit/tree/diff and PR
	// identity forward (mirroring delivery/errors.go markFailed) so a retry
	// can resume and prove ownership of the run's own PR instead of refusing
	// it as foreign after the latest-wins upsert.
	if existing != nil {
		rec.CommitSHA = existing.CommitSHA
		rec.DiffRef = existing.DiffRef
		rec.TreeSHA = existing.TreeSHA
		rec.RemoteID = existing.RemoteID
		rec.URL = existing.URL
	}
	// Best effort: carry the delivery policy shape for operator context.
	if raw, serr := repo.GetRunSnapshot(ctx, runID); serr == nil {
		if _, compiled, _, verr := validateWorkflowResumeSnapshot(run, raw); verr == nil {
			if policy, ok := delivery.FromCompiled(compiled); ok {
				rec.Mode = policy.Mode
				rec.BaseRef = policy.Base
				rec.HeadRef = "wf/" + run.WorktreeName
			}
		}
	}
	text := textutil.TruncateRuneSafe(deliverErr.Error(), maxAutoDeliveryErrorBytes)
	ref := "sha256:" + workflowledger.DigestHex([]byte(text))
	if serr := repo.StoreContent(ctx, ref, []byte(text)); serr != nil {
		return
	}
	rec.ErrorRef = ref
	_ = repo.UpsertDelivery(ctx, rec)
}

// deliveryErrorInline renders a stored delivery error body for one status
// line: whitespace-collapsed (newlines become spaces), trimmed, and bounded so
// a long hook or host message never floods the status report.
func deliveryErrorInline(body string) string {
	const maxInline = 200
	body = strings.TrimSpace(body)
	body = strings.Join(strings.Fields(body), " ")
	if len(body) > maxInline {
		body = textutil.TruncateRuneSafe(body, maxInline) + "..."
	}
	return body
}
