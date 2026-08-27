package cliworktree

// worktree_lifecycle_seam_gap_test.go covers the register-fault branch of
// AdoptManagedWorktree behind the lifecycleRegisterAdoptedInstance seam: a
// store that refuses the adopted-instance registration after the marker write
// must remove the just-written marker and abandon the creation.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// TestLifecycleRegisterAdoptedFaultRemovesMarker covers the register-failure
// branch after a marker write: the adoption must surface the store fault,
// remove the just-written marker, and abandon the creation.
func TestLifecycleRegisterAdoptedFaultRemovesMarker(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	worktree, err := vcs.CreateWithPrefix(context.Background(), repo, "adopt-fault", "HEAD", "mivia/")
	if err != nil {
		t.Fatal(err)
	}
	// The adoption's create branch requires the legacy unbound route for the
	// worktree to be present before it may reserve the instance.
	if err := RegisterWorktreeRoute(repo, worktree); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("scripted register fault")
	original := lifecycleRegisterAdoptedInstance
	lifecycleRegisterAdoptedInstance = func(context.Context, *storage.SQLite, contextstate.Principal, contextstate.WorktreeInstance, string) error {
		return sentinel
	}
	t.Cleanup(func() { lifecycleRegisterAdoptedInstance = original })

	if _, err := AdoptManagedWorktree(repo, worktree); !errors.Is(err, sentinel) {
		t.Fatalf("AdoptManagedWorktree() error = %v, want the scripted register fault", err)
	}

	canonical, err := CanonicalMarkerRoot(worktree.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(WorktreeMarkerPath(canonical)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("marker still present after the failed registration: %v", statErr)
	}
}
