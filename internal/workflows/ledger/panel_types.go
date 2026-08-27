package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// Validate checks that a durable panel task has the fields needed to rebuild work.
func (s PanelTaskSpec) Validate() error {
	return s.validate(false)
}

// validateLegacy accepts an event written before work fingerprints existed.
// It still validates every other durable field. New admissions use Validate.
func (s PanelTaskSpec) validateLegacy() error {
	return s.validate(true)
}

func (s PanelTaskSpec) validate(allowMissingWorkFingerprint bool) error {
	if s.TaskName == "" || s.InputRef == "" || s.InputSchemaRef == "" || s.OutputSchemaRef == "" || s.AgentName == "" || s.AgentDigest == "" || s.Skill == "" || s.Scope == "" || s.Provider == "" || s.Model == "" || !isCoordinatorFingerprint(s.CoordinatorRequestFingerprint) || (!allowMissingWorkFingerprint && !isCoordinatorFingerprint(s.WorkFingerprint)) {
		return fmt.Errorf("incomplete panel task specification")
	}
	if !isSHA256(s.InputDigest) || !isSHA256(s.InputSchemaDigest) || !isAgentDigest(s.AgentDigest) || !isSHA256(s.OutputSchemaDigest) {
		return fmt.Errorf("invalid panel task digest")
	}
	if len(s.DependsOn) != 0 {
		return fmt.Errorf("panel task dependencies must be empty")
	}
	if s.Budget < 0 || s.Timeout <= 0 || s.DeadlineAt.IsZero() || s.WorkLimits.DeadlineAt.IsZero() || !s.WorkLimits.DeadlineAt.Equal(s.DeadlineAt) {
		return fmt.Errorf("invalid panel task limits")
	}
	// MaxTurns may be 0 (unlimited) per runtime.WorkLimits semantics and the
	// per-step max_turns workflow knob (definition.Step.MaxTurns, default 0 =
	// unlimited): a read-only reviewer's turn count is not a work bound when
	// the child loop is still bounded by MaxOutputPerCall, MaxToolCalls, the
	// attempt deadline, and the panel's retry policy. MaxPromptTokens may also
	// be 0 (unlimited cumulative prompt): prompt volume is not a work bound
	// when every provider call is bounded by the model context window with a
	// prompt-too-long compaction retry. MaxOutputTokens may also be 0
	// (unlimited cumulative output): a finite cumulative cap with
	// ceiling-charged accounting kills deep read-only reviews mid-panel with
	// "work limit exceeded: output tokens" (same bogus bound class; see
	// panel_attempt.go). Only negative values are rejected.
	if s.WorkLimits.MaxTurns < 0 || s.WorkLimits.MaxPromptTokens < 0 || s.WorkLimits.MaxOutputTokens < 0 || s.WorkLimits.MaxOutputPerCall <= 0 || s.WorkLimits.MaxToolCalls <= 0 {
		return fmt.Errorf("incomplete panel work limits")
	}
	if !s.Policy.NoRetry || !s.Policy.FailInterrupted || s.Policy.RetryMaxRetries != 0 || s.Policy.RetryBaseBackoff != 0 || s.Policy.RetryMaxBackoff != 0 || s.Policy.RetryBackoffFactor != 0 || s.Policy.RetryJitterFraction != 0 {
		return fmt.Errorf("panel task policy must disable retries and fail interruptions")
	}
	if s.WorkFingerprint != "" && s.WorkFingerprint != s.workFingerprint() {
		return fmt.Errorf("invalid panel work fingerprint")
	}
	return nil
}

// workFingerprint covers durable work fields that the coordinator task model
// does not carry, such as limits and the absolute deadline.
func (s PanelTaskSpec) workFingerprint() string {
	type work struct {
		TaskName, InputRef, InputDigest, InputSchemaRef, InputSchemaDigest string
		Budget                                                             int
		Scope, AgentName, AgentDigest, Skill, Provider, Model              string
		OutputSchemaDigest, OutputSchemaRef                                string
		Timeout                                                            time.Duration
		DeadlineAt                                                         time.Time
		WorkLimits                                                         PanelWorkLimits
		Policy                                                             ledgercore.RunPolicy
		CoordinatorRequestFingerprint                                      string
	}
	value, _ := json.Marshal(work{
		TaskName: s.TaskName, InputRef: s.InputRef, InputDigest: s.InputDigest,
		InputSchemaRef: s.InputSchemaRef, InputSchemaDigest: s.InputSchemaDigest,
		Budget: s.Budget, Scope: s.Scope, AgentName: s.AgentName, AgentDigest: s.AgentDigest,
		Skill: s.Skill, Provider: s.Provider, Model: s.Model,
		OutputSchemaDigest: s.OutputSchemaDigest, OutputSchemaRef: s.OutputSchemaRef,
		Timeout: s.Timeout, DeadlineAt: s.DeadlineAt, WorkLimits: s.WorkLimits,
		Policy: s.Policy, CoordinatorRequestFingerprint: s.CoordinatorRequestFingerprint,
	})
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// FinalizePanelTaskSpec records the fingerprint of the durable work fields.
func FinalizePanelTaskSpec(spec *PanelTaskSpec) {
	if spec != nil {
		spec.WorkFingerprint = spec.workFingerprint()
	}
}

func isCoordinatorFingerprint(value string) bool {
	const prefix = "sha256:"
	return strings.HasPrefix(value, prefix) && isSHA256(strings.TrimPrefix(value, prefix))
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func isAgentDigest(value string) bool {
	return isSHA256(value) || (strings.HasPrefix(value, "sha256:") && isSHA256(strings.TrimPrefix(value, "sha256:")))
}

// PanelChildPrincipal derives the only principal panel child operations use.
func PanelChildPrincipal(workflowRunID string) runtime.Caller {
	sum := panelHash("mivia:panel-child-principal:v1", workflowRunID)
	return runtime.Caller{SessionID: hex.EncodeToString(sum[:]), Role: "workflow-panel"}
}

// ContextWithPanelChildPrincipal replaces the caller for every panel child
// coordinator operation. It never inherits a host caller's authority scope.
func ContextWithPanelChildPrincipal(ctx context.Context, workflowRunID string) context.Context {
	return runtime.ContextWithCaller(ctx, PanelChildPrincipal(workflowRunID))
}

// PanelChildIDs returns deterministic coordinator identifiers for one child.
// Each identifier uses the coordinator's canonical run ID encoding.
func PanelChildIDs(workflowRunID, attemptID, childID string) (runID, taskID string) {
	run := panelHash("mivia:panel-child-run:v1", workflowRunID, attemptID, childID)
	task := panelHash("mivia:panel-child-task:v1", workflowRunID, attemptID, childID)
	encoder := base32.StdEncoding.WithPadding(base32.NoPadding)
	return "run-" + encoder.EncodeToString(run[:16]), "task-" + encoder.EncodeToString(task[:16])
}

func panelHash(domain string, parts ...string) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(domain))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// PanelPhase identifies the durable phase of a panel step attempt.
type PanelPhase string

const (
	PanelPhaseMembersAdmitted   PanelPhase = "members_admitted"
	PanelPhaseSynthesisAdmitted PanelPhase = "synthesis_admitted"
	PanelPhaseCancelPending     PanelPhase = "cancel_pending"
)

// PanelWorkLimits is the durable form of runtime work limits.
type PanelWorkLimits = runtime.WorkLimits

// PanelTaskSpec records exact, non-authority data for one admitted child task.
type PanelTaskSpec struct {
	TaskName                      string               `json:"task_name"`
	DependsOn                     []string             `json:"depends_on"`
	InputRef                      string               `json:"input_ref"`
	InputDigest                   string               `json:"input_digest"`
	InputSchemaRef                string               `json:"input_schema_ref"`
	InputSchemaDigest             string               `json:"input_schema_digest"`
	Budget                        int                  `json:"budget"`
	Scope                         string               `json:"scope"`
	AgentName                     string               `json:"agent_name"`
	AgentDigest                   string               `json:"agent_digest"`
	Skill                         string               `json:"skill"`
	Provider                      string               `json:"provider"`
	Model                         string               `json:"model"`
	OutputSchemaDigest            string               `json:"output_schema_digest"`
	OutputSchemaRef               string               `json:"output_schema_ref"`
	Timeout                       time.Duration        `json:"timeout"`
	DeadlineAt                    time.Time            `json:"deadline_at"`
	WorkLimits                    PanelWorkLimits      `json:"work_limits"`
	Policy                        ledgercore.RunPolicy `json:"policy"`
	WorkFingerprint               string               `json:"work_fingerprint"`
	CoordinatorRequestFingerprint string               `json:"coordinator_request_fingerprint"`
}

func (s PanelTaskSpec) clone() PanelTaskSpec {
	s.DependsOn = append([]string(nil), s.DependsOn...)
	return s
}

// PanelMemberExecution records one member identity and its exact work.
type PanelMemberExecution struct {
	MemberID         string        `json:"member_id"`
	CoordinatorRunID string        `json:"coordinator_run_id"`
	TaskID           string        `json:"task_id"`
	Work             PanelTaskSpec `json:"work"`
	Order            int           `json:"order"`
}

func (m PanelMemberExecution) clone() PanelMemberExecution {
	m.Work = m.Work.clone()
	return m
}

// PanelSynthesisExecution records exact synthesis work after its phase intent.
type PanelSynthesisExecution struct {
	Work PanelTaskSpec `json:"work"`
}

func (s *PanelSynthesisExecution) clone() *PanelSynthesisExecution {
	if s == nil {
		return nil
	}
	clone := *s
	clone.Work = s.Work.clone()
	return &clone
}

// PanelExecution records all panel child identities and phase state.
type PanelExecution struct {
	Members         []PanelMemberExecution   `json:"members"`
	SynthesisRunID  string                   `json:"synthesis_run_id"`
	SynthesisTaskID string                   `json:"synthesis_task_id"`
	Synthesis       *PanelSynthesisExecution `json:"synthesis,omitempty"`
	Phase           PanelPhase               `json:"phase"`
}

func (p *PanelExecution) clone() *PanelExecution {
	if p == nil {
		return nil
	}
	clone := *p
	clone.Members = make([]PanelMemberExecution, len(p.Members))
	for i := range p.Members {
		clone.Members[i] = p.Members[i].clone()
	}
	clone.Synthesis = p.Synthesis.clone()
	return &clone
}

func (p *PanelExecution) validateInitial(workflowRunID, attemptID string) error {
	if p == nil {
		return nil
	}
	if p.Phase != PanelPhaseMembersAdmitted || p.SynthesisRunID == "" || p.SynthesisTaskID == "" || p.Synthesis != nil || len(p.Members) < 2 || len(p.Members) > 4 {
		return fmt.Errorf("invalid initial panel execution")
	}
	wantSynthesisRun, wantSynthesisTask := PanelChildIDs(workflowRunID, attemptID, "synthesis")
	if p.SynthesisRunID != wantSynthesisRun || p.SynthesisTaskID != wantSynthesisTask {
		return fmt.Errorf("invalid panel synthesis identity")
	}
	seen := make(map[string]struct{}, len(p.Members))
	for i, member := range p.Members {
		if member.MemberID == "" || member.MemberID == "synthesis" || member.CoordinatorRunID == "" || member.TaskID == "" || member.Order != i {
			return fmt.Errorf("invalid panel member")
		}
		if _, ok := seen[member.MemberID]; ok {
			return fmt.Errorf("duplicate panel member")
		}
		wantRun, wantTask := PanelChildIDs(workflowRunID, attemptID, member.MemberID)
		if member.CoordinatorRunID != wantRun || member.TaskID != wantTask {
			return fmt.Errorf("invalid panel member identity")
		}
		seen[member.MemberID] = struct{}{}
		if err := member.Work.validateLegacy(); err != nil {
			return err
		}
	}
	return nil
}
