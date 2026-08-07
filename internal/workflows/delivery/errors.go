package delivery

import (
	"context"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// maxErrorBytes bounds one stored failure text.
const maxErrorBytes = 4 << 10

// markFailed records a failed delivery attempt: the error text is stored as
// content and the record is upserted with Status "failed" and its ErrorRef.
// It preserves the attempt's CommitSHA, DiffRef and TreeSHA so a retry can
// resume (find-by-head or tree verification) instead of refusing the worktree
// as foreign, and it carries RemoteID/URL forward from the existing record
// (mirroring the pushed record in pushAndPublish) so a retry can still prove
// ownership of the run's own PR instead of misjudging it as foreign. Best
// effort: storage failures are swallowed — the caller's original error is the
// result.
func markFailed(ctx context.Context, repo ledger.Repository, key string, req Request, err error) {
	errText := err.Error()
	if len(errText) > maxErrorBytes {
		errText = errText[:maxErrorBytes]
	}
	ref := "sha256:" + ledger.DigestHex([]byte(errText))
	if serr := repo.StoreContent(ctx, ref, []byte(errText)); serr != nil {
		return
	}
	rec := deliveryRecord(req, key, "failed")
	rec.ErrorRef = ref
	if existing, gerr := repo.GetDeliveryByIdempotencyKey(ctx, key); gerr == nil {
		rec.CommitSHA = existing.CommitSHA
		rec.DiffRef = existing.DiffRef
		rec.TreeSHA = existing.TreeSHA
		rec.RemoteID = existing.RemoteID
		rec.URL = existing.URL
	}
	_ = repo.UpsertDelivery(ctx, rec)
}

// boundText truncates text to maxBytes, appending a notice line when
// truncated, so the result never exceeds maxBytes.
func boundText(text string, maxBytes int, notice string) string {
	if len(text) <= maxBytes {
		return text
	}
	marker := "\n[mivia] " + notice + "\n"
	if len(marker) >= maxBytes {
		return text[:maxBytes]
	}
	return text[:maxBytes-len(marker)] + marker
}
