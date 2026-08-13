package delivery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// scriptedGHRunner records gh calls and fails on demand, so merge flows are
// testable without a real host.
type scriptedGHRunner struct {
	mu            sync.Mutex
	ready         []string
	merges        []string
	checks        int
	firstMergeErr error
	checkErr      error
}

func (s *scriptedGHRunner) run(_ context.Context, op string, args ...string) ([]byte, error) {
	if len(args) < 2 || args[0] != "pr" {
		return nil, errors.New("unexpected gh argv: " + strings.Join(args, " "))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch args[1] {
	case "ready":
		s.ready = append(s.ready, args[2])
		return []byte("marked ready\n"), nil
	case "merge":
		s.merges = append(s.merges, args[2])
		if s.firstMergeErr != nil && len(s.merges) == 1 {
			return nil, s.firstMergeErr
		}
		return []byte("merged\n"), nil
	case "checks":
		s.checks++
		if s.checkErr != nil {
			// Match runGH's real contract: nil out on error, the message
			// only lives in the wrapped error string.
			return nil, s.checkErr
		}
		return []byte("checks passed\n"), nil
	}
	return nil, errors.New("unexpected gh pr argv: " + strings.Join(args, " "))
}

func TestMergePullRequestDraftMarksReadyThenMerges(t *testing.T) {
	prev := ghRun
	ghRun = (&scriptedGHRunner{}).run
	t.Cleanup(func() { ghRun = prev })

	fake := &scriptedGHRunner{}
	ghRun = fake.run
	if err := MergePullRequest(context.Background(), "o/r", "42", true); err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if len(fake.ready) != 1 || fake.ready[0] != "42" {
		t.Errorf("draft PR was not marked ready: ready=%v", fake.ready)
	}
	if len(fake.merges) != 1 || fake.merges[0] != "42" {
		t.Errorf("PR was not merged: merges=%v", fake.merges)
	}
	if fake.checks != 0 {
		t.Errorf("checks watched %d times; want 0 (merge succeeded immediately)", fake.checks)
	}
}

func TestMergePullRequestNonDraftSkipsReady(t *testing.T) {
	fake := &scriptedGHRunner{}
	prev := ghRun
	ghRun = fake.run
	t.Cleanup(func() { ghRun = prev })

	if err := MergePullRequest(context.Background(), "o/r", "7", false); err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if len(fake.ready) != 0 {
		t.Errorf("non-draft PR was marked ready: %v", fake.ready)
	}
	if len(fake.merges) != 1 || fake.merges[0] != "7" {
		t.Errorf("PR was not merged: merges=%v", fake.merges)
	}
}

func TestMergePullRequestWaitsChecksThenRetries(t *testing.T) {
	fake := &scriptedGHRunner{firstMergeErr: errors.New("merge PR 9: 1/1 checks pending")}
	prev := ghRun
	ghRun = fake.run
	t.Cleanup(func() { ghRun = prev })

	if err := MergePullRequest(context.Background(), "o/r", "9", false); err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if fake.checks != 1 {
		t.Errorf("checks watched %d times; want 1", fake.checks)
	}
	if len(fake.merges) != 2 {
		t.Errorf("merge attempts = %d; want 2 (refused, then retried)", len(fake.merges))
	}
}

func TestMergePullRequestNoChecksReportedIsGreen(t *testing.T) {
	fake := &scriptedGHRunner{
		firstMergeErr: errors.New("merge PR 5: 1/1 checks pending"),
		checkErr:      errors.New("no checks reported on the 'abc123' branch"),
	}
	prev := ghRun
	ghRun = fake.run
	t.Cleanup(func() { ghRun = prev })

	if err := MergePullRequest(context.Background(), "o/r", "5", false); err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if len(fake.merges) != 2 {
		t.Errorf("merge attempts = %d; want 2", len(fake.merges))
	}
}

func TestMergePullRequestRedChecksSurface(t *testing.T) {
	fake := &scriptedGHRunner{
		firstMergeErr: errors.New("merge PR 3: 1/1 checks pending"),
		checkErr:      errors.New("X failed — 1/1 checks failed"),
	}
	prev := ghRun
	ghRun = fake.run
	t.Cleanup(func() { ghRun = prev })

	err := MergePullRequest(context.Background(), "o/r", "3", false)
	if err == nil {
		t.Fatal("MergePullRequest succeeded; want red-check error")
	}
	if !strings.Contains(err.Error(), "checks failed") {
		t.Errorf("error = %v; want the failed-check detail", err)
	}
}
