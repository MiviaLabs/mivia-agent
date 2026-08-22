package ledger

// coverage_gaps_test.go covers the remaining uncovered statements in
// agenttools_types.go (UnsetRepoFactory), service.go (Events on an unknown
// run) and store.go (the conflicting-payload branch of appendEvent).

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUnsetRepoFactoryReturnsErrRepoUnset(t *testing.T) {
	repo, closeFn, err := UnsetRepoFactory(context.Background())
	if repo != nil {
		t.Fatalf("UnsetRepoFactory repo = %v, want nil", repo)
	}
	if !errors.Is(err, ErrRepoUnset) {
		t.Fatalf("UnsetRepoFactory error = %v, want ErrRepoUnset", err)
	}
	closeFn()
}

func TestServiceEventsUnknownRunIsNotFound(t *testing.T) {
	svc, err := NewService(ServiceOptions{Repo: func(context.Context) (Repository, func(), error) {
		return NewMemoryRepository(), func() {}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Events(context.Background(), "no-such-run", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Events(unknown run) error = %v, want not found", err)
	}
}

func TestMarshalAndAppendConflictingPayloadIsTaskConflict(t *testing.T) {
	s := NewMemoryStore()
	if err := s.marshalAndAppend("run-x", "evt-1", "kind", map[string]int{"a": 1}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Same event ID, different payload: the logical key was taken, so the
	// retry must surface ErrTaskConflict rather than silently succeed.
	err := s.marshalAndAppend("run-x", "evt-1", "kind", map[string]int{"a": 2})
	if !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("second append error = %v, want ErrTaskConflict", err)
	}
}
