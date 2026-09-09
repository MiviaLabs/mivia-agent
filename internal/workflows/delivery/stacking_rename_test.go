package delivery

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// bigBody returns n numbered lines, so a fixture can create a file whose diff
// size is known exactly.
func bigBody(prefix string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s line %d\n", prefix, i)
	}
	return b.String()
}

// seedDetectedRename stages a rename that git's similarity index actually
// reports as a rename: the file moves and only `edits` of its `total` lines
// change. Both extremes are useless for these tests - a pure rename has a zero
// line delta and never reaches the deferral loop (which is why the existing
// rename test missed this bug), and a fully rewritten file is not detected as
// a rename at all. It returns the base commit the diff is measured against,
// and fails the test if git does not in fact report a rename.
func seedDetectedRename(t *testing.T, ctx context.Context, worktreeRoot string, gc GitContext, src, dst string, total, edits int, extra map[string]string) string {
	t.Helper()
	writeWorktreeFile(t, worktreeRoot, src, bigBody("keep", total))
	runGit(t, worktreeRoot, "add", "-A")
	runGit(t, worktreeRoot, "commit", "-m", "seed the file that will be renamed")
	base := runGitOut(t, worktreeRoot, "rev-parse", "HEAD")

	runGit(t, worktreeRoot, "mv", src, dst)
	edited := bigBody("keep", total)
	for i := 0; i < edits; i++ {
		edited = strings.Replace(edited, fmt.Sprintf("keep line %d\n", i), fmt.Sprintf("edited line %d\n", i), 1)
	}
	writeWorktreeFile(t, worktreeRoot, dst, edited)
	for name, body := range extra {
		writeWorktreeFile(t, worktreeRoot, name, body)
	}

	// Guard the fixture itself: without a detected rename these tests would
	// pass vacuously and prove nothing.
	staged, err := RealGit{}.Run(ctx, gc, "-c", "core.fsmonitor=false", "add", "-A")
	if err != nil {
		t.Fatalf("stage fixture: %v (%s)", err, staged)
	}
	probe, err := RealGit{}.Run(ctx, gc, "diff", "--cached", "--numstat", "--find-renames", base)
	if err != nil {
		t.Fatalf("numstat probe: %v", err)
	}
	if !strings.Contains(probe, "=>") {
		t.Fatalf("fixture did not produce a detected rename; numstat was:\n%s", probe)
	}
	return base
}

// TestNumstatPathsAreIndividuallyStageable is the root regression.
//
// Git's rename detection is ON BY DEFAULT (diff.renames), so omitting
// --find-renames does not turn it off. A renamed file then arrives in numstat
// as the pseudo-path "old => new", which is not a path: it cannot be staged,
// it cannot be reset, and as an exclude pathspec git silently ignores it.
// computeDeterministicSplit read that pseudo-path as fields[2], sorted it
// largest-first (it carries the summed count), deferred it first, and then
// failed its own verify - reporting "exceeds hard limit even after deferring
// 1 file(s); shrink the chunk" for a chunk no agent could shrink.
//
// Every path the splitter emits must be one git names on its own.
func TestNumstatPathsAreIndividuallyStageable(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, _, _, _, _ := newDeliveryFixture(t)

	base := seedDetectedRename(t, ctx, worktreeRoot, gc, "renamed_src.txt", "renamed_dst.txt", 200, 20,
		map[string]string{"big.txt": bigBody("big", 300)})

	deferred, kept, err := computeDeterministicSplit(ctx, RealGit{}, gc, base, 100)
	if err != nil {
		t.Fatalf("computeDeterministicSplit: %v", err)
	}
	nameOnly := runGitOut(t, worktreeRoot, "diff", "--cached", "--name-only", "--no-renames", base)
	real := map[string]bool{}
	for _, p := range strings.Split(nameOnly, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			real[p] = true
		}
	}
	got := append(append([]string{}, deferred...), kept...)
	if len(got) == 0 {
		t.Fatal("computeDeterministicSplit returned no paths for an oversized diff")
	}
	for _, p := range got {
		if strings.Contains(p, "=>") {
			t.Fatalf("split produced the numstat rename pseudo-path %q, which git cannot stage", p)
		}
		if !real[p] {
			t.Fatalf("split produced path %q, which is not one of git's changed paths %v", p, real)
		}
	}
}

// TestMeasureAndSplitAgreeOnRenames pins the contract MeasureChunkDiffSize
// documents: the split and the gate that verifies it must measure the SAME
// diff. They disagreed - the gate passed --find-renames while the split relied
// on the default - so a detected rename made verify reject the split.
func TestMeasureAndSplitAgreeOnRenames(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, _, _, _, _ := newDeliveryFixture(t)

	base := seedDetectedRename(t, ctx, worktreeRoot, gc, "moved_src.txt", "moved_dst.txt", 120, 12, nil)

	measured, err := MeasureChunkDiffSize(ctx, RealGit{}, gc, base, 1_000_000, nil)
	if err != nil {
		t.Fatalf("MeasureChunkDiffSize: %v", err)
	}
	out, err := RealGit{}.Run(ctx, gc, "-c", "core.quotePath=false", "diff", "--cached",
		"--no-ext-diff", "--no-textconv", "--numstat", "-z",
		"--find-renames", "--ignore-all-space", base)
	if err != nil {
		t.Fatalf("numstat: %v", err)
	}
	_, splitTotal, err := numstatPerFile(out)
	if err != nil {
		t.Fatalf("numstatPerFile: %v", err)
	}
	if measured != splitTotal {
		t.Fatalf("MeasureChunkDiffSize = %d but the split measures %d; the gate and the splitter must agree", measured, splitTotal)
	}
}

// TestSplitDefersRenamePlusOneMoreFile is the "keep at least one file" guard
// regression. That guard must count deferred FILE RECORDS, never deferred
// PATHS: a detected rename contributes two paths (old and new) for one
// record, so counting paths makes the guard fire one record early whenever a
// rename is among the deferred files.
//
// The fixture reproduces the exact trace that broke it: four files sized
// 50, 30, 15, 5 (total 100) with hard=15. The largest file is a detected
// rename (2 paths). Deferring it plus the second-largest file is not enough
// to fit under hard (100-50-30=20 > 15), so a correct split must also defer
// the third file (100-50-30-15=5 <= 15), leaving the smallest file kept.
// The path-counting guard instead stops after only two records are
// deferred, because by then len(deferred) (3 paths: 2 from the rename, 1
// from the second file) already equals len(files)-1 (3) - one record short
// of what the trace needs, so it returns kept=20 > hard and the caller sees
// no split at all.
func TestSplitDefersRenamePlusOneMoreFile(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, _, _, _, _ := newDeliveryFixture(t)

	const hard = 15
	base := seedDetectedRename(t, ctx, worktreeRoot, gc, "renamed.txt", "renamed_moved.txt", 200, 25, map[string]string{
		"file30.txt": bigBody("thirty", 30),
		"file15.txt": bigBody("fifteen", 15),
		"file5.txt":  bigBody("five", 5),
	})

	deferred, kept, err := computeDeterministicSplit(ctx, RealGit{}, gc, base, hard)
	if err != nil {
		t.Fatalf("computeDeterministicSplit: %v", err)
	}
	if len(deferred) == 0 || len(kept) == 0 {
		t.Fatalf("computeDeterministicSplit returned no split (deferred=%v kept=%v); a valid split exists that keeps file5.txt under hard=%d", deferred, kept, hard)
	}
	if len(kept) != 1 || kept[0] != "file5.txt" {
		t.Fatalf("kept = %v, want exactly [\"file5.txt\"]: the smallest file is the only one that fits under hard=%d once the rename and file30.txt are deferred", kept, hard)
	}
}

// TestExcludePathspecActuallyExcludes proves the deferral exclusion is real
// for a renamed file. A pathspec git ignores is worse than one that errors:
// the verify step still counts the deferred file, so verified > hard and the
// split is rejected no matter what the splitter chose.
func TestExcludePathspecActuallyExcludes(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, _, _, _, _ := newDeliveryFixture(t)

	base := seedDetectedRename(t, ctx, worktreeRoot, gc, "src.txt", "dst.txt", 150, 15,
		map[string]string{"small.txt": "one line\n"})

	full, err := MeasureChunkDiffSize(ctx, RealGit{}, gc, base, 1_000_000, nil)
	if err != nil {
		t.Fatalf("MeasureChunkDiffSize(nil): %v", err)
	}
	excluded, err := MeasureChunkDiffSize(ctx, RealGit{}, gc, base, 1_000_000, []string{"dst.txt", "src.txt"})
	if err != nil {
		t.Fatalf("MeasureChunkDiffSize(exclude the rename): %v", err)
	}
	if excluded >= full {
		t.Fatalf("excluding the renamed file measured %d, not less than the full %d: the exclusion did not apply", excluded, full)
	}
}
