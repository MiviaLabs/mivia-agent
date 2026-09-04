package clichat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

// TestContextWorkspaceID_MatchesWorktreerouteLeaf guards the one identity
// catalog rows key on: internal/cli's private hash and the shared leaf's
// exported copy must stay byte-identical, or previously stored sessions
// strand under a workspace id nothing lists anymore. Covers the inputs the
// hash treats specially - relative paths, symlinks, trailing separators.
func TestContextWorkspaceID_MatchesWorktreerouteLeaf(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "repo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "repo-link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}

	inputs := []string{".", dir, dir + string(filepath.Separator), link}
	for _, in := range inputs {
		if got, want := contextWorkspaceID(in), worktreeroute.WorkspaceID(in); got != want {
			t.Errorf("WorkspaceID(%q) drift: cli=%q leaf=%q", in, got, want)
		}
	}

	// Golden value: parity alone cannot catch a COORDINATED change to both
	// copies, which strands every stored chat_sessions row exactly the way
	// this test's doc comment warns. The input does not exist, so Abs is a
	// pure join and EvalSymlinks keeps the resolved path - the digest is
	// stable across machines: sha256("/nonexistent-mivia-golden-fixture").
	const goldenIn = "/nonexistent-mivia-golden-fixture"
	const golden = "workspace-fe5715e77b011c8c"
	if got := worktreeroute.WorkspaceID(goldenIn); got != golden {
		t.Errorf("WorkspaceID(%q) = %q, want the pinned golden %q - changing the digest scheme strands every stored session keyed on the old ids", goldenIn, got, golden)
	}
	if got := contextWorkspaceID(goldenIn); got != golden {
		t.Errorf("contextWorkspaceID(%q) = %q, want the pinned golden %q", goldenIn, got, golden)
	}
}
