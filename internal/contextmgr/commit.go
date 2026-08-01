package contextmgr

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// PublicationRequest keeps summary generation, request mapping, and durable
// publication in one explicit call boundary. No memory publication happens in
// this package; the caller adopts the preparation only after success.
type PublicationRequest struct {
	Store          contextstate.Store
	Summarizer     *Summarizer
	SummaryRequest SummaryRequest
	Preparation    Preparation
	Result         TurnResult
}

// CommitPreparation validates and summarizes before calling Store.Commit.
// Summary/provider failures therefore occur before any durable CAS attempt,
// and a persistence failure leaves the caller's preparation untouched.
func CommitPreparation(ctx context.Context, request PublicationRequest) error {
	if request.Store == nil {
		return fmt.Errorf("%w: context store is missing", contextstate.ErrCheckpointConflict)
	}
	preparation := request.Preparation
	if preparation.Compacted {
		if request.Summarizer == nil {
			return fmt.Errorf("%w: compacted preparation has no summarizer", contextstate.ErrSummaryUnavailable)
		}
		summary, err := request.Summarizer.Summarize(ctx, request.SummaryRequest)
		if err != nil {
			return err
		}
		metadata, err := summary.Metadata(request.Summarizer.Policy.RedactionConfigured)
		if err != nil {
			return err
		}
		preparation.Candidate.SummaryMetadata = metadata
	}
	commit, err := BuildCommitRequest(ctx, preparation, request.Result, preparation.Token.Principal, preparation.Token.Revision, preparation.Token.Binding)
	if err != nil {
		return err
	}
	return request.Store.Commit(ctx, commit)
}

// PreparationCommitter adapts the publication function to the narrow
// CheckpointPublisher interface used by chat. The summary request is captured
// by the host together with the preparation's provider/model policy snapshot.
type PreparationCommitter struct {
	Store          contextstate.Store
	Summarizer     *Summarizer
	SummaryRequest SummaryRequest
}

func (c PreparationCommitter) Commit(ctx context.Context, preparation Preparation, result TurnResult) error {
	return CommitPreparation(ctx, PublicationRequest{
		Store: c.Store, Summarizer: c.Summarizer, SummaryRequest: c.SummaryRequest,
		Preparation: preparation, Result: result,
	})
}
