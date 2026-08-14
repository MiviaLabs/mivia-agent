// Stacking delivery: reserved chunk-mode inputs (pr_base, stack_part) and the
// actual-diff-size gate. The controller and driver inject pr_base and
// stack_part on chunk-mode runs; delivery honors them when present and rejects
// invalid values with a repairable PRMetadataError, so the delivery repair
// loop receives a delivery hint naming the problem and fixes it before the
// next attempt. An over-limit delivered diff is a repairable DiffSizeError
// (deliberately NOT a PRMetadataError: metadata edits cannot shrink a diff,
// so repair routing must send it to a step that edits the worktree). Absent
// inputs leave single-PR delivery unchanged.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// Reserved stacking input names (plan D3). The controller and driver inject
// these on chunk-mode runs; delivery honors pr_base and stack_part when
// present.
const (
	InputPRBase    = "pr_base"
	InputStackPart = "stack_part"
	// InputDeferredFiles carries the diff-size gate's own split decision
	// (§5.2, revised per §10): a JSON-encoded array of workspace-relative
	// paths whose edits ship in a separate follow-up PR instead of this
	// delivery. checkAndSplitChunkDiffSize computes this HOST-side from
	// measured git diff sizes when the diff exceeds the hard limit and
	// splitting is enabled - never agent-authored, so there is no trust
	// boundary between what an agent claims and what the worktree actually
	// contains (the design this replaced was rejected for exactly that
	// reason; see spec-auto-split-oversized-prs.md §10).
	InputDeferredFiles = "deferred_files"
)

// maxPRBaseBytes bounds one pr_base value. GitHub branch names are capped far
// below this, so the limit only guards against absurd inputs.
const maxPRBaseBytes = 100

// prBaseRE is the allowed character set of a pr_base branch name. The '..'
// and leading '-' cases are rejected separately: the regex permits both.
var prBaseRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// stackPartRE matches the canonical "k/N" chunk-part form with positive
// integers. Zero and leading zeros are rejected: the driver emits canonical
// parts like "3/12".
var stackPartRE = regexp.MustCompile(`^([1-9][0-9]*)/([1-9][0-9]*)$`)

// ValidatePRBase validates a pr_base value (a git branch name): the allowed
// characters are [A-Za-z0-9._/-], the value is at most 100 characters, does
// not start with '-' (a git option-injection guard), and does not contain
// '..' (a ref-traversal guard). Every violation is a repairable
// PRMetadataError naming the problem, so the repair loop can fix the input.
func ValidatePRBase(name string) error {
	if name == "" {
		return &PRMetadataError{Reason: "delivery: pr_base is empty; provide a non-empty target branch name"}
	}
	if len(name) > maxPRBaseBytes {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_base %q is %d characters, exceeding the %d-character limit", redact.Text(name), len(name), maxPRBaseBytes)}
	}
	if strings.HasPrefix(name, "-") {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_base %q must not start with '-'", redact.Text(name))}
	}
	if strings.Contains(name, "..") {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_base %q must not contain '..'", redact.Text(name))}
	}
	if !prBaseRE.MatchString(name) {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_base %q contains characters outside [A-Za-z0-9._/-]", redact.Text(name))}
	}
	return nil
}

// parseStackPart validates a stack_part value of the canonical "k/N" form:
// positive integers with k <= N. It returns the parsed parts. Every violation
// is a repairable PRMetadataError naming the problem.
func parseStackPart(value string) (k, n int, err error) {
	m := stackPartRE.FindStringSubmatch(value)
	if m == nil {
		return 0, 0, &PRMetadataError{Reason: fmt.Sprintf("delivery: stack_part %q is not of the form k/N with positive integers; fix the stacking input", redact.Text(value))}
	}
	k, kErr := strconv.Atoi(m[1])
	n, nErr := strconv.Atoi(m[2])
	if kErr != nil || nErr != nil {
		return 0, 0, &PRMetadataError{Reason: fmt.Sprintf("delivery: stack_part %q is out of range; fix the stacking input", redact.Text(value))}
	}
	if k > n {
		return 0, 0, &PRMetadataError{Reason: fmt.Sprintf("delivery: stack_part %q has k greater than N; fix the stacking input", redact.Text(value))}
	}
	return k, n, nil
}

// appendStackPartTitle appends the "Stack-Part: k/N" trailer to a resolved PR
// title, following the repo's trailer convention: a "Label: value" line
// separated from the body by a blank line. The trailer is host-appended AFTER
// sanitization and policy validation, mirroring how the body footer is
// appended after validation, so the agent-controlled title that passed
// validation stays intact as the first line. A result over GitHub's 256-rune
// ceiling is a repairable PRMetadataError: the agent must shorten the title.
func appendStackPartTitle(title, stackPart string) (string, error) {
	if stackPart == "" {
		return title, nil
	}
	k, total, err := parseStackPart(stackPart)
	if err != nil {
		return "", err
	}
	trailer := fmt.Sprintf("Stack-Part: %d/%d", k, total)
	withTrailer := title + "\n\n" + trailer
	if runes := utf8.RuneCountInString(withTrailer); runes > MaxTitleRunes {
		return "", &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_title with the %s trailer is %d characters, exceeding GitHub's %d-character limit; shorten the title", trailer, runes, MaxTitleRunes)}
	}
	return withTrailer, nil
}

// resolveStackingInputs honors the reserved stacking inputs on one delivery
// request: a valid pr_base overrides the workflow's default PR base branch;
// a stack_part is validated for its canonical k/N shape (the title trailer
// itself is appended later, after metadata validation). Invalid values are
// repairable PRMetadataErrors; absent inputs leave the request unchanged.
func resolveStackingInputs(req Request) (Request, error) {
	if prBase, ok := req.Inputs[InputPRBase]; ok {
		if err := ValidatePRBase(prBase); err != nil {
			return req, err
		}
		req.Policy.Base = prBase
	}
	if stackPart, ok := req.Inputs[InputStackPart]; ok {
		if _, _, err := parseStackPart(stackPart); err != nil {
			return req, err
		}
	}
	return req, nil
}

// DiffSizeError marks a REPAIRABLE delivery rejection whose cause is a chunk
// diff that exceeds the stacking hard limit. It is deliberately NOT a
// PRMetadataError: a metadata step cannot shrink a diff, so delivery repair
// routing (delivery.RepairTarget) sends it to the workflow's diff-size repair
// step, which edits the worktree. A RefusalError is permanent; a
// DiffSizeError returns the run to the agent for a diff-size fix.
type DiffSizeError struct{ Reason string }

// Error implements error.
func (e *DiffSizeError) Error() string { return e.Reason }

// IsDiffSizeError reports whether err is a DiffSizeError (possibly wrapped).
func IsDiffSizeError(err error) bool {
	var de *DiffSizeError
	return errors.As(err, &de)
}

// MeasureChunkDiffSize measures the actual added+deleted line count of the
// staged worktree diff vs base, using the same staging and numstat rules the
// delivery gate applies (--find-renames, --ignore-all-space, untracked files
// included via git add -A). hard <= 0 means the gate is off and 0 is returned
// without touching git. excludePaths, when non-empty, excludes those
// workspace-relative paths from the measured diff via a pathspec exclusion
// (spec-auto-split-oversized-prs.md §5.2: a repair's deferred_files are
// committed separately and must not count against the delivered diff's own
// size). It is shared by the delivery gate and the controller's post-implement
// fail-fast gate so both measure identically; the controller's gate always
// passes nil (deferred_files is a repair-time decision, unknown that early).
func MeasureChunkDiffSize(ctx context.Context, git GitRunner, gc GitContext, baseCommit string, hard int, excludePaths []string) (int, error) {
	if hard <= 0 {
		return 0, nil
	}
	if _, err := git.Run(ctx, gc, "-c", "core.fsmonitor=false", "add", "-A"); err != nil {
		return 0, fmt.Errorf("cannot stage the delivery diff for size measurement: %w", err)
	}
	args := []string{"diff", "--cached",
		"--no-ext-diff", "--no-textconv", "--numstat",
		"--find-renames", "--ignore-all-space", baseCommit}
	if len(excludePaths) > 0 {
		args = append(args, "--")
		args = append(args, excludePathspecs(excludePaths)...)
	}
	out, err := git.Run(ctx, gc, args...)
	if err != nil {
		return 0, fmt.Errorf("cannot measure the delivery diff size: %w", err)
	}
	return numstatSize(out)
}

// excludePathspecs turns workspace-relative paths into git "exclude"
// pathspecs (":!path", plus the whole tree "." so the exclusions have a
// positive pathspec to narrow) for a `git diff ... -- <pathspecs>` call.
func excludePathspecs(paths []string) []string {
	specs := make([]string, 0, len(paths)+1)
	specs = append(specs, ".")
	for _, p := range paths {
		specs = append(specs, ":!"+p)
	}
	return specs
}

// checkChunkDiffSize enforces the stacking hard diff-size limit on the actual
// PR branch diff vs the admitted base. Without a resolved stacking
// configuration (StackingHardLines <= 0) the gate is OFF and single-PR
// delivery is unchanged. The measured size is the added+deleted line count,
// excluding pure renames and whitespace-only changes (--find-renames,
// --ignore-all-space). The change is staged with git add -A first - exactly
// what commitOrResume stages - so untracked files are measured too: a bare
// `git diff <commit>` ignores untracked files and would under-count a chunk
// that adds files.
//
// An over-limit diff is either a repairable DiffSizeError (split disabled,
// or no split fits) or, when the workflow's [stacking] split_deferred is on,
// a host-computed automatic split (§5.2, revised per §10): the largest
// files are deferred to a follow-up PR until the kept diff fits, and the
// decision is written into req.Inputs[InputDeferredFiles] for
// freshDeliveryCommit to execute - req.Inputs is a map (reference type), so
// this mutation is visible to every later step of this SAME Deliver() call
// without threading Request back through return values. No repair round
// trip and no agent involvement: the split is deterministic and re-verified
// against the actual measured diff before it is trusted.
func checkChunkDiffSize(ctx context.Context, git GitRunner, req Request) error {
	hard := req.Policy.StackingHardLines
	if hard <= 0 {
		return nil
	}
	size, err := MeasureChunkDiffSize(ctx, git, req.GitCtx, req.BaseCommit, hard, nil)
	if err != nil {
		return err
	}
	if size <= hard {
		return nil
	}
	oversized := &DiffSizeError{Reason: fmt.Sprintf("delivery: chunk diff size %d exceeds hard limit %d; shrink the chunk so the delivered diff fits", size, hard)}
	if !req.Policy.SplitDeferred {
		return oversized
	}
	deferred, err := computeDeterministicSplit(ctx, git, req.GitCtx, req.BaseCommit, hard)
	if err != nil {
		return err
	}
	if len(deferred) == 0 {
		return oversized
	}
	verified, err := MeasureChunkDiffSize(ctx, git, req.GitCtx, req.BaseCommit, hard, deferred)
	if err != nil {
		return err
	}
	if verified > hard {
		return &DiffSizeError{Reason: fmt.Sprintf("delivery: chunk diff size %d exceeds hard limit %d even after deferring %d file(s); shrink the chunk", verified, hard, len(deferred))}
	}
	encoded, err := json.Marshal(deferred)
	if err != nil {
		return fmt.Errorf("cannot encode the automatic split decision: %w", err)
	}
	if req.Inputs == nil {
		return fmt.Errorf("delivery request has no inputs map to record the automatic split decision")
	}
	req.Inputs[InputDeferredFiles] = string(encoded)
	return nil
}

// fileDiffSize is one file's measured added+deleted line count from a
// `git diff --numstat` line.
type fileDiffSize struct {
	path  string
	lines int
}

// computeDeterministicSplit measures the actual per-file diff size (never an
// agent's claim - see InputDeferredFiles) and greedily defers the LARGEST
// files first until the remaining (kept) diff fits within hard. Deferring
// largest-first moves the fewest files out of this delivery. It intentionally
// does not use --find-renames: a rename line's "old => new" path is
// ambiguous to stage by exact path, and the selection here only needs to be
// a good-enough estimate - checkChunkDiffSize re-verifies the actual result
// with MeasureChunkDiffSize (which does use --find-renames) before trusting
// it. At least one file always stays kept: a delivered commit with nothing
// in it defeats the purpose of a split (there is no smaller PR left to
// review), so deferring literally every file is refused, not attempted. This
// also naturally refuses a diff with only one file, since deferring it would
// leave zero kept. Returns nil when it cannot bring the kept diff under hard
// without deferring everything: the caller falls back to a plain
// DiffSizeError since no split can help.
func computeDeterministicSplit(ctx context.Context, git GitRunner, gc GitContext, baseCommit string, hard int) ([]string, error) {
	if _, err := git.Run(ctx, gc, "-c", "core.fsmonitor=false", "add", "-A"); err != nil {
		return nil, fmt.Errorf("cannot stage the delivery diff for split measurement: %w", err)
	}
	out, err := git.Run(ctx, gc, "diff", "--cached",
		"--no-ext-diff", "--no-textconv", "--numstat", "--ignore-all-space", baseCommit)
	if err != nil {
		return nil, fmt.Errorf("cannot measure per-file delivery diff sizes: %w", err)
	}
	files, kept, err := numstatPerFile(out)
	if err != nil {
		return nil, err
	}
	if len(files) < 2 {
		return nil, nil // nothing separable: one file cannot split into two PRs
	}
	sort.Slice(files, func(i, j int) bool { return files[i].lines > files[j].lines })
	var deferred []string
	for i, f := range files {
		if kept <= hard {
			break
		}
		if len(deferred) == len(files)-1 {
			break // always keep at least the smallest remaining file
		}
		if f.lines == 0 && i < len(files)-1 {
			continue // deferring a zero-weight (binary/unmeasurable) file never helps
		}
		deferred = append(deferred, f.path)
		kept -= f.lines
	}
	if kept > hard {
		return nil, nil
	}
	return deferred, nil
}

// ParseDeferredFiles decodes the InputDeferredFiles reserved input: a
// JSON-encoded array of workspace-relative paths, or "" (no deferral). A
// present-but-malformed value is a repairable PRMetadataError (the engine's
// own read of the repair step's output was well-formed JSON matching the
// schema; a malformed value here means something upstream corrupted it,
// which is worth surfacing as a repair-loop-visible failure rather than
// silently ignoring the split decision).
func ParseDeferredFiles(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var files []string
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil, &PRMetadataError{Reason: fmt.Sprintf("delivery: reserved input deferred_files is not a valid JSON string array: %v", err)}
	}
	return files, nil
}

// DeferredBranchName derives the local branch name a split delivery
// (freshDeliveryCommitSplit) saves its deferred commit under, deterministic
// from the chunk's own delivery branch so no extra ledger field is needed to
// find it afterward: the driver (internal/cli) computes the same name to
// look it up and push it as a follow-up PR after this chunk's delivery
// succeeds.
func DeferredBranchName(branch string) string {
	return branch + "-deferred"
}

// branchExists reports whether a local branch ref exists, best-effort (a git
// failure reports false rather than propagating - callers use this only for
// non-gating evidence, never a correctness-critical decision).
func branchExists(ctx context.Context, git GitRunner, gc GitContext, branch string) bool {
	_, err := git.Run(ctx, gc, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+branch)
	return err == nil
}

// CountUnshippedCommits counts the commits on the current worktree HEAD after
// deliveredCommit (git rev-list --count deliveredCommit..HEAD): the trailing
// commits a diff-size repair left on the branch after committing the
// review-sized slice as deliveredCommit (spec-auto-split-oversized-prs.md
// §5.2-5.3). Zero means no trailing commits: the repair produced exactly one
// delivered slice and nothing else. Shared by the delivery engine (to record
// DeliveryRecord.StackRemainingCommits after a successful re-delivery) and by
// the driver's follow-up chunk admission (§5.3), so both count identically.
func CountUnshippedCommits(ctx context.Context, git GitRunner, gc GitContext, deliveredCommit string) (int, error) {
	if strings.TrimSpace(deliveredCommit) == "" {
		return 0, fmt.Errorf("cannot count unshipped commits: delivered commit is empty")
	}
	out, err := git.Run(ctx, gc, "rev-list", "--count", deliveredCommit+"..HEAD")
	if err != nil {
		return 0, fmt.Errorf("cannot count unshipped commits after %s: %w", deliveredCommit, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("cannot parse unshipped commit count %q: %w", out, err)
	}
	return n, nil
}

// numstatSize sums the added and deleted line counts of a git diff --numstat
// output. Pure renames report 0/0 (rename detection is on), so a rename
// contributes nothing; binary entries report "-" and contribute nothing. A
// malformed line is an error: the size gate must fail closed rather than
// under-count a diff it cannot parse.
func numstatSize(out string) (int, error) {
	total := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			return 0, fmt.Errorf("delivery diff size: cannot parse numstat line %q", line)
		}
		for _, field := range fields[:2] {
			if field == "-" {
				continue
			}
			n, err := strconv.Atoi(field)
			if err != nil {
				return 0, fmt.Errorf("delivery diff size: cannot parse numstat count %q in line %q", field, line)
			}
			total += n
		}
	}
	return total, nil
}

// numstatPerFile parses a plain (no --find-renames) `git diff --numstat`
// output into one fileDiffSize per line, alongside the summed total. Same
// parse rules as numstatSize (binary "-" contributes 0); a malformed line is
// an error for the same fail-closed reason.
func numstatPerFile(out string) ([]fileDiffSize, int, error) {
	var files []fileDiffSize
	total := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			return nil, 0, fmt.Errorf("delivery diff size: cannot parse numstat line %q", line)
		}
		lines := 0
		for _, field := range fields[:2] {
			if field == "-" {
				continue
			}
			n, err := strconv.Atoi(field)
			if err != nil {
				return nil, 0, fmt.Errorf("delivery diff size: cannot parse numstat count %q in line %q", field, line)
			}
			lines += n
		}
		files = append(files, fileDiffSize{path: fields[2], lines: lines})
		total += lines
	}
	return files, total, nil
}
