package delivery

// The merge probe: the one merge-observation oracle both stack drivers
// (internal/cli/chat and internal/workflows/localengine) share. Before this
// file the two drivers carried sibling implementations of the same contract
// and only the CLI side honored the local-ancestor fast path, the pushed
// evidence gate, and the fail-closed unknown verdict. Extracted here so the
// contract has exactly one implementation (see .agents/memories/
// sibling-implementations-drift.md).
//
// The probe NEVER merges. It only answers "did the PR already land?".
// The merge action stays in prmerge.go and is called only by the CLI driver.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ErrMergeProbeUnavailable marks a merge verdict that no probe could answer.
// It never means "not merged": callers that must fail closed keep waiting,
// and callers that can tolerate an unknown verdict can tell it apart from a
// confident "not merged" (which is reported as merged=false with a nil error).
var ErrMergeProbeUnavailable = errors.New("merge probe unavailable")

// MergeProbe reports whether a PR is merged from durable git state and, when
// necessary, the remote PR state. Both stack drivers construct it with their
// own adapters; the verdict semantics are identical for both.
//
// wasPushed is the driver's durable pushed evidence for the run (a delivery
// record that reached pushed/succeeded with a commit SHA). Without it a
// missing remote ref only means "never pushed", not "merged".
type MergeProbe struct {
	Git GitRunner
	PR  PRClient
	GC  GitContext
}

// Merged reports whether the chunk's PR has landed. It requires durable
// pushed evidence (wasPushed) and a known head commit. A branch that was
// closed unmerged and then deleted must NOT read as merged.
//
// Order of probes: the local ancestor check answers normal and fast-forward
// merges without network; the remote PR state answers squash and rebase
// merges and pruned branches. Only when neither probe answers does it return
// an error wrapping ErrMergeProbeUnavailable, so the stack keeps waiting
// instead of completing on a guess.
func (p MergeProbe) Merged(ctx context.Context, headBranch, baseBranch, headCommit, repoSlug string, wasPushed bool) (bool, error) {
	if strings.TrimSpace(headBranch) == "" || strings.TrimSpace(baseBranch) == "" || strings.TrimSpace(headCommit) == "" || !wasPushed {
		return false, nil
	}

	// Fast, network-free check: is the pushed commit already in the base
	// branch? This covers normal and fast-forward merges.
	ancestor, localErr := p.localAncestor(ctx, baseBranch, headCommit)
	if ancestor {
		return true, nil
	}

	// The local answer is not final: it is either inconclusive (a squash or
	// rebase merge is not an ancestor) or unavailable (localErr). Ask the
	// remote host for the PR state.
	if strings.TrimSpace(repoSlug) == "" || p.PR == nil {
		if localErr != nil {
			// No probe answered. Do not report an unknown as "not merged".
			return false, probeUnavailable(localErr)
		}
		return false, nil
	}
	merged, err := p.PR.IsMerged(ctx, repoSlug, headBranch)
	if err != nil {
		return false, probeUnavailable(err)
	}
	return merged, nil
}

// localAncestor reports whether headCommit is already in the remote base
// branch. A (false, nil) result is git's confident "not an ancestor" (exit
// code 1). A non-nil error means the local probe could not answer at all:
// git exits 128 for a missing ref (the base branch was deleted after a squash
// merge and pruned) and for a missing commit (never fetched, or garbage
// collected). That case must fall through to the remote probe, which is the
// probe designed to answer it.
func (p MergeProbe) localAncestor(ctx context.Context, baseBranch, headCommit string) (bool, error) {
	if p.Git == nil {
		return false, nil
	}
	baseRef := "refs/remotes/origin/" + baseBranch
	if _, err := p.Git.Run(ctx, p.GC, "merge-base", "--is-ancestor", headCommit, baseRef); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// probeUnavailable wraps a probe failure so callers can match both the
// sentinel (ErrMergeProbeUnavailable) and the cause.
func probeUnavailable(err error) error {
	return fmt.Errorf("probe merge state: %w: %w", ErrMergeProbeUnavailable, err)
}

// ProbeRunMerged answers whether a run's PR merged, deriving every input from
// the durable ledger: the pushed head branch and commit SHA from the run's
// delivery records, the base ref from the run snapshot, and the repo slug
// from the recorded remote URL. Both stack drivers call this so a run's merge
// verdict cannot diverge between the in-process drive and the CLI drive.
//
// The local ancestor probe needs the base ref. A run snapshot without one
// (older engine snapshots) skips it and asks the remote PR state alone; the
// pushed-evidence gate still applies, so a never-pushed run cannot read as
// merged.
func ProbeRunMerged(ctx context.Context, repo workflowledger.Repository, run workflowledger.RunSnapshot, git GitRunner, pr PRClient, gc GitContext) (bool, error) {
	head, commit := runPushedRef(ctx, repo, run)
	if head == "" && run.WorktreeName != "" {
		head = "wf/" + run.WorktreeName
	}
	if head == "" {
		return false, nil
	}
	slug, _ := ParseOwnerRepo(run.RemoteURL)
	if slug == "" || pr == nil {
		return false, nil
	}
	probe := MergeProbe{Git: git, PR: pr, GC: gc}
	if strings.TrimSpace(run.BaseRef) != "" {
		return probe.Merged(ctx, head, run.BaseRef, commit, slug, commit != "")
	}
	// Unknown base ref: the local ancestor probe cannot run. The remote PR
	// state is the only probe that can answer; the pushed-evidence gate still
	// applies (a never-pushed run cannot read as merged), and an unknown
	// verdict stays an error so the caller keeps waiting.
	if commit == "" {
		return false, nil
	}
	merged, err := pr.IsMerged(ctx, slug, head)
	if err != nil {
		return false, probeUnavailable(err)
	}
	return merged, nil
}

// runPushedRef returns the pushed head branch and commit SHA from the run's
// delivery records. Both are empty when the run was never pushed.
func runPushedRef(ctx context.Context, repo workflowledger.Repository, run workflowledger.RunSnapshot) (head, commit string) {
	records, err := repo.ListDeliveries(ctx, run.RunID)
	if err != nil {
		return "", ""
	}
	for _, rec := range records {
		if rec.CommitSHA == "" {
			continue
		}
		switch rec.Status {
		case "pushed", "succeeded":
			return rec.HeadRef, rec.CommitSHA
		}
	}
	return "", ""
}
