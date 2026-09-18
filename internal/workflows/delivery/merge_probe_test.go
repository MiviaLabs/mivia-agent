package delivery

import (
	"context"
	"errors"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type mergeProbeTestGit struct{ err error }

func (g mergeProbeTestGit) Run(context.Context, GitContext, ...string) (string, error) {
	return "", g.err
}

type mergeProbeTestPR struct {
	merged bool
	err    error
}

func (p mergeProbeTestPR) FindByHead(context.Context, string, string) (*PRRef, error) {
	return nil, nil
}
func (p mergeProbeTestPR) IsMerged(context.Context, string, string) (bool, error) {
	return p.merged, p.err
}
func (p mergeProbeTestPR) Create(context.Context, string, PRInput) (PRRef, error) {
	return PRRef{}, nil
}

func TestMergeProbeFallsBackAndFailsClosed(t *testing.T) {
	probe := MergeProbe{PR: mergeProbeTestPR{merged: true}}
	merged, err := probe.Merged(context.Background(), "head", "main", "commit", "owner/repo", true)
	if err != nil || !merged {
		t.Fatalf("remote fallback = %v, %v", merged, err)
	}
	probe.Git = mergeProbeTestGit{err: errors.New("missing ref")}
	probe.PR = nil
	_, err = probe.Merged(context.Background(), "head", "main", "commit", "", true)
	if !errors.Is(err, ErrMergeProbeUnavailable) {
		t.Fatalf("unavailable local probe = %v", err)
	}
	merged, err = (MergeProbe{}).Merged(context.Background(), "head", "main", "commit", "owner/repo", true)
	if err != nil || merged {
		t.Fatalf("no probes = %v, %v", merged, err)
	}
}

func TestProbeRunMergedUsesPushedEvidenceAndRemoteFallback(t *testing.T) {
	_, _, gc, _, _, run, repo := newDeliveryFixture(t)
	run.BaseRef = ""
	if merged, err := ProbeRunMerged(context.Background(), repo, run, nil, mergeProbeTestPR{merged: true}, gc); err != nil || merged {
		t.Fatalf("unpublished run = %v, %v", merged, err)
	}
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "probe-record", Status: "pushed", HeadRef: "wf/wt-test", CommitSHA: "abc",
	}); err != nil {
		t.Fatal(err)
	}
	run.RemoteURL = "https://github.com/owner/repo.git"
	merged, err := ProbeRunMerged(context.Background(), repo, run, nil, mergeProbeTestPR{merged: true}, gc)
	if err != nil || !merged {
		t.Fatalf("remote fallback = %v, %v", merged, err)
	}
	_, err = ProbeRunMerged(context.Background(), repo, run, nil, mergeProbeTestPR{err: errors.New("offline")}, gc)
	if !errors.Is(err, ErrMergeProbeUnavailable) {
		t.Fatalf("remote error = %v", err)
	}
}
