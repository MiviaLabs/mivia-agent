package controller

// PanelFinding is one finding inside a panel-review-v1.json member report.
// Fields match the JSON schema exactly; DecodeStrictPanelMemberReport rejects
// any report that carries additional or missing fields.
type PanelFinding struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

// PanelMemberReport is one decoded panel-review-v1.json member report.
type PanelMemberReport struct {
	Verdict  string         `json:"verdict"`
	Findings []PanelFinding `json:"findings"`
}

// Panel verdict values. The model can supply only these two values in a
// member report; the host, not the model, computes the final gate verdict.
const (
	PanelVerdictApproved         = "approved"
	PanelVerdictChangesRequested = "changes_requested"
)

// PanelDisposition is the final disposition of one canonical source key.
// D10 removes resolved_conflict from version 1: only these two values exist.
type PanelDisposition string

const (
	PanelDispositionIncluded  PanelDisposition = "included"
	PanelDispositionDuplicate PanelDisposition = "duplicate"
)

// CanonicalSourceKey is the (member_id, finding_id) pair D10 defines as the
// one canonical source key for every panel finding.
type CanonicalSourceKey struct {
	MemberID  string
	FindingID string
}

// PanelSourceDisposition is one synthesizer-authored disposition for one
// canonical source key. The host validates it; it never trusts it blindly.
type PanelSourceDisposition struct {
	MemberID       string           `json:"member_id"`
	FindingID      string           `json:"finding_id"`
	Disposition    PanelDisposition `json:"disposition"`
	FinalFindingID string           `json:"final_finding_id"`
}

// PanelMemberProvenance holds the fields the host stamps for one member per
// D11. Every field here comes from host-known data (the admitted work spec,
// the coordinator result, and the bounded decoded report) and never from
// parsing arbitrary model-authored JSON, so the model cannot author or
// conflict with any of them.
type PanelMemberProvenance struct {
	StepID            string `json:"step_id"`
	MemberID          string `json:"member_id"`
	AgentName         string `json:"agent_name"`
	AgentDigest       string `json:"agent_digest"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	CoordinatorRunID  string `json:"coordinator_run_id"`
	CoordinatorTaskID string `json:"coordinator_task_id"`
	TerminalStatus    string `json:"terminal_status"`
	OutputDigest      string `json:"output_digest"`
	FindingCount      int    `json:"finding_count"`
	SourceKeyDigest   string `json:"source_key_digest"`
}

// PanelSynthesisMemberEnvelope is one member's entry in the host-owned
// synthesis envelope: host-stamped provenance next to the member's own
// bounded, untrusted report content. The two never merge into one text blob.
type PanelSynthesisMemberEnvelope struct {
	Provenance PanelMemberProvenance `json:"provenance"`
	Report     PanelMemberReport     `json:"report"`
}

// PanelSynthesisEnvelope is the one host-owned JSON document the synthesizer
// receives. HostVerdict is computed by ComputeHostVerdict; the synthesizer
// cannot change it (D10).
type PanelSynthesisEnvelope struct {
	StepID      string                         `json:"step_id"`
	HostVerdict string                         `json:"host_verdict"`
	Members     []PanelSynthesisMemberEnvelope `json:"members"`
}

// PanelSynthesisMemberInput names one member's raw, already-terminal
// coordinator output plus the host-known identity fields needed to stamp its
// provenance. RawOutput is untrusted model content until decoded.
type PanelSynthesisMemberInput struct {
	MemberID          string
	AgentName         string
	AgentDigest       string
	Provider          string
	Model             string
	CoordinatorRunID  string
	CoordinatorTaskID string
	TerminalStatus    string
	RawOutput         []byte
}
