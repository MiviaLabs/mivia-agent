package delivery

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// guardDeferredSplitConsistency refuses a deferred-file split that would
// separate a file from its test companion: deferring a file's tests while
// keeping the code (or vice versa) ships a delivered commit that fails the
// repository's own test gate for reasons the evidence gates never saw - the
// observed delivery-repair death loop, where the repair agent kept reverting
// production code to satisfy the delivered commit's stale test expectations.
// The repair agent declared this split (InputDeferredFiles), so refuse it
// with a repairable DiffSizeError naming the pair instead of committing an
// inconsistent delivered commit. The guard mirrors the automatic split's
// guard in checkChunkDiffSize, which uses the same companion rules on the
// host-computed deferred/kept partition.
func guardDeferredSplitConsistency(ctx context.Context, git GitRunner, req Request, deferred []string) error {
	allStaged, err := stagedPaths(ctx, git, req.GitCtx)
	if err != nil {
		return err
	}
	if dPath, kPath := deferredSplitSeparatesCompanion(deferred, subtractPaths(allStaged, deferred)); kPath != "" {
		return &DiffSizeError{Reason: fmt.Sprintf("delivery: deferred_files separates %s from its test companion %s; include both in the delivered commit or both in deferred_files so the delivered commit stays internally consistent (a delivered commit that fails its own tests cannot be published)", dPath, kPath)}
	}
	return nil
}

// deferredSplitSeparatesCompanion reports the first file a split separates
// from its test companion: a path in deferred whose same-directory companion
// (under common test-file naming conventions) stays in kept, or a path in
// kept whose companion is deferred. Splitting a file from its tests ships a
// delivered commit that can fail the repository's own test gate (the
// pre-push hook) for reasons the workflow's evidence gates never saw - the
// observed delivery-repair death loop. It returns the two paths, or "" when
// the split keeps every file with its companion. Language-agnostic by
// construction: it matches the widely used *_test, *.test, *_spec, *.spec
// suffixes and test_/Test prefixes, never a specific language's tooling.
func deferredSplitSeparatesCompanion(deferred, kept []string) (deferredPath, keptPath string) {
	keptSet := make(map[string]struct{}, len(kept))
	for _, p := range kept {
		keptSet[p] = struct{}{}
	}
	deferredSet := make(map[string]struct{}, len(deferred))
	for _, p := range deferred {
		deferredSet[p] = struct{}{}
	}
	for _, d := range deferred {
		for _, c := range testCompanions(d) {
			if _, ok := keptSet[c]; ok {
				return d, c
			}
		}
	}
	for _, k := range kept {
		for _, c := range testCompanions(k) {
			if _, ok := deferredSet[c]; ok {
				return c, k
			}
		}
	}
	return "", ""
}

// testCompanions returns the same-directory paths a common test-file naming
// convention pairs with path: a test file maps back to its single source
// (fts_test.go -> fts.go, test_fts.py -> fts.py, fts.spec.ts -> fts.ts); a
// source file maps to its candidate test forms. The companion set is
// deliberately a superset of any one language's convention so the split guard
// fails safe (refuses the split) rather than shipping an internally
// inconsistent delivered commit.
func testCompanions(path string) []string {
	dir, base := filepath.Split(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	// A file that IS a test maps back to its single source companion.
	for _, m := range []string{"_test", ".test", "_spec", ".spec"} {
		if strings.HasSuffix(stem, m) && len(stem) > len(m) {
			return []string{dir + strings.TrimSuffix(stem, m) + ext}
		}
	}
	for _, m := range []string{"test_", "Test"} {
		if strings.HasPrefix(stem, m) && len(stem) > len(m) {
			return []string{dir + stem[len(m):] + ext}
		}
	}
	// A source file maps to its candidate test forms.
	var out []string
	for _, m := range []string{"_test", ".test", "_spec", ".spec"} {
		out = append(out, dir+stem+m+ext)
	}
	for _, m := range []string{"test_", "Test"} {
		out = append(out, dir+m+stem+ext)
	}
	return out
}

// subtractPaths returns paths minus remove (order-preserving).
func subtractPaths(paths, remove []string) []string {
	set := make(map[string]struct{}, len(remove))
	for _, p := range remove {
		set[p] = struct{}{}
	}
	var out []string
	for _, p := range paths {
		if _, ok := set[p]; !ok {
			out = append(out, p)
		}
	}
	return out
}
