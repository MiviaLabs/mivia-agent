package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/textutil"
)

// Bounds. See plan 62, "Bounds": 24 KiB per raw and per canonical member
// report, 128 KiB total encoded synthesis envelope, 32 KiB final synthesis
// output, panel-review-v1.json limits of 64-character finding IDs and 16
// findings per report.
const (
	maxRawPanelMemberReportBytes       = 24 * 1024
	maxCanonicalPanelMemberReportBytes = 24 * 1024
	maxSynthesisEnvelopeBytes          = 128 * 1024
	maxFinalSynthesisOutputBytes       = 32 * 1024
	maxFindingsPerPanelReport          = 16
	maxFindingIDRunes                  = 64
	maxFindingIDBytes                  = 64
)

// DecodeStrictPanelMemberReport strictly decodes one panel-review-v1.json
// member report from untrusted raw model output. It rejects duplicate JSON
// keys, duplicate finding IDs, an invalid verdict, too many findings, and a
// finding ID that exceeds either the schema's character bound or the host's
// byte bound (D10). Unknown fields are skipped, not rejected: a model
// occasionally adds a junk field (e.g. "elapsed"), and one extra field must
// not fail an entire review panel. It returns the decoded report and its
// canonical (re-encoded) form, which carries exactly the bounded fields and
// is bounded to maxCanonicalPanelMemberReportBytes.
func DecodeStrictPanelMemberReport(raw []byte) (PanelMemberReport, []byte, error) {
	if len(raw) == 0 {
		return PanelMemberReport{}, nil, fmt.Errorf("panel member report is empty")
	}
	if len(raw) > maxRawPanelMemberReportBytes {
		return PanelMemberReport{}, nil, fmt.Errorf("panel member report is %d bytes, exceeds %d byte bound", len(raw), maxRawPanelMemberReportBytes)
	}
	if err := checkNoDuplicateJSONKeys(raw); err != nil {
		return PanelMemberReport{}, nil, fmt.Errorf("panel member report: %w", err)
	}
	// No DisallowUnknownFields: unknown fields are skipped (see above) so the
	// canonical form drops them while every known field stays strictly checked.
	dec := json.NewDecoder(bytes.NewReader(raw))
	var report PanelMemberReport
	if err := dec.Decode(&report); err != nil {
		return PanelMemberReport{}, nil, fmt.Errorf("panel member report: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return PanelMemberReport{}, nil, fmt.Errorf("panel member report: trailing content after JSON value")
	}
	if report.Verdict != PanelVerdictApproved && report.Verdict != PanelVerdictChangesRequested {
		return PanelMemberReport{}, nil, fmt.Errorf("panel member report: invalid verdict %q", report.Verdict)
	}
	if len(report.Findings) > maxFindingsPerPanelReport {
		return PanelMemberReport{}, nil, fmt.Errorf("panel member report: %d findings exceeds %d bound", len(report.Findings), maxFindingsPerPanelReport)
	}
	seen := make(map[string]struct{}, len(report.Findings))
	for _, f := range report.Findings {
		if f.ID == "" || f.Title == "" || f.Severity == "" || f.Description == "" {
			return PanelMemberReport{}, nil, fmt.Errorf("panel member report: finding is missing a required field")
		}
		// Schema character-length check (what a JSON Schema maxLength: 64
		// would enforce, counting Unicode characters, not bytes).
		if utf8.RuneCountInString(f.ID) > maxFindingIDRunes {
			return PanelMemberReport{}, nil, fmt.Errorf("panel member report: finding id exceeds %d character bound", maxFindingIDRunes)
		}
		// Host byte check (D10): catches IDs a character-length check alone
		// would accept, such as 64 four-byte Unicode characters.
		if len(f.ID) > maxFindingIDBytes {
			return PanelMemberReport{}, nil, fmt.Errorf("panel member report: finding id exceeds %d byte bound", maxFindingIDBytes)
		}
		// A control byte in a finding ID (notably 0x00 and 0x1e, the
		// separators sourceKeyDigest uses between key fields) would let two
		// different canonical source keys hash to the same digest, e.g.
		// member "security" with finding "X\x1esecurity\x00Y" and member
		// "security" with two findings "X" and "Y" produce the same digest
		// input. Reject it here so no finding ID can ever collide with a
		// separator the host's own encoding relies on.
		if textutil.HasControlByte(f.ID) {
			return PanelMemberReport{}, nil, fmt.Errorf("panel member report: finding id contains a control character")
		}
		if _, dup := seen[f.ID]; dup {
			return PanelMemberReport{}, nil, fmt.Errorf("panel member report: duplicate finding id %q", f.ID)
		}
		seen[f.ID] = struct{}{}
	}
	canonical, err := json.Marshal(report)
	if err != nil {
		return PanelMemberReport{}, nil, fmt.Errorf("panel member report: canonical encode: %w", err)
	}
	if len(canonical) > maxCanonicalPanelMemberReportBytes {
		return PanelMemberReport{}, nil, fmt.Errorf("panel member canonical report is %d bytes, exceeds %d byte bound", len(canonical), maxCanonicalPanelMemberReportBytes)
	}
	return report, canonical, nil
}

// checkNoDuplicateJSONKeys walks raw as a JSON token stream and rejects a
// duplicate key in any JSON object at any depth. encoding/json silently
// keeps the last value for a duplicate key; this catches what it will not.
func checkNoDuplicateJSONKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := walkNoDuplicateJSONKeys(dec); err != nil {
		return err
	}
	return nil
}

func walkNoDuplicateJSONKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("expected a JSON object key")
			}
			if _, dup := seen[key]; dup {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkNoDuplicateJSONKeys(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume closing '}'
		return err
	case '[':
		for dec.More() {
			if err := walkNoDuplicateJSONKeys(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume closing ']'
		return err
	default:
		return nil
	}
}

// dropPanelFindings returns the findings whose id is not in the dropped
// set, preserving order.
func dropPanelFindings(findings []PanelFinding, dropped []string) []PanelFinding {
	drop := make(map[string]bool, len(dropped))
	for _, id := range dropped {
		drop[id] = true
	}
	kept := make([]PanelFinding, 0, len(findings))
	for _, f := range findings {
		if !drop[f.ID] {
			kept = append(kept, f)
		}
	}
	return kept
}

// ComputeHostVerdict computes the final panel gate verdict from decoded
// member reports (D10). The model cannot override this computation: it is
// changes_requested if any member reports that verdict or has one or more
// findings, and approved only when every member approves with no findings.
func ComputeHostVerdict(reports []PanelMemberReport) string {
	for _, r := range reports {
		if r.Verdict == PanelVerdictChangesRequested || len(r.Findings) > 0 {
			return PanelVerdictChangesRequested
		}
	}
	return PanelVerdictApproved
}

// ValidateSourceDispositions checks that the synthesizer supplied exactly one
// legal disposition for every canonical source key the bounded member
// reports produced, and no dispositions for any other key (D10).
func ValidateSourceDispositions(keys []CanonicalSourceKey, dispositions []PanelSourceDisposition) error {
	want := make(map[CanonicalSourceKey]struct{}, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
	}
	seen := make(map[CanonicalSourceKey]struct{}, len(dispositions))
	for _, d := range dispositions {
		if d.Disposition != PanelDispositionIncluded && d.Disposition != PanelDispositionDuplicate {
			return fmt.Errorf("invalid disposition %q", d.Disposition)
		}
		if d.FinalFindingID == "" {
			return fmt.Errorf("disposition for (%q, %q) has no final finding id", d.MemberID, d.FindingID)
		}
		key := CanonicalSourceKey{MemberID: d.MemberID, FindingID: d.FindingID}
		if _, ok := want[key]; !ok {
			return fmt.Errorf("disposition references unknown source key (%q, %q)", d.MemberID, d.FindingID)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("duplicate disposition for source key (%q, %q)", d.MemberID, d.FindingID)
		}
		seen[key] = struct{}{}
	}
	if len(seen) != len(want) {
		return fmt.Errorf("missing disposition for %d of %d source keys", len(want)-len(seen), len(want))
	}
	return nil
}

// canonicalSourceKeys derives the ordered canonical source keys for one
// member's decoded findings. The host never persists a copy of this list; it
// is always re-derived from the bounded member report (D11).
func canonicalSourceKeys(memberID string, report PanelMemberReport) []CanonicalSourceKey {
	keys := make([]CanonicalSourceKey, len(report.Findings))
	for i, f := range report.Findings {
		keys[i] = CanonicalSourceKey{MemberID: memberID, FindingID: f.ID}
	}
	return keys
}

// sourceKeyDigest digests the ordered canonical source keys for one member,
// for D11's "digest of ordered canonical source keys" provenance field.
func sourceKeyDigest(keys []CanonicalSourceKey) string {
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k.MemberID))
		h.Write([]byte{0})
		h.Write([]byte(k.FindingID))
		h.Write([]byte{0x1e})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func digestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// synthesisSkeleton builds the host metadata skeleton with empty member
// reports, for the bounds-reservation check the plan requires: this exact
// size plus member_count * maxCanonicalPanelMemberReportBytes must fit
// within maxSynthesisEnvelopeBytes before any provider call starts.
func synthesisSkeleton(stepID string, inputs []PanelSynthesisMemberInput) ([]byte, error) {
	skeleton := PanelSynthesisEnvelope{StepID: stepID, HostVerdict: PanelVerdictApproved}
	for _, in := range inputs {
		skeleton.Members = append(skeleton.Members, PanelSynthesisMemberEnvelope{
			Provenance: PanelMemberProvenance{
				StepID: stepID, MemberID: in.MemberID, AgentName: in.AgentName, AgentDigest: in.AgentDigest,
				Provider: in.Provider, Model: in.Model, CoordinatorRunID: in.CoordinatorRunID,
				CoordinatorTaskID: in.CoordinatorTaskID, TerminalStatus: in.TerminalStatus,
				OutputDigest: digestHex(nil), FindingCount: 0, SourceKeyDigest: sourceKeyDigest(nil),
			},
			Report: PanelMemberReport{Verdict: PanelVerdictApproved, Findings: nil},
		})
	}
	return json.Marshal(skeleton)
}

// BuildSynthesisEnvelope builds the one host-owned JSON envelope the
// synthesizer receives (D11) with no member-report filtering. It decodes
// each member's raw output with DecodeStrictPanelMemberReport, so invalid
// or oversized member JSON never reaches synthesis (Fan-in matrix item 2).
// It stamps provenance for every member from host-known data only, computes
// the monotonic host verdict from the bounded reports, and enforces every
// bound in the plan's Bounds table with overflow-safe sums. A panel with a
// single surviving member still synthesizes: one successful member is
// sufficient to build an envelope.
func BuildSynthesisEnvelope(stepID string, inputs []PanelSynthesisMemberInput) (PanelSynthesisEnvelope, []byte, error) {
	return BuildSynthesisEnvelopeWithFilter(stepID, inputs, nil)
}

// BuildSynthesisEnvelopeWithFilter is BuildSynthesisEnvelope plus a
// host-side member-report filter. The filter returns the ids of findings
// the host drops (the chunk finding-scope rule); the builder removes them,
// records them in the envelope's DroppedFindings, and neutralizes a
// changes_requested verdict whose findings list is empty after the drop -
// or was empty from the start. A verdict with no findings carries no
// actionable content, and the synthesizer's dispositions validate against
// the filtered reports only. With a nil filter the builder applies none of
// this, so non-chunk panels keep the exact legacy behavior. A panel with a
// single surviving member still synthesizes: one successful member is
// sufficient to build an envelope.
func BuildSynthesisEnvelopeWithFilter(stepID string, inputs []PanelSynthesisMemberInput, filter func(memberID string, report *PanelMemberReport) []string) (PanelSynthesisEnvelope, []byte, error) {
	if len(inputs) < 1 || len(inputs) > 4 {
		return PanelSynthesisEnvelope{}, nil, fmt.Errorf("panel synthesis envelope requires 1 to 4 members, got %d", len(inputs))
	}
	seenMembers := make(map[string]struct{}, len(inputs))
	for _, in := range inputs {
		if in.MemberID == "" {
			return PanelSynthesisEnvelope{}, nil, fmt.Errorf("panel synthesis member has no member id")
		}
		if _, dup := seenMembers[in.MemberID]; dup {
			return PanelSynthesisEnvelope{}, nil, fmt.Errorf("duplicate panel member id %q", in.MemberID)
		}
		seenMembers[in.MemberID] = struct{}{}
	}

	// Reserve space for the full host metadata skeleton with empty member
	// reports plus member_count * maxCanonicalPanelMemberReportBytes, before
	// decoding any member content or starting any provider call.
	skeleton, err := synthesisSkeleton(stepID, inputs)
	if err != nil {
		return PanelSynthesisEnvelope{}, nil, fmt.Errorf("panel synthesis envelope skeleton: %w", err)
	}
	reserved := len(skeleton) + len(inputs)*maxCanonicalPanelMemberReportBytes
	if reserved < len(skeleton) || reserved > maxSynthesisEnvelopeBytes {
		return PanelSynthesisEnvelope{}, nil, fmt.Errorf("panel synthesis envelope reservation of %d bytes exceeds %d byte bound", reserved, maxSynthesisEnvelopeBytes)
	}

	reports := make([]PanelMemberReport, len(inputs))
	envelope := PanelSynthesisEnvelope{StepID: stepID}
	for i, in := range inputs {
		report, _, err := DecodeStrictPanelMemberReport(in.RawOutput)
		if err != nil {
			return PanelSynthesisEnvelope{}, nil, fmt.Errorf("panel member %q: %w", in.MemberID, err)
		}
		if filter != nil {
			if dropped := filter(in.MemberID, &report); len(dropped) > 0 {
				if envelope.DroppedFindings == nil {
					envelope.DroppedFindings = make(map[string][]string, len(inputs))
				}
				envelope.DroppedFindings[in.MemberID] = dropped
				report.Findings = dropPanelFindings(report.Findings, dropped)
			}
			if report.Verdict == PanelVerdictChangesRequested && len(report.Findings) == 0 {
				report.Verdict = PanelVerdictApproved
			}
		}
		reports[i] = report
		keys := canonicalSourceKeys(in.MemberID, report)
		prov := PanelMemberProvenance{
			StepID: stepID, MemberID: in.MemberID, AgentName: in.AgentName, AgentDigest: in.AgentDigest,
			Provider: in.Provider, Model: in.Model, CoordinatorRunID: in.CoordinatorRunID,
			CoordinatorTaskID: in.CoordinatorTaskID, TerminalStatus: in.TerminalStatus,
			OutputDigest: digestHex(in.RawOutput), FindingCount: len(report.Findings),
			SourceKeyDigest: sourceKeyDigest(keys),
		}
		envelope.Members = append(envelope.Members, PanelSynthesisMemberEnvelope{Provenance: prov, Report: report})
	}
	envelope.HostVerdict = ComputeHostVerdict(reports)

	encoded, err := json.Marshal(envelope)
	if err != nil {
		return PanelSynthesisEnvelope{}, nil, fmt.Errorf("panel synthesis envelope: encode: %w", err)
	}
	if len(encoded) > maxSynthesisEnvelopeBytes {
		return PanelSynthesisEnvelope{}, nil, fmt.Errorf("panel synthesis envelope is %d bytes, exceeds %d byte bound", len(encoded), maxSynthesisEnvelopeBytes)
	}
	return envelope, encoded, nil
}
