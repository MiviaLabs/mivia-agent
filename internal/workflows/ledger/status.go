package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/textutil"
)

func buildStatusView(ctx context.Context, repo Repository, runID string) (StatusView, error) {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return StatusView{}, fmt.Errorf("workflow run %q not found", runID)
		}
		return StatusView{}, err
	}
	view := StatusView{
		RunID:      run.RunID,
		Workflow:   run.WorkflowName,
		Status:     string(run.Status),
		ActiveStep: run.ActiveStepID,
		Version:    run.Version,
		StartedAt:  formatTime(run.StartedAt),
		DeadlineAt: formatTimePtr(run.DeadlineAt),
		FinishedAt: formatTimePtr(run.FinishedAt),
		BaseRef:    run.BaseRef,
		BaseCommit: run.BaseCommit,
		Worktree:   run.WorktreeName,
		Attempts:   []AttemptView{},
	}
	if run.Status == RunStatusDeliveryPending {
		if _, at, ok, err := repo.GetRunClaim(ctx, runID); err == nil && ok {
			view.DeliveryClaimHeld = true
			view.DeliveryClaimAt = formatTime(at)
		}
	}
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return StatusView{}, err
	}
	for _, a := range attempts {
		view.Attempts = append(view.Attempts, attemptView(a))
	}
	counters, err := repo.GetLoopCounters(ctx, runID)
	if err != nil {
		return StatusView{}, err
	}
	for _, c := range counters {
		view.Loops = append(view.Loops, LoopView{Name: c.LoopName, Iterations: c.Iterations})
	}
	approvals, err := repo.ListApprovals(ctx, runID)
	if err != nil {
		return StatusView{}, err
	}
	for _, a := range approvals {
		view.Approvals = append(view.Approvals, ApprovalView{
			ApprovalID: a.ApprovalID,
			Step:       a.StepID,
			Status:     a.Status,
			Actor:      a.Actor,
			Reason:     a.Reason,
		})
	}
	deliveries, err := repo.ListDeliveries(ctx, runID)
	if err != nil {
		return StatusView{}, err
	}
	for _, d := range deliveries {
		view.Delivery = append(view.Delivery, DeliveryView{
			IdempotencyKey: d.IdempotencyKey,
			Status:         d.Status,
			Mode:           d.Mode,
			URL:            d.URL,
			CommitSHA:      d.CommitSHA,
			ErrorRef:       d.ErrorRef,
			ErrorText:      resolvedDeliveryError(ctx, repo, d.ErrorRef),
		})
	}
	return view, nil
}

// deliveryErrorHintMax bounds one resolved delivery failure text.
const deliveryErrorHintMax = 4 << 10

// resolvedDeliveryError resolves a failed delivery's stored error text so the
// status view surfaces the failure hint automatically. Fail-soft: an empty or
// unresolvable ref yields an empty string; a missing hint must not block the
// status view (DC-9).
func resolvedDeliveryError(ctx context.Context, repo Repository, ref string) string {
	if ref == "" {
		return ""
	}
	body, err := repo.LoadContent(ctx, ref)
	if err != nil || len(body) == 0 {
		return ""
	}
	return textutil.TruncateRuneSafe(string(body), deliveryErrorHintMax)
}

// attemptView renders one ledger attempt as an AttemptView, including the
// RFC3339 UTC timestamps and the elapsed seconds from the ledger.
func attemptView(a StepAttempt) AttemptView {
	av := AttemptView{
		Step:                          a.StepID,
		Attempt:                       a.AttemptNo,
		Status:                        string(a.Status),
		ToStep:                        a.ToStepID,
		OutputDigest:                  a.OutputDigest,
		OutputRef:                     a.OutputRef,
		ErrorRef:                      a.ErrorRef,
		CoordinatorRunID:              a.CoordinatorRunID,
		TaskID:                        a.TaskID,
		MatchDigest:                   a.MatchDigest,
		StartedAt:                     formatTime(a.StartedAt),
		FinishedAt:                    formatTimePtr(a.FinishedAt),
		ElapsedSeconds:                attemptElapsedSeconds(a),
		LastHeartbeatAt:               formatTime(a.LastHeartbeatAt),
		LastHeartbeatStalenessSeconds: attemptHeartbeatStaleness(a),
	}
	if v := extractVerdict(a); v != "" {
		av.Verdict = v
	}
	return av
}

// attemptElapsedSeconds returns the attempt's wall-clock duration in whole
// seconds: finished minus started for a completed attempt, or elapsed since
// start for a running one. Zero when the start time is unknown or the clock
// is skewed (negative duration).
func attemptElapsedSeconds(a StepAttempt) int64 {
	if a.StartedAt.IsZero() {
		return 0
	}
	end := time.Now()
	if a.FinishedAt != nil {
		end = *a.FinishedAt
	}
	d := end.Sub(a.StartedAt)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}

// attemptHeartbeatStaleness returns the seconds since the attempt's latest
// heartbeat, or 0 when none is recorded or the clock is skewed (a future
// heartbeat reads as fresh). Heartbeats only exist for RUNNING attempts, so a
// completed attempt carries 0 unless its final tick was persisted.
func attemptHeartbeatStaleness(a StepAttempt) int64 {
	if a.LastHeartbeatAt.IsZero() {
		return 0
	}
	d := time.Since(a.LastHeartbeatAt)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}

// extractVerdict pulls a gate verdict from decision JSON or stored output fields
// without loading full output bodies into the status envelope.
func extractVerdict(a StepAttempt) string {
	if len(a.DecisionJSON) == 0 {
		return ""
	}
	var decision map[string]any
	if err := json.Unmarshal(a.DecisionJSON, &decision); err != nil {
		return ""
	}
	// Prefer explicit selected output fields from the matcher decision.
	if selected, ok := decision["selected"].(map[string]any); ok {
		if output, ok := selected["output"].(map[string]any); ok {
			if v, ok := output["verdict"].(string); ok {
				return v
			}
		}
		if v, ok := selected["verdict"].(string); ok {
			return v
		}
	}
	if v, ok := decision["verdict"].(string); ok {
		return v
	}
	return ""
}

// buildInspectView renders one step attempt for workflow_inspect. offset and
// limit are byte offsets into the artifact's text form (limit clamped to
// DefaultInspectPageBytes). The WHOLE artifact is redacted before any page is
// sliced, so a secret split across a page boundary is redacted identically in
// every page and key-named secrets are caught wherever the boundary falls.
// When offset and limit are omitted the call behaves like offset=0,
// limit=DefaultInspectPageBytes, which keeps the pre-pagination behavior for
// artifacts that fit the page. Artifacts larger than MaxPageableBytes are
// refused outright.
func buildInspectView(ctx context.Context, repo Repository, runID string, attempt StepAttempt, page ...int) (InspectView, error) {
	pageOffset, pageLimit := 0, DefaultInspectPageBytes
	if len(page) > 0 {
		pageOffset = page[0]
	}
	if len(page) > 1 {
		pageLimit = page[1]
	}
	if pageOffset < 0 {
		pageOffset = 0
	}
	if pageLimit < 0 {
		pageLimit = 0
	}
	if pageLimit > DefaultInspectPageBytes {
		pageLimit = DefaultInspectPageBytes
	}
	view := InspectView{
		RunID:            runID,
		Step:             attempt.StepID,
		Attempt:          attempt.AttemptNo,
		Status:           string(attempt.Status),
		CoordinatorRunID: attempt.CoordinatorRunID,
		TaskID:           attempt.TaskID,
		OutputRef:        attempt.OutputRef,
		OutputDigest:     attempt.OutputDigest,
		ErrorRef:         attempt.ErrorRef,
		StartedAt:        formatTime(attempt.StartedAt),
		FinishedAt:       formatTimePtr(attempt.FinishedAt),
		ElapsedSeconds:   attemptElapsedSeconds(attempt),
	}
	if len(attempt.EvidenceJSON) > 0 {
		var evidence any
		if err := json.Unmarshal(attempt.EvidenceJSON, &evidence); err == nil {
			view.EvidenceSelection = redact.JSONValue(evidence)
		}
	}
	if attempt.ToStepID != "" || attempt.MatchDigest != "" || len(attempt.DecisionJSON) > 0 {
		tv := &TransitionView{
			Index:       attempt.TransitionIndex,
			ToStep:      attempt.ToStepID,
			MatchDigest: attempt.MatchDigest,
		}
		if len(attempt.DecisionJSON) > 0 {
			var decision map[string]any
			if err := json.Unmarshal(attempt.DecisionJSON, &decision); err == nil {
				if selected, ok := decision["selected"].(map[string]any); ok {
					tv.Selected, _ = redact.JSONValue(selected).(map[string]any)
				} else {
					tv.Selected, _ = redact.JSONValue(decision).(map[string]any)
				}
			}
		}
		view.Transition = tv
	}
	if attempt.OutputRef != "" {
		data, err := repo.LoadContent(ctx, attempt.OutputRef)
		if err != nil && !errors.Is(err, ErrContentNotFound) {
			return InspectView{}, err
		}
		if err == nil && len(data) > 0 {
			if len(data) > MaxPageableBytes {
				return InspectView{}, fmt.Errorf("artifact %s is %d bytes, exceeding the %d byte workflow_inspect paging ceiling", attempt.OutputRef, len(data), MaxPageableBytes)
			}
			fillInspectOutput(&view, data, pageOffset, pageLimit)
		}
	}
	if attempt.ErrorRef != "" {
		data, err := repo.LoadContent(ctx, attempt.ErrorRef)
		if err != nil && !errors.Is(err, ErrContentNotFound) {
			return InspectView{}, err
		}
		if err == nil && len(data) > 0 {
			view.ErrorText = redact.Text(string(data))
		}
	}
	return view, nil
}

// fillInspectOutput redacts the WHOLE artifact with the existing semantics
// (redact.JSONValue after a successful json.Unmarshal, otherwise redact.Text
// on the string) and renders it either as the structured value (when offset is
// 0 and the artifact fits the page) or as a rune-aligned text page of the
// already-redacted artifact. Pages always slice the redacted text; the text is
// never redacted per page, so redaction parity holds across page boundaries.
func fillInspectOutput(view *InspectView, raw []byte, offset, limit int) {
	var redactedText string
	var output any
	if json.Unmarshal(raw, &output) == nil {
		redacted := redact.JSONValue(output)
		view.Output = redacted
		if b, err := json.Marshal(redacted); err == nil {
			redactedText = string(b)
		} else {
			redactedText = redact.Text(string(raw))
		}
	} else {
		redactedText = redact.Text(string(raw))
	}
	// The paging chain lives in the REDACTED text space: pages slice
	// redactedText, so the empty-page guard must compare against its length.
	// Comparing against the raw byte count would terminate the chain early
	// when the redaction policy expands text (placeholder growth), silently
	// dropping the artifact tail. OutputBytes stays the RAW artifact size: it
	// is metadata about the underlying artifact, not a page coordinate.
	rawTotal := len(raw)
	if offset >= len(redactedText) {
		// Empty page past the end of the redacted artifact: metadata only, no
		// error.
		view.OutputBytes = rawTotal
		view.OutputOffset = offset
		return
	}
	if offset == 0 && rawTotal <= limit {
		// Backward-compatible small page: the parsed/redacted value, no text
		// page and no paging metadata.
		if view.Output == nil {
			view.Output = redactedText
		}
		return
	}
	// Paginated page of the whole-artifact redacted text.
	view.Output = nil
	view.OutputText, view.OutputNextOffset = inspectTextPage(redactedText, offset, limit)
	view.OutputBytes = rawTotal
	view.OutputOffset = offset
}

// inspectTextPage returns the [offset, offset+limit) byte window of text as a
// valid UTF-8 string (invalid bytes replaced with U+FFFD) trimmed to UTF-8
// rune boundaries at both ends, plus the byte offset of the next page (0 when
// the window reaches the end of the text). Mirroring evidencePreview, the
// window is sanitized before rune-boundary trimming so the returned slice is
// always valid UTF-8 and never splits a rune across pages.
func inspectTextPage(text string, offset, limit int) (string, int) {
	sanitized := strings.ToValidUTF8(text, "\uFFFD")
	if limit <= 0 || offset >= len(sanitized) {
		return "", 0
	}
	end := offset + limit
	if end > len(sanitized) {
		end = len(sanitized)
	}
	window := sanitized[offset:end]
	before := len(window)
	// Drop a rune cut in half at the window start (a complete U+FFFD from
	// sanitization reports size > 1 and is kept).
	for len(window) > 0 {
		r, size := utf8.DecodeRuneInString(window)
		if r != utf8.RuneError || size > 1 {
			break
		}
		window = window[size:]
	}
	leftTrimmed := before - len(window)
	// Drop a rune cut in half at the window end.
	for len(window) > 0 {
		r, size := utf8.DecodeLastRuneInString(window)
		if r != utf8.RuneError || size > 1 {
			break
		}
		window = window[:len(window)-size]
	}
	emittedEnd := offset + leftTrimmed + len(window)
	if emittedEnd >= len(sanitized) {
		return window, 0
	}
	return window, emittedEnd
}
