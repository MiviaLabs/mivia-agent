package delivery

// adoptRecordedC1Commit recovers the crash window between the C1 commit and
// the CommitSHA re-upsert (record.CommitSHA == "", TreeSHA holds the
// pre-commit snapshot). These tests drive it with a scripted git so each
// branch is pinned by the exact git calls it makes.

import (
	"context"
	"errors"
	"testing"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// upsertRecorder captures the delivery record adoption re-upserts.
type upsertRecorder struct {
	records []ledger.DeliveryRecord
	err     error
}

func (r *upsertRecorder) UpsertDelivery(_ context.Context, d ledger.DeliveryRecord) error {
	r.records = append(r.records, d)
	return r.err
}

// adoptTestRequest is the request shape adoptRecordedC1Commit reads: only the
// git context and the base commit matter.
func adoptTestRequest() Request {
	return Request{BaseCommit: "base0000", Branch: "wf/wt-test"}
}

// TestAdoptRecordedC1CommitMatchingTreeSkipsVerification pins the fast path:
// when HEAD's tree equals the recorded pre-commit snapshot, the commit is
// proven to be the run's own C1 by tree identity alone, so no further git
// verification runs and the record is re-upserted with the adopted commit.
func TestAdoptRecordedC1CommitMatchingTreeSkipsVerification(t *testing.T) {
	git := &scriptedGit{t: t, script: map[string]scriptedResult{
		"rev-parse HEAD^{tree}": {out: "tree1111\n"},
	}}
	repo := &upsertRecorder{}
	existing := ledger.DeliveryRecord{RunID: "run-1", IdempotencyKey: "key-1", Status: "pending", TreeSHA: "tree1111", DeferredFiles: `["deferred.txt"]`}

	c1, c1Tree, err := adoptRecordedC1Commit(context.Background(), repo, git, adoptTestRequest(), existing, "commit2222")
	if err != nil {
		t.Fatalf("adoptRecordedC1Commit: %v", err)
	}
	if c1 != "commit2222" || c1Tree != "tree1111" {
		t.Fatalf("adopted = (%q, %q), want (commit2222, tree1111)", c1, c1Tree)
	}
	if len(git.calls) != 1 {
		t.Fatalf("git calls = %v, want only the tree lookup when the tree matches", git.calls)
	}
	if len(repo.records) != 1 {
		t.Fatalf("upserts = %d, want exactly one re-upsert", len(repo.records))
	}
	got := repo.records[0]
	if got.CommitSHA != "commit2222" || got.TreeSHA != "tree1111" {
		t.Fatalf("re-upserted record = %+v, want the adopted commit and tree", got)
	}
	if got.RunID != existing.RunID || got.Status != existing.Status || got.DeferredFiles != existing.DeferredFiles {
		t.Fatalf("re-upserted record = %+v, want every other field preserved from %+v", got, existing)
	}
}

// TestAdoptRecordedC1CommitMismatchedTreeVerifiesCommit pins the
// tree-mutating-hook path: a tree that differs from the snapshot is only
// adopted after verifyMiviaCommitOnTop proves HEAD is exactly one mivia
// commit on top of the base, and the record then carries HEAD's real tree.
func TestAdoptRecordedC1CommitMismatchedTreeVerifiesCommit(t *testing.T) {
	git := &scriptedGit{t: t, script: map[string]scriptedResult{
		"rev-parse HEAD^{tree}":           {out: "hooktree\n"},
		"rev-list --count base0000..HEAD": {out: "1\n"},
		"rev-parse HEAD~1":                {out: "base0000\n"},
		"log -1 --format=%an/%ae HEAD":    {out: mviaCommitAuthorName + "/" + mviaCommitAuthorEmail + "\n"},
	}}
	repo := &upsertRecorder{}
	existing := ledger.DeliveryRecord{RunID: "run-1", Status: "pending", TreeSHA: "snapshot"}

	c1, c1Tree, err := adoptRecordedC1Commit(context.Background(), repo, git, adoptTestRequest(), existing, "commit2222")
	if err != nil {
		t.Fatalf("adoptRecordedC1Commit: %v", err)
	}
	if c1 != "commit2222" || c1Tree != "hooktree" {
		t.Fatalf("adopted = (%q, %q), want (commit2222, hooktree)", c1, c1Tree)
	}
	if len(git.calls) != 4 {
		t.Fatalf("git calls = %v, want the tree lookup plus the three verification calls", git.calls)
	}
	if len(repo.records) != 1 || repo.records[0].TreeSHA != "hooktree" {
		t.Fatalf("re-upserted records = %+v, want one record carrying HEAD's real tree", repo.records)
	}
}

// TestAdoptRecordedC1CommitRefusesForeignCommit pins the refusal: a HEAD with
// a mismatched tree that is not the run's own single commit must never be
// adopted, and nothing is written to the ledger.
func TestAdoptRecordedC1CommitRefusesForeignCommit(t *testing.T) {
	git := &scriptedGit{t: t, script: map[string]scriptedResult{
		"rev-parse HEAD^{tree}":           {out: "hooktree\n"},
		"rev-list --count base0000..HEAD": {out: "2\n"},
	}}
	repo := &upsertRecorder{}
	existing := ledger.DeliveryRecord{RunID: "run-1", Status: "pending", TreeSHA: "snapshot"}

	_, _, err := adoptRecordedC1Commit(context.Background(), repo, git, adoptTestRequest(), existing, "commit2222")
	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v (%T), want a RefusalError for a foreign commit", err, err)
	}
	if len(repo.records) != 0 {
		t.Fatalf("upserts = %+v, want none on refusal", repo.records)
	}
}

// TestAdoptRecordedC1CommitTreeLookupFailureIsRecoverable pins that a git
// failure reading HEAD's tree stays a plain error, not a refusal: the run
// remains delivery_pending and can retry.
func TestAdoptRecordedC1CommitTreeLookupFailureIsRecoverable(t *testing.T) {
	boom := errors.New("git exploded")
	git := &scriptedGit{t: t, script: map[string]scriptedResult{
		"rev-parse HEAD^{tree}": {err: boom},
	}}
	repo := &upsertRecorder{}

	_, _, err := adoptRecordedC1Commit(context.Background(), repo, git, adoptTestRequest(), ledger.DeliveryRecord{}, "commit2222")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the git failure wrapped", err)
	}
	var refusal *RefusalError
	if errors.As(err, &refusal) {
		t.Fatal("a git execution failure must stay recoverable, not a refusal")
	}
	if len(repo.records) != 0 {
		t.Fatalf("upserts = %+v, want none when the tree lookup fails", repo.records)
	}
}
