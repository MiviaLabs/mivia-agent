package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// stackedPolicy builds a delivery policy from a compiled workflow that carries
// a resolved stacking configuration, mirroring how the engine and CLI derive
// the policy for a stacking-enabled run. hard is the resolved HardLines value.
func stackedPolicy(t *testing.T, hard int) Policy {
	t.Helper()
	cw := &compiler.CompiledWorkflow{
		Delivery: &definition.Delivery{
			Kind:                  "pull_request",
			Mode:                  "draft",
			Provider:              "github",
			Base:                  "main",
			TitleTemplate:         "feat: {{ inputs.task }}",
			CommitMessageTemplate: "feat: {{ inputs.task }}\n\nBody.",
		},
		Stacking: &definition.StackingConfig{
			Enabled: true, PlanStep: "plan", ImplementStep: "implement",
			HardLines: hard,
		},
	}
	p, ok := FromCompiled(cw)
	if !ok {
		t.Fatal("FromCompiled ok = false for an active pull_request policy")
	}
	return p
}

// TestFromCompiledStackingHardLines pins the policy plumbing: a resolved
// stacking configuration flows its hard limit into Policy.StackingHardLines,
// and a workflow without one yields zero (no size gate).
func TestFromCompiledStackingHardLines(t *testing.T) {
	t.Run("resolved stacking config flows the hard limit", func(t *testing.T) {
		if p := stackedPolicy(t, 400); p.StackingHardLines != 400 {
			t.Fatalf("StackingHardLines = %d, want 400", p.StackingHardLines)
		}
	})
	t.Run("no stacking config yields zero", func(t *testing.T) {
		p, ok := FromCompiled(newCompiledPRWorkflow(t, "draft"))
		if !ok {
			t.Fatal("ok = false")
		}
		if p.StackingHardLines != 0 {
			t.Fatalf("StackingHardLines = %d, want 0 for a workflow without stacking", p.StackingHardLines)
		}
	})
}

// TestValidatePRBaseValid pins the valid branch-name shapes a pr_base may
// take: plain names, slashes, dots, underscores, and dashes are all legal.
func TestValidatePRBaseValid(t *testing.T) {
	for _, v := range []string{"main", "release/1.2", "feature_x", "v1.2.3", "a-b.c/d_e", "hotfix-2024"} {
		if err := ValidatePRBase(v); err != nil {
			t.Fatalf("ValidatePRBase(%q) = %v, want nil", v, err)
		}
	}
}

// TestParseStackPart pins the canonical k/N grammar: positive integers with
// k <= N. Zero, leading zeros, non-numeric, reversed, and overflowing values
// are repairable PRMetadataErrors.
func TestParseStackPart(t *testing.T) {
	if k, n, err := parseStackPart("3/12"); err != nil || k != 3 || n != 12 {
		t.Fatalf("parseStackPart(3/12) = %d, %d, %v; want 3, 12, nil", k, n, err)
	}
	for _, v := range []string{"0/12", "12/3", "03/12", "3", "x/y", "3/0", "3/12/1", "99999999999999999999/2", ""} {
		if _, _, err := parseStackPart(v); err == nil || !IsPRMetadataError(err) {
			t.Fatalf("parseStackPart(%q) err = %v, want a repairable PRMetadataError", v, err)
		}
	}
}

// TestAppendStackPartTitle pins the host-appended "[stack k/N]" tag: a
// single-line bracket suffix (the same derivedPRTitle convention
// EnsureFollowUpPublished uses for a deferred/split PR), an absent
// stack_part changes nothing, an invalid value is a PRMetadataError, and a
// result over GitHub's 256-rune ceiling is a PRMetadataError too (the agent
// must shorten the title).
func TestAppendStackPartTitle(t *testing.T) {
	t.Run("appends a single-line bracket tag", func(t *testing.T) {
		got, err := appendStackPartTitle("feat(agent): chunk three", "3/12")
		if err != nil {
			t.Fatalf("appendStackPartTitle: %v", err)
		}
		want := "feat(agent): chunk three [stack 3/12]"
		if got != want {
			t.Fatalf("title = %q, want %q", got, want)
		}
	})
	t.Run("absent stack_part leaves the title unchanged", func(t *testing.T) {
		got, err := appendStackPartTitle("feat: x", "")
		if err != nil {
			t.Fatalf("appendStackPartTitle: %v", err)
		}
		if got != "feat: x" {
			t.Fatalf("title = %q, want the unchanged title", got)
		}
	})
	t.Run("invalid stack_part is a repairable metadata error", func(t *testing.T) {
		if _, err := appendStackPartTitle("feat: x", "x/y"); err == nil || !IsPRMetadataError(err) {
			t.Fatalf("appendStackPartTitle err = %v, want a repairable PRMetadataError", err)
		}
	})
	t.Run("over the 256-rune ceiling is a repairable metadata error", func(t *testing.T) {
		_, err := appendStackPartTitle(strings.Repeat("a", MaxTitleRunes-1), "3/12")
		if err == nil || !IsPRMetadataError(err) {
			t.Fatalf("appendStackPartTitle err = %v, want a repairable PRMetadataError", err)
		}
		if !strings.Contains(err.Error(), "[stack 3/12]") {
			t.Fatalf("appendStackPartTitle err = %q, want a hint naming the tag", err)
		}
	})
}

// TestResolveStackingInputs pins the input resolution: pr_base overrides the
// policy base, absent inputs change nothing, and invalid values are
// repairable PRMetadataErrors.
func TestResolveStackingInputs(t *testing.T) {
	t.Run("pr_base overrides the policy base", func(t *testing.T) {
		req := Request{Policy: defaultPolicy("draft"), Inputs: map[string]string{"pr_base": "release"}}
		got, err := resolveStackingInputs(req)
		if err != nil {
			t.Fatalf("resolveStackingInputs: %v", err)
		}
		if got.Policy.Base != "release" {
			t.Fatalf("Policy.Base = %q, want the pr_base override release", got.Policy.Base)
		}
	})
	t.Run("absent inputs leave the request unchanged", func(t *testing.T) {
		req := Request{Policy: defaultPolicy("draft"), Inputs: map[string]string{"task": "x"}}
		got, err := resolveStackingInputs(req)
		if err != nil {
			t.Fatalf("resolveStackingInputs: %v", err)
		}
		if got.Policy.Base != "main" {
			t.Fatalf("Policy.Base = %q, want the unchanged default main", got.Policy.Base)
		}
	})
	t.Run("invalid inputs are repairable metadata errors", func(t *testing.T) {
		for _, input := range []map[string]string{
			{"pr_base": "a..b"},
			{"pr_base": "-evil"},
			{"stack_part": "0/12"},
			{"stack_part": "x/y"},
		} {
			req := Request{Policy: defaultPolicy("draft"), Inputs: input}
			if _, err := resolveStackingInputs(req); err == nil || !IsPRMetadataError(err) {
				t.Fatalf("resolveStackingInputs(%v) err = %v, want a repairable PRMetadataError", input, err)
			}
		}
	})
}

// TestNumstatSize pins the added+deleted sum: renames and binary entries
// contribute nothing, and a malformed line fails closed instead of
// under-counting the diff.
func TestNumstatSize(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int
	}{
		{"empty", "", 0},
		{"added and deleted", "5\t3\tfile.go\n", 8},
		{"multiple files", "1\t0\ta.go\n0\t2\tb.go\n", 3},
		{"rename contributes nothing", "0\t0\ta.go => b.go\n", 0},
		{"binary contributes nothing", "-\t-\tblob.bin\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := numstatSize(tc.out)
			if err != nil {
				t.Fatalf("numstatSize: %v", err)
			}
			if got != tc.want {
				t.Fatalf("numstatSize(%q) = %d, want %d", tc.out, got, tc.want)
			}
		})
	}
	t.Run("malformed lines fail closed", func(t *testing.T) {
		for _, out := range []string{"garbage\n", "1\tx\tf.go\n", "onlyone\n"} {
			if _, err := numstatSize(out); err == nil {
				t.Fatalf("numstatSize(%q) = nil error, want a parse failure", out)
			}
		}
	})
}

// TestStackingContractPRBaseValid: a valid pr_base overrides the workflow's
// default PR base end to end - the PR is created against it, the result and
// the durable delivery record both name it.
func TestStackingContractPRBaseValid(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// The override base must exist on the admitted remote: push a "release"
	// branch at the base commit.
	runGit(t, repoRoot, "branch", "release", baseCommit)
	runGit(t, repoRoot, "push", "-u", "origin", "release")
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x", "pr_base": "release"})
	res, err := Deliver(ctx, repo, RealGit{}, pr, req)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" || res.BaseRef != "release" {
		t.Fatalf("Result = %+v, want succeeded with BaseRef release", res)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	if c := pr.created[0]; c.Base != "release" {
		t.Fatalf("PR base = %q, want the pr_base override release", c.Base)
	}
	rec := deliveryRecordByKey(t, repo, run)
	if rec.BaseRef != "release" {
		t.Fatalf("record BaseRef = %q, want release", rec.BaseRef)
	}
}

// TestStackingContractPRBaseInvalid: every invalid pr_base shape is rejected
// with a repairable PRMetadataError whose hint names the problem, before any
// PR create, delivery record, or push - so the repair loop can fix it.
func TestStackingContractPRBaseInvalid(t *testing.T) {
	cases := []struct {
		name, value, wantHint string
	}{
		{"empty", "", "pr_base is empty"},
		{"leading dash", "-evil", "must not start with '-'"},
		{"dotdot", "a..b", "must not contain '..'"},
		{"space", "bad name", "characters outside [A-Za-z0-9._/-]"},
		{"control", "bad\nname", "characters outside [A-Za-z0-9._/-]"},
		{"too long", strings.Repeat("x", 101), "exceeding the 100-character limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
			writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
			pr := &fakePRClient{}
			req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x", "pr_base": tc.value})
			_, err := Deliver(ctx, repo, RealGit{}, pr, req)
			if err == nil || !IsPRMetadataError(err) {
				t.Fatalf("Deliver err = %v, want a repairable PRMetadataError", err)
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("Deliver err = %q, want a hint containing %q", err, tc.wantHint)
			}
			assertZeroCreates(t, pr)
			assertNoRecord(t, repo, run)
			assertNoBranchOnOrigin(t, repoRoot, originURL)
		})
	}
}

// TestStackingContractStackPartTrailer: a stack_part input appends a
// "[stack k/N]" tag to the PR title for both the agent-provided title and the
// legacy template fallback.
func TestStackingContractStackPartTrailer(t *testing.T) {
	t.Run("agent title carries the tag", func(t *testing.T) {
		ctx := context.Background()
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		seedChangeSummary(t, repo, run, "implement", 1, `{"pr_title": "feat(agent): chunk three", "pr_summary": "Adds the chunk."}`)
		pr := &fakePRClient{}
		req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x", "stack_part": "3/12"})
		res, err := Deliver(ctx, repo, RealGit{}, pr, req)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded", res)
		}
		if n := pr.createdCount(); n != 1 {
			t.Fatalf("Create calls = %d, want 1", n)
		}
		want := "feat(agent): chunk three [stack 3/12]"
		if got := pr.created[0].Title; got != want {
			t.Fatalf("Title = %q, want %q", got, want)
		}
	})
	t.Run("legacy template title carries the tag", func(t *testing.T) {
		ctx := context.Background()
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		pr := &fakePRClient{}
		req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x", "stack_part": "1/1"})
		res, err := Deliver(ctx, repo, RealGit{}, pr, req)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded", res)
		}
		want := "feat: x [stack 1/1]"
		if got := pr.created[0].Title; got != want {
			t.Fatalf("Title = %q, want %q", got, want)
		}
	})
}

// TestStackingContractStackPartInvalid: a malformed stack_part is rejected
// with a repairable PRMetadataError naming the problem, before any PR create,
// delivery record, or push.
func TestStackingContractStackPartInvalid(t *testing.T) {
	for _, value := range []string{"0/12", "12/3", "x/y", "3", "03/12", "3/0", ""} {
		t.Run(value, func(t *testing.T) {
			ctx := context.Background()
			repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
			writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
			pr := &fakePRClient{}
			req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x", "stack_part": value})
			_, err := Deliver(ctx, repo, RealGit{}, pr, req)
			if err == nil || !IsPRMetadataError(err) {
				t.Fatalf("Deliver err = %v, want a repairable PRMetadataError", err)
			}
			if !strings.Contains(err.Error(), "stack_part") {
				t.Fatalf("Deliver err = %q, want a hint naming stack_part", err)
			}
			assertZeroCreates(t, pr)
			assertNoRecord(t, repo, run)
			assertNoBranchOnOrigin(t, repoRoot, originURL)
		})
	}
}

// TestStackingContractSizeGate: the actual-diff-size gate rejects a chunk
// whose added+deleted lines exceed the resolved hard limit with the exact
// delivery hint, and passes at or under the limit. A workflow without a
// stacking configuration is unchanged: a huge diff publishes with no gate.
func TestStackingContractSizeGate(t *testing.T) {
	t.Run("over the hard limit rejects with the exact hint", func(t *testing.T) {
		ctx := context.Background()
		repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "big.txt", strings.Repeat("line of code\n", 500))
		pr := &fakePRClient{}
		req := newRequest(run, gc, baseCommit, originURL, stackedPolicy(t, 400), map[string]string{"task": "x"})
		_, err := Deliver(ctx, repo, RealGit{}, pr, req)
		if err == nil || !IsDiffSizeError(err) {
			t.Fatalf("Deliver err = %v, want a repairable DiffSizeError", err)
		}
		if !strings.Contains(err.Error(), "chunk diff size 500 exceeds hard limit 400") {
			t.Fatalf("Deliver err = %q, want the exact size hint", err)
		}
		assertZeroCreates(t, pr)
		assertNoRecord(t, repo, run)
		assertNoBranchOnOrigin(t, repoRoot, originURL)
	})

	t.Run("at the hard limit passes", func(t *testing.T) {
		ctx := context.Background()
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "exact.txt", strings.Repeat("line of code\n", 400))
		pr := &fakePRClient{}
		req := newRequest(run, gc, baseCommit, originURL, stackedPolicy(t, 400), map[string]string{"task": "x"})
		res, err := Deliver(ctx, repo, RealGit{}, pr, req)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded", res)
		}
		if n := pr.createdCount(); n != 1 {
			t.Fatalf("Create calls = %d, want 1", n)
		}
	})

	t.Run("under the hard limit passes", func(t *testing.T) {
		ctx := context.Background()
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "small.txt", strings.Repeat("line of code\n", 10))
		pr := &fakePRClient{}
		req := newRequest(run, gc, baseCommit, originURL, stackedPolicy(t, 400), map[string]string{"task": "x"})
		res, err := Deliver(ctx, repo, RealGit{}, pr, req)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded", res)
		}
		if n := pr.createdCount(); n != 1 {
			t.Fatalf("Create calls = %d, want 1", n)
		}
	})

	t.Run("no stacking config is unchanged", func(t *testing.T) {
		ctx := context.Background()
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "huge.txt", strings.Repeat("line of code\n", 500))
		pr := &fakePRClient{}
		req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})
		res, err := Deliver(ctx, repo, RealGit{}, pr, req)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded with no size gate", res)
		}
		if n := pr.createdCount(); n != 1 {
			t.Fatalf("Create calls = %d, want 1", n)
		}
	})
}

// TestStackingContractSizeGateIgnoresRenamesAndWhitespace pins the size
// accounting: a pure rename and a whitespace-only change measure zero lines,
// so they pass even a tiny hard limit (the delivered PR is still created).
func TestStackingContractSizeGateIgnoresRenamesAndWhitespace(t *testing.T) {
	t.Run("pure rename counts zero lines", func(t *testing.T) {
		ctx := context.Background()
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		runGit(t, worktreeRoot, "mv", "a.txt", "renamed.txt")
		pr := &fakePRClient{}
		req := newRequest(run, gc, baseCommit, originURL, stackedPolicy(t, 1), map[string]string{"task": "x"})
		res, err := Deliver(ctx, repo, RealGit{}, pr, req)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded for a zero-line rename", res)
		}
		if n := pr.createdCount(); n != 1 {
			t.Fatalf("Create calls = %d, want 1", n)
		}
	})

	t.Run("pure whitespace change counts zero lines", func(t *testing.T) {
		ctx := context.Background()
		_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "a.txt", "base   \n")
		pr := &fakePRClient{}
		req := newRequest(run, gc, baseCommit, originURL, stackedPolicy(t, 1), map[string]string{"task": "x"})
		res, err := Deliver(ctx, repo, RealGit{}, pr, req)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded for a whitespace-only change", res)
		}
		if n := pr.createdCount(); n != 1 {
			t.Fatalf("Create calls = %d, want 1", n)
		}
	})
}
