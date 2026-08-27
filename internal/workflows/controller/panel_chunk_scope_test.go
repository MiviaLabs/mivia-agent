package controller

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// chunkScopeController builds a bare controller armed (or not) for chunk
// finding scope. The filter reads only Workflow.Stacking and Inputs, so no
// run harness is needed at builder level.
func chunkScopeController(inputs map[string]any) *LinearController {
	return &LinearController{
		Workflow: &definition.CompiledWorkflow{Stacking: &definition.StackingConfig{HardLines: 400}},
		Inputs:   inputs,
	}
}

// siblingPackageReport is one member report that demands the sibling
// packages of the runeutil chunk: the live incident shape.
func siblingPackageReport() PanelFinding {
	return PanelFinding{
		ID: "f1", Severity: "high", Title: "Missing sibling packages",
		Description: "The task requires internal/pathutil with SplitExt and internal/envutil with ParseBool; neither package is implemented",
	}
}

// TestPanelChunkScopeDropsSiblingPackageFindings pins the live-incident
// shape at the panel surface: a member whose only finding demands sibling
// package directories loses the finding, its verdict flips to approved,
// the host verdict approves, the envelope records the drop, and the
// provenance counts the filtered findings.
func TestPanelChunkScopeDropsSiblingPackageFindings(t *testing.T) {
	ctrl := chunkScopeController(chunkModeInputs(runeutilPlan))
	inputs := []PanelSynthesisMemberInput{
		panelMemberInput("correctness", validPanelReportJSON(PanelVerdictChangesRequested, siblingPackageReport())),
		panelMemberInput("security", validPanelReportJSON(PanelVerdictApproved)),
	}
	envelope, _, err := BuildSynthesisEnvelopeWithFilter("review_panel", inputs, ctrl.panelChunkScopeFilter("review_panel", 1))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.HostVerdict != PanelVerdictApproved {
		t.Fatalf("host verdict = %q, want approved (sibling finding dropped)", envelope.HostVerdict)
	}
	m := envelope.Members[0]
	if len(m.Report.Findings) != 0 {
		t.Fatalf("findings = %#v, want empty", m.Report.Findings)
	}
	if m.Report.Verdict != PanelVerdictApproved {
		t.Fatalf("member verdict = %q, want approved (all findings dropped)", m.Report.Verdict)
	}
	if m.Provenance.FindingCount != 0 {
		t.Fatalf("FindingCount = %d, want 0 (provenance counts filtered findings)", m.Provenance.FindingCount)
	}
	if ids := envelope.DroppedFindings["correctness"]; len(ids) != 1 || ids[0] != "f1" {
		t.Fatalf("DroppedFindings[correctness] = %#v, want [f1]", envelope.DroppedFindings["correctness"])
	}
}

// TestPanelChunkScopeKeepsSiblingFileMention pins the audit fix: prose that
// DISCUSSES a sibling file is evidence, not a demand for a missing package.
// Only directory-shaped sibling tokens (missing packages) may drop.
func TestPanelChunkScopeKeepsSiblingFileMention(t *testing.T) {
	ctrl := chunkScopeController(chunkModeInputs(runeutilPlan))
	f := PanelFinding{
		ID: "f2", Severity: "critical", Title: "Wrong file named in error string",
		Description: "The new error path prints internal/runeutil/extra.go as the offending file; it must print the file actually written",
	}
	inputs := []PanelSynthesisMemberInput{
		panelMemberInput("correctness", validPanelReportJSON(PanelVerdictChangesRequested, f)),
		panelMemberInput("security", validPanelReportJSON(PanelVerdictApproved)),
	}
	envelope, _, err := BuildSynthesisEnvelopeWithFilter("review_panel", inputs, ctrl.panelChunkScopeFilter("review_panel", 1))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.HostVerdict != PanelVerdictChangesRequested {
		t.Fatalf("host verdict = %q, want changes_requested (sibling FILE mention kept)", envelope.HostVerdict)
	}
	if len(envelope.Members[0].Report.Findings) != 1 {
		t.Fatalf("findings = %#v, want the sibling-file finding kept", envelope.Members[0].Report.Findings)
	}
}

// TestPanelChunkScopeKeepsDeclaredBaseName pins the challenge fix: prose
// that names the declared file by BASE name (no path) keeps the finding.
func TestPanelChunkScopeKeepsDeclaredBaseName(t *testing.T) {
	ctrl := chunkScopeController(chunkModeInputs(runeutilPlan))
	f := PanelFinding{
		ID: "f3", Severity: "high", Title: "runeutil.go: off-by-one in Runes",
		Description: "Runes trims one rune too many; the pattern used by internal/pathutil is the model to follow",
	}
	inputs := []PanelSynthesisMemberInput{
		panelMemberInput("correctness", validPanelReportJSON(PanelVerdictChangesRequested, f)),
		panelMemberInput("security", validPanelReportJSON(PanelVerdictApproved)),
	}
	envelope, _, err := BuildSynthesisEnvelopeWithFilter("review_panel", inputs, ctrl.panelChunkScopeFilter("review_panel", 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope.Members[0].Report.Findings) != 1 {
		t.Fatalf("findings = %#v, want kept (declared base name named in prose)", envelope.Members[0].Report.Findings)
	}
	if envelope.HostVerdict != PanelVerdictChangesRequested {
		t.Fatalf("host verdict = %q, want changes_requested", envelope.HostVerdict)
	}
}

// TestPanelChunkScopeKeepsInScopePath pins the plain keep: a finding whose
// text names the declared file by full path stays.
func TestPanelChunkScopeKeepsInScopePath(t *testing.T) {
	ctrl := chunkScopeController(chunkModeInputs(runeutilPlan))
	f := PanelFinding{
		ID: "f4", Severity: "medium", Title: "TrimSpace misapplied",
		Description: "Runes in internal/runeutil/runeutil.go trims leading spaces the task asked to keep",
	}
	inputs := []PanelSynthesisMemberInput{
		panelMemberInput("correctness", validPanelReportJSON(PanelVerdictChangesRequested, f)),
		panelMemberInput("security", validPanelReportJSON(PanelVerdictApproved)),
	}
	envelope, _, err := BuildSynthesisEnvelopeWithFilter("review_panel", inputs, ctrl.panelChunkScopeFilter("review_panel", 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope.Members[0].Report.Findings) != 1 || envelope.HostVerdict != PanelVerdictChangesRequested {
		t.Fatalf("in-scope finding must stay: findings=%#v verdict=%q", envelope.Members[0].Report.Findings, envelope.HostVerdict)
	}
}

// TestPanelChunkScopeFlipsFindingsLessChangesRequested pins the wedge
// closure: a member verdict changes_requested with NO findings carries no
// actionable content. Under an armed filter it approves; the nil-filter
// builder keeps the old behavior for non-chunk panels.
func TestPanelChunkScopeFlipsFindingsLessChangesRequested(t *testing.T) {
	ctrl := chunkScopeController(chunkModeInputs(runeutilPlan))
	inputs := []PanelSynthesisMemberInput{
		panelMemberInput("correctness", validPanelReportJSON(PanelVerdictChangesRequested)),
		panelMemberInput("security", validPanelReportJSON(PanelVerdictApproved)),
	}
	filtered, _, err := BuildSynthesisEnvelopeWithFilter("review_panel", inputs, ctrl.panelChunkScopeFilter("review_panel", 1))
	if err != nil {
		t.Fatal(err)
	}
	if filtered.HostVerdict != PanelVerdictApproved {
		t.Fatalf("host verdict = %q, want approved (findings-less verdict neutralized)", filtered.HostVerdict)
	}
	plain, _, err := BuildSynthesisEnvelope("review_panel", inputs)
	if err != nil {
		t.Fatal(err)
	}
	if plain.HostVerdict != PanelVerdictChangesRequested {
		t.Fatalf("nil-filter host verdict = %q, want the unchanged legacy behavior", plain.HostVerdict)
	}
}

// TestPanelChunkScopeUnarmedWithoutChunkMode pins the arming gate: without
// the chunk-mode reserved inputs the filter is nil and nothing drops.
func TestPanelChunkScopeUnarmedWithoutChunkMode(t *testing.T) {
	ctrl := chunkScopeController(map[string]any{"task": "x"})
	if f := ctrl.panelChunkScopeFilter("review_panel", 1); f != nil {
		t.Fatal("filter must be nil without chunk mode")
	}
}

// TestPanelChunkScopeMixedKeepsVerdict pins the partial drop: one sibling
// finding dropped, one in-scope finding kept - the member verdict stays
// changes_requested and the host verdict follows.
func TestPanelChunkScopeMixedKeepsVerdict(t *testing.T) {
	ctrl := chunkScopeController(chunkModeInputs(runeutilPlan))
	sibling := siblingPackageReport()
	inScope := PanelFinding{
		ID: "f5", Severity: "low", Title: "Doc comment stale",
		Description: "The package comment in internal/runeutil/runeutil.go names the old function",
	}
	inputs := []PanelSynthesisMemberInput{
		panelMemberInput("correctness", validPanelReportJSON(PanelVerdictChangesRequested, sibling, inScope)),
		panelMemberInput("security", validPanelReportJSON(PanelVerdictApproved)),
	}
	envelope, _, err := BuildSynthesisEnvelopeWithFilter("review_panel", inputs, ctrl.panelChunkScopeFilter("review_panel", 1))
	if err != nil {
		t.Fatal(err)
	}
	m := envelope.Members[0]
	if len(m.Report.Findings) != 1 || m.Report.Findings[0].ID != "f5" {
		t.Fatalf("findings = %#v, want only the in-scope f5", m.Report.Findings)
	}
	if m.Report.Verdict != PanelVerdictChangesRequested || envelope.HostVerdict != PanelVerdictChangesRequested {
		t.Fatalf("verdicts = %q/%q, want changes_requested on both", m.Report.Verdict, envelope.HostVerdict)
	}
	if ids := envelope.DroppedFindings["correctness"]; len(ids) != 1 || ids[0] != "f1" {
		t.Fatalf("DroppedFindings[correctness] = %#v, want [f1]", envelope.DroppedFindings["correctness"])
	}
}
