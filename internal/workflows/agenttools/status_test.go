package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// seedCompletedAttempt mirrors seedRunningAttemptWithOutput's shape but runs
// in the internal package so buildInspectView can be exercised directly with
// explicit offset/limit. It returns the completed attempt carrying out.
func seedCompletedAttempt(t *testing.T, repo workflowledger.Repository, runID string, out []byte) workflowledger.StepAttempt {
	t.Helper()
	ctx := context.Background()
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "digest",
		Inputs: map[string]string{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", WorkflowDigest: "digest",
		SnapshotDigest: workflowledger.SnapshotDigest(snapshot),
		InputDigest:    workflowledger.InputDigest(map[string]string{"task": "build"}),
		Status:         workflowledger.RunStatusPending, ActiveStepID: "one",
		StartedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, 1, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: runID, StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	ref := "sha256:" + workflowledger.DigestHex(out)
	if err := repo.StoreContent(ctx, ref, out); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := json.Marshal(map[string]any{"selected": map[string]any{"output": map[string]any{"verdict": "approved"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, OutputDigest: workflowledger.DigestHex(out),
		ToStepID: "two", TransitionIndex: 0, MatchDigest: "md", DecisionJSON: decision,
		CoordinatorRunID: "coord-1", TaskID: "task-1", EvidenceJSON: []byte(`[{"name":"task"}]`),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// seedRunningAttemptForStatus persists a run with one still-running attempt,
// mirroring seedRunningAttempt's shape for the internal package.
func seedRunningAttemptForStatus(t *testing.T, repo workflowledger.Repository, runID string) workflowledger.StepAttempt {
	t.Helper()
	ctx := context.Background()
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "digest",
		Inputs: map[string]string{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", WorkflowDigest: "digest",
		SnapshotDigest: workflowledger.SnapshotDigest(snapshot),
		InputDigest:    workflowledger.InputDigest(map[string]string{"task": "build"}),
		Status:         workflowledger.RunStatusPending, ActiveStepID: "one",
		StartedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, 1, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "wfa-one-1", RunID: runID, StepID: "one", AttemptNo: 1,
		Status: workflowledger.AttemptStatusRunning,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// withKeyPolicy installs a redaction policy whose keyNames include api_key and
// restores the previous policy when the test finishes.
func withKeyPolicy(t *testing.T) {
	t.Helper()
	policy, err := redact.Compile(nil, []string{"api_key"}, "")
	if err != nil {
		t.Fatal(err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })
}

func TestBuildInspectViewSmallArtifactBackwardCompat(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-page-small-1"
	raw := []byte(`{"ok":true,"verdict":"approved"}`)
	attempt := seedCompletedAttempt(t, repo, runID, raw)

	// Omitted offset/limit behaves like offset=0, limit=DefaultInspectPageBytes.
	view, err := buildInspectView(context.Background(), repo, runID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := view.Output.(map[string]any)
	if !ok {
		t.Fatalf("small artifact output type = %T, want parsed map", view.Output)
	}
	if output["ok"] != true || output["verdict"] != "approved" {
		t.Fatalf("output = %#v, want parsed/redacted value", output)
	}
	if view.OutputText != "" || view.OutputBytes != 0 || view.OutputOffset != 0 || view.OutputNextOffset != 0 {
		t.Fatalf("backward-compat page must carry no paging fields: %+v", view)
	}

	// An explicit offset 0 with a page that fits keeps the same behavior.
	view2, err := buildInspectView(context.Background(), repo, runID, attempt, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := view2.Output.(map[string]any); !ok {
		t.Fatalf("explicit fitting page output type = %T, want parsed map", view2.Output)
	}
	if view2.OutputText != "" || view2.OutputNextOffset != 0 {
		t.Fatalf("fitting page must not paginate: %+v", view2)
	}
}

func TestBuildInspectViewSmallNonJSONBackwardCompat(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-page-smalltext-1"
	raw := []byte("hello world\nline two\n")
	attempt := seedCompletedAttempt(t, repo, runID, raw)

	view, err := buildInspectView(context.Background(), repo, runID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := view.Output.(string)
	if !ok {
		t.Fatalf("non-JSON output type = %T, want redacted string", view.Output)
	}
	if s != string(raw) {
		t.Fatalf("output = %q, want %q", s, raw)
	}
	if view.OutputText != "" || view.OutputBytes != 0 || view.OutputNextOffset != 0 {
		t.Fatalf("backward-compat page must carry no paging fields: %+v", view)
	}
}

// TestBuildInspectViewPagesHugeArtifact pins plan v3 P2 on a 200KiB artifact:
// the default page returns a redacted text page plus byte metadata, and page
// windows chained through OutputNextOffset reconstruct the whole redacted
// artifact exactly.
func TestBuildInspectViewPagesHugeArtifact(t *testing.T) {
	withKeyPolicy(t)
	// The api_key value is 10 bytes, the same length as "[redacted]", so the
	// redacted text keeps the exact byte layout of the raw artifact and page
	// offsets map 1:1 onto it.
	const secret = "abcd123456"
	raw := []byte(fmt.Sprintf(`{"api_key":"%s","filler":"%s"}`, secret, strings.Repeat("x", 200<<10)))
	expected := strings.Replace(string(raw), secret, "[redacted]", 1)

	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-page-huge-1"
	attempt := seedCompletedAttempt(t, repo, runID, raw)

	view, err := buildInspectView(context.Background(), repo, runID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if view.Output != nil {
		t.Fatalf("huge artifact must omit structured output, got %T", view.Output)
	}
	if view.OutputText == "" {
		t.Fatal("huge artifact must return a text page")
	}
	if len(view.OutputText) > DefaultInspectPageBytes {
		t.Fatalf("page len %d exceeds DefaultInspectPageBytes %d", len(view.OutputText), DefaultInspectPageBytes)
	}
	if view.OutputBytes != len(raw) {
		t.Fatalf("OutputBytes = %d, want raw length %d", view.OutputBytes, len(raw))
	}
	if view.OutputOffset != 0 {
		t.Fatalf("OutputOffset = %d, want 0", view.OutputOffset)
	}
	if view.OutputNextOffset == 0 {
		t.Fatal("first page of a huge artifact must set OutputNextOffset")
	}
	if view.OutputText != expected[:len(view.OutputText)] {
		t.Fatalf("page 1 is not the leading window of the redacted text")
	}
	if strings.Contains(view.OutputText, secret) {
		t.Fatalf("page 1 leaks the secret: %.40q", view.OutputText)
	}

	var sb strings.Builder
	sb.WriteString(view.OutputText)
	next := view.OutputNextOffset
	for next != 0 {
		page, err := buildInspectView(context.Background(), repo, runID, attempt, next, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		if page.OutputText == "" {
			t.Fatalf("non-terminal offset %d returned an empty page", next)
		}
		if page.OutputOffset != next {
			t.Fatalf("page OutputOffset = %d, want requested %d", page.OutputOffset, next)
		}
		if page.OutputBytes != len(raw) {
			t.Fatalf("page OutputBytes = %d, want %d", page.OutputBytes, len(raw))
		}
		if page.OutputText != expected[next:next+len(page.OutputText)] {
			t.Fatalf("page at %d is not the redacted-text window", next)
		}
		if strings.Contains(page.OutputText, secret) {
			t.Fatalf("page at %d leaks the secret", next)
		}
		sb.WriteString(page.OutputText)
		if page.OutputNextOffset != 0 && page.OutputNextOffset <= next {
			t.Fatalf("next offset %d must advance past %d", page.OutputNextOffset, next)
		}
		next = page.OutputNextOffset
	}
	if sb.String() != expected {
		t.Fatalf("reconstructed pages differ from the redacted artifact")
	}
	if strings.Contains(sb.String(), secret) {
		t.Fatal("reconstructed output leaks the secret")
	}
}

// TestBuildInspectViewRedactsSecretAcrossPageBoundary pins redaction parity:
// the raw api_key value straddles the page boundary, yet both pages show the
// redacted placeholder and neither leaks any part of the secret.
func TestBuildInspectViewRedactsSecretAcrossPageBoundary(t *testing.T) {
	withKeyPolicy(t)
	const secret = "sk-0123456789ab" // 16 bytes.
	raw := []byte(fmt.Sprintf(`{"api_key":"%s","pad":"%s"}`, secret, strings.Repeat("x", 4096)))
	// The api_key value starts at raw byte 12 (`{"api_key":"` is 12 bytes);
	// a 20-byte page splits bytes 12..27 (the whole secret) in half.
	const limit = 20
	if !strings.Contains(string(raw[:limit]), secret[:8]) {
		t.Fatalf("test construction: secret must straddle the raw boundary, got %q", raw[:limit])
	}
	expected := strings.Replace(string(raw), secret, "[redacted]", 1)

	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-page-split-1"
	attempt := seedCompletedAttempt(t, repo, runID, raw)

	page1, err := buildInspectView(context.Background(), repo, runID, attempt, 0, limit)
	if err != nil {
		t.Fatal(err)
	}
	if page1.OutputText != expected[:limit] {
		t.Fatalf("page 1 = %q, want redacted window %q", page1.OutputText, expected[:limit])
	}
	if page1.OutputNextOffset != limit {
		t.Fatalf("page 1 OutputNextOffset = %d, want %d", page1.OutputNextOffset, limit)
	}
	if strings.Contains(page1.OutputText, secret) {
		t.Fatalf("page 1 leaks the boundary secret: %q", page1.OutputText)
	}

	page2, err := buildInspectView(context.Background(), repo, runID, attempt, page1.OutputNextOffset, limit)
	if err != nil {
		t.Fatal(err)
	}
	if page2.OutputText != expected[limit:2*limit] {
		t.Fatalf("page 2 = %q, want redacted window %q", page2.OutputText, expected[limit:2*limit])
	}
	if strings.Contains(page2.OutputText, secret) {
		t.Fatalf("page 2 leaks the boundary secret: %q", page2.OutputText)
	}
	if strings.Contains(page1.OutputText+page2.OutputText, secret) {
		t.Fatal("boundary pages together leak the secret")
	}
	if !strings.Contains(page1.OutputText+page2.OutputText, "[redacted]") {
		t.Fatalf("boundary pages must carry the placeholder: %q | %q", page1.OutputText, page2.OutputText)
	}
}

func TestBuildInspectViewOffsetBeyondEndReturnsEmptyPage(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-page-end-1"
	raw := []byte(`{"ok":true}`)
	attempt := seedCompletedAttempt(t, repo, runID, raw)

	view, err := buildInspectView(context.Background(), repo, runID, attempt, len(raw)+100, 4096)
	if err != nil {
		t.Fatalf("offset past the end must not error: %v", err)
	}
	if view.OutputText != "" || view.OutputNextOffset != 0 {
		t.Fatalf("empty page must carry no text or next offset: %+v", view)
	}
	if view.OutputBytes != len(raw) {
		t.Fatalf("OutputBytes = %d, want %d", view.OutputBytes, len(raw))
	}
	if view.OutputOffset != len(raw)+100 {
		t.Fatalf("OutputOffset = %d, want %d", view.OutputOffset, len(raw)+100)
	}
}

// TestBuildInspectViewPagesNonJSONRuneAligned pins text-artifact pagination:
// pages are rune-safe (never split a UTF-8 rune), pattern secrets are redacted
// in the whole artifact before slicing, and the chained windows reconstruct
// the redacted text exactly.
func TestBuildInspectViewPagesNonJSONRuneAligned(t *testing.T) {
	policy, err := redact.Compile([]string{`secret-[a-z0-9]+`}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	raw := []byte(strings.Repeat("a", 10) + "日本語" + strings.Repeat("b", 100) + "secret-abc123\n" + strings.Repeat("c", 50))
	expected := redact.Text(string(raw))
	if strings.Contains(expected, "secret-abc123") {
		t.Fatalf("test construction: expected text must already be redacted, got %q", expected)
	}

	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-page-text-1"
	attempt := seedCompletedAttempt(t, repo, runID, raw)

	const limit = 13
	var sb strings.Builder
	next := 0
	pages := 0
	for {
		view, err := buildInspectView(context.Background(), repo, runID, attempt, next, limit)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(view.OutputText) {
			t.Fatalf("page at offset %d is not valid UTF-8: %q", next, view.OutputText)
		}
		if len(view.OutputText) > limit {
			t.Fatalf("page at offset %d exceeds the limit: %d > %d", next, len(view.OutputText), limit)
		}
		if view.OutputBytes != len(raw) {
			t.Fatalf("page at offset %d OutputBytes = %d, want %d", next, view.OutputBytes, len(raw))
		}
		if strings.Contains(view.OutputText, "secret-abc123") {
			t.Fatalf("page at offset %d leaks the text secret", next)
		}
		sb.WriteString(view.OutputText)
		pages++
		if view.OutputNextOffset == 0 {
			break
		}
		if view.OutputNextOffset <= next {
			t.Fatalf("next offset %d must advance past %d", view.OutputNextOffset, next)
		}
		next = view.OutputNextOffset
	}
	if pages < 3 {
		t.Fatalf("expected multiple pages, got %d", pages)
	}
	if sb.String() != expected {
		t.Fatalf("reconstructed pages differ from the redacted text:\n got %q\nwant %q", sb.String(), expected)
	}
	if strings.Contains(sb.String(), "secret-abc123") {
		t.Fatal("reconstructed text leaks the secret")
	}

	// An offset landing mid-rune must trim the partial rune and still advance.
	mid, err := buildInspectView(context.Background(), repo, runID, attempt, 11, limit)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(mid.OutputText) {
		t.Fatalf("mid-rune page is not valid UTF-8: %q", mid.OutputText)
	}
	if mid.OutputNextOffset <= 11 {
		t.Fatalf("mid-rune page must advance past 11, got next %d", mid.OutputNextOffset)
	}
}

// TestBuildInspectViewClampsLimit pins the limit <= DefaultInspectPageBytes
// contract: an oversized requested limit is clamped to the page budget.
func TestBuildInspectViewClampsLimit(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-page-clamp-1"
	raw := []byte(fmt.Sprintf(`{"filler":"%s"}`, strings.Repeat("x", 100<<10)))
	attempt := seedCompletedAttempt(t, repo, runID, raw)

	view, err := buildInspectView(context.Background(), repo, runID, attempt, 0, DefaultInspectPageBytes*8)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.OutputText) != DefaultInspectPageBytes {
		t.Fatalf("clamped page len = %d, want %d", len(view.OutputText), DefaultInspectPageBytes)
	}
}

// TestBuildInspectViewRefusesOversizedArtifact pins the MaxPageableBytes
// ceiling documented on InspectView: artifacts beyond it are refused outright.
func TestBuildInspectViewRefusesOversizedArtifact(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-page-huge-refused-1"
	raw := []byte(fmt.Sprintf(`{"filler":"%s"}`, strings.Repeat("x", MaxPageableBytes+1)))
	attempt := seedCompletedAttempt(t, repo, runID, raw)

	_, err := buildInspectView(context.Background(), repo, runID, attempt)
	if err == nil {
		t.Fatal("oversized artifact must be refused")
	}
	if !strings.Contains(err.Error(), "paging ceiling") {
		t.Fatalf("refusal error = %q, want paging ceiling mention", err.Error())
	}
}

// TestInspectPagingExpandingRedactionReachesTail pins the audit fix for the
// paging chain: pages slice the REDACTED text, so the empty-page guard must
// compare against the redacted length. With an expanding redaction policy the
// raw and redacted lengths differ; following OutputNextOffset must reconstruct
// the FULL redacted artifact, never terminate early and silently drop the tail.
func TestInspectPagingExpandingRedactionReachesTail(t *testing.T) {
	policy, err := redact.Compile([]string{`\b\d{4}\b`}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	// Non-JSON artifact whose redaction EXPANDS: every 4-digit group becomes
	// the 11-byte placeholder, so redacted length > raw length.
	raw := []byte(strings.Repeat("1234 ", 30<<10))
	expected := redact.Text(string(raw))
	if len(expected) <= len(raw) {
		t.Fatalf("test policy must expand the text (raw %d, redacted %d)", len(raw), len(expected))
	}

	const limit = 64 << 10
	var builder strings.Builder
	offset := 0
	pages := 0
	for {
		var view InspectView
		fillInspectOutput(&view, raw, offset, limit)
		builder.WriteString(view.OutputText)
		pages++
		if view.OutputNextOffset == 0 {
			break
		}
		if pages > 100 {
			t.Fatal("paging chain did not terminate")
		}
		offset = view.OutputNextOffset
	}
	if builder.String() != expected {
		t.Fatalf("paged redacted text (%d bytes) differs from the full redacted artifact (%d bytes)", builder.Len(), len(expected))
	}
}

// TestBuildStatusViewCompletedAttemptTiming pins G6 for the status view: a
// completed attempt surfaces its ledger started and finished timestamps plus
// the elapsed seconds computed from them.
func TestBuildStatusViewCompletedAttemptTiming(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-status-timing-1"
	attempt := seedCompletedAttempt(t, repo, runID, []byte(`{"ok":true}`))

	view, err := buildStatusView(context.Background(), repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(view.Attempts))
	}
	av := view.Attempts[0]
	wantStarted := attempt.StartedAt.UTC().Format(time.RFC3339)
	if av.StartedAt != wantStarted {
		t.Fatalf("StartedAt = %q, want %q", av.StartedAt, wantStarted)
	}
	if attempt.FinishedAt == nil {
		t.Fatal("seeded attempt must be completed")
	}
	wantFinished := attempt.FinishedAt.UTC().Format(time.RFC3339)
	if av.FinishedAt != wantFinished {
		t.Fatalf("FinishedAt = %q, want %q", av.FinishedAt, wantFinished)
	}
	wantElapsed := int64(attempt.FinishedAt.Sub(attempt.StartedAt).Seconds())
	if av.ElapsedSeconds != wantElapsed {
		t.Fatalf("ElapsedSeconds = %d, want %d", av.ElapsedSeconds, wantElapsed)
	}
}

// TestBuildStatusViewRunningAttemptTiming pins G6 for a live attempt: started
// is set, finished stays empty, and elapsed is non-negative.
func TestBuildStatusViewRunningAttemptTiming(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-status-timing-running-1"
	attempt := seedRunningAttemptForStatus(t, repo, runID)

	view, err := buildStatusView(context.Background(), repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(view.Attempts))
	}
	av := view.Attempts[0]
	if av.StartedAt != attempt.StartedAt.UTC().Format(time.RFC3339) {
		t.Fatalf("StartedAt = %q, want %q", av.StartedAt, attempt.StartedAt.UTC().Format(time.RFC3339))
	}
	if av.FinishedAt != "" {
		t.Fatalf("running attempt FinishedAt = %q, want empty", av.FinishedAt)
	}
	if av.ElapsedSeconds < 0 {
		t.Fatalf("running attempt ElapsedSeconds = %d, want >= 0", av.ElapsedSeconds)
	}
}

// TestBuildInspectViewAttemptTiming pins G6 for the inspect view: the same
// three timing fields mirror the ledger attempt.
func TestBuildInspectViewAttemptTiming(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	runID := "wfr-inspect-timing-1"
	attempt := seedCompletedAttempt(t, repo, runID, []byte(`{"ok":true}`))

	view, err := buildInspectView(context.Background(), repo, runID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if view.StartedAt != attempt.StartedAt.UTC().Format(time.RFC3339) {
		t.Fatalf("StartedAt = %q, want %q", view.StartedAt, attempt.StartedAt.UTC().Format(time.RFC3339))
	}
	if attempt.FinishedAt == nil {
		t.Fatal("seeded attempt must be completed")
	}
	if view.FinishedAt != attempt.FinishedAt.UTC().Format(time.RFC3339) {
		t.Fatalf("FinishedAt = %q, want %q", view.FinishedAt, attempt.FinishedAt.UTC().Format(time.RFC3339))
	}
	wantElapsed := int64(attempt.FinishedAt.Sub(attempt.StartedAt).Seconds())
	if view.ElapsedSeconds != wantElapsed {
		t.Fatalf("ElapsedSeconds = %d, want %d", view.ElapsedSeconds, wantElapsed)
	}
}

// TestStatusViewSurfacesDeliveryErrorText pins that a failed delivery's
// stored failure text is resolved into the status view, so the harness shows
// the error hint automatically instead of an opaque error_ref.
func TestStatusViewSurfacesDeliveryErrorText(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	const runID = "wfr-errhint"
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "digest",
		Inputs: map[string]string{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "two-step", WorkflowDigest: "digest",
		SnapshotDigest: workflowledger.SnapshotDigest(snapshot),
		InputDigest:    workflowledger.InputDigest(map[string]string{"task": "build"}),
		Status:         workflowledger.RunStatusPending,
		StartedAt:      time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}, snapshot); err != nil {
		t.Fatal(err)
	}
	// Advance to delivery_pending through the legal transitions: a run cannot
	// be created directly in delivery_pending (pending -> running ->
	// delivery_pending).
	for _, next := range []workflowledger.RunStatus{
		workflowledger.RunStatusRunning,
		workflowledger.RunStatusDeliveryPending,
	} {
		run, getErr := repo.GetRun(ctx, runID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if casErr := repo.CompareAndSetRunStatus(ctx, runID, run.Version, next, nil); casErr != nil {
			t.Fatal(casErr)
		}
	}
	errText := "git push origin HEAD:refs/heads/wf/x: signal: killed: verify_agent_config: ok"
	ref := "sha256:" + workflowledger.DigestHex([]byte(errText))
	if err := repo.StoreContent(ctx, ref, []byte(errText)); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: runID, IdempotencyKey: "wfdel:k", Mode: "draft", BaseRef: "master",
		HeadRef: "wf/x", Provider: "github", Status: "failed", ErrorRef: ref,
	}); err != nil {
		t.Fatal(err)
	}
	view, err := buildStatusView(ctx, repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Delivery) != 1 {
		t.Fatalf("delivery views = %d, want 1", len(view.Delivery))
	}
	if view.Delivery[0].ErrorText != errText {
		t.Fatalf("error text = %q, want %q", view.Delivery[0].ErrorText, errText)
	}
}
