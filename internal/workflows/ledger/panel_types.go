package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// Validate checks that a durable panel task has the fields needed to rebuild work.
func (s PanelTaskSpec) Validate() error {
	if s.TaskName == "" || s.InputRef == "" || s.InputSchemaRef == "" || s.OutputSchemaRef == "" || s.AgentName == "" || s.AgentDigest == "" || s.Skill == "" || s.Scope == "" || s.Provider == "" || s.Model == "" || !isCoordinatorFingerprint(s.CoordinatorRequestFingerprint) {
		return fmt.Errorf("incomplete panel task specification")
	}
	if !isSHA256(s.InputDigest) || !isSHA256(s.InputSchemaDigest) || !isSHA256(s.AgentDigest) || !isSHA256(s.OutputSchemaDigest) {
		return fmt.Errorf("invalid panel task digest")
	}
	if len(s.DependsOn) != 0 {
		return fmt.Errorf("panel task dependencies must be empty")
	}
	if s.Budget < 0 || s.Timeout <= 0 || s.DeadlineAt.IsZero() || s.WorkLimits.DeadlineAt.IsZero() || !s.WorkLimits.DeadlineAt.Equal(s.DeadlineAt) {
		return fmt.Errorf("invalid panel task limits")
	}
	if s.WorkLimits.MaxTurns <= 0 || s.WorkLimits.MaxPromptTokens <= 0 || s.WorkLimits.MaxOutputTokens <= 0 || s.WorkLimits.MaxOutputPerCall <= 0 || s.WorkLimits.MaxToolCalls <= 0 {
		return fmt.Errorf("incomplete panel work limits")
	}
	if !s.Policy.NoRetry || !s.Policy.FailInterrupted {
		return fmt.Errorf("panel task policy must disable retries and fail interruptions")
	}
	return nil
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

// PanelWorkLimits records fixed limits for admitted panel work. It grants no authority.
type PanelWorkLimits struct {
	MaxTurns         int       `json:"max_turns"`
	MaxPromptTokens  int       `json:"max_prompt_tokens"`
	MaxOutputTokens  int       `json:"max_output_tokens"`
	MaxOutputPerCall int       `json:"max_output_per_call"`
	MaxToolCalls     int       `json:"max_tool_calls"`
	DeadlineAt       time.Time `json:"deadline_at"`
}

// PanelTaskSpec records exact, non-authority data for one admitted child task.
type PanelTaskSpec struct {
	TaskName                      string                `json:"task_name"`
	DependsOn                     []string              `json:"depends_on"`
	InputRef                      string                `json:"input_ref"`
	InputDigest                   string                `json:"input_digest"`
	InputSchemaRef                string                `json:"input_schema_ref"`
	InputSchemaDigest             string                `json:"input_schema_digest"`
	Budget                        int                   `json:"budget"`
	Scope                         string                `json:"scope"`
	AgentName                     string                `json:"agent_name"`
	AgentDigest                   string                `json:"agent_digest"`
	Skill                         string                `json:"skill"`
	Provider                      string                `json:"provider"`
	Model                         string                `json:"model"`
	OutputSchemaDigest            string                `json:"output_schema_digest"`
	OutputSchemaRef               string                `json:"output_schema_ref"`
	Timeout                       time.Duration         `json:"timeout"`
	DeadlineAt                    time.Time             `json:"deadline_at"`
	WorkLimits                    PanelWorkLimits       `json:"work_limits"`
	Policy                        coordledger.RunPolicy `json:"policy"`
	CoordinatorRequestFingerprint string                `json:"coordinator_request_fingerprint"`
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
		if err := member.Work.Validate(); err != nil {
			return err
		}
	}
	return nil
}
