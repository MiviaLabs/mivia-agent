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
	"errors"
	"fmt"
	"regexp"
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
// without touching git. It is shared by the delivery gate and the controller's
// post-implement fail-fast gate so both measure identically.
func MeasureChunkDiffSize(ctx context.Context, git GitRunner, gc GitContext, baseCommit string, hard int) (int, error) {
	if hard <= 0 {
		return 0, nil
	}
	if _, err := git.Run(ctx, gc, "-c", "core.fsmonitor=false", "add", "-A"); err != nil {
		return 0, fmt.Errorf("cannot stage the delivery diff for size measurement: %w", err)
	}
	out, err := git.Run(ctx, gc, "diff", "--cached",
		"--no-ext-diff", "--no-textconv", "--numstat",
		"--find-renames", "--ignore-all-space", baseCommit)
	if err != nil {
		return 0, fmt.Errorf("cannot measure the delivery diff size: %w", err)
	}
	return numstatSize(out)
}

// checkChunkDiffSize enforces the stacking hard diff-size limit on the actual
// PR branch diff vs the admitted base. Without a resolved stacking
// configuration (StackingHardLines <= 0) the gate is OFF and single-PR
// delivery is unchanged. The measured size is the added+deleted line count,
// excluding pure renames and whitespace-only changes (--find-renames,
// --ignore-all-space). The change is staged with git add -A first - exactly
// what commitOrResume stages - so untracked files are measured too: a bare
// `git diff <commit>` ignores untracked files and would under-count a chunk
// that adds files. An over-limit diff is a repairable DiffSizeError: the
// repair loop shrinks the chunk and delivers again.
func checkChunkDiffSize(ctx context.Context, git GitRunner, req Request) error {
	hard := req.Policy.StackingHardLines
	if hard <= 0 {
		return nil
	}
	size, err := MeasureChunkDiffSize(ctx, git, req.GitCtx, req.BaseCommit, hard)
	if err != nil {
		return err
	}
	if size > hard {
		return &DiffSizeError{Reason: fmt.Sprintf("delivery: chunk diff size %d exceeds hard limit %d; shrink the chunk so the delivered diff fits", size, hard)}
	}
	return nil
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
